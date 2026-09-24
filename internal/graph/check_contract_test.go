package graph

import (
	"path/filepath"
	"testing"
)

func TestCheckGraphUsesDistinctCommittedIDs(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	committed := GraphV1{
		Meta: GraphMeta{Version: 1},
		Nodes: []NodeV1{
			{ID: "same", Name: "first", Kind: "function", Path: "app.ts", Span: "L1-L1", BodyHash: "first"},
			{ID: "same", Name: "last", Kind: "function", Path: "app.ts", Span: "L1-L1", BodyHash: "last"},
		},
		Edges: []EdgeV1{},
	}
	if _, err := Write(committed, outDir); err != nil {
		t.Fatalf("Write(graph, %q) error = %v", outDir, err)
	}

	got, err := CheckGraph(root, outDir)
	if err != nil {
		t.Fatalf("CheckGraph(%q, %q) error = %v", root, outDir, err)
	}
	if len(got.Removed) != 1 {
		t.Errorf("CheckGraph(%q, %q) = %#v, want one removed distinct committed ID", root, outDir, got)
	}
}
