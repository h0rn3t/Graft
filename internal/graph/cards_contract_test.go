package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCardsContract(t *testing.T) {
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "old", "stale.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	signature := "export function alpha() {}"
	graph := GraphV1{Nodes: []NodeV1{
		{ID: "src/a.ts", Name: "a.ts", Kind: "file", Path: "src/a.ts", Span: "L1-L1"},
		{ID: "src/a.ts#alpha", Name: "alpha", Kind: "function", Path: "src/a.ts", Span: "L1-L1", Signature: &signature},
		{ID: "b.ts", Name: "b.ts", Kind: "file", Path: "b.ts", Span: "L1-L1"},
	}}
	stats, err := WriteCards(graph, outDir)
	if err != nil {
		t.Fatalf("WriteCards(graph, %q) error = %v, want nil", outDir, err)
	}
	if stats.Written != 2 || stats.Pruned != 1 {
		t.Errorf("WriteCards(graph, %q) = %#v, want 2 written and 1 pruned", outDir, stats)
	}
	for _, item := range []struct {
		path string
		want string
	}{
		{"src/a.md", "# src/a.ts\n\n- alpha · function · L1-L1 — export function alpha() {}\n"},
		{"b.md", "# b.ts\n\n_No extracted symbols in this file._\n"},
	} {
		got, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(item.path)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != item.want {
			t.Errorf("WriteCards(graph, %q) %s = %q, want %q", outDir, item.path, got, item.want)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "old")); !os.IsNotExist(err) {
		t.Errorf("WriteCards(graph, %q) old directory Stat error = %v, want absent", outDir, err)
	}
	index, err := os.ReadFile(filepath.Join(outDir, "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "2 per-file wiring cards") || !strings.Contains(string(index), "(1 carry extracted symbols)") {
		t.Errorf("WriteCards(graph, %q) INDEX.md = %q, want card summary", outDir, index)
	}
}

func TestWriteCardsIgnoresLegacyConceptCards(t *testing.T) {
	outDir := t.TempDir()
	concept := "---\nname: Server\nslug: server\ntype: service\nsources:\n  - path: src/server.ts\n    hash: abc\n---\nsummary\n"
	conceptPath := filepath.Join(outDir, "server.md")
	if err := os.WriteFile(conceptPath, []byte(concept), 0o644); err != nil {
		t.Fatal(err)
	}
	graph := GraphV1{Nodes: []NodeV1{{ID: "src/server.ts", Name: "server.ts", Kind: "file", Path: "src/server.ts", Span: "L1-L1"}}}
	if _, err := WriteCards(graph, outDir); err != nil {
		t.Fatalf("WriteCards(graph, %q) error = %v, want nil", outDir, err)
	}
	got, err := os.ReadFile(conceptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != concept {
		t.Errorf("WriteCards(graph, %q) legacy concept = %q, want unchanged %q", outDir, got, concept)
	}
	index, err := os.ReadFile(filepath.Join(outDir, "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "## Concepts") || strings.Contains(string(index), "[server](server.md)") {
		t.Errorf("WriteCards(graph, %q) INDEX.md = %q, want no concept section", outDir, index)
	}
}
