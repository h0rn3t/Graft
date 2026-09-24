package hosts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

const (
	statuslineCommand = `node "${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-statusline.cjs"`
	statuslineHelper  = "graft-statusline.cjs"
	footerRegex       = `graft/[\w./-]+\.md`
	repoHelpers       = "${CLAUDE_PROJECT_DIR:-.}/.claude/helpers"
)

var (
	allowEntries    = []string{"Bash(graft:*)", "Bash(npx graft:*)", "Bash(graft-dev:*)"}
	graftAllowEntry = regexp.MustCompile(`^Bash\((?:graft|npx graft|graft-dev|node dist/cli\.js)(?::|\))`)
)

// ClaudeTargets lists the repo-level Claude Code files.
func ClaudeTargets(repo string) []PlannedWrite {
	target := func(path, what string, kind WriteKind) PlannedWrite {
		return PlannedWrite{HostID: "claude", ID: "claude", Path: path, Scope: ScopeRepo, Kind: kind, What: what}
	}
	return []PlannedWrite{
		target(filepath.Join(repo, ".claude", "settings.json"), "graft statusline + hook blocks", WriteClaude),
		target(filepath.Join(repo, ".claude", "helpers", "graft-statusline.cjs"), "statusline shim", WriteClaude),
		target(filepath.Join(repo, ".claude", "helpers", "graft-hooks.cjs"), "hooks shim", WriteClaude),
		target(filepath.Join(repo, ".claude", "skills", "graft", "SKILL.md"), "graft skill", WriteClaude),
		target(filepath.Join(repo, ".mcp.json"), "mcpServers.graft", WriteMCP),
	}
}

// globalHelpersDir is where the user-level shim lives.
func globalHelpersDir(home string) string {
	return filepath.Join(home, ".claude", "helpers")
}

// ClaudeGlobalTargets lists the user-level Claude Code files under home.
func ClaudeGlobalTargets(home string) []PlannedWrite {
	target := func(id, path string, kind WriteKind, what string) PlannedWrite {
		return PlannedWrite{HostID: "claude", ID: id, Path: path, Scope: ScopeGlobal, Kind: kind, What: what}
	}
	return []PlannedWrite{
		target("claude-global-shim", filepath.Join(globalHelpersDir(home), "graft-hooks.cjs"), WriteHook, "hooks shim (user level)"),
		target("claude-global-hooks", filepath.Join(home, ".claude", "settings.json"), WriteHook, "SessionStart / UserPromptSubmit / PostToolUse / Stop"),
		target("claude-global-mcp", filepath.Join(home, ".claude.json"), WriteMCP, "mcpServers.graft"),
	}
}

func hookCommand(sub, helpers string) string {
	return `node "` + helpers + `/graft-hooks.cjs" ` + sub
}

type graftBlock struct {
	event  string
	blocks []jsonjs.Value
}

func graftBlocks(helpers string) []graftBlock {
	block := func(matcher, sub string, timeout float64) *jsonjs.Object {
		handler := jsonjs.NewObject()
		handler.Set("type", "command")
		handler.Set("command", hookCommand(sub, helpers))
		handler.Set("timeout", timeout)
		out := jsonjs.NewObject()
		if matcher != "" {
			out.Set("matcher", matcher)
		}
		out.Set("hooks", []jsonjs.Value{handler})
		return out
	}
	return []graftBlock{
		{event: "PostToolUse", blocks: []jsonjs.Value{
			block("Write|Edit|MultiEdit", "post-edit", 10000),
			block("Bash|mcp__graft__|Read|Grep|Glob", "tool-savings", 8000),
		}},
		{event: "UserPromptSubmit", blocks: []jsonjs.Value{block("", "prompt", 15000)}},
		{event: "SessionStart", blocks: []jsonjs.Value{block("", "session-start", 8000)}},
		{event: "Stop", blocks: []jsonjs.Value{block("", "stop", 8000)}},
	}
}

// IsGraftAllowEntry reports whether a Bash allowlist entry is one graft wrote.
func IsGraftAllowEntry(entry jsonjs.Value) bool {
	return graftAllowEntry.MatchString(jsonjs.String(entry))
}

// IsGraftFooterRegex reports whether a footer regex points at graft's cards.
func IsGraftFooterRegex(value jsonjs.Value) bool {
	return strings.Contains(jsonjs.String(value), "graft/")
}

func isGraftStatusline(value jsonjs.Value) bool {
	object, ok := jsonjs.AsObject(value)
	if !ok {
		return false
	}
	command, ok := object.Get("command")
	text, isString := command.(string)
	return ok && isString && strings.Contains(text, statuslineHelper)
}

func applyStatusline(merged *jsonjs.Object, key string, wanted bool, foreignWarning string, warnings *[]string) {
	current, present := merged.Get(key)
	empty := !jsonjs.Truthy(current, present)
	ours := isGraftStatusline(current)
	switch {
	case !wanted && (empty || ours):
		merged.Delete(key)
	case wanted && (empty || ours):
		statusline := jsonjs.NewObject()
		statusline.Set("type", "command")
		statusline.Set("command", statuslineCommand)
		merged.Set(key, statusline)
	default:
		*warnings = append(*warnings, foreignWarning)
	}
}

// StatuslineWanted reports whether init should write graft's statusLine.
func StatuslineWanted(statusline bool) bool {
	return statusline && !envTruthy("GRAFT_NO_STATUSLINE")
}

func mergeHookBlocks(merged *jsonjs.Object, helpers string) {
	hooksValue, present := merged.Get("hooks")
	hooks := jsonjs.Spread(hooksValue, present && hooksValue != nil)
	merged.Set("hooks", hooks)
	for _, entry := range graftBlocks(helpers) {
		current, _ := hooks.Get(entry.event)
		prior, _ := jsonjs.AsArray(current)
		next := make([]jsonjs.Value, 0, len(prior)+len(entry.blocks))
		for _, item := range prior {
			if !isGraftEntry(item) {
				next = append(next, item)
			}
		}
		hooks.Set(entry.event, append(next, entry.blocks...))
	}
}

// MergeGraftSettings merges graft's statusline, hooks, footer regex, and Bash
// allowlist into a repo's .claude/settings.json, keeping the user's entries.
func MergeGraftSettings(existing *jsonjs.Object, statusline bool) (*jsonjs.Object, []string) {
	merged := existing.Clone()
	warnings := make([]string, 0)
	wanted := StatuslineWanted(statusline)
	applyStatusline(merged, "statusLine", wanted,
		"Existing statusLine left untouched (a session allows only one). To use Graft, point it at .claude/helpers/graft-statusline.cjs.", &warnings)
	applyStatusline(merged, "subagentStatusLine", wanted, "Existing subagentStatusLine left untouched.", &warnings)
	mergeHookBlocks(merged, repoHelpers)

	footerValue, _ := merged.Get("footerLinksRegexes")
	priorFooter, _ := jsonjs.AsArray(footerValue)
	footers := make([]jsonjs.Value, 0, len(priorFooter)+1)
	for _, item := range priorFooter {
		if !IsGraftFooterRegex(item) {
			footers = append(footers, item)
		}
	}
	merged.Set("footerLinksRegexes", append(footers, footerRegex))

	permissionsValue, present := merged.Get("permissions")
	permissions := jsonjs.Spread(permissionsValue, present && permissionsValue != nil)
	merged.Set("permissions", permissions)
	allowValue, _ := permissions.Get("allow")
	priorAllow, _ := jsonjs.AsArray(allowValue)
	allow := make([]jsonjs.Value, 0, len(priorAllow)+len(allowEntries))
	for _, item := range priorAllow {
		if !IsGraftAllowEntry(item) {
			allow = append(allow, item)
		}
	}
	for _, entry := range allowEntries {
		allow = append(allow, entry)
	}
	permissions.Set("allow", allow)
	return merged, warnings
}

// ClaudeInitResult reports the repo-level Claude Code writes.
type ClaudeInitResult struct {
	SettingsPath string
	Shims        []string
	Skill        string
	MCP          ConfigWrite
	Global       []ConfigWrite
	Warnings     []string
}

// RunClaudeInit writes the repo's Claude Code layer, plus the user-level copy
// under home unless global is false.
func RunClaudeInit(repo string, env Env, statusline, global bool) (ClaudeInitResult, error) {
	targets := ClaudeTargets(repo)
	settingsPath, statuslinePath, hooksPath, skillPath, mcpPath := targets[0].Path, targets[1].Path, targets[2].Path, targets[3].Path, targets[4].Path
	if err := os.MkdirAll(filepath.Dir(statuslinePath), 0o755); err != nil {
		return ClaudeInitResult{}, err
	}
	existing := jsonjs.NewObject()
	if data, err := os.ReadFile(settingsPath); err == nil {
		if value, err := jsonjs.Parse(data); err == nil {
			existing = jsonjs.Spread(value, value != nil)
		}
	}
	merged, warnings := MergeGraftSettings(existing, statusline)
	if err := os.WriteFile(settingsPath, []byte(jsonjs.Stringify(merged, 2)+"\n"), 0o644); err != nil {
		return ClaudeInitResult{}, err
	}
	for _, shim := range []struct{ path, content string }{
		{statuslinePath, StatuslineShim(env.BakedDir)},
		{hooksPath, HooksShim(env.BakedDir)},
	} {
		if err := os.WriteFile(shim.path, []byte(shim.content), 0o755); err != nil {
			return ClaudeInitResult{}, err
		}
		if err := os.Chmod(shim.path, 0o755); err != nil {
			return ClaudeInitResult{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		return ClaudeInitResult{}, err
	}
	if err := os.WriteFile(skillPath, []byte(SkillTemplate()), 0o644); err != nil {
		return ClaudeInitResult{}, err
	}
	mcp, err := MergeJSONKey("claude", mcpPath, "mcpServers", env.Launch.object())
	if err != nil {
		return ClaudeInitResult{}, err
	}
	result := ClaudeInitResult{SettingsPath: settingsPath, Shims: []string{statuslinePath, hooksPath}, Skill: skillPath, MCP: mcp, Warnings: warnings}
	if global {
		result.Global = InstallClaudeGlobal(env)
	}
	return result, nil
}

// InstallClaudeGlobal writes the user-level shim, hook entries, and MCP
// registration. Failures are reported as skipped writes, never returned.
func InstallClaudeGlobal(env Env) []ConfigWrite {
	targets := ClaudeGlobalTargets(env.Home)
	shimTarget, settingsTarget, mcpTarget := targets[0], targets[1], targets[2]
	skipped := func(target PlannedWrite) ConfigWrite {
		return ConfigWrite{ID: target.ID, Path: target.Path, Action: ActionUnparseable}
	}
	out := make([]ConfigWrite, 0, 3)
	shim, err := writeOwnedFile(shimTarget.ID, shimTarget.Path, HooksShim(env.BakedDir), 0o755)
	if err != nil {
		shim = skipped(shimTarget)
	}
	out = append(out, shim)
	if shim.Action != ActionUnparseable {
		write, err := upsertGlobalHooks(settingsTarget, filepath.ToSlash(globalHelpersDir(env.Home)))
		if err != nil {
			write = skipped(settingsTarget)
		}
		out = append(out, write)
	}
	mcp, err := MergeJSONKey(mcpTarget.ID, mcpTarget.Path, "mcpServers", env.Launch.object())
	if err != nil {
		mcp = skipped(mcpTarget)
	}
	return append(out, mcp)
}

func upsertGlobalHooks(target PlannedWrite, helpers string) (ConfigWrite, error) {
	root, existed, ok := readJSONObject(target.Path)
	if !ok {
		return ConfigWrite{ID: target.ID, Path: target.Path, Action: ActionUnparseable}, nil
	}
	before := jsonjs.Stringify(root, 0)
	merged := root.Clone()
	mergeHookBlocks(merged, helpers)
	if jsonjs.Stringify(merged, 0) == before {
		return ConfigWrite{ID: target.ID, Path: target.Path, Action: ActionUnchanged}, nil
	}
	if err := writeJSON(target.Path, merged); err != nil {
		return ConfigWrite{}, err
	}
	action := ActionCreated
	if existed {
		action = ActionUpdated
	}
	return ConfigWrite{ID: target.ID, Path: target.Path, Action: action}, nil
}
