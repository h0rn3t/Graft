package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// The MCP server refreshes through its query cache's memo, so the first call
// after an edit answers from the graph and index the refresh built, without
// decoding them again, and answers exactly as a fresh read from disk does.

func TestMCPRefreshHandsItsGraphToQueries(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	root, dir := buildFillFixture(t, map[string]string{
		"go.mod":         "module example.com/fx\n",
		"cache/cache.go": "package cache\n\n// LoadSnapshot reads the stored snapshot from disk.\nfunc LoadSnapshot(path string) string {\n\treturn path\n}\n",
		"drift/drift.go": "package drift\n\n// ProbeDrift compares file hashes against the fingerprint.\nfunc ProbeDrift(hashes []string) bool {\n\treturn len(hashes) > 0\n}\n",
	})
	var cache queryCache
	t.Cleanup(cache.memo.Wait) // a refresh writes its files in the background
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		edit       string
		want       string
	}{
		{
			name: "find_code", tool: "graft_find_code", args: map[string]any{"query": "drift since fingerprint"},
			edit: "// DriftSince lists the files changed since the fingerprint.\nfunc DriftSince(hashes []string) []string {\n\treturn hashes\n}\n",
			want: "DriftSince",
		},
		{
			name: "read_symbol", tool: "graft_read_symbol", args: map[string]any{"symbol": "ProbeDrift"},
			edit: "func driftCount(hashes []string) int {\n\treturn len(hashes)\n}\n",
			want: "func ProbeDrift(hashes []string) bool",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(root, "drift", "drift.go"))
			if err != nil {
				t.Fatal(err)
			}
			writeFixtureFile(t, root, "drift/drift.go", string(source)+"\n"+tc.edit)
			got := mcpCallWithCache(t.Context(), root, dir, "", tc.tool, tc.args, &cache)
			note, answer, _ := strings.Cut(got.text, "\n")
			answer = strings.TrimPrefix(answer, "\n")
			if got.isError || !strings.HasPrefix(note, "[graft] refreshed the graph (1 file changed)") || !strings.Contains(answer, tc.want) {
				t.Fatalf("mcpCallWithCache(%s, %v) after an edit = %+v, want a refresh note, then %q", tc.tool, tc.args, got, tc.want)
			}
			// No refresh is left to do, so this answer comes from the files on disk.
			if want := mcpCall(t.Context(), root, dir, "", tc.tool, tc.args); want.isError || answer != want.text {
				t.Errorf("mcpCallWithCache(%s, %v) after an edit answered\n%s\nwant what a read from disk answers\n%s", tc.tool, tc.args, answer, want.text)
			}

			graphFile, indexFile := statQueryFiles(t, dir)
			written, writtenIndex, ok := cache.memo.Written(dir, graphFile, indexFile)
			if !ok || written == nil || writtenIndex == nil {
				t.Fatalf("cache.memo.Written(%q) after the refresh = (%v, %v, %t), want its graph and index", dir, written, writtenIndex, ok)
			}
			wiring, index, err := cache.load(dir)
			if err != nil || wiring != written || index != writtenIndex {
				t.Errorf("cache.load(%q) = (%p, %p, %v), want the refreshed graph and index (%p, %p) without decoding them", dir, wiring, index, err, written, writtenIndex)
			}
			if fromDisk := readAskIndex(filepath.Join(dir, ".cache", "ask-index.json")); !reflect.DeepEqual(writtenIndex, fromDisk) {
				t.Errorf("refreshed ask index = %+v, want what readAskIndex decodes from its file, %+v", writtenIndex, fromDisk)
			}
			if fromDisk, err := graph.Read(graph.WiringPath(dir)); err != nil || !reflect.DeepEqual(written, fromDisk) {
				t.Errorf("refreshed graph = %+v, want what graph.Read decodes from its file, %+v (error %v)", written, fromDisk, err)
			}
		})
	}
}

func TestMCPConcurrentCallsAfterAnEdit(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	root, dir := buildFillFixture(t, map[string]string{
		"go.mod":         "module example.com/fx\n",
		"drift/drift.go": "package drift\n\n// ProbeDrift compares file hashes against the fingerprint.\nfunc ProbeDrift(hashes []string) bool {\n\treturn len(hashes) > 0\n}\n",
	})
	var cache queryCache
	t.Cleanup(cache.memo.Wait) // a refresh writes its files in the background
	args := map[string]any{"query": "drift since fingerprint"}
	for edit := range 3 {
		writeFixtureFile(t, root, "drift/drift.go", fmt.Sprintf("package drift\n\n// DriftSince%[1]d lists the files changed since the fingerprint.\nfunc DriftSince%[1]d(hashes []string) []string {\n\treturn hashes\n}\n", edit))
		// Calls that find the refresh in flight answer from the graph before it.
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() {
				if got := mcpCallWithCache(t.Context(), root, dir, "", "graft_find_code", args, &cache); got.isError {
					t.Errorf("concurrent mcpCallWithCache(graft_find_code) after edit %d = %+v, want an answer", edit, got)
				}
			})
		}
		wg.Wait()
		want := fmt.Sprintf("DriftSince%d", edit)
		if got := mcpCallWithCache(t.Context(), root, dir, "", "graft_find_code", args, &cache); got.isError || !strings.Contains(got.text, want) {
			t.Errorf("mcpCallWithCache(graft_find_code) after edit %d settled = %+v, want %s", edit, got, want)
		}
	}
}

func TestMCPServerSavesItsLastRefreshBeforeExit(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	root, dir := buildFillFixture(t, map[string]string{
		"go.mod":         "module example.com/fx\n",
		"drift/drift.go": "package drift\n\n// ProbeDrift compares file hashes against the fingerprint.\nfunc ProbeDrift(hashes []string) bool {\n\treturn len(hashes) > 0\n}\n",
	})
	writeFixtureFile(t, root, "drift/drift.go", "package drift\n\n// DriftSince lists the files changed since the fingerprint.\nfunc DriftSince(hashes []string) []string {\n\treturn hashes\n}\n")
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"graft_find_code","arguments":{"query":"drift since"}}}` + "\n"
	var stdout lockedBuffer
	var stderr strings.Builder
	if status := runMCP(t.Context(), callersOptions{command: "mcp", root: root, rootSet: true}, strings.NewReader(input), &stdout, &stderr); status != 0 {
		t.Fatalf("runMCP(find_code after an edit) status = %d, want 0; stderr = %q", status, stderr.String())
	}
	drift, err := graph.ProbeDrift(root, dir, graph.ExtractorID, sourcefiles.Options{})
	if err != nil || drift == nil || len(drift.Changed)+len(drift.Added)+len(drift.Removed) != 0 {
		t.Errorf("ProbeDrift(%q) after runMCP returned = (%+v, %v), want the refresh saved, with no drift left", root, drift, err)
	}
	wiring, err := graph.Read(graph.WiringPath(dir))
	if err != nil || !slices.ContainsFunc(wiring.Nodes, func(node graph.NodeV1) bool { return node.ID == "drift/drift.go#DriftSince" }) {
		t.Errorf("graph.Read(%q) after runMCP returned = (%v, %v), want drift/drift.go#DriftSince saved", graph.WiringPath(dir), wiring, err)
	}
}

func TestMCPCheckFreshnessRightAfterARefresh(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	root, dir := buildFillFixture(t, map[string]string{
		"go.mod":         "module example.com/fx\n",
		"drift/drift.go": "package drift\n\n// ProbeDrift compares file hashes against the fingerprint.\nfunc ProbeDrift(hashes []string) bool {\n\treturn len(hashes) > 0\n}\n",
	})
	var cache queryCache
	t.Cleanup(cache.memo.Wait) // a refresh writes its files in the background
	writeFixtureFile(t, root, "extra/extra.go", "package extra\n\n// Extra is new.\nfunc Extra() {}\n")
	if got := mcpCallWithCache(t.Context(), root, dir, "", "graft_find_code", map[string]any{"query": "extra"}, &cache); got.isError {
		t.Fatalf("mcpCallWithCache(graft_find_code) after adding a file = %+v, want an answer", got)
	}
	// The check reads the files on disk, which the refresh writes after answering.
	if got := mcpCallWithCache(t.Context(), root, dir, "", "graft_check_freshness", nil, &cache); got.isError || !strings.Contains(got.text, "graph check: OK") {
		t.Errorf("mcpCallWithCache(graft_check_freshness) right after a refresh = %+v, want graph check: OK", got)
	}
}

func statQueryFiles(t *testing.T, dir string) (graphFile, indexFile os.FileInfo) {
	t.Helper()
	graphFile, err := os.Stat(graph.WiringPath(dir))
	if err != nil {
		t.Fatalf("Stat(%q) error = %v, want nil", graph.WiringPath(dir), err)
	}
	indexFile, err = os.Stat(filepath.Join(dir, ".cache", "ask-index.json"))
	if err != nil {
		t.Fatalf("Stat(ask index) error = %v, want nil", err)
	}
	return graphFile, indexFile
}
