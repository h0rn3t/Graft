package hosts

import (
	"path/filepath"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// Env is the machine context host writes run in.
type Env struct {
	// Home is the user's home directory.
	Home string
	// Binary is the running graft executable, the shims' first candidate.
	Binary string
	// Launch is the MCP launch command decided for this run.
	Launch Launch
}

// CodexHookTargets lists the Codex hook files, both under ~/.codex and so
// global; empty when Codex is not installed.
func CodexHookTargets(home string) []PlannedWrite {
	base := filepath.Join(home, ".codex")
	if !dirExists(base) {
		return nil
	}
	return []PlannedWrite{
		{HostID: "agents", ID: "codex-hook-shim", Path: filepath.Join(base, "hooks", "graft", "graft-hooks.cjs"), Scope: ScopeGlobal, Kind: WriteHook, What: "post-edit hook shim"},
		{HostID: "agents", ID: "codex-hooks", Path: filepath.Join(base, "hooks.json"), Scope: ScopeGlobal, Kind: WriteHook, What: "SessionStart / UserPromptSubmit / PostToolUse / Stop"},
	}
}

type hookEntry struct {
	event   string
	matcher string
	sub     string
	timeout float64
}

// shellPath quotes path for a hook command line. Double quotes keep the usual
// path readable and work in cmd.exe as well; a path holding a character that
// double quotes leave special in a POSIX shell is single-quoted instead.
func shellPath(path string) string {
	if !strings.ContainsAny(path, "\"$`\n") && !strings.Contains(path, `\\`) {
		return `"` + path + `"`
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// installCodexHooks writes the shared shim and graft's Codex hook entries.
// Codex reads a hook's timeout in seconds.
func (f *files) installCodexHooks(env Env) ([]ConfigWrite, error) {
	targets := CodexHookTargets(env.Home)
	if len(targets) == 0 {
		return nil, nil
	}
	shimPath, configPath := targets[0].Path, targets[1].Path
	shim, err := f.writeOwnedFile("codex-hook-shim", shimPath, HooksShim(env.Binary), 0o755)
	if err != nil {
		return nil, err
	}
	entries := []hookEntry{
		{event: "SessionStart", matcher: "startup|resume|compact", sub: "session-start", timeout: 10},
		{event: "UserPromptSubmit", sub: "prompt", timeout: 15},
		{event: "PostToolUse", matcher: "apply_patch|Write|Edit|MultiEdit", sub: "post-edit", timeout: 10},
		{event: "Stop", sub: "stop", timeout: 10},
	}
	write, err := f.mergeHookConfig("codex-hooks", configPath, false, entries, func(entry hookEntry) *jsonjs.Object {
		handler := jsonjs.NewObject()
		handler.Set("type", "command")
		handler.Set("command", "node "+shellPath(shimPath)+" "+entry.sub)
		handler.Set("timeout", entry.timeout)
		out := jsonjs.NewObject()
		if entry.matcher != "" {
			out.Set("matcher", entry.matcher)
		}
		out.Set("hooks", []jsonjs.Value{handler})
		return out
	})
	if err != nil {
		return nil, err
	}
	return []ConfigWrite{shim, write}, nil
}

// cursorHookScript is the shim as Cursor's project hooks name it: they run
// from the project root, so the committed config carries no absolute path.
const cursorHookScript = ".cursor/hooks/graft-hooks.cjs"

// CursorHookTargets lists Cursor's repo-local hook files.
func CursorHookTargets(repo string) []PlannedWrite {
	return []PlannedWrite{
		{HostID: "cursor", ID: "cursor-hook-shim", Path: filepath.Join(repo, filepath.FromSlash(cursorHookScript)), Scope: ScopeRepo, Kind: WriteHook, What: "session-scoring hook shim"},
		{HostID: "cursor", ID: "cursor-hooks", Path: filepath.Join(repo, ".cursor", "hooks.json"), Scope: ScopeRepo, Kind: WriteHook, What: "postToolUse / afterMCPExecution / sessionEnd"},
	}
}

// installCursorHooks writes the shim and graft's Cursor project hook entries.
func (f *files) installCursorHooks(repo string, env Env) ([]ConfigWrite, error) {
	targets := CursorHookTargets(repo)
	shimPath, configPath := targets[0].Path, targets[1].Path
	shim, err := f.writeOwnedFile("cursor-hook-shim", shimPath, HooksShim(env.Binary), 0o755)
	if err != nil {
		return nil, err
	}
	entries := []hookEntry{
		{event: "postToolUse", matcher: "Read|Grep|Glob|Search|Shell", sub: "cursor-post-tool"},
		{event: "afterMCPExecution", sub: "cursor-mcp"},
		{event: "sessionEnd", sub: "cursor-session-end"},
	}
	write, err := f.mergeHookConfig("cursor-hooks", configPath, true, entries, func(entry hookEntry) *jsonjs.Object {
		out := jsonjs.NewObject()
		if entry.matcher != "" {
			out.Set("matcher", entry.matcher)
		}
		out.Set("command", `node "`+cursorHookScript+`" `+entry.sub)
		return out
	})
	if err != nil {
		return nil, err
	}
	return []ConfigWrite{shim, write}, nil
}

// mergeHookConfig replaces graft's entries in a hooks.json, keeping foreign
// entries, and leaves a config of the wrong shape untouched. withVersion adds
// Cursor's schema version when the file has none.
func (f *files) mergeHookConfig(id, path string, withVersion bool, entries []hookEntry, render func(hookEntry) *jsonjs.Object) (ConfigWrite, error) {
	skipped := ConfigWrite{ID: id, Path: path, Action: ActionUnparseable}
	root, existed, ok, err := f.readJSONObject(path)
	if err != nil {
		return ConfigWrite{}, err
	}
	if !ok {
		return skipped, nil
	}
	before := jsonjs.Stringify(root, 0)
	if withVersion && !root.Has("version") {
		root.Set("version", 1.0)
	}
	hooks, ok := ensureObject(root, "hooks")
	if !ok {
		return skipped, nil
	}
	for _, entry := range entries {
		current, present := hooks.Get(entry.event)
		prior, isArray := jsonjs.AsArray(current)
		if present && !isArray {
			return skipped, nil
		}
		next := make([]jsonjs.Value, 0, len(prior)+1)
		for _, item := range prior {
			if !isGraftEntry(item) {
				next = append(next, item)
			}
		}
		hooks.Set(entry.event, append(next, render(entry)))
	}
	if jsonjs.Stringify(root, 0) == before {
		return ConfigWrite{ID: id, Path: path, Action: ActionUnchanged}, nil
	}
	if err := f.writeJSON(path, root); err != nil {
		return ConfigWrite{}, err
	}
	action := ActionCreated
	if existed {
		action = ActionUpdated
	}
	return ConfigWrite{ID: id, Path: path, Action: action}, nil
}

// AntigravitySkillTargets lists Antigravity's shared skill, a global write.
func AntigravitySkillTargets(home string) []PlannedWrite {
	return []PlannedWrite{{
		HostID: "antigravity", ID: "antigravity-skill", Path: filepath.Join(home, ".gemini", "skills", "graft", "SKILL.md"),
		Scope: ScopeGlobal, Kind: WriteSkill, What: "graft skill (shared)",
	}}
}

// installAntigravitySkill writes graft's skill into ~/.gemini/skills.
func (f *files) installAntigravitySkill(home string) ([]ConfigWrite, error) {
	target := AntigravitySkillTargets(home)[0]
	write, err := f.writeOwnedFile(target.ID, target.Path, SkillTemplate(), 0)
	if err != nil {
		return nil, err
	}
	return []ConfigWrite{write}, nil
}
