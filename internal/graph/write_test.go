package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
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
}
