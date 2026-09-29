package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

const testShim = "#!/usr/bin/env node\nconst result = spawnSync(binary, ['_hook', process.argv[2]], { stdio: 'inherit', env });\n"

// hookCommands lists every hook command in a settings or hooks.json file, in
// file order.
func hookCommands(t *testing.T, path string) []string {
	t.Helper()
	value, err := jsonjs.Parse([]byte(readTestFile(t, path)))
	if err != nil {
		t.Fatalf("jsonjs.Parse(%s) error = %v, want nil", path, err)
	}
	root, _ := jsonjs.AsObject(value)
	hooks, _ := jsonjs.AsObject(mustGet(root, "hooks"))
	var commands []string
	for _, event := range hooks.Keys() {
		entries, _ := jsonjs.AsArray(mustGet(hooks, event))
		for _, entry := range entries {
			object, _ := jsonjs.AsObject(entry)
			if command, ok := mustGet(object, "command").(string); ok {
				commands = append(commands, command)
			}
			handlers, _ := jsonjs.AsArray(mustGet(object, "hooks"))
			for _, handler := range handlers {
				handler, _ := jsonjs.AsObject(handler)
				if command, ok := mustGet(handler, "command").(string); ok {
					commands = append(commands, command)
				}
			}
		}
	}
	return commands
}

func TestRunClaudeInitWritesDirectEntries(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	env := Env{Home: home, Binary: "/pkg/graft", Launch: binLaunch}
	if _, err := RunClaudeInit(repo, env, true, true); err != nil {
		t.Fatalf("RunClaudeInit() error = %v, want nil", err)
	}

	repoSettings := filepath.Join(repo, ".claude", "settings.json")
	for _, command := range hookCommands(t, repoSettings) {
		if !strings.HasPrefix(command, "graft _hook ") || strings.HasSuffix(command, "--user") {
			t.Errorf("RunClaudeInit() repo hook command = %q, want bare `graft _hook <sub>`", command)
		}
	}
	if got := readTestFile(t, repoSettings); !strings.Contains(got, `"command": "graft _statusline"`) || strings.Contains(got, "node") {
		t.Errorf("RunClaudeInit() repo settings = %s, want the direct statusline and no node", got)
	}
	userSettings := filepath.Join(home, ".claude", "settings.json")
	commands := hookCommands(t, userSettings)
	if len(commands) == 0 {
		t.Fatalf("RunClaudeInit() user settings has no hook commands, want graft's")
	}
	for _, command := range commands {
		if !strings.HasPrefix(command, `"/pkg/graft" _hook `) || !strings.HasSuffix(command, " --user") {
			t.Errorf("RunClaudeInit() user hook command = %q, want `\"/pkg/graft\" _hook <sub> --user`", command)
		}
	}
	for _, shim := range append(legacyClaudeShims(repo), legacyClaudeGlobalShim(home)) {
		if _, err := os.Lstat(shim); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("RunClaudeInit() left %s (err = %v), want no shim written", shim, err)
		}
	}
}

func TestMergeGraftSettingsReplacesBothForms(t *testing.T) {
	const settings = `{"statusLine":{"type":"command","command":"node \"${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-statusline.cjs\""},
	"hooks":{"Stop":[
		{"hooks":[{"type":"command","command":"echo before"}]},
		{"hooks":[{"type":"command","command":"node \"${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-hooks.cjs\" stop"}]},
		{"hooks":[{"type":"command","command":"graft _hook stop","timeout":8}]},
		{"hooks":[{"type":"command","command":"echo after"}]}
	]}}`
	value, err := jsonjs.Parse([]byte(settings))
	if err != nil {
		t.Fatal(err)
	}
	existing, _ := jsonjs.AsObject(value)
	merged, warnings := MergeGraftSettings(existing, true)
	if len(warnings) != 0 {
		t.Errorf("MergeGraftSettings(mixed forms) warnings = %v, want none", warnings)
	}
	hooks, _ := jsonjs.AsObject(mustGet(merged, "hooks"))
	var got []string
	entries, _ := jsonjs.AsArray(mustGet(hooks, "Stop"))
	for _, entry := range entries {
		object, _ := jsonjs.AsObject(entry)
		handlers, _ := jsonjs.AsArray(mustGet(object, "hooks"))
		handler, _ := jsonjs.AsObject(handlers[0])
		got = append(got, mustGet(handler, "command").(string))
	}
	if want := []string{"echo before", "echo after", "graft _hook stop"}; !slices.Equal(got, want) {
		t.Errorf("MergeGraftSettings(mixed forms) Stop = %q, want %q", got, want)
	}
	statusline, _ := jsonjs.AsObject(mustGet(merged, "statusLine"))
	if got := mustGet(statusline, "command"); got != "graft _statusline" {
		t.Errorf("MergeGraftSettings(legacy statusline) statusLine.command = %v, want graft _statusline", got)
	}

	foreign := jsonjs.NewObject()
	own := jsonjs.NewObject()
	own.Set("type", "command")
	own.Set("command", "~/.claude/statusline.sh")
	foreign.Set("statusLine", own)
	merged, warnings = MergeGraftSettings(foreign, true)
	statusline, _ = jsonjs.AsObject(mustGet(merged, "statusLine"))
	if got := mustGet(statusline, "command"); got != "~/.claude/statusline.sh" || len(warnings) == 0 {
		t.Errorf("MergeGraftSettings(foreign statusline) = (%v, %v), want it kept with a warning", got, warnings)
	}
}

func TestInstallCodexHooksNamesTheBinary(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := openFiles(t.TempDir(), home)
	defer f.close()
	if _, err := f.installCodexHooks(Env{Home: home, Binary: "/opt/go bin/graft", Launch: binLaunch}); err != nil {
		t.Fatalf("installCodexHooks() error = %v, want nil", err)
	}
	commands := hookCommands(t, filepath.Join(home, ".codex", "hooks.json"))
	if len(commands) == 0 {
		t.Fatalf("installCodexHooks() wrote no hook commands, want graft's")
	}
	for _, command := range commands {
		if !strings.HasPrefix(command, `"/opt/go bin/graft" _hook `) || strings.Contains(command, "--user") {
			t.Errorf("installCodexHooks() command = %q, want the quoted binary and no --user", command)
		}
	}
}

func TestInitRemovesLegacyShims(t *testing.T) {
	for _, tt := range []struct {
		name        string
		content     string
		wantRemoved bool
	}{
		{name: "graft shim", content: testShim, wantRemoved: true},
		{name: "TypeScript-era shim", content: "import { pathToFileURL } from 'url';\n", wantRemoved: true},
		{name: "foreign file", content: "console.log('mine')\n", wantRemoved: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, home := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
				t.Fatal(err)
			}
			shims := append(legacyClaudeShims(repo), legacyClaudeGlobalShim(home), legacyCodexShim(home), legacyCursorShim(repo))
			for _, shim := range shims {
				writeTestFile(t, shim, tt.content)
			}
			env := Env{Home: home, Binary: "/pkg/graft", Launch: binLaunch}
			claude, err := RunClaudeInit(repo, env, true, true)
			if err != nil {
				t.Fatalf("RunClaudeInit() error = %v, want nil", err)
			}
			hosts, err := RunHostsInit(repo, env, InitOptions{Agents: []string{"agents", "cursor"}, Hooks: true, Global: true})
			if err != nil {
				t.Fatalf("RunHostsInit() error = %v, want nil", err)
			}
			for _, shim := range shims {
				_, err := os.Lstat(shim)
				if removed := errors.Is(err, os.ErrNotExist); removed != tt.wantRemoved {
					t.Errorf("init left %s removed = %t, want %t", shim, removed, tt.wantRemoved)
				}
			}
			wantAction := ActionKeptForeign
			if tt.wantRemoved {
				wantAction = ActionRemoved
			}
			for _, id := range []string{"codex-hook-shim", "cursor-hook-shim"} {
				if !slices.ContainsFunc(hosts.Hooks, func(write ConfigWrite) bool { return write.ID == id && write.Action == wantAction }) {
					t.Errorf("RunHostsInit() hooks = %+v, want %s %s", hosts.Hooks, id, wantAction)
				}
			}
			if !slices.ContainsFunc(claude.Global, func(write ConfigWrite) bool { return write.ID == "claude-global-shim" && write.Action == wantAction }) {
				t.Errorf("RunClaudeInit() global = %+v, want claude-global-shim %s", claude.Global, wantAction)
			}
			if tt.wantRemoved {
				if !slices.Equal(claude.Shims, legacyClaudeShims(repo)) {
					t.Errorf("RunClaudeInit() shims = %q, want both repo shims reported removed", claude.Shims)
				}
				if _, err := os.Lstat(filepath.Join(home, ".codex", "hooks")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("init left ~/.codex/hooks (err = %v), want the emptied shim directories pruned", err)
				}
			} else if len(claude.Shims) != 0 || !slices.ContainsFunc(claude.Warnings, func(w string) bool { return strings.Contains(w, "not a graft shim") }) {
				t.Errorf("RunClaudeInit() = (shims %q, warnings %q), want foreign shims kept with a warning", claude.Shims, claude.Warnings)
			}
		})
	}

	repo := t.TempDir()
	claude, err := RunClaudeInit(repo, Env{Home: t.TempDir(), Launch: binLaunch}, true, true)
	if err != nil || len(claude.Shims) != 0 || slices.ContainsFunc(claude.Global, func(write ConfigWrite) bool { return write.ID == "claude-global-shim" }) {
		t.Errorf("RunClaudeInit(no shims) = (%+v, %v), want no shim results", claude, err)
	}
}

func TestRetractRemovesBothForms(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(repo, ".claude", "settings.json"), `{"statusLine":{"type":"command","command":"graft _statusline"},
	"hooks":{"Stop":[
		{"hooks":[{"type":"command","command":"echo user"}]},
		{"hooks":[{"type":"command","command":"graft _hook stop"}]},
		{"hooks":[{"type":"command","command":"node \"${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-hooks.cjs\" stop"}]}
	]}}`)
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"), `{"statusLine":{"type":"command","command":"~/.claude/statusline.sh"},
	"hooks":{"Stop":[{"hooks":[{"type":"command","command":"\"/pkg/graft\" _hook stop --user"}]}]}}`)
	writeTestFile(t, filepath.Join(home, ".codex", "hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"\"/pkg/graft\" _hook stop"}]}]}}`)
	shims := []string{legacyClaudeShims(repo)[0], legacyClaudeGlobalShim(home), legacyCodexShim(home)}
	for _, shim := range shims {
		writeTestFile(t, shim, testShim)
	}
	foreignShim := legacyClaudeShims(repo)[1]
	writeTestFile(t, foreignShim, "console.log('mine')\n")

	Retract(repo, Env{Home: home, Launch: binLaunch}, RetractOptions{Apply: true, Global: true})

	if got := readTestFile(t, filepath.Join(repo, ".claude", "settings.json")); strings.Contains(got, "_hook") || strings.Contains(got, "_statusline") || !strings.Contains(got, "echo user") {
		t.Errorf("Retract() repo settings = %s, want graft entries gone and the user hook kept", got)
	}
	if got := readTestFile(t, filepath.Join(home, ".claude", "settings.json")); strings.Contains(got, "_hook") || !strings.Contains(got, "statusline.sh") {
		t.Errorf("Retract() user settings = %s, want graft hooks gone and the foreign statusline kept", got)
	}
	if got, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json")); err == nil && strings.Contains(string(got), "_hook") {
		t.Errorf("Retract() codex hooks = %s, want graft entries gone", got)
	}
	for _, shim := range shims {
		if _, err := os.Lstat(shim); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Retract() left %s (err = %v), want the legacy shim deleted", shim, err)
		}
	}
	if _, err := os.Lstat(foreignShim); err != nil {
		t.Errorf("Retract() removed %s (err = %v), want a file graft did not write kept", foreignShim, err)
	}
}

// TestRetractRemovesCursorHooks checks Cursor's repo-local hook config: graft's
// entries and the schema version it adds go, a user's entries stay, and a
// config left without entries is deleted.
func TestRetractRemovesCursorHooks(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	hooks := filepath.Join(repo, ".cursor", "hooks.json")
	writeTestFile(t, hooks, `{"version":1,"hooks":{
		"postToolUse":[
			{"matcher":"Read","command":"graft _hook cursor-post-tool"},
			{"matcher":"Shell","command":"echo mine"}
		],
		"sessionEnd":[{"command":"graft _hook cursor-session-end"}]}}`)
	writeTestFile(t, legacyCursorShim(repo), testShim)

	Retract(repo, Env{Home: home, Launch: binLaunch}, RetractOptions{Apply: true})

	got := readTestFile(t, hooks)
	if strings.Contains(got, "_hook") || !strings.Contains(got, `"echo mine"`) {
		t.Errorf("Retract() cursor hooks = %s, want graft entries gone and the user entry kept", got)
	}
	if _, err := os.Lstat(legacyCursorShim(repo)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Retract() left the Cursor shim (err = %v), want the legacy shim deleted", err)
	}

	empty := t.TempDir()
	only := filepath.Join(empty, ".cursor", "hooks.json")
	writeTestFile(t, only, `{"version":1,"hooks":{"sessionEnd":[{"command":"graft _hook cursor-session-end"}]}}`)
	Retract(empty, Env{Home: t.TempDir(), Launch: binLaunch}, RetractOptions{Apply: true})
	if _, err := os.Lstat(only); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Retract() left %s (err = %v), want a hooks config of graft entries deleted", only, err)
	}
}

// TestRetractKeepsForeignHooksConfigKeys checks that retraction takes back only
// what graft wrote: a schema version the user chose themselves, and any other
// key, survive the removal of graft's entries.
func TestRetractKeepsForeignHooksConfigKeys(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	hooks := filepath.Join(repo, ".cursor", "hooks.json")
	writeTestFile(t, hooks, `{"version":2,"hooks":{"sessionEnd":[{"command":"graft _hook cursor-session-end"}]},"mine":true}`)

	Retract(repo, Env{Home: home, Launch: binLaunch}, RetractOptions{Apply: true})

	value, err := jsonjs.Parse([]byte(readTestFile(t, hooks)))
	if err != nil {
		t.Fatalf("jsonjs.Parse(cursor hooks) error = %v, want nil", err)
	}
	root, _ := jsonjs.AsObject(value)
	if _, ok := root.Get("hooks"); ok {
		t.Errorf("Retract() cursor hooks = %s, want graft's hooks object gone", readTestFile(t, hooks))
	}
	if mine, _ := root.Get("mine"); mine != true {
		t.Errorf("Retract() cursor hooks = %s, want the foreign key kept", readTestFile(t, hooks))
	}
	if version, _ := root.Get("version"); version != float64(2) {
		t.Errorf("Retract() cursor hooks = %s, want the user's schema version kept", readTestFile(t, hooks))
	}
}
