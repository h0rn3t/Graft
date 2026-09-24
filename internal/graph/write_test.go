package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWiringPathUsesGraphDirectory(t *testing.T) {
	got := WiringPath("context")
	want := filepath.Join("context", ".graph", "wiring.json")
	if got != want {
		t.Errorf("WiringPath(%q) = %q, want %q", "context", got, want)
	}
}

func TestWriteSortsAtomicallyAndStripsBodyText(t *testing.T) {
	bodyText := "body"
	graph := GraphV1{
		Meta: GraphMeta{Version: 1, NodeCount: 2, EdgeCount: 2, Languages: []string{"ts"}},
		Nodes: []NodeV1{
			{ID: "b", Name: "b", Kind: "function", Span: "L2-L3", BodyText: &bodyText},
			{ID: "a", Name: "a", Kind: "function", Span: "L1-L1"},
		},
		Edges: []EdgeV1{
			{Source: "b", Target: "z", Relation: "calls", Confidence: "extracted"},
			{Source: "a", Target: "b", Relation: "contains", Confidence: "extracted"},
		},
	}
	outDir := t.TempDir()
	path, err := Write(graph, outDir)
	if err != nil {
		t.Fatalf("Write(graph) error = %v", err)
	}
	if path != WiringPath(outDir) {
		t.Errorf("Write(graph) path = %q, want %q", path, WiringPath(outDir))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Errorf("Write(graph) output has no trailing newline: %q", data)
	}
	var encoded GraphV1
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatalf("json.Unmarshal(Write(graph)) error = %v", err)
	}
	if got := encoded.Nodes[0].ID; got != "a" {
		t.Errorf("Write(graph) first node = %q, want %q", got, "a")
	}
	if got := encoded.Edges[0].Source; got != "a" {
		t.Errorf("Write(graph) first edge source = %q, want %q", got, "a")
	}
	if encoded.Nodes[1].BodyText != nil {
		t.Errorf("Write(graph) serialized body_text = %q, want omitted", *encoded.Nodes[1].BodyText)
	}
	if graph.Nodes[0].BodyText == nil || *graph.Nodes[0].BodyText != bodyText {
		t.Errorf("Write(graph) mutated input body_text")
	}

	loaded, err := Read(path)
	if err != nil {
		t.Fatalf("Read(%q) error = %v", path, err)
	}
	if loaded.Meta.Version != graph.Meta.Version || len(loaded.Nodes) != len(graph.Nodes) {
		t.Errorf("Read(%q) = %#v, want version %d and %d nodes", path, loaded, graph.Meta.Version, len(graph.Nodes))
	}
}

func TestReadReturnsErrorsForMissingAndInvalidGraphs(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("Read(missing graph) error = nil, want error")
	}
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Error("Read(invalid graph) error = nil, want error")
	}
	if err := os.WriteFile(path, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Error("Read(null graph) error = nil, want error")
	}
}

func TestWritePreservesAnotherTemporaryFile(t *testing.T) {
	outDir := t.TempDir()
	path := WiringPath(outDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	other := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(other, []byte("another writer"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Write(GraphV1{Meta: GraphMeta{Version: 1}, Nodes: []NodeV1{}, Edges: []EdgeV1{}}, outDir)
	if err != nil {
		t.Fatalf("Write(graph) error = %v, want nil", err)
	}
	got, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "another writer" {
		t.Errorf("temporary file = %q, want %q", got, "another writer")
	}
}

func TestLocaleKeysOrderLikeLocaleCompare(t *testing.T) {
	words := []string{
		"", "a", "A", "b", "B", "_a", "a_b", "a-b", "a.b", "a/b", "a#b", "a~2", "a~10",
		"src/app.ts", "src/App.ts", "src/app.ts#greet", "src/app.ts#Greet", "src/app.tsx",
		"internal/graph/build.go#BuildGraph", "internal/graph/build.go#buildGraph",
		"café", "cafe", "Café", "résumé", "resume", "naïve", "straße", "strasse",
		"über", "Uber", "日本", "😀", "a😀", "1", "10", "2", "a1", "a10", "a2", " a", "a ",
		"calls", "contains", "imports", "references", "extends", "implements", "�",
	}
	// Enough copies to spread the keys over several workers.
	var texts []string
	for range 200 {
		texts = append(texts, words...)
	}
	keys := localeSortKeys(texts)
	compare := localeCompare()
	sign := func(n int) int { return min(max(n, -1), 1) }
	for a := range words {
		for b := range words {
			x, y := a+len(words)*(a%200), b+len(words)*(199-b%200)
			if got, want := sign(bytes.Compare(keys[x], keys[y])), sign(compare(words[a], words[b])); got != want {
				t.Errorf("bytes.Compare(localeSortKeys(%q), localeSortKeys(%q)) = %d, want %d", words[a], words[b], got, want)
			}
		}
	}
}

func TestSortNodesByIDIsStableLocaleOrder(t *testing.T) {
	nodes := []NodeV1{{ID: "b", Name: "1"}, {ID: "A", Name: "2"}, {ID: "a", Name: "3"}, {ID: "b", Name: "4"}, {ID: "_c", Name: "5"}}
	want := slices.Clone(nodes)
	compare := localeCompare()
	slices.SortStableFunc(want, func(a, b NodeV1) int { return compare(a.ID, b.ID) })
	if got := sortNodesByID(nodes); !slices.Equal(names(got), names(want)) {
		t.Errorf("sortNodesByID(%v) = %v, want %v", names(nodes), names(got), names(want))
	}
}

func names(nodes []NodeV1) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.ID+"/"+node.Name)
	}
	return out
}
