package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestHookPromptTimeoutContract(t *testing.T) {
	root := t.TempDir()
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(repo claude) error = %v, want nil", err)
	}
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(user claude) error = %v, want nil", err)
	}
	if got := hookPromptAskTimeout(root); got != 6*time.Second {
		t.Errorf("hookPromptAskTimeout(no settings) = %s, want 6s", got)
	}
	project := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":"node .claude/helpers/graft-hooks.cjs prompt","timeout":15000}]}]}}`
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(project), 0o644); err != nil {
		t.Fatalf("os.WriteFile(project settings) error = %v, want nil", err)
	}
	if got := hookPromptAskTimeout(root); got != 13*time.Second {
		t.Errorf("hookPromptAskTimeout(15s) = %s, want 13s", got)
	}
	user := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":"node /home/.claude/helpers/graft-hooks.cjs prompt","timeout":8000}]}]}}`
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(user), 0o644); err != nil {
		t.Fatalf("os.WriteFile(user settings) error = %v, want nil", err)
	}
	if got := hookPromptAskTimeout(root); got != 6*time.Second {
		t.Errorf("hookPromptAskTimeout(smallest declared) = %s, want 6s", got)
	}
}

func TestHookEditedFileContract(t *testing.T) {
	root := t.TempDir()
	direct := filepath.Join(root, "src", "auth.go")
	if got := hookEditedFile(hookInput{"tool_input": map[string]any{"file_path": direct}}, root); got != direct {
		t.Errorf("hookEditedFile(direct) = %q, want %q", got, direct)
	}
	patch := "*** Begin Patch\n*** Update File: src/auth.go\n@@\n-old\n+new\n*** End Patch"
	got := hookEditedFile(hookInput{"tool_input": map[string]any{"command": patch}}, root)
	if want := filepath.Join(root, "src", "auth.go"); got != want {
		t.Errorf("hookEditedFile(patch) = %q, want %q", got, want)
	}
	if got := hookEditedFile(hookInput{"tool_input": map[string]any{"command": "no patch"}}, root); got != "" {
		t.Errorf("hookEditedFile(no patch) = %q, want empty", got)
	}
	contextFile := filepath.Join(hookContextDir(root), "INDEX.md")
	if !hookUnderContext(root, contextFile) || hookUnderContext(root, filepath.Join(root, "src", "auth.go")) {
		t.Errorf("hookUnderContext() did not distinguish context from source paths")
	}
}

func TestHookLastFileScopeContract(t *testing.T) {
	root := t.TempDir()
	scopes := []graph.ScopeV1{{Prefix: ""}, {Prefix: "backend"}, {Prefix: "frontend"}}
	wiring := graph.GraphV1{
		Meta: graph.GraphMeta{Version: 1, Scopes: &scopes},
		Nodes: []graph.NodeV1{
			{ID: "backend/auth.go", Name: "auth.go", Kind: "file", Path: "backend/auth.go"},
			{ID: "frontend/auth.go", Name: "auth.go", Kind: "file", Path: "frontend/auth.go"},
			{ID: "root.go", Name: "root.go", Kind: "file", Path: "root.go"},
		},
	}
	if _, err := graph.Write(wiring, hookContextDir(root)); err != nil {
		t.Fatalf("graph.Write() error = %v, want nil", err)
	}
	var stderr bytes.Buffer
	if got := lastHookFileScope(root, "auth.go", &stderr); got != "" || stderr.Len() == 0 {
		t.Errorf("lastHookFileScope(ambiguous) = (%q, %q), want empty with diagnostic", got, stderr.String())
	}
	stderr.Reset()
	wiring.Nodes = wiring.Nodes[:1]
	if _, err := graph.Write(wiring, hookContextDir(root)); err != nil {
		t.Fatalf("graph.Write(unique) error = %v, want nil", err)
	}
	if got := lastHookFileScope(root, "auth.go", &stderr); got != "backend" || stderr.Len() != 0 {
		t.Errorf("lastHookFileScope(unique) = (%q, %q), want backend", got, stderr.String())
	}
}

func TestHookMalformedInputFailsSoft(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	var stdout, stderr bytes.Buffer
	if status := runWithInput([]string{"_hook", "prompt"}, strings.NewReader("{"), &stdout, &stderr); status != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("runWithInput(malformed prompt) = (%d, %q, %q), want silent success", status, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if status := runWithInput([]string{"_statusline"}, strings.NewReader("not-json"), &stdout, &stderr); status != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
		t.Errorf("runWithInput(malformed statusline) = (%d, %q, %q), want not-built line", status, stdout.String(), stderr.String())
	}
}

func TestHookStopRunsOneSyncAndReleasesLock(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GRAFT_DIR", "")
	t.Setenv("GRAFT_NO_REFRESH", "1")
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(source) error = %v, want nil", err)
	}
	if status := runBuild(callersOptions{command: "build", root: root, rootSet: true}, io.Discard, io.Discard); status != 0 {
		t.Fatalf("runBuild(initial) status = %d, want 0", status)
	}
	if err := os.WriteFile(source, []byte("package main\n\nfunc main() { println(\"freshmarker\") }\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(edit) error = %v, want nil", err)
	}
	if _, err := patchHookStats(root, hookStatsPatch{Dirty: new(true), StaleCount: new(1)}); err != nil {
		t.Fatalf("patchHookStats(dirty) error = %v, want nil", err)
	}

	originalStart := hookStartSync
	t.Cleanup(func() { hookStartSync = originalStart })
	builds := 0
	hookStartSync = func(dir string) bool {
		builds++
		runHookSync(dir, io.Discard, io.Discard)
		return true
	}
	handleHookStop(hookInput{}, root)
	handleHookStop(hookInput{}, root)
	if builds != 1 {
		t.Errorf("handleHookStop() builds = %d, want 1", builds)
	}
	stats := readHookStats(root)
	if stats == nil || stats.Dirty || stats.Syncing || stats.StaleCount != 0 || stats.SyncedAt == nil {
		t.Errorf("readHookStats(after sync) = %#v, want clean synced stats", stats)
	}
	if _, err := os.Stat(filepath.Join(hookCacheDir(root), ".sync.lock")); !os.IsNotExist(err) {
		t.Errorf("sync lock after handleHookStop() stat error = %v, want absent", err)
	}
	data, err := os.ReadFile(source)
	if err != nil || !strings.Contains(string(data), "freshmarker") {
		t.Errorf("source after sync = (%q, %v), want edited source preserved", data, err)
	}
}

func TestHookAskTimeoutIsFailSoft(t *testing.T) {
	original := hookAsk
	t.Cleanup(func() { hookAsk = original })
	hookAsk = func(ctx context.Context, _, _, _, _ string) (graph.AskResult, bool) {
		<-ctx.Done()
		return graph.AskResult{}, false
	}
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	var stdout, stderr bytes.Buffer
	if status := runWithInput([]string{"_hook", "prompt"}, strings.NewReader(`{"prompt":"this prompt is long enough"}`), &stdout, &stderr); status != 0 || stdout.Len() != 0 {
		t.Errorf("runWithInput(timed-out prompt) = (%d, %q, %q), want silent success", status, stdout.String(), stderr.String())
	}
}
