package hosts

import (
	"testing"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

func TestIsGraftHookCommand(t *testing.T) {
	for _, tt := range []struct {
		command string
		want    bool
	}{
		{"graft _hook post-edit", true},
		{`"/home/me/go/bin/graft" _hook session-start --user`, true},
		{`"C:/Users/me/go/bin/graft.exe" _hook stop`, true},
		{`'/home/it'\''s/graft' _hook prompt`, true},
		{"graft-dev _hook prompt", true},
		{`/tmp/go-build/graft.test	_hook stop`, true},
		{`node "${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-hooks.cjs" post-edit`, true},
		{`node ".cursor/hooks/graft-hooks.cjs" cursor-mcp`, true},
		{"graft _statusline", false},
		{"graft _hooks post-edit", false},
		{"echo _hook", true},
		{"echo user", false},
		{"my-linter --fix _hook", false},
		{`"graft _hook"`, false},
		{"", false},
	} {
		if got := IsGraftHookCommand(tt.command); got != tt.want {
			t.Errorf("IsGraftHookCommand(%q) = %t, want %t", tt.command, got, tt.want)
		}
	}
}

func TestIsGraftStatuslineCommand(t *testing.T) {
	for _, tt := range []struct {
		command string
		want    bool
	}{
		{"graft _statusline", true},
		{`"/Users/me/go/bin/graft" _statusline`, true},
		{`node "${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-statusline.cjs"`, true},
		{"graft _hook stop", false},
		{"~/.claude/statusline.sh", false},
		{"", false},
	} {
		if got := isGraftStatuslineCommand(tt.command); got != tt.want {
			t.Errorf("isGraftStatuslineCommand(%q) = %t, want %t", tt.command, got, tt.want)
		}
	}
}

func TestIsGraftEntry(t *testing.T) {
	for _, tt := range []struct {
		entry string
		want  bool
	}{
		{`{"matcher":"Write","hooks":[{"type":"command","command":"graft _hook post-edit"}]}`, true},
		{`{"hooks":[{"type":"command","command":"echo user"},{"type":"command","command":"\"/bin/graft\" _hook stop --user"}]}`, true},
		{`{"matcher":"Shell","command":"graft _hook cursor-post-tool"}`, true},
		{`{"hooks":[{"type":"command","command":"node \"/home/me/.claude/helpers/graft-hooks.cjs\" stop"}]}`, true},
		{`{"hooks":[{"type":"command","command":"echo user"}]}`, false},
		{`{"command":"graft _statusline"}`, false},
		{`"graft _hook stop"`, false},
		{`null`, false},
	} {
		value, err := jsonjs.Parse([]byte(tt.entry))
		if err != nil {
			t.Fatalf("jsonjs.Parse(%s) error = %v, want nil", tt.entry, err)
		}
		if got := isGraftEntry(value); got != tt.want {
			t.Errorf("isGraftEntry(%s) = %t, want %t", tt.entry, got, tt.want)
		}
	}
}

func TestHasLegacyShim(t *testing.T) {
	const shim = "spawnSync(binary, ['_hook', process.argv[2]])"
	all := []string{"agents", "claude", "cursor"}
	for _, tt := range []struct {
		name          string
		path          func(repo, home string) string
		content       string
		ids           []string
		hooks, global bool
		want          bool
	}{
		{name: "repo hooks shim", path: func(repo, _ string) string { return legacyClaudeShims(repo)[0] }, content: shim, want: true},
		{name: "repo statusline shim", path: func(repo, _ string) string { return legacyClaudeShims(repo)[1] }, content: "spawnSync(binary, ['_statusline'])", want: true},
		{name: "TypeScript-era shim", path: func(repo, _ string) string { return legacyClaudeShims(repo)[0] }, content: "pathToFileURL(cli)", want: true},
		{name: "foreign file at a shim path", path: func(repo, _ string) string { return legacyClaudeShims(repo)[0] }, content: "console.log('mine')", ids: all, hooks: true, global: true, want: false},
		{name: "cursor shim, cursor wired with hooks", path: func(repo, _ string) string { return legacyCursorShim(repo) }, content: shim, ids: all, hooks: true, want: true},
		{name: "cursor shim without hooks", path: func(repo, _ string) string { return legacyCursorShim(repo) }, content: shim, ids: all, want: false},
		{name: "cursor shim, cursor not wired", path: func(repo, _ string) string { return legacyCursorShim(repo) }, content: shim, ids: []string{"claude"}, hooks: true, global: true, want: false},
		{name: "user-level shim, claude wired with global", path: func(_, home string) string { return legacyClaudeGlobalShim(home) }, content: shim, ids: all, global: true, want: true},
		{name: "user-level shim without global", path: func(_, home string) string { return legacyClaudeGlobalShim(home) }, content: shim, ids: all, hooks: true, want: false},
		{name: "user-level shim, claude not wired", path: func(_, home string) string { return legacyClaudeGlobalShim(home) }, content: shim, ids: []string{"cursor"}, hooks: true, global: true, want: false},
		{name: "codex shim, agents wired with hooks and global", path: func(_, home string) string { return legacyCodexShim(home) }, content: shim, ids: all, hooks: true, global: true, want: true},
		{name: "codex shim without hooks", path: func(_, home string) string { return legacyCodexShim(home) }, content: shim, ids: all, global: true, want: false},
		{name: "codex shim, agents not wired", path: func(_, home string) string { return legacyCodexShim(home) }, content: shim, ids: []string{"claude"}, hooks: true, global: true, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, home := t.TempDir(), t.TempDir()
			writeTestFile(t, tt.path(repo, home), tt.content)
			if got := HasLegacyShim(repo, home, tt.ids, tt.hooks, tt.global); got != tt.want {
				t.Errorf("HasLegacyShim(%v, hooks=%t, global=%t) with %s = %t, want %t", tt.ids, tt.hooks, tt.global, tt.name, got, tt.want)
			}
		})
	}
	if HasLegacyShim(t.TempDir(), t.TempDir(), all, true, true) {
		t.Errorf("HasLegacyShim(empty repo and home) = true, want false")
	}
}
