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

func TestWriteCardsPreservesConceptCollision(t *testing.T) {
	outDir := t.TempDir()
	concept := "---\nname: Server\nslug: server\ntype: service\nsources:\n  - path: server.ts\n    hash: abc\n---\nsummary\n"
	path := filepath.Join(outDir, "server.md")
	if err := os.WriteFile(path, []byte(concept), 0o644); err != nil {
		t.Fatal(err)
	}
	graph := GraphV1{Nodes: []NodeV1{
		{ID: "server.ts", Name: "server.ts", Kind: "file", Path: "server.ts", Span: "L1-L1"},
		{ID: "server.ts#serve", Name: "serve", Kind: "function", Path: "server.ts", Span: "L1-L1"},
	}}
	if _, err := WriteCards(graph, outDir); err != nil {
		t.Fatalf("WriteCards(graph, %q) error = %v, want nil", outDir, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), strings.TrimSuffix(concept, "---\nsummary\n")) || !strings.Contains(string(got), "covers:\n  - symbol: serve\n    kind: function\n    at: 'server.ts:L1-L1'\n---\nsummary\n") {
		t.Errorf("WriteCards(graph, %q) concept = %q, want source frontmatter and covers with body preserved", outDir, got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "_root", "server.md")); err != nil {
		t.Errorf("WriteCards(graph, %q) root card Stat error = %v, want present", outDir, err)
	}
	card, err := os.ReadFile(filepath.Join(outDir, "_root", "server.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(card), "[[server]]") {
		t.Errorf("WriteCards(graph, %q) root card = %q, want concept uplink", outDir, card)
	}
}

func TestWriteCardsRecognizesCRLFConcept(t *testing.T) {
	outDir := t.TempDir()
	concept := strings.ReplaceAll("---\nslug: server\nname: Server\nsources: []\n---\nHuman note.\n", "\n", "\r\n")
	if err := os.WriteFile(filepath.Join(outDir, "server.md"), []byte(concept), 0o644); err != nil {
		t.Fatal(err)
	}
	graph := GraphV1{Nodes: []NodeV1{{ID: "server.ts", Name: "server.ts", Kind: "file", Path: "server.ts", Span: "L1-L1"}}}
	if _, err := WriteCards(graph, outDir); err != nil {
		t.Fatalf("WriteCards(graph, %q) error = %v, want nil", outDir, err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "_root", "server.md")); err != nil {
		t.Errorf("WriteCards(graph, %q) CRLF concept collision Stat error = %v, want relocated card", outDir, err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "server.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Human note.") {
		t.Errorf("WriteCards(graph, %q) concept = %q, want human note preserved", outDir, data)
	}
}
