package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// writeSavingsFixture builds a graph over one source file large enough that
// every retrieval answer is smaller than reading it whole.
func writeSavingsFixture(t *testing.T) (root, contextDir string) {
	t.Helper()
	root = t.TempDir()
	contextDir = filepath.Join(root, "graft")
	var source strings.Builder
	source.WriteString("export function alpha() { return beta(); }\n")
	source.WriteString("export function beta() { return 1; }\n")
	for line := range 400 {
		fmt.Fprintf(&source, "// padding line %d keeps the file larger than any answer about it\n", line)
	}
	sourcePath := filepath.Join(root, "src", "app.ts")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: contextDir})
	if err != nil {
		t.Fatalf("BuildGraph(%q) error = %v, want nil", root, err)
	}
	if _, err := graph.Write(built.Graph, contextDir); err != nil {
		t.Fatalf("Write(BuildGraph(%q)) error = %v, want nil", root, err)
	}
	return root, contextDir
}

func TestMCPRetrievalRecordsPendingSavings(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"graft_find_code", map[string]any{"query": "alpha"}},
		{"graft_read_symbol", map[string]any{"symbol": "alpha"}},
		{"graft_file_api", map[string]any{"file": "src/app.ts"}},
		{"graft_trace_calls", map[string]any{"symbol": "beta"}},
		{"graft_find_all", map[string]any{"pattern": "alpha"}},
		{"graft_repo_map", nil},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			root, contextDir := writeSavingsFixture(t)
			if result := mcpCall(t.Context(), root, contextDir, "", tc.tool, tc.args); result.isError {
				t.Fatalf("mcpCall(%s) = %+v, want success", tc.tool, result)
			}
			if got := savings.DrainPending(filepath.Join(contextDir, ".cache")); got <= 0 {
				t.Errorf("mcpCall(%s) pending savings = %d, want > 0", tc.tool, got)
			}
		})
	}
}

func TestCLIRetrievalRecordsPendingSavings(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	for _, args := range [][]string{
		{"skeleton", "src/app.ts"},
		{"read", "alpha"},
	} {
		t.Run(args[0], func(t *testing.T) {
			root, contextDir := writeSavingsFixture(t)
			t.Chdir(root)
			var stdout, stderr bytes.Buffer
			if status := runWithInput(args, bytes.NewReader(nil), &stdout, &stderr); status != 0 {
				t.Fatalf("graft %v status = %d, want 0; stderr = %q", args, status, stderr.String())
			}
			if got := savings.DrainPending(filepath.Join(contextDir, ".cache")); got <= 0 {
				t.Errorf("graft %v pending savings = %d, want > 0", args, got)
			}
		})
	}
}

func TestHookToolUseCreditsPendingSavingsToItsSession(t *testing.T) {
	root := t.TempDir()
	savings.RecordPending(hookContextDir(root), 1200)
	input := hookInput{"session_id": "s1", "tool_name": "mcp__graft__graft_find_code", "tool_input": map[string]any{"query": "x"}}
	handleHookToolUse(input, root, io.Discard)
	handleHookToolUse(input, root, io.Discard)
	if got := readHookSession(root, "s1").SavedTokens; got != 1200 {
		t.Errorf("handleHookToolUse() savedTokens = %d, want 1200 credited once", got)
	}
	handleHookStop(hookInput{"session_id": "s2", "hook_event_name": "Stop"}, root)
	if got := readHookSession(root, "s2").SavedTokens; got != 0 {
		t.Errorf("handleHookStop(other session) savedTokens = %d, want 0 after the tool hook drained the ledger", got)
	}
}
