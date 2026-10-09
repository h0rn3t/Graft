package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// These contracts each save an agent a model round: a single call reads
// several known symbols, a mistaken scope still answers, and a capped
// search says where the rest of its hits are.

func TestMCPReadSymbolAlsoContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root := t.TempDir()
	writeReadFixture(t, root)
	dir := filepath.Join(root, "graft")
	for _, tc := range []struct {
		name    string
		args    map[string]any
		wantErr bool
		want    []string
	}{
		{name: "two symbols in one call", args: map[string]any{"symbol": "alpha", "also": []any{"src/two.ts::shared"}}, want: []string{"return 1;", "return 2;"}},
		{name: "empty also reads one symbol", args: map[string]any{"symbol": "alpha", "also": []any{}}, want: []string{"return 1;"}},
		{name: "also not an array", args: map[string]any{"symbol": "alpha", "also": "shared"}, wantErr: true, want: []string{"also must be an array of symbol names"}},
		{name: "also with a non-string", args: map[string]any{"symbol": "alpha", "also": []any{float64(1)}}, wantErr: true, want: []string{"also must be an array of symbol names"}},
		{name: "more than eight symbols", args: map[string]any{"symbol": "alpha", "also": []any{"a", "b", "c", "d", "e", "f", "g", "h"}}, wantErr: true, want: []string{"between 1 and 8"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpCall(t.Context(), root, dir, "", "graft_read_symbol", tc.args)
			if got.isError != tc.wantErr || !containsAll(got.text, tc.want) {
				t.Errorf("mcpCall(graft_read_symbol, %v) = (%q, isError %t), want isError %t and text containing %q", tc.args, got.text, got.isError, tc.wantErr, tc.want)
			}
		})
	}
}

func TestMCPScopeOutsideIndexContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root := t.TempDir()
	writeReadFixture(t, root)
	dir := filepath.Join(root, "graft")
	for _, tc := range []struct {
		name   string
		tool   string
		args   map[string]any
		want   []string
		reject string
	}{
		{name: "find_all in the graph directory", tool: "graft_find_all", args: map[string]any{"pattern": "alpha", "in": "graft/"}, want: []string{`nothing indexed under "graft/"`, "src/one.ts"}},
		{name: "find_code in a missing directory", tool: "graft_find_code", args: map[string]any{"query": "alpha", "in": "missing"}, want: []string{`nothing indexed under "missing"`, "alpha"}},
		{name: "an indexed scope still narrows", tool: "graft_find_all", args: map[string]any{"pattern": "shared", "in": "src/two.ts"}, want: []string{"src/two.ts"}, reject: "src/one.ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpCall(t.Context(), root, dir, "", tc.tool, tc.args)
			if got.isError || !containsAll(got.text, tc.want) || (tc.reject != "" && strings.Contains(got.text, tc.reject)) {
				t.Errorf("mcpCall(%s, %v) = (%q, isError %t), want an answer containing %q and not %q", tc.tool, tc.args, got.text, got.isError, tc.want, tc.reject)
			}
		})
	}
}

func TestMCPFindAllNamesFilesPastTheCapContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, lines := range map[string]int{"wide.ts": 400, "narrow.ts": 2} {
		var body strings.Builder
		fmt.Fprintf(&body, "export function %s() {\n", strings.TrimSuffix(name, ".ts"))
		for i := range lines {
			fmt.Fprintf(&body, "  const value%d = needle(%d);\n", i, i)
		}
		body.WriteString("}\n")
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte(body.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "graft")
	built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Write(built.Graph, dir); err != nil {
		t.Fatal(err)
	}

	capped := mcpCall(t.Context(), root, dir, "", "graft_find_all", map[string]any{"pattern": "needle"})
	if capped.isError || len(capped.text) > mcpGrepBudget+1000 || !regexp.MustCompile(`more hits in: src/wide\.ts \(\d+\)`).MatchString(capped.text) {
		t.Errorf("mcpCall(graft_find_all, needle) = (%d bytes, isError %t, %q…), want at most ~%d bytes naming src/wide.ts as holding more hits", len(capped.text), capped.isError, capped.text[:min(len(capped.text), 300)], mcpGrepBudget)
	}
	whole := mcpCall(t.Context(), root, dir, "", "graft_find_all", map[string]any{"pattern": "needle", "in": "src/narrow.ts"})
	if whole.isError || strings.Contains(whole.text, "more hits in:") {
		t.Errorf("mcpCall(graft_find_all, needle in src/narrow.ts) = (%q, isError %t), want every hit and no remainder note", whole.text, whole.isError)
	}
}

func containsAll(text string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
