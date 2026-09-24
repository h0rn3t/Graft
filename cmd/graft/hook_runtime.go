package main

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/hosts"
	"github.com/NanoNets/context-graph-engine/internal/savings"
	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
	"github.com/NanoNets/context-graph-engine/internal/telemetry"
	"github.com/NanoNets/context-graph-engine/internal/upkeep"
)

const (
	hookMinPromptChars  = 12
	hookTimeoutDefault  = 8 * time.Second
	hookTimeoutOverhead = 2 * time.Second
	hookTimeoutFloor    = 4 * time.Second
)

var hookPatchFilePattern = regexp.MustCompile(`(?m)^\*\*\*\s+(?:Add|Update)\s+File:\s+(.+?)\s*$`)

var (
	hookHomeDir = homeDir
	hookAsk     = askHookGraph
	hookCheck   = func(root, contextDir string) int {
		result, err := graph.CheckGraph(root, contextDir)
		if err != nil {
			return 0
		}
		return len(result.Added) + len(result.Changed) + len(result.Removed)
	}
	hookStartSync = startHookSync
)

type hookInput map[string]any

func readHookInput(stdin io.Reader) hookInput {
	input := hookInput{}
	if stdin == nil {
		return input
	}
	data, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil || jsonv2.Unmarshal(data, &input) != nil {
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
	lastFile := filepath.Base(file)
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

func sampleHookTurnCost(input hookInput, root string) {
	path := input.string("transcript_path")
	if path == "" {
		return
	}
	billing := lastHookTurnBilling(path)
	if billing == nil {
		return
	}
	id := hookSessionID(input)
	session := readHookSession(root, id)
	if session.LastBillingUUID != nil && *session.LastBillingUUID == billing.UUID {
		return
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
	_ = writeHookSession(root, id, session)
}

func countHookTallyTurn(input hookInput, root string) {
	id := hookSessionID(input)
	session := readHookSession(root, id)
	if session.TurnUsedGraft == nil || !*session.TurnUsedGraft {
		return
	}
	turn := lastHookAssistantTurn(input.string("transcript_path"))
	if turn == nil || (session.LastTallyUUID != nil && *session.LastTallyUUID == turn.UUID) {
		session.TurnUsedGraft = new(false)
		_ = writeHookSession(root, id, session)
		return
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
	session.TurnUsedGraft = new(false)
	_ = writeHookSession(root, id, session)
}

func hookSyncBuild(root string) error {
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
	if err := command.Start(); err != nil {
		return false
	}
	_ = command.Process.Release()
	return true
}

func handleHookStop(input hookInput, root string) {
	sampleHookTurnCost(input, root)
	countHookTallyTurn(input, root)
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
	if err := hookSyncBuild(root); err != nil {
		_, _ = patchHookStats(root, hookStatsPatch{Syncing: new(false)})
		graph.ReleaseLock(hookCacheDir(root))
		return
	}
	wiring, err := graph.Read(graph.WiringPath(hookContextDir(root)))
	if err != nil {
		_, _ = patchHookStats(root, hookStatsPatch{Syncing: new(false)})
		graph.ReleaseLock(hookCacheDir(root))
		return
	}
	stats := computeHookStats(*wiring)
	dirty, stale, syncing, synced := false, 0, false, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	_, _ = patchHookStats(root, hookStatsPatch{
		NodeCount: &stats.NodeCount, EdgeCount: &stats.EdgeCount, Languages: &stats.Languages,
		TotalCount: &stats.TotalCount, ReadyCount: &stats.ReadyCount, Dirty: &dirty, StaleCount: &stale,
		Syncing: &syncing, SyncedAtSet: true, SyncedAt: &synced,
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

func hookInstalledTimeout(root, event string) (int, bool) {
	user := os.Getenv("CLAUDE_CONFIG_DIR")
	if user == "" {
		user = filepath.Join(homeDir(), ".claude")
	}
	smallest := 0
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
				if strings.Contains(hook.Command, "graft-hooks.cjs") && hook.Timeout != nil {
					timeout := int(*hook.Timeout)
					if smallest == 0 || timeout < smallest {
						smallest = timeout
					}
				}
			}
		}
	}
	return smallest, smallest > 0
}

func hookPromptAskTimeout(root string) time.Duration {
	installed, ok := hookInstalledTimeout(root, "UserPromptSubmit")
	if !ok {
		return hookTimeoutDefault - hookTimeoutOverhead
	}
	return max(hookTimeoutFloor, time.Duration(installed)*time.Millisecond-hookTimeoutOverhead)
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
	prefix := ""
	found := 0
	ambiguous := false
	for _, node := range wiring.Nodes {
		if node.Kind != "file" || (node.Path != lastFile && !strings.HasSuffix(node.Path, "/"+lastFile)) {
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
	id := hookSessionID(input)
	session := readHookSession(root, id)
	session.LastQuery = &prompt
	if agent := input.object("agent")["name"]; agent != nil {
		if name, ok := agent.(string); ok && name != "" {
			session.PerAgentQuery[name] = prompt
		}
	}
	if text := relevantHookRetrieval(&result, &session, 3); text != "" {
		emitHookContext(stdout, "UserPromptSubmit", text)
	}
	_ = writeHookSession(root, id, session)
}

func hookSessionStartLines(root string) []string {
	now := time.Now()
	home := hookHomeDir()
	env := hosts.Env{Home: home, BakedDir: packageRoot(), Launch: hosts.ServerEntry()}
	lines := make([]string, 0, 2)
	if note := upkeep.ReconcileWiring(root, "", currentVersion(), now,
		func(repo string) ([]string, error) { return upkeep.WiredHostIDs(repo), nil },
		func(repo string, ids []string, options upkeep.WiringOptions) error {
			return upkeep.RewriteWiring(context.Background(), repo, ids, options, env)
		},
	); note != "" {
		lines = append(lines, note)
	}
	if home != "" {
		lines = append(lines, upkeep.StartupLines(currentVersion(), home)...)
	}
	return lines
}

func runHook(event string, stdin io.Reader, stdout, stderr io.Writer) {
	input := readHookInput(stdin)
	root := hookProjectDir(input)

	switch event {
	case "session-start":
		flushClosedHookSessions(root, time.Now(), hookHomeDir(), "claude-code")
		lines := hookSessionStartLines(root)
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
		ctx, cancel := context.WithTimeout(context.Background(), hookPromptAskTimeout(root))
		defer cancel()
		handleHookPrompt(ctx, input, root, stdout, stderr)
	case "post-edit":
		ctx, cancel := context.WithTimeout(context.Background(), hookTimeoutDefault)
		defer cancel()
		handleHookPostEdit(ctx, input, root, stdout, stderr)
	case "tool-savings":
		handleHookToolUse(input, root)
	case "cursor-post-tool":
		handleHookCursorPostTool(input, root)
	case "cursor-mcp":
		handleHookCursorMCP(input, root)
	case "cursor-session-end":
		summarizeHookSession(root, hookSessionID(input), hookHomeDir(), "cursor")
	case "stop":
		handleHookStop(input, root)
	case "post-edit-sync":
		ctx, cancel := context.WithTimeout(context.Background(), hookTimeoutDefault)
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

func runHookInstall() {
	home := hookHomeDir()
	if telemetry.TrackInstallIfNew(telemetry.Context{Home: home, Version: currentVersion()}, os.Getenv("npm_config_global") == "true") {
		telemetry.FlushInBackground(home)
	}
}
