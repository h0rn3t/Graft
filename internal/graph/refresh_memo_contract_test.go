package graph

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// A refresh through a RefreshMemo must leave a context directory exactly as a
// refresh without one does, apart from the caches it keeps in memory instead.

func TestEnsureFreshGraphMemoWritesWhatAPlainRefreshWrites(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	plainRoot, memoRoot := newMemoFixture(t), newMemoFixture(t)
	var memo RefreshMemo
	for _, edit := range []struct{ rel, source string }{
		{rel: "src/b.ts", source: "export function b() {}\nexport function c() {}\n"},
		// The second refresh starts from what the first left in the memo.
		{rel: "src/a.ts", source: "export function a() { return c() }\n"},
		{rel: "src/d.ts", source: "export function d() { return a() }\n"},
	} {
		writeRefreshSource(t, plainRoot, edit.rel, edit.source)
		writeRefreshSource(t, memoRoot, edit.rel, edit.source)
		if got := EnsureFreshGraph(plainRoot, RefreshOptions{}); !got.Refreshed {
			t.Fatalf("EnsureFreshGraph(%q) after editing %s = %#v, want a refresh", plainRoot, edit.rel, got)
		}
		parse, terms := readMemoCaches(t, memoRoot)
		if got := EnsureFreshGraph(memoRoot, RefreshOptions{Memo: &memo}); !got.Refreshed {
			t.Fatalf("EnsureFreshGraph(%q, memo) after editing %s = %#v, want a refresh", memoRoot, edit.rel, got)
		}
		// The files are written in the background.
		memo.Wait()
		for _, rel := range []string{".graph/wiring.json", ".cache/ask-index.json", "INDEX.md", "src/a.md", "src/b.md", "src/d.md"} {
			want, wantErr := os.ReadFile(filepath.Join(plainRoot, "graft", rel))
			got, gotErr := os.ReadFile(filepath.Join(memoRoot, "graft", rel))
			if !bytes.Equal(got, want) || (gotErr == nil) != (wantErr == nil) {
				t.Errorf("after editing %s, graft/%s through a memo = (%q, %v), want (%q, %v)", edit.rel, rel, got, gotErr, want, wantErr)
			}
		}
		if gotParse, gotTerms := readMemoCaches(t, memoRoot); !bytes.Equal(gotParse, parse) || !bytes.Equal(gotTerms, terms) {
			t.Errorf("EnsureFreshGraph(%q, memo) after editing %s rewrote the parse or term cache, want both left as they were", memoRoot, edit.rel)
		}
	}
}

func TestRefreshMemoWrittenContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	root := newMemoFixture(t)
	outDir := filepath.Join(root, "graft")
	var memo RefreshMemo
	writeRefreshSource(t, root, "src/b.ts", "export function b() {}\nexport function c() {}\n")
	if got := EnsureFreshGraph(root, RefreshOptions{Memo: &memo}); !got.Refreshed {
		t.Fatalf("EnsureFreshGraph(%q, memo) = %#v, want a refresh", root, got)
	}
	memo.Wait()
	graphFile, indexFile := statMemoFiles(t, outDir)

	written, index, ok := memo.Written(outDir, graphFile, indexFile)
	if !ok || written == nil || index == nil {
		t.Fatalf("Written(%q) right after the refresh = (%v, %v, %t), want its graph and index", outDir, written, index, ok)
	}
	// Written's graph is the persisted one: rewriting it changes no byte.
	want, _ := os.ReadFile(WiringPath(outDir))
	again := t.TempDir()
	if _, err := Write(*written, again); err != nil {
		t.Fatalf("Write(Written(%q), %q) error = %v, want nil", outDir, again, err)
	}
	if got, _ := os.ReadFile(WiringPath(again)); !bytes.Equal(got, want) {
		t.Errorf("Write(Written(%q)) = %q, want the refreshed wiring.json %q", outDir, got, want)
	}
	if _, ok := index.Docs["src/b.ts#c"]; !ok || index.DocCount != len(index.Docs) {
		t.Errorf("Written(%q) index docs = %v (count %d), want src/b.ts#c among them", outDir, slices.Collect(maps.Keys(index.Docs)), index.DocCount)
	}
	if got, gotIndex, ok := memo.Written(outDir, graphFile, nil); got != written || gotIndex != nil || !ok {
		t.Errorf("Written(%q, graph file, nil) = (%p, %v, %t), want (%p, nil, true): the graph alone", outDir, got, gotIndex, ok, written)
	}
	if got, gotIndex, ok := memo.Written(filepath.Join(t.TempDir(), "graft"), graphFile, indexFile); ok {
		t.Errorf("Written(another directory) = (%v, %v, true), want not current", got, gotIndex)
	}
	var empty RefreshMemo
	if got, gotIndex, ok := empty.Written(outDir, graphFile, indexFile); ok {
		t.Errorf("RefreshMemo{}.Written(%q) = (%v, %v, true), want not current", outDir, got, gotIndex)
	}
	var none *RefreshMemo
	if got, gotIndex, ok := none.Written(outDir, graphFile, indexFile); ok {
		t.Errorf("(*RefreshMemo)(nil).Written(%q) = (%v, %v, true), want not current", outDir, got, gotIndex)
	}

	// Another writer replaces the graph, here with a language-server edge:
	// the memo no longer offers its own, and the next refresh starts from the
	// file, so the edge of an unchanged caller survives it.
	lsp := EdgeV1{Source: "src/b.ts#c", Target: "src/a.ts#a", Relation: "calls", Confidence: "lsp_resolved"}
	elsewhere := *written
	elsewhere.Edges = append(slices.Clone(written.Edges), lsp)
	if _, err := Write(elsewhere, outDir); err != nil {
		t.Fatalf("Write(graph with an LSP edge, %q) error = %v, want nil", outDir, err)
	}
	replaced, _ := statMemoFiles(t, outDir)
	if got, gotIndex, ok := memo.Written(outDir, replaced, indexFile); ok {
		t.Errorf("Written(%q) after another writer replaced the graph = (%v, %v, true), want not current", outDir, got, gotIndex)
	}
	writeRefreshSource(t, root, "src/d.ts", "export function d() {}\n")
	if got := EnsureFreshGraph(root, RefreshOptions{Memo: &memo}); !got.Refreshed {
		t.Fatalf("EnsureFreshGraph(%q, memo) after another writer = %#v, want a refresh", root, got)
	}
	memo.Wait()
	wiring, err := Read(WiringPath(outDir))
	if err != nil || !slices.Contains(wiring.Edges, lsp) {
		t.Errorf("Read(%q) after the memo refresh = (edges %v, %v), want the other writer's %v kept", WiringPath(outDir), wiring.Edges, err, lsp)
	}
}

func TestRefreshMemoReportsAFailedWrite(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	root := newMemoFixture(t)
	outDir := filepath.Join(root, "graft")
	graphDir := filepath.Dir(WiringPath(outDir))
	var memo RefreshMemo
	writeRefreshSource(t, root, "src/b.ts", "export function b() {}\nexport function c() {}\n")
	if err := os.Chmod(graphDir, 0o500); err != nil { //nolint:gosec // G302: a directory made read-only on purpose, then restored
		t.Fatalf("Chmod(%q) error = %v, want nil", graphDir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(graphDir, 0o755) }) //nolint:gosec // G302: the mode MkdirAll gave it
	// The answer comes from memory, before the write that fails.
	if got := EnsureFreshGraph(root, RefreshOptions{Memo: &memo}); !got.Refreshed {
		t.Fatalf("EnsureFreshGraph(%q, memo) into a read-only graph directory = %#v, want the refresh answered from memory", root, got)
	}
	memo.Wait()
	graphFile, indexFile := statMemoFiles(t, outDir)
	if got, gotIndex, ok := memo.Written(outDir, graphFile, indexFile); ok {
		t.Errorf("Written(%q) after its write failed = (%v, %v, true), want not current: the files hold the graph before it", outDir, got, gotIndex)
	}
	if err := os.Chmod(graphDir, 0o755); err != nil { //nolint:gosec // G302: the mode MkdirAll gave it
		t.Fatalf("Chmod(%q) error = %v, want nil", graphDir, err)
	}
	// The next refresh says the last one was not saved, and saves it.
	got := EnsureFreshGraph(root, RefreshOptions{Memo: &memo})
	if !got.Refreshed || !strings.Contains(got.Note, "not saved") || !strings.Contains(got.Note, "write graph") {
		t.Errorf("EnsureFreshGraph(%q, memo) after a failed write = %#v, want a refresh whose note reports the failed write", root, got)
	}
	memo.Wait()
	if wiring, err := Read(WiringPath(outDir)); err != nil || !slices.ContainsFunc(wiring.Nodes, func(node NodeV1) bool { return node.ID == "src/b.ts#c" }) {
		t.Errorf("Read(%q) after the retry = (%v, %v), want src/b.ts#c saved", WiringPath(outDir), wiring, err)
	}
	if again := EnsureFreshGraph(root, RefreshOptions{Memo: &memo}); again.Refreshed || again.Note != "" {
		t.Errorf("EnsureFreshGraph(%q, memo) once saved = %#v, want nothing to do and no note", root, again)
	}
}

// newMemoFixture builds a graph with cards, an ask index and both caches, as
// graft build leaves it.
func newMemoFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeRefreshSource(t, root, "src/a.ts", "export function a() { return b() }\n")
	writeRefreshSource(t, root, "src/b.ts", "export function b() {}\n")
	opts := sourcefiles.Options{OutDir: filepath.Join(root, "graft")}
	buildAndWriteRefreshGraph(t, root, opts)
	built, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q) error = %v, want nil", root, err)
	}
	if err := WriteAskIndex(opts.OutDir, built.Graph); err != nil {
		t.Fatalf("WriteAskIndex(%q) error = %v, want nil", opts.OutDir, err)
	}
	if _, err := WriteCards(built.Graph, opts.OutDir); err != nil {
		t.Fatalf("WriteCards(%q) error = %v, want nil", opts.OutDir, err)
	}
	return root
}

func readMemoCaches(t *testing.T, root string) (parse, terms []byte) {
	t.Helper()
	cache := filepath.Join(root, "graft", ".cache")
	parse, err := os.ReadFile(filepath.Join(cache, "extract."+ExtractorID+".json"))
	if err != nil {
		t.Fatalf("ReadFile(parse cache) error = %v, want nil", err)
	}
	terms, err = os.ReadFile(filepath.Join(cache, "ask-terms.json"))
	if err != nil {
		t.Fatalf("ReadFile(term cache) error = %v, want nil", err)
	}
	return parse, terms
}

func statMemoFiles(t *testing.T, outDir string) (graphFile, indexFile os.FileInfo) {
	t.Helper()
	graphFile, err := os.Stat(WiringPath(outDir))
	if err != nil {
		t.Fatalf("Stat(%q) error = %v, want nil", WiringPath(outDir), err)
	}
	indexFile, err = os.Stat(filepath.Join(outDir, ".cache", "ask-index.json"))
	if err != nil {
		t.Fatalf("Stat(ask index) error = %v, want nil", err)
	}
	return graphFile, indexFile
}
