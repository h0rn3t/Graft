package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHookSessionIDNeverLeavesTheCache(t *testing.T) {
	tests := []struct {
		id   string
		file string
	}{
		{id: "../../../../pwned", file: "default.json"},
		{id: "a/b", file: "default.json"},
		{id: `a\b`, file: "default.json"},
		{id: "..", file: "default.json"},
		{id: "/etc/pwned", file: "default.json"},
		{id: "abc-123_DEF", file: "abc-123_DEF.json"},
	}
	for _, tt := range tests {
		parent := t.TempDir()
		root := filepath.Join(parent, "repo")
		t.Setenv("CLAUDE_PROJECT_DIR", root)
		t.Setenv("GRAFT_DIR", "")
		stdin := `{"session_id":` + quoteJSON(tt.id) + `,"tool_name":"Read"}`
		var stdout, stderr bytes.Buffer
		if status := runWithInput([]string{"_hook", "tool-savings"}, strings.NewReader(stdin), &stdout, &stderr); status != 0 {
			t.Fatalf("runWithInput(tool-savings, session %q) status = %d, want 0", tt.id, status)
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "repo" {
			t.Errorf("tool-savings(session %q) wrote %v next to the repository, want only repo", tt.id, entries)
		}
		if session := readHookSession(root, strings.TrimSuffix(tt.file, ".json")); session.SourceReads != 1 {
			t.Errorf("tool-savings(session %q) %s sourceReads = %d, want 1", tt.id, tt.file, session.SourceReads)
		}
	}
}

func quoteJSON(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
}

func TestRecordHookToolUseConcurrentlyKeepsEveryUse(t *testing.T) {
	root := t.TempDir()
	const uses = 20
	var wg sync.WaitGroup
	for range uses {
		wg.Go(func() {
			if err := recordHookToolUse(root, "parallel", hookToolUse{Kind: hookToolSource}); err != nil {
				t.Errorf("recordHookToolUse(parallel) error = %v, want nil", err)
			}
		})
	}
	wg.Wait()
	if got := readHookSession(root, "parallel").SourceReads; got != uses {
		t.Errorf("readHookSession(after %d parallel uses) sourceReads = %d, want %d", uses, got, uses)
	}
}

func TestPatchHookStatsConcurrentlyKeepsEveryField(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = patchHookStats(root, hookStatsPatch{Dirty: new(true)}) })
	wg.Go(func() { _, _ = patchHookStats(root, hookStatsPatch{Syncing: new(true)}) })
	wg.Go(func() { _, _ = patchHookStats(root, hookStatsPatch{StaleCount: new(3)}) })
	wg.Go(func() { _, _ = patchHookStats(root, hookStatsPatch{NodeCount: new(7)}) })
	wg.Wait()
	stats := readHookStats(root)
	if stats == nil || !stats.Dirty || !stats.Syncing || stats.StaleCount != 3 || stats.NodeCount != 7 {
		t.Errorf("readHookStats(after parallel patches) = %#v, want every patched field", stats)
	}
}

func TestHookSyncKeepsAnEditMadeDuringTheBuild(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GRAFT_DIR", "")
	t.Setenv("GRAFT_NO_REFRESH", "1")
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := patchHookStats(root, hookStatsPatch{Dirty: new(true), Syncing: new(true)}); err != nil {
		t.Fatal(err)
	}
	build := hookSyncBuild
	t.Cleanup(func() { hookSyncBuild = build })
	hookSyncBuild = func(dir string) error {
		err := build(dir)
		// The agent edits a file after the build read it.
		handleHookPostEdit(t.Context(), hookInput{"tool_input": map[string]any{"file_path": source}}, dir, io.Discard, io.Discard)
		return err
	}
	runHookSync(root, io.Discard, io.Discard)
	stats := readHookStats(root)
	if stats == nil || !stats.Dirty || stats.Syncing || stats.SyncedAt == nil {
		t.Errorf("readHookStats(after sync with a mid-build edit) = %#v, want synced but still dirty", stats)
	}

	hookSyncBuild = build
	runHookSync(root, io.Discard, io.Discard)
	if stats := readHookStats(root); stats == nil || stats.Dirty || stats.StaleCount != 0 {
		t.Errorf("readHookStats(after a quiet sync) = %#v, want clean", stats)
	}
}

func TestHookPostEditCountsDriftWithoutRebuilding(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GRAFT_DIR", "")
	t.Setenv("GRAFT_NO_REFRESH", "1")
	source := filepath.Join(root, "pkg", "main.go")
	writeFixtureFile(t, root, "pkg/main.go", "package main\n\nfunc main() {}\n")
	if status := runBuild(callersOptions{command: "build", root: root, rootSet: true}, io.Discard, io.Discard); status != 0 {
		t.Fatalf("runBuild(%q) status = %d, want 0", root, status)
	}
	if err := os.WriteFile(source, []byte("package main\n\nfunc main() { println() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handleHookPostEdit(t.Context(), hookInput{"tool_input": map[string]any{"file_path": source}}, root, io.Discard, io.Discard)
	stats := readHookStats(root)
	if stats == nil || !stats.Dirty || stats.StaleCount != 1 || stats.LastFile == nil || *stats.LastFile != "pkg/main.go" {
		t.Errorf("readHookStats(after post-edit) = %#v, want dirty, one stale file, repo-relative lastFile", stats)
	}
}

func TestReadHookInputIsLenient(t *testing.T) {
	large := strings.Repeat("y", 2<<20)
	tests := []struct {
		name  string
		input string
	}{
		{name: "lone surrogate", input: `{"tool_input":{"file_path":"/repo/a.go","content":"\ud800 tail"}}`},
		{name: "invalid UTF-8", input: "{\"tool_input\":{\"file_path\":\"/repo/a.go\",\"content\":\"\xff\xfe\"}}"},
		{name: "duplicate key", input: `{"tool_input":{"file_path":"/repo/old.go"},"tool_input":{"file_path":"/repo/a.go"}}`},
		{name: "over 1 MiB", input: `{"tool_input":{"content":"` + large + `","file_path":"/repo/a.go"}}`},
	}
	for _, tt := range tests {
		input := readHookInput(strings.NewReader(tt.input))
		if got, _ := input.object("tool_input")["file_path"].(string); got != "/repo/a.go" {
			t.Errorf("readHookInput(%s) tool_input.file_path = %q, want %q", tt.name, got, "/repo/a.go")
		}
	}
	if input := readHookInput(strings.NewReader(`{"tool_input":`)); len(input) != 0 {
		t.Errorf("readHookInput(truncated) = %v, want empty input", input)
	}
}

func TestHookTranscriptEntriesAreLenient(t *testing.T) {
	path := writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"q"}}`,
		`{"type":"assistant","uuid":"a1","uuid":"a2","message":{"role":"assistant","content":"graft saved ~1,000 tokens \udc00"}}`,
	)
	turn := lastHookAssistantTurn(hookTranscriptEntries(path))
	if turn == nil || turn.UUID != "a2" || !hasSavingsTally(turn.Text) {
		t.Errorf("lastHookAssistantTurn(lone surrogate, duplicate uuid) = %#v, want turn a2 with its tally", turn)
	}
}
