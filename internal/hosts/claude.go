package hosts

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

const (
	// Repo-level entries name bare graft: the settings file is usually
	// committed, so it must not carry this machine's binary path.
	repoBinary        = "graft"
	statuslineCommand = repoBinary + " _statusline"
	footerRegex       = `graft/[\w./-]+\.md`
)

var (
	allowEntries    = []string{"Bash(graft:*)", "Bash(graft-dev:*)"}
	graftAllowEntry = regexp.MustCompile(`^Bash\((?:graft|npx graft|graft-dev|node dist/cli\.js)(?::|\))`)
)

// ClaudeTargets lists the repo-level Claude Code files.
func ClaudeTargets(repo string) []PlannedWrite {
	target := func(path, what string, kind WriteKind) PlannedWrite {
		return PlannedWrite{HostID: "claude", ID: "claude", Path: path, Scope: ScopeRepo, Kind: kind, What: what}
	}
	return []PlannedWrite{
		target(filepath.Join(repo, ".claude", "settings.json"), "graft statusline + hook blocks", WriteClaude),
		target(filepath.Join(repo, ".claude", "skills", "graft", "SKILL.md"), "graft skill", WriteClaude),
		target(filepath.Join(repo, ".mcp.json"), "mcpServers.graft", WriteMCP),
	}
}

// globalHelpersDir is where the user-level legacy shim lived.
func globalHelpersDir(home string) string {
	return filepath.Join(home, ".claude", "helpers")
}

// ClaudeGlobalTargets lists the user-level Claude Code files under home.
func ClaudeGlobalTargets(home string) []PlannedWrite {
	target := func(id, path string, kind WriteKind, what string) PlannedWrite {
		return PlannedWrite{HostID: "claude", ID: id, Path: path, Scope: ScopeGlobal, Kind: kind, What: what}
	}
	return []PlannedWrite{
		target("claude-global-hooks", filepath.Join(home, ".claude", "settings.json"), WriteHook, "SessionStart / UserPromptSubmit / PostToolUse / Stop / SubagentStop"),
		target("claude-global-mcp", filepath.Join(home, ".claude.json"), WriteMCP, "mcpServers.graft"),
	}
}

type graftBlock struct {
	event  string
	blocks []jsonjs.Value
}

// graftBlocks renders graft's Claude Code hook blocks running binary, a bare
// name or a quoted path, with suffix after each subcommand. Claude Code reads
// a hook's timeout in seconds.
func graftBlocks(binary, suffix string) []graftBlock {
	block := func(matcher, sub string, timeout float64) *jsonjs.Object {
		handler := jsonjs.NewObject()
		handler.Set("type", "command")
		handler.Set("command", binary+" _hook "+sub+suffix)
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
			block("Write|Edit|MultiEdit", "post-edit", 10),
			// Search tools get the nudge; graft's own tools and Bash credit query
			// savings to the calling session. Tool counts come from the
			// transcript when a turn or subagent stops.
			block("Grep|Bash|mcp__graft__.*", "tool-savings", 8),
		}},
		{event: "UserPromptSubmit", blocks: []jsonjs.Value{block("", "prompt", 15)}},
		{event: "SessionStart", blocks: []jsonjs.Value{block("", "session-start", 8)}},
		{event: "Stop", blocks: []jsonjs.Value{block("", "stop", 8)}},
		{event: "SubagentStop", blocks: []jsonjs.Value{block("", "stop", 8)}},
	}
}

// ClaudeWired reports whether repo's .claude/settings.json registers one of
// graft's hooks, in either form, or a legacy shim graft wrote is still there.
func ClaudeWired(repo string) bool {
	if HasLegacyShim(repo, "", nil, false, false) {
		return true
	}
	data, err := os.ReadFile(filepath.Join(repo, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	value, err := jsonjs.Parse(data)
	if err != nil {
		return false
	}
	root, ok := jsonjs.AsObject(value)
	if !ok {
		return false
	}
	hooks, ok := jsonjs.AsObject(mustGet(root, "hooks"))
	if !ok {
		return false
	}
	for _, event := range hooks.Keys() {
		entries, _ := jsonjs.AsArray(mustGet(hooks, event))
		if slices.ContainsFunc(entries, isGraftEntry) {
			return true
		}
	}
	return false
}

// ClaudeMCPWired reports whether Claude Code registers graft's MCP server for
// repo: in the repo's .mcp.json, or in home's .claude.json at user scope or
// for this project.
func ClaudeMCPWired(repo, home string) bool {
	read := func(path string) jsonjs.Value {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		value, _ := jsonjs.Parse(data)
		return value
	}
	has := func(value jsonjs.Value, keys ...string) bool {
		for _, key := range keys {
			object, ok := jsonjs.AsObject(value)
			if !ok || object == nil {
				return false
			}
			if value, ok = object.Get(key); !ok {
				return false
			}
		}
		return true
	}
	if has(read(filepath.Join(repo, ".mcp.json")), "mcpServers", "graft") {
		return true
	}
	user := read(filepath.Join(home, ".claude.json"))
	return has(user, "mcpServers", "graft") || has(user, "projects", repo, "mcpServers", "graft")
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
	return ok && isString && isGraftStatuslineCommand(text)
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

// arrayField returns object[key] as an array: absent or null is empty, and
// anything else reports false so the caller leaves the user's value alone.
func arrayField(object *jsonjs.Object, key string) ([]jsonjs.Value, bool) {
	value, present := object.Get(key)
	if !present || value == nil {
		return nil, true
	}
	return jsonjs.AsArray(value)
}

// ownObject replaces object[key] with a copy it may edit, like
// `object[key] = { ...object[key] }`: absent or null becomes {}, and anything
// else but an object reports false and stays as it is.
func ownObject(object *jsonjs.Object, key string) (*jsonjs.Object, bool) {
	value, present := object.Get(key)
	child := jsonjs.NewObject()
	if present && value != nil {
		current, ok := jsonjs.AsObject(value)
		if !ok {
			return nil, false
		}
		child = current.Clone()
	}
	object.Set(key, child)
	return child, true
}

// mergeHookBlocks replaces graft's hook blocks in merged.hooks, keeping the
// user's blocks. A hooks value or event that is not the expected shape is left
// as it is and reported, where naming the file.
func mergeHookBlocks(merged *jsonjs.Object, binary, suffix, where string) []string {
	hooks, ok := ownObject(merged, "hooks")
	if !ok {
		return []string{where + ": hooks is not an object — graft's hooks were not added."}
	}
	var warnings []string
	for _, entry := range graftBlocks(binary, suffix) {
		prior, ok := arrayField(hooks, entry.event)
		if !ok {
			warnings = append(warnings, where+": hooks."+entry.event+" is not an array — graft's "+entry.event+" hook was not added.")
			continue
		}
		next := make([]jsonjs.Value, 0, len(prior)+len(entry.blocks))
		for _, item := range prior {
			if !isGraftEntry(item) {
				next = append(next, item)
			}
		}
		hooks.Set(entry.event, append(next, entry.blocks...))
	}
	return warnings
}

// MergeGraftSettings merges graft's statusline, hooks, footer regex, and Bash
// allowlist into a repo's .claude/settings.json, keeping the user's entries.
// A field of an unexpected shape is left untouched and reported.
func MergeGraftSettings(existing *jsonjs.Object, statusline bool) (*jsonjs.Object, []string) {
	const where = ".claude/settings.json"
	merged := existing.Clone()
	warnings := make([]string, 0)
	wanted := StatuslineWanted(statusline)
	applyStatusline(merged, "statusLine", wanted,
		"Existing statusLine left untouched (a session allows only one). To use Graft, set its command to `graft _statusline`.", &warnings)
	applyStatusline(merged, "subagentStatusLine", wanted, "Existing subagentStatusLine left untouched.", &warnings)
	warnings = append(warnings, mergeHookBlocks(merged, repoBinary, "", where)...)

	if priorFooter, ok := arrayField(merged, "footerLinksRegexes"); ok {
		footers := make([]jsonjs.Value, 0, len(priorFooter)+1)
		for _, item := range priorFooter {
			if !IsGraftFooterRegex(item) {
				footers = append(footers, item)
			}
		}
		merged.Set("footerLinksRegexes", append(footers, footerRegex))
	} else {
		warnings = append(warnings, where+": footerLinksRegexes is not an array — graft's footer link was not added.")
	}

	permissions, ok := ownObject(merged, "permissions")
	if !ok {
		return merged, append(warnings, where+": permissions is not an object — graft's Bash allowlist was not added.")
	}
	priorAllow, ok := arrayField(permissions, "allow")
	if !ok {
		return merged, append(warnings, where+": permissions.allow is not an array — graft's Bash allowlist was not added.")
	}
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
	// Shims are the legacy Node shims init deleted.
	Shims    []string
	Skill    string
	MCP      ConfigWrite
	Global   []ConfigWrite
	Warnings []string
}

// RunClaudeInit writes the repo's Claude Code layer, plus the user-level copy
// under home unless global is false. Each file is written only when its
// content changes. A settings file that cannot be read or is not a JSON object
// is left alone with a warning.
func RunClaudeInit(repo string, env Env, statusline, global bool) (ClaudeInitResult, error) {
	f := openFiles(repo, env.Home)
	defer f.close()
	targets := ClaudeTargets(repo)
	settingsPath, skillPath, mcpPath := targets[0].Path, targets[1].Path, targets[2].Path
	result := ClaudeInitResult{SettingsPath: settingsPath, Skill: skillPath}
	existing, _, ok, err := f.readJSONObject(settingsPath)
	switch {
	case err != nil:
		result.Warnings = append(result.Warnings, "Could not read "+settingsPath+" ("+err.Error()+") — left unchanged; graft's hooks were not added.")
	case !ok:
		result.Warnings = append(result.Warnings, settingsPath+" is not a valid JSON object — left unchanged; graft's hooks were not added.")
	default:
		merged, warnings := MergeGraftSettings(existing, statusline)
		result.Warnings = append(result.Warnings, warnings...)
		if _, err := f.writeOwnedFile("claude", settingsPath, jsonjs.Stringify(merged, 2)+"\n", 0); err != nil {
			return ClaudeInitResult{}, err
		}
	}
	for _, shim := range legacyClaudeShims(repo) {
		write, ok, err := f.removeLegacyShim("claude", shim)
		switch {
		case err != nil:
			return ClaudeInitResult{}, err
		case !ok:
		case write.Action == ActionKeptForeign:
			result.Warnings = append(result.Warnings, shim+" is not a graft shim — left in place.")
		default:
			result.Shims = append(result.Shims, shim)
		}
	}
	if _, err := f.writeOwnedFile("claude", skillPath, SkillTemplate(), 0); err != nil {
		return ClaudeInitResult{}, err
	}
	if result.MCP, err = f.mergeJSONKey("claude", mcpPath, "mcpServers", env.Launch.object()); err != nil {
		return ClaudeInitResult{}, err
	}
	if global {
		writes, warnings, err := f.installClaudeGlobal(env)
		if err != nil {
			return ClaudeInitResult{}, err
		}
		result.Global = writes
		result.Warnings = append(result.Warnings, warnings...)
	}
	return result, nil
}

// installClaudeGlobal writes the user-level hook entries and MCP registration
// and deletes the legacy user-level shim. The entries carry --user so a run
// can yield to the project's own registration. A config that is not a JSON
// object is reported as skipped; a read or write failure is returned.
func (f *files) installClaudeGlobal(env Env) ([]ConfigWrite, []string, error) {
	targets := ClaudeGlobalTargets(env.Home)
	settingsTarget, mcpTarget := targets[0], targets[1]
	hooks, warnings, err := f.upsertGlobalHooks(settingsTarget, binaryCommand(env.Binary))
	if err != nil {
		return nil, nil, err
	}
	mcp, err := f.mergeJSONKey(mcpTarget.ID, mcpTarget.Path, "mcpServers", env.Launch.object())
	if err != nil {
		return nil, nil, err
	}
	writes := []ConfigWrite{hooks, mcp}
	shim, ok, err := f.removeLegacyShim("claude-global-shim", legacyClaudeGlobalShim(env.Home))
	if err != nil {
		return nil, nil, err
	}
	if ok {
		writes = append(writes, shim)
	}
	return writes, warnings, nil
}

func (f *files) upsertGlobalHooks(target PlannedWrite, binary string) (ConfigWrite, []string, error) {
	root, existed, ok, err := f.readJSONObject(target.Path)
	if err != nil {
		return ConfigWrite{}, nil, err
	}
	if !ok {
		return ConfigWrite{ID: target.ID, Path: target.Path, Action: ActionUnparseable}, nil, nil
	}
	before := jsonjs.Stringify(root, 0)
	merged := root.Clone()
	warnings := mergeHookBlocks(merged, binary, " --user", target.Path)
	if jsonjs.Stringify(merged, 0) == before {
		return ConfigWrite{ID: target.ID, Path: target.Path, Action: ActionUnchanged}, warnings, nil
	}
	if err := f.writeJSON(target.Path, merged); err != nil {
		return ConfigWrite{}, nil, err
	}
	action := ActionCreated
	if existed {
		action = ActionUpdated
	}
	return ConfigWrite{ID: target.ID, Path: target.Path, Action: action}, warnings, nil
}
