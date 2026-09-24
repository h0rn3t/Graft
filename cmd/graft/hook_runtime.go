package main

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/hosts"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
	"github.com/h0rn3t/Graft/internal/upkeep"
)

const (
	hookMinPromptChars  = 12
	hookTimeoutDefault  = 8 * time.Second
	hookTimeoutOverhead = 2 * time.Second
	hookTimeoutFloor    = 4 * time.Second
	// hookTimeoutCeiling is the prompt budget graft's own 15 s hook allows. It
	// also bounds a millisecond timeout written by older graft releases, which
	// the host reads as seconds.
	hookTimeoutCeiling = 13 * time.Second
	// hookInputLimit caps the payload a hook reads. Hosts inline whole tool
	// results, so the cap is generous; a payload past it is dropped whole
	// rather than cut into invalid JSON.
	hookInputLimit = 64 << 20
)

var hookPatchFilePattern = regexp.MustCompile(`(?m)^\*\*\*\s+(?:Add|Update)\s+File:\s+(.+?)\s*$`)

var (
	hookHomeDir = homeDir
	hookAsk     = askHookGraph
	// hookCheck counts the files that drifted from the last build's
	// fingerprint: a walk plus a hash of files whose size or mtime changed,
	// never a re-extraction.
	hookCheck = func(root, contextDir string) int {
		drift, err := graph.ProbeDrift(root, contextDir, graph.ExtractorID, sourcefiles.Options{})
		if err != nil || drift == nil {
			return 0
		}
		return len(drift.Added) + len(drift.Changed) + len(drift.Removed)
	}
	hookStartSync = startHookSync
	hookSyncBuild = buildHookSync
)

type hookInput map[string]any

func readHookInput(stdin io.Reader) hookInput {
	input := hookInput{}
	if stdin == nil {
		return input
	}
	data, err := io.ReadAll(io.LimitReader(stdin, hookInputLimit+1))
	if err != nil || len(data) > hookInputLimit || jsonv2.Unmarshal(data, &input, hookLenientJSON) != nil {
		return hookInput{}
	}
	return input
}

func (input hookInput) string(key string) string {
	value, _ := input[key].(string)
	return value
}

func (input hookInput) object(key string) map[string]any {
	value, _ := input[key].(map[string]any)
	return value
}

func hookProjectDir(input hookInput) string {
	if dir := os.Getenv("CLAUDE_PROJECT_DIR"); dir != "" {
		return dir
	}
	if dir := input.string("cwd"); dir != "" {
		return dir
	}
	dir, _ := os.Getwd()
	return dir
}

func hookSessionID(input hookInput) string {
	if id := input.string("session_id"); id != "" {
		return id
	}
	if id := input.string("conversation_id"); id != "" {
		return id
	}
	return "default"
}

func emitHookContext(stdout io.Writer, event, context string) {
	payload := struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	payload.HookSpecificOutput.HookEventName = event
	payload.HookSpecificOutput.AdditionalContext = context
	data, err := jsonv2.Marshal(payload, nil)
	if err == nil {
		_, _ = stdout.Write(data)
	}
}

func hookUnderContext(root, file string) bool {
	contextDir := hookContextDir(root)
	rel, err := filepath.Rel(contextDir, file)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hookEditedFile(input hookInput, root string) string {
	toolInput := input.object("tool_input")
	if file, _ := toolInput["file_path"].(string); strings.TrimSpace(file) != "" {
		return file
	}
	command, _ := toolInput["command"].(string)
	match := hookPatchFilePattern.FindStringSubmatch(command)
	if len(match) != 2 {
		return ""
	}
	if filepath.IsAbs(match[1]) {
		return match[1]
	}
	return filepath.Join(root, match[1])
}

func hookCheckStaleCount(ctx context.Context, root, contextDir string) int {
	select {
	case <-ctx.Done():
		return 0
	default:
		return hookCheck(root, contextDir)
	}
}

func handleHookPostEdit(ctx context.Context, input hookInput, root string, stdout, stderr io.Writer) {
	file := hookEditedFile(input, root)
	if file == "" || hookUnderContext(root, file) {
		return
	}
	contextDir := hookContextDir(root)
	// Repo-relative, so a scope hint can tell backend/auth.go from
	// frontend/auth.go; a file outside the repository keeps its base name.
	lastFile := filepath.Base(file)
	if rel, err := filepath.Rel(root, file); err == nil && filepath.IsLocal(rel) {
		lastFile = filepath.ToSlash(rel)
	}
	_, _ = patchHookStats(root, hookStatsPatch{
		Dirty:       new(true),
		StaleCount:  new(hookCheckStaleCount(ctx, root, contextDir)),
		LastFileSet: true,
		LastFile:    &lastFile,
	})
	wiring, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		return
	}
	if blast := formatHookBlastRadius(*wiring, file, 8); blast != "" {
		emitHookContext(stdout, "PostToolUse", blast)
	}
}

func classifyAndScoreHookUse(toolName, command string, payload any) hookToolUse {
	kind := classifyHookToolUse(toolName, command)
	if kind == hookToolSource {
		return hookToolUse{Kind: kind}
	}
	data, err := jsonv2.Marshal(payload, nil)
	saved := 0
	if err == nil {
		saved = parseHookSavings(string(data))
	}
	if saved > 0 {
		kind = hookToolGraft
	}
	return hookToolUse{Kind: kind, SavedTokens: saved}
}

func handleHookToolUse(input hookInput, root string) {
	toolInput := input.object("tool_input")
	command, _ := toolInput["command"].(string)
	payload := input["tool_response"]
	if payload == nil {
		payload = input
	}
	use := classifyAndScoreHookUse(input.string("tool_name"), command, payload)
	use.Host = "claude-code"
	_ = recordHookToolUse(root, hookSessionID(input), use)
}

func handleHookCursorPostTool(input hookInput, root string) {
	toolName := input.string("tool_name")
	if isMCPToolName(toolName) || isGraftMCPTool(toolName) {
		return
	}
	toolInput := input.object("tool_input")
	command, _ := toolInput["command"].(string)
	if command == "" {
		command, _ = toolInput["cmd"].(string)
	}
	payload := input["tool_output"]
	if payload == nil {
		payload = input["tool_response"]
	}
	if payload == nil {
		payload = input
	}
	use := classifyAndScoreHookUse(toolName, command, payload)
	use.Host = "cursor"
	_ = recordHookToolUse(root, hookSessionID(input), use)
}

func handleHookCursorMCP(input hookInput, root string) {
	if !isGraftMCPTool(input.string("tool_name")) {
		return
	}
	payload := input["result_json"]
	if payload == nil {
		payload = input["result"]
	}
	if payload == nil {
		payload = input
	}
	data, _ := jsonv2.Marshal(payload, nil)
	_ = recordHookToolUse(root, hookSessionID(input), hookToolUse{
		Kind: hookToolGraft, SavedTokens: parseHookSavings(string(data)), Host: "cursor",
	})
}

func sampleHookTurnCost(root, id string, entries []hookTranscriptEntry) {
	billing := lastHookTurnBilling(entries)
	if billing == nil {
		return
	}
	_ = updateHookSession(root, id, func(session *sessionState) bool {
		if session.LastBillingUUID != nil && *session.LastBillingUUID == billing.UUID {
			return false
		}
		cost := billing.CostMicros
		if session.InputCostMicros != nil {
			cost += *session.InputCostMicros
		}
		tokens := billing.Tokens
		if session.InputTokensBilled != nil {
			tokens += *session.InputTokensBilled
		}
		uuid := billing.UUID
		session.InputCostMicros = &cost
		session.InputTokensBilled = &tokens
		session.LastBillingUUID = &uuid
		return true
	})
}

func countHookTallyTurn(root, id string, entries []hookTranscriptEntry) {
	_ = updateHookSession(root, id, func(session *sessionState) bool {
		if session.TurnUsedGraft == nil || !*session.TurnUsedGraft {
			return false
		}
		session.TurnUsedGraft = new(false)
		turn := lastHookAssistantTurn(entries)
		if turn == nil || (session.LastTallyUUID != nil && *session.LastTallyUUID == turn.UUID) {
			return true
		}
		graftTurns := 1
		if session.GraftTurns != nil {
			graftTurns += *session.GraftTurns
		}
		session.GraftTurns = &graftTurns
		if hasSavingsTally(turn.Text) {
			reported := 1
			if session.ReportedTurns != nil {
				reported += *session.ReportedTurns
			}
			session.ReportedTurns = &reported
		}
		uuid := turn.UUID
		session.LastTallyUUID = &uuid
		return true
	})
}

func buildHookSync(root string) error {
	var stdout, stderr bytes.Buffer
	opts := callersOptions{command: "build", root: root, rootSet: true}
	if os.Getenv("GRAFT_DIR") != "" {
		opts.contextDir = hookContextDir(root)
	}
	if status := runBuild(opts, &stdout, &stderr); status != 0 {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("build failed")
	}
	return nil
}

func startHookSync(root string) bool {
	executable, err := os.Executable()
	if err != nil || strings.HasSuffix(strings.TrimSuffix(filepath.Base(executable), ".exe"), ".test") {
		return false
	}
	command := exec.Command(executable, "_sync-run", root)
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = detachedProcAttr()
	if err := command.Start(); err != nil {
		return false
	}
	_ = command.Process.Release()
	return true
}

func handleHookStop(input hookInput, root string) {
	id := hookSessionID(input)
	entries := hookTranscriptEntries(input.string("transcript_path"))
	sampleHookTurnCost(root, id, entries)
	countHookTallyTurn(root, id, entries)
	stats := readHookStats(root)
	if stats == nil || !stats.Dirty {
		return
	}
	acquired, err := graph.AcquireLock(hookCacheDir(root))
	if err != nil || !acquired {
		return
	}
	if _, err := patchHookStats(root, hookStatsPatch{Syncing: new(true)}); err != nil {
		graph.ReleaseLock(hookCacheDir(root))
		return
	}
	if !hookStartSync(root) {
		_, _ = patchHookStats(root, hookStatsPatch{Syncing: new(false)})
		graph.ReleaseLock(hookCacheDir(root))
	}
}

func runHookSync(root string, stdout, stderr io.Writer) {
	// Claim the pending edits before building: an edit that lands while the
	// build runs sets Dirty again, and the final update below keeps it.
	_, _ = patchHookStats(root, hookStatsPatch{Dirty: new(false)})
	if err := hookSyncBuild(root); err != nil {
		_, _ = patchHookStats(root, hookStatsPatch{Syncing: new(false), Dirty: new(true)})
		graph.ReleaseLock(hookCacheDir(root))
		return
	}
	wiring, err := graph.Read(graph.WiringPath(hookContextDir(root)))
	if err != nil {
		_, _ = patchHookStats(root, hookStatsPatch{Syncing: new(false), Dirty: new(true)})
		graph.ReleaseLock(hookCacheDir(root))
		return
	}
	built := computeHookStats(*wiring)
	synced := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	_, _ = updateHookStats(root, func(stats *hookStats) {
		stats.NodeCount, stats.EdgeCount, stats.Languages = built.NodeCount, built.EdgeCount, built.Languages
		stats.TotalCount, stats.ReadyCount = built.TotalCount, built.ReadyCount
		stats.Syncing, stats.SyncedAt = false, &synced
		if !stats.Dirty {
			stats.StaleCount = 0
		}
	})
	graph.ReleaseLock(hookCacheDir(root))
}

func computeHookStats(wiring graph.GraphV1) hookStats {
	stats := emptyHookStats()
	if wiring.Meta.Version != 0 {
		stats.NodeCount = wiring.Meta.NodeCount
		stats.EdgeCount = wiring.Meta.EdgeCount
	} else {
		stats.NodeCount = len(wiring.Nodes)
		stats.EdgeCount = len(wiring.Edges)
	}
	if wiring.Meta.Languages != nil {
		stats.Languages = wiring.Meta.Languages
	}
	stats.TotalCount = len(wiring.Nodes)
	for _, node := range wiring.Nodes {
		if node.SummaryState == "ready" {
			stats.ReadyCount++
		}
	}
	return stats
}

// hookInstalledTimeout is the smallest timeout the Claude Code settings give
// graft's hook for event. Claude Code reads the value in seconds.
func hookInstalledTimeout(root, event string) (time.Duration, bool) {
	user := os.Getenv("CLAUDE_CONFIG_DIR")
	if user == "" {
		user = filepath.Join(homeDir(), ".claude")
	}
	var smallest time.Duration
	for _, file := range []string{
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".claude", "settings.local.json"),
		filepath.Join(user, "settings.json"),
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var settings struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string   `json:"command"`
					Timeout *float64 `json:"timeout"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if jsonv2.Unmarshal(data, &settings) != nil {
			continue
		}
		for _, block := range settings.Hooks[event] {
			for _, hook := range block.Hooks {
				if !strings.Contains(hook.Command, "graft-hooks.cjs") || hook.Timeout == nil || !(*hook.Timeout > 0) {
					continue
				}
				// Capped at a million seconds so the conversion cannot overflow.
				timeout := time.Duration(min(*hook.Timeout, 1e6) * float64(time.Second))
				if smallest == 0 || timeout < smallest {
					smallest = timeout
				}
			}
		}
	}
	return smallest, smallest > 0
}

// hookPromptAskTimeout is graft's own budget inside the host's prompt-hook
// timeout: hookTimeoutOverhead is left for process start and output, and the
// budget stays below the host timeout however small that is.
func hookPromptAskTimeout(root string) time.Duration {
	host, ok := hookInstalledTimeout(root, "UserPromptSubmit")
	if !ok {
		return hookTimeoutDefault - hookTimeoutOverhead
	}
	budget := host - hookTimeoutOverhead
	if budget < hookTimeoutFloor {
		budget = min(hookTimeoutFloor, host/2)
	}
	return min(budget, hookTimeoutCeiling)
}

func lastHookFileScope(root, lastFile string, stderr io.Writer) string {
	if lastFile == "" {
		return ""
	}
	wiring, err := graph.Read(graph.WiringPath(hookContextDir(root)))
	if err != nil {
		return ""
	}
	scopes := wiring.Meta.Scopes
	if scopes == nil || len(*scopes) <= 1 {
		return ""
	}
	// A repo-relative lastFile names one file; a bare base name, as older
	// releases stored it, matches that name in any directory.
	matches := func(path string) bool {
		return path == lastFile || !strings.Contains(lastFile, "/") && strings.HasSuffix(path, "/"+lastFile)
	}
	prefix := ""
	found := 0
	ambiguous := false
	for _, node := range wiring.Nodes {
		if node.Kind != "file" || !matches(node.Path) {
			continue
		}
		found++
		for _, scope := range *scopes {
			if scope.Prefix == "" || node.Path != scope.Prefix && !strings.HasPrefix(node.Path, scope.Prefix+"/") {
				continue
			}
			if found == 1 {
				prefix = scope.Prefix
			} else if prefix != scope.Prefix {
				ambiguous = true
			}
			break
		}
	}
	if found == 0 {
		writeDiagnostic(stderr, "[graft] prompt hook: lastFile %q not found in the graph — skipping scope hint\n", lastFile)
		return ""
	}
	if ambiguous {
		writeDiagnostic(stderr, "[graft] prompt hook: lastFile %q matches more than one scope — skipping scope hint\n", lastFile)
		return ""
	}
	return prefix
}

func askHookGraph(ctx context.Context, root, contextDir, prompt, scope string) (graph.AskResult, bool) {
	_ = graph.EnsureFreshGraph(root, graph.RefreshOptions{Source: sourcefiles.Options{OutDir: contextDir}})
	select {
	case <-ctx.Done():
		return graph.AskResult{}, false
	default:
	}
	loaded, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		return graph.AskResult{}, false
	}
	limit := 3.0
	result, err := graph.Ask(*loaded, prompt, graph.AskOptions{
		Limit: &limit, In: scope, Index: readAskIndex(filepath.Join(contextDir, ".cache", "ask-index.json")),
	})
	if err != nil {
		return graph.AskResult{}, false
	}
	select {
	case <-ctx.Done():
		return graph.AskResult{}, false
	default:
		return result, true
	}
}

func handleHookPrompt(ctx context.Context, input hookInput, root string, stdout, stderr io.Writer) {
	prompt := strings.TrimSpace(input.string("prompt"))
	if savings.Length(prompt) < hookMinPromptChars {
		return
	}
	contextDir := hookContextDir(root)
	scope := ""
	if stats := readHookStats(root); stats != nil && stats.LastFile != nil {
		scope = lastHookFileScope(root, *stats.LastFile, stderr)
	}
	result, ok := hookAsk(ctx, root, contextDir, prompt, scope)
	if !ok {
		return
	}
	agent, _ := input.object("agent")["name"].(string)
	text := ""
	remember := func(session *sessionState) bool {
		session.LastQuery = &prompt
		if agent != "" {
			session.PerAgentQuery[agent] = prompt
		}
		text = relevantHookRetrieval(&result, session, 3)
		return true
	}
	id := hookSessionID(input)
	if err := updateHookSession(root, id, remember); errors.Is(err, context.DeadlineExceeded) {
		// A peer holds the session lock: still answer the prompt, just
		// without recording it.
		session := readHookSession(root, id)
		remember(&session)
	}
	if text != "" {
		emitHookContext(stdout, "UserPromptSubmit", text)
	}
}

func hookSessionStartLines(ctx context.Context, root string) []string {
	now := time.Now()
	home := hookHomeDir()
	env := hosts.Env{Home: home, BakedDir: packageRoot(), Launch: hosts.ServerEntry()}
	lines := make([]string, 0, 2)
	if note := upkeep.ReconcileWiring(root, "", currentVersion(), now,
		func(repo string) ([]string, error) { return upkeep.WiredHostIDs(repo), nil },
		func(repo string, ids []string, options upkeep.WiringOptions) error {
			return upkeep.RewriteWiring(ctx, repo, ids, options, env)
		},
	); note != "" {
		lines = append(lines, note)
	}
	return lines
}

// runHook handles one host hook event. ctx ends when the process is told to
// stop; each event's own budget is derived from it.
func runHook(ctx context.Context, event string, stdin io.Reader, stdout, stderr io.Writer) {
	input := readHookInput(stdin)
	root := hookProjectDir(input)

	switch event {
	case "session-start":
		lines := hookSessionStartLines(ctx, root)
		index, err := os.ReadFile(filepath.Join(hookContextDir(root), "INDEX.md"))
		if err == nil {
			orientation := formatHookOrientation(string(index), 1500, hookStaleBanner(hookIndexFreshness(root)))
			if len(lines) > 0 {
				orientation = strings.Join(lines, "\n") + "\n\n" + orientation
			}
			emitHookContext(stdout, "SessionStart", orientation)
		} else if len(lines) > 0 {
			emitHookContext(stdout, "SessionStart", strings.Join(lines, "\n"))
		}
	case "prompt":
		ctx, cancel := context.WithTimeout(ctx, hookPromptAskTimeout(root))
		defer cancel()
		handleHookPrompt(ctx, input, root, stdout, stderr)
	case "post-edit":
		ctx, cancel := context.WithTimeout(ctx, hookTimeoutDefault)
		defer cancel()
		handleHookPostEdit(ctx, input, root, stdout, stderr)
	case "tool-savings":
		handleHookToolUse(input, root)
	case "cursor-post-tool":
		handleHookCursorPostTool(input, root)
	case "cursor-mcp":
		handleHookCursorMCP(input, root)
	case "cursor-session-end":
		// Cursor configs written by graft init still call this event; it has
		// nothing to do now that session summaries are gone.
	case "stop":
		handleHookStop(input, root)
	case "post-edit-sync":
		ctx, cancel := context.WithTimeout(ctx, hookTimeoutDefault)
		defer cancel()
		handleHookPostEdit(ctx, input, root, stdout, stderr)
		handleHookStop(input, root)
	}
}

func runHookStatusline(stdin io.Reader, stdout io.Writer) {
	input := readHookInput(stdin)
	root := hookProjectDir(input)
	session := readHookSession(root, hookSessionID(input))
	if agent := input.object("agent")["name"]; agent != nil {
		if name, ok := agent.(string); ok && name != "" {
			_, _ = io.WriteString(stdout, renderHookSubagent(name, &session))
			return
		}
	}
	var contextPercent *int
	if window := input.object("context_window"); window != nil {
		if value, ok := window["used_percentage"].(float64); ok {
			rounded := int(value + 0.5)
			contextPercent = &rounded
		}
	}
	_, _ = io.WriteString(stdout, strings.Join(renderHookStatusline(resolveHookStats(root), &session, contextPercent), "\n"))
}
