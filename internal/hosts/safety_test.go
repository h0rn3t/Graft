package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want nil", path, err)
	}
	return string(data)
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(link), err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Symlink(%q, %q) error = %v; symlinks unavailable", target, link, err)
	}
}

func retraction(retractions []Retraction, path string) Retraction {
	for _, retraction := range retractions {
		if retraction.Path == path {
			return retraction
		}
	}
	return Retraction{Path: path, Action: "not listed"}
}

// TestRepoSymlinksNeverRedirectWrites plants the symlinks a hostile repository
// could commit and checks that neither init nor uninstall touches the files
// they point at outside the repository.
func TestRepoSymlinksNeverRedirectWrites(t *testing.T) {
	tests := []struct {
		name string
		link string // repo-relative symlink pointing at the victim
		dir  bool   // the link replaces a directory on the way
	}{
		{name: "hooks shim", link: ".claude/helpers/graft-hooks.cjs"},
		{name: "settings", link: ".claude/settings.json"},
		{name: "mcp config", link: ".mcp.json"},
		{name: "helpers directory", link: ".claude/helpers", dir: true},
		{name: "fenced section file", link: "AGENTS.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, home, outside := t.TempDir(), t.TempDir(), t.TempDir()
			victim := filepath.Join(outside, "victim.txt")
			target := victim
			if tt.dir {
				target = outside
				writeTestFile(t, filepath.Join(outside, "graft-hooks.cjs"), "victim")
				victim = filepath.Join(outside, "graft-hooks.cjs")
			} else {
				writeTestFile(t, victim, "<!-- graft:start -->\nvictim\n<!-- graft:end -->\n")
			}
			before := readTestFile(t, victim)
			if err := os.Chmod(victim, 0o600); err != nil {
				t.Fatalf("Chmod(%q) error = %v, want nil", victim, err)
			}
			symlinkOrSkip(t, target, filepath.Join(repo, filepath.FromSlash(tt.link)))
			env := Env{Home: home, BakedDir: "/pkg", Launch: binLaunch}

			_, _ = RunClaudeInit(repo, env, true, false)                                       // an error is fine; a write through the link is not
			_, _ = RunHostsInit(repo, env, InitOptions{Agents: []string{"agents"}, MCP: true}) // same
			Retract(repo, env, RetractOptions{Apply: true, Cache: true})

			if got := readTestFile(t, victim); got != before {
				t.Errorf("init+uninstall through %s changed %q to %q, want %q", tt.link, victim, got, before)
			}
			info, err := os.Stat(victim)
			if err != nil {
				t.Fatalf("Stat(%q) error = %v, want the victim kept", victim, err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Errorf("init through %s chmodded %q to %o, want 600", tt.link, victim, info.Mode().Perm())
			}
		})
	}
}

func TestInRepoSymlinkKeepsItsLayout(t *testing.T) {
	repo := t.TempDir()
	real := filepath.Join(repo, "docs", "AGENTS.md")
	writeTestFile(t, real, "# Notes\n")
	symlinkOrSkip(t, filepath.Join("docs", "AGENTS.md"), filepath.Join(repo, "AGENTS.md"))
	if _, err := RunHostsInit(repo, Env{Home: t.TempDir(), Launch: binLaunch}, InitOptions{Agents: []string{"agents"}}); err != nil {
		t.Fatalf("RunHostsInit() error = %v, want nil", err)
	}
	if info, err := os.Lstat(filepath.Join(repo, "AGENTS.md")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("RunHostsInit() AGENTS.md = (%v, %v), want the symlink kept", info, err)
	}
	if got := readTestFile(t, real); !strings.HasPrefix(got, "# Notes\n\n"+GraftMarkers.Start) {
		t.Errorf("RunHostsInit() docs/AGENTS.md = %q, want the section appended to the link target", got)
	}
}

func TestRunClaudeInitLeavesUnreadableSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings string
	}{
		{name: "not JSON", settings: "{\n  \"hooks\": {},\n  // a comment\n}\n"},
		{name: "not an object", settings: "[1, 2]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, ".claude", "settings.json")
			writeTestFile(t, path, tt.settings)
			result, err := RunClaudeInit(repo, Env{Home: t.TempDir(), Launch: binLaunch}, true, false)
			if err != nil {
				t.Fatalf("RunClaudeInit() error = %v, want nil", err)
			}
			if got := readTestFile(t, path); got != tt.settings {
				t.Errorf("RunClaudeInit() settings = %q, want untouched %q", got, tt.settings)
			}
			if !slices.ContainsFunc(result.Warnings, func(warning string) bool { return strings.Contains(warning, path) }) {
				t.Errorf("RunClaudeInit() warnings = %q, want one naming %s", result.Warnings, path)
			}
		})
	}
}

func TestUnclosedMarkerKeepsUserText(t *testing.T) {
	const text = "# Mine\n<!-- graft:start -->\nkeep this paragraph\nand this one\n"
	repo := t.TempDir()
	path := filepath.Join(repo, "AGENTS.md")
	writeTestFile(t, path, text)
	env := Env{Home: t.TempDir(), Launch: binLaunch}

	if _, err := RunHostsInit(repo, env, InitOptions{Agents: []string{"agents"}}); !errors.Is(err, errUnclosedMarker) {
		t.Errorf("RunHostsInit(unclosed marker) error = %v, want errUnclosedMarker", err)
	}
	if got := readTestFile(t, path); got != text {
		t.Errorf("RunHostsInit(unclosed marker) AGENTS.md = %q, want untouched %q", got, text)
	}
	got := retraction(Retract(repo, env, RetractOptions{Apply: true}), path)
	if got.Action != RetractUnparseable || !errors.Is(got.Err, errUnclosedMarker) {
		t.Errorf("Retract(unclosed marker) = (%s, %v), want (%s, errUnclosedMarker)", got.Action, got.Err, RetractUnparseable)
	}
	if got := readTestFile(t, path); got != text {
		t.Errorf("Retract(unclosed marker) AGENTS.md = %q, want untouched %q", got, text)
	}
}

// TestRetractStopsPruningAtTheRepoAndHostDirs checks that removing graft's
// last file never deletes the repository, its parents, or ~/.codex.
func TestRetractStopsPruningAtTheRepoAndHostDirs(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "a", "b", "repo")
	home := t.TempDir()
	owned := filepath.Join(repo, ".kiro", "steering", "graft.md")
	writeTestFile(t, owned, "graft")
	shim := filepath.Join(home, ".codex", "hooks", "graft", "graft-hooks.cjs")
	writeTestFile(t, shim, "shim")

	Retract(repo, Env{Home: home, Launch: binLaunch}, RetractOptions{Apply: true, Global: true})

	for _, gone := range []string{owned, filepath.Join(repo, ".kiro"), shim, filepath.Join(home, ".codex", "hooks")} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Retract() left %q (err = %v), want it removed", gone, err)
		}
	}
	for _, kept := range []string{repo, filepath.Join(base, "a"), filepath.Join(home, ".codex")} {
		if info, err := os.Stat(kept); err != nil || !info.IsDir() {
			t.Errorf("Retract() removed %q (err = %v), want it kept", kept, err)
		}
	}
}

func TestRetractReportsRemovalFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not deny removal here")
	}
	repo := t.TempDir()
	dir := filepath.Join(repo, ".kiro", "steering")
	owned := filepath.Join(dir, "graft.md")
	writeTestFile(t, owned, "graft")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod(%q) error = %v, want nil", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // let t.TempDir remove it
	got := retraction(Retract(repo, Env{Home: t.TempDir(), Launch: binLaunch}, RetractOptions{Apply: true}), owned)
	if got.Action != RetractFailed || got.Err == nil {
		t.Errorf("Retract(read-only dir) = (%s, %v), want (%s, an error)", got.Action, got.Err, RetractFailed)
	}
	if text := FormatRetractions([]Retraction{got}, true); !strings.Contains(text, "could not remove") || !strings.Contains(text, got.Err.Error()) {
		t.Errorf("FormatRetractions(failed) = %q, want the cause", text)
	}
}

func TestStripTOMLSection(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		want      string
		wantFound bool
	}{
		{
			name:      "header with a trailing comment and spaces",
			text:      "[other]\nx = 1\n\n[ mcp_servers . graft ]  # graft\ncommand = \"graft\"\n",
			want:      "[other]\nx = 1\n",
			wantFound: true,
		},
		{
			name:      "quoted keys",
			text:      "[mcp_servers.\"graft\"]\ncommand = \"graft\"\n\n[other]\nx = 1\n",
			want:      "[other]\nx = 1\n",
			wantFound: true,
		},
		{
			name:      "comments above the next table stay with it",
			text:      "[mcp_servers.graft]\ncommand = \"graft\"\n\n# my server\n[mcp_servers.mine]\ncommand = \"mine\"\n",
			want:      "# my server\n[mcp_servers.mine]\ncommand = \"mine\"\n",
			wantFound: true,
		},
		{
			name:      "subtables go too",
			text:      "[mcp_servers.graft]\ncommand = \"graft\"\n\n[mcp_servers.graft.env]\nA = \"1\"\n\n[other]\nx = 1\n",
			want:      "[other]\nx = 1\n",
			wantFound: true,
		},
		{
			name:      "blank runs elsewhere are the user's",
			text:      "[a]\nx = 1\n\n\n\n[b]\ny = 2\n\n[mcp_servers.graft]\ncommand = \"graft\"\n",
			want:      "[a]\nx = 1\n\n\n\n[b]\ny = 2\n",
			wantFound: true,
		},
		{
			name: "no graft table",
			text: "[mcp_servers.graftlib]\ncommand = \"x\"\n",
			want: "[mcp_servers.graftlib]\ncommand = \"x\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := StripTOMLSection(tt.text)
			if got != tt.want || found != tt.wantFound {
				t.Errorf("StripTOMLSection(%q) = (%q, %t), want (%q, %t)", tt.text, got, found, tt.want, tt.wantFound)
			}
		})
	}
}

func TestUpsertTOMLReplacesAnnotatedHeaderOnce(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".grok", "config.toml")
	writeTestFile(t, path, "[mcp_servers.graft] # added by graft\ncommand = \"old\"\n\n[mcp_servers.graft.env]\nA = \"1\"\n")
	f := openFiles(repo, t.TempDir())
	defer f.close()
	if _, err := f.upsertTOML("grok", path, binLaunch); err != nil {
		t.Fatalf("upsertTOML() error = %v, want nil", err)
	}
	got := readTestFile(t, path)
	if strings.Count(got, "[mcp_servers.graft]") != 1 || !strings.Contains(got, "[mcp_servers.graft.env]\nA = \"1\"") || strings.Contains(got, "old") {
		t.Errorf("upsertTOML() = %q, want one graft table and the env subtable kept", got)
	}
}

func TestStripIgnoreEntriesMatchesOnlyGraftLines(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		want       string
		wantAction RetractAction
	}{
		{
			name:       "foreign graft comment and blank runs stay",
			text:       "# graftlib build output\nbuild/\n\n\n\nnode_modules/\n",
			want:       "# graftlib build output\nbuild/\n\n\n\nnode_modules/\n",
			wantAction: RetractAbsent,
		},
		{
			name:       "graft's own comment and entry go",
			text:       "node_modules/\n\n# graft's local graph cache — regenerable, not committed (run `graft build`).\n/graft/\n",
			want:       "node_modules/\n",
			wantAction: RetractRemoved,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, ".gitignore")
			writeTestFile(t, path, tt.text)
			got := retraction(Retract(repo, Env{Home: t.TempDir(), Launch: binLaunch}, RetractOptions{Apply: true, Cache: true}), path)
			if got.Action != tt.wantAction {
				t.Errorf("Retract(%q) action = %s, want %s", tt.text, got.Action, tt.wantAction)
			}
			if text := readTestFile(t, path); text != tt.want {
				t.Errorf("Retract(%q) .gitignore = %q, want %q", tt.text, text, tt.want)
			}
		})
	}
}

func TestMergeGraftSettingsKeepsForeignShapes(t *testing.T) {
	existing, err := jsonjs.Parse([]byte(`{"hooks":{"Stop":"echo mine"},"footerLinksRegexes":"x","permissions":{"allow":"Bash(*)"}}`))
	if err != nil {
		t.Fatalf("jsonjs.Parse() error = %v, want nil", err)
	}
	object, _ := jsonjs.AsObject(existing)
	merged, warnings := MergeGraftSettings(object, false)
	hooks, _ := jsonjs.AsObject(mustGet(merged, "hooks"))
	permissions, _ := jsonjs.AsObject(mustGet(merged, "permissions"))
	for _, check := range []struct {
		field string
		got   jsonjs.Value
		want  string
	}{
		{"hooks.Stop", mustGet(hooks, "Stop"), "echo mine"},
		{"footerLinksRegexes", mustGet(merged, "footerLinksRegexes"), "x"},
		{"permissions.allow", mustGet(permissions, "allow"), "Bash(*)"},
	} {
		if check.got != check.want {
			t.Errorf("MergeGraftSettings() %s = %v, want %q kept", check.field, check.got, check.want)
		}
		if !slices.ContainsFunc(warnings, func(warning string) bool { return strings.Contains(warning, check.field) }) {
			t.Errorf("MergeGraftSettings() warnings = %q, want one for %s", warnings, check.field)
		}
	}
	if _, ok := jsonjs.AsArray(mustGet(hooks, "PostToolUse")); !ok {
		t.Errorf("MergeGraftSettings() hooks.PostToolUse = %v, want graft's blocks still added", mustGet(hooks, "PostToolUse"))
	}
}

func TestRunClaudeInitWritesOnlyChangedFiles(t *testing.T) {
	repo := t.TempDir()
	env := Env{Home: t.TempDir(), Launch: binLaunch}
	if _, err := RunClaudeInit(repo, env, true, false); err != nil {
		t.Fatalf("RunClaudeInit() error = %v, want nil", err)
	}
	targets := ClaudeTargets(repo)
	for _, target := range targets[:4] {
		marker := target.Path + ".marker"
		if err := os.Link(target.Path, marker); err != nil {
			t.Skipf("Link(%q) error = %v; hard links unavailable", target.Path, err)
		}
	}
	if _, err := RunClaudeInit(repo, env, true, false); err != nil {
		t.Fatalf("RunClaudeInit() again error = %v, want nil", err)
	}
	for _, target := range targets[:4] {
		before, err := os.Stat(target.Path + ".marker")
		if err != nil {
			t.Fatalf("Stat(%q) error = %v, want nil", target.Path+".marker", err)
		}
		after, err := os.Stat(target.Path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v, want nil", target.Path, err)
		}
		if !os.SameFile(before, after) {
			t.Errorf("RunClaudeInit() again replaced %q, want an unchanged file left alone", target.Path)
		}
	}
}

func TestHookCommands(t *testing.T) {
	for _, entry := range graftBlocks(repoHookScript) {
		for _, block := range entry.blocks {
			object, _ := jsonjs.AsObject(block)
			handlers, _ := jsonjs.AsArray(mustGet(object, "hooks"))
			handler, _ := jsonjs.AsObject(handlers[0])
			if timeout, _ := mustGet(handler, "timeout").(float64); timeout <= 0 || timeout > 60 {
				t.Errorf("graftBlocks() %s timeout = %v, want seconds", entry.event, timeout)
			}
		}
	}
	for _, tt := range []struct{ path, want string }{
		{"/home/me/.claude/helpers/graft-hooks.cjs", `"/home/me/.claude/helpers/graft-hooks.cjs"`},
		{`C:\Users\me\.codex\hooks\graft\graft-hooks.cjs`, `"C:\Users\me\.codex\hooks\graft\graft-hooks.cjs"`},
		{`/home/a"b/x.cjs`, `'/home/a"b/x.cjs'`},
		{"/home/$USER/x.cjs", "'/home/$USER/x.cjs'"},
		{"/home/it's/$x.cjs", `'/home/it'\''s/$x.cjs'`},
	} {
		if got := shellPath(tt.path); got != tt.want {
			t.Errorf("shellPath(%q) = %s, want %s", tt.path, got, tt.want)
		}
	}
	repo := t.TempDir()
	f := openFiles(repo, t.TempDir())
	defer f.close()
	if _, err := f.installCursorHooks(repo, Env{Launch: binLaunch}); err != nil {
		t.Fatalf("installCursorHooks() error = %v, want nil", err)
	}
	if got := readTestFile(t, filepath.Join(repo, ".cursor", "hooks.json")); strings.Contains(got, repo) || !strings.Contains(got, `node \".cursor/hooks/graft-hooks.cjs\"`) {
		t.Errorf("installCursorHooks() hooks.json = %q, want repo-relative shim commands", got)
	}
}

// TestRetractFindsConfigsOfUninstalledTools checks that opencode.json and
// ~/.codex/config.toml are retracted even once the tool itself is gone, and
// that a full retraction drops the wiring stamp too.
func TestRetractFindsConfigsOfUninstalledTools(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	opencode := filepath.Join(repo, "opencode.json")
	writeTestFile(t, opencode, `{"mcp":{"graft":{"type":"local"}}}`)
	stamp := filepath.Join(repo, "graft", ".cache", "wiring-stamp.json")
	writeTestFile(t, stamp, `{"version":"1.0.0","hosts":["agents"]}`)
	t.Setenv("GRAFT_DIR", "")

	Retract(repo, Env{Home: home, Launch: binLaunch}, RetractOptions{Apply: true, Global: true})

	for _, gone := range []string{opencode, stamp} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Retract() left %q (err = %v), want it removed", gone, err)
		}
	}
}

func TestServerEntryNeverDownloads(t *testing.T) {
	t.Setenv("GRAFT_MCP_NPX", "")
	t.Setenv("PATH", t.TempDir())
	launch := ServerEntry().resolved()
	if !filepath.IsAbs(launch.Command) || !slices.Equal(launch.Args, []string{"mcp"}) {
		t.Errorf("ServerEntry() without graft on PATH = %+v, want the absolute path of this executable", launch)
	}
}
