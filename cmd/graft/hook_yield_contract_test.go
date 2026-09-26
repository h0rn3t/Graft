package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHookYieldFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v, want nil", path, err)
	}
}

func TestHookUserShimYieldsToProjectRegistration(t *testing.T) {
	userDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", userDir)
	userShim := filepath.Join(userDir, "helpers", "graft-hooks.cjs")
	writeHookYieldFile(t, userShim, "// shim\n")
	codexShim := filepath.Join(t.TempDir(), ".codex", "hooks", "graft", "graft-hooks.cjs")
	writeHookYieldFile(t, codexShim, "// shim\n")

	settings := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"node \"${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-hooks.cjs\" prompt"}]}],` +
		`"Stop":[{"hooks":[{"type":"command","command":"node other-tool.js stop"}]}]}}`
	wired := t.TempDir()
	projectShim := filepath.Join(wired, ".claude", "helpers", "graft-hooks.cjs")
	writeHookYieldFile(t, projectShim, "// shim\n")
	writeHookYieldFile(t, filepath.Join(wired, ".claude", "settings.json"), settings)
	local := t.TempDir()
	writeHookYieldFile(t, filepath.Join(local, ".claude", "helpers", "graft-hooks.cjs"), "// shim\n")
	writeHookYieldFile(t, filepath.Join(local, ".claude", "settings.local.json"), settings)
	shimless := t.TempDir()
	writeHookYieldFile(t, filepath.Join(shimless, ".claude", "settings.json"), settings)
	direct := t.TempDir()
	writeHookYieldFile(t, filepath.Join(direct, ".claude", "settings.json"),
		`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"graft _hook prompt"}]}]}}`)
	bare := t.TempDir()

	tests := []struct {
		name  string
		user  bool
		shim  string
		root  string
		event string
		want  bool
	}{
		{name: "user shim, project registers the event", shim: userShim, root: wired, event: "UserPromptSubmit", want: true},
		{name: "registration in settings.local.json", shim: userShim, root: local, event: "UserPromptSubmit", want: true},
		{name: "project shim itself", shim: projectShim, root: wired, event: "UserPromptSubmit", want: false},
		{name: "event not registered by graft", shim: userShim, root: wired, event: "Stop", want: false},
		{name: "payload without event name", shim: userShim, root: wired, event: "", want: false},
		{name: "project settings without project shim", shim: userShim, root: shimless, event: "UserPromptSubmit", want: true},
		{name: "repository without wiring", shim: userShim, root: bare, event: "UserPromptSubmit", want: false},
		{name: "codex shim", shim: codexShim, root: wired, event: "UserPromptSubmit", want: false},
		{name: "older shim without path", shim: "", root: wired, event: "UserPromptSubmit", want: false},
		{name: "user flag, project registers a direct entry", user: true, root: direct, event: "UserPromptSubmit", want: true},
		{name: "user flag, project registers a shim entry", user: true, root: wired, event: "UserPromptSubmit", want: true},
		{name: "user flag, event not registered by graft", user: true, root: wired, event: "Stop", want: false},
		{name: "user flag, repository without wiring", user: true, root: bare, event: "UserPromptSubmit", want: false},
		{name: "user flag, payload without event name", user: true, root: direct, event: "", want: false},
		{name: "project direct entry itself", root: direct, event: "UserPromptSubmit", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := hookInput{}
			if tt.event != "" {
				input["hook_event_name"] = tt.event
			}
			if got := hookYieldsToProject(tt.user, tt.shim, tt.root, input); got != tt.want {
				t.Errorf("hookYieldsToProject(%t, %q, %q, event=%q) = %t, want %t", tt.user, tt.shim, tt.root, tt.event, got, tt.want)
			}
		})
	}
}

func TestRunHookUserShimStaysSilentWhenProjectIsWired(t *testing.T) {
	userDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", userDir)
	userShim := filepath.Join(userDir, "helpers", "graft-hooks.cjs")
	writeHookYieldFile(t, userShim, "// shim\n")
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	writeHookYieldFile(t, filepath.Join(root, ".claude", "helpers", "graft-hooks.cjs"), "// shim\n")
	writeHookYieldFile(t, filepath.Join(root, ".claude", "settings.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"node \"${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-hooks.cjs\" session-start"}]}]}}`)
	writeHookYieldFile(t, filepath.Join(hookContextDir(root), "INDEX.md"), "# repo map\n")
	payload := `{"session_id":"s","hook_event_name":"SessionStart"}`

	for _, tc := range []struct {
		name string
		shim string
		user bool
		want bool
	}{
		{name: "user shim", shim: userShim, want: false},
		{name: "user flag", user: true, want: false},
		{name: "project shim", shim: filepath.Join(root, ".claude", "helpers", "graft-hooks.cjs"), want: true},
		{name: "older shim without GRAFT_HOOK_SHIM", shim: "", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GRAFT_HOOK_SHIM", tc.shim)
			var out, diagnostic bytes.Buffer
			args := []string{"_hook", "session-start"}
			if tc.user {
				args = append(args, "--user")
			}
			status := runWithInput(args, strings.NewReader(payload), &out, &diagnostic)
			if status != 0 || strings.Contains(out.String(), "repo map") != tc.want {
				t.Errorf("%v (GRAFT_HOOK_SHIM=%q) = (%d, %q, %q), want orientation %t", args, tc.shim, status, out.String(), diagnostic.String(), tc.want)
			}
		})
	}
}
