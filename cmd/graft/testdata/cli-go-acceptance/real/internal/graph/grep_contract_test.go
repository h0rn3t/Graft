package graph

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGrepContract(t *testing.T) {
	dir := t.TempDir()
	writeGrepFile(t, dir, "src/a.ts", "NEEDLE module\nexport function root() {\n  NEEDLE root\n}\n")
	writeGrepFile(t, dir, "src/b.ts", "export function other() {\n  NEEDLE other\n}\n")
	rootChars := 59
	otherChars := 51
	root := grepContractNode("src/a.ts#root", "root", "function", "src/a.ts", "L2-L4", 0)
	other := grepContractNode("src/b.ts#other", "other", "function", "src/b.ts", "L1-L3", 0)
	fileA := grepContractNode("src/a.ts", "a.ts", "file", "src/a.ts", "L1-L4", rootChars)
	fileB := grepContractNode("src/b.ts", "b.ts", "file", "src/b.ts", "L1-L3", otherChars)
	wiring := GraphV1{
		Nodes: []NodeV1{fileA, root, fileB, other},
		Edges: []EdgeV1{
			{Source: "src/caller.ts#one", Target: root.ID, Relation: "calls"},
			{Source: "src/caller.ts#two", Target: root.ID, Relation: "calls"},
		},
	}

	result, err := Grep(wiring, dir, "NEEDLE", GrepOptions{})
	if err != nil {
		t.Fatalf("Grep(%q) error = %v, want nil", "NEEDLE", err)
	}
	if result.FilesSearched != 2 || result.TotalHits != 3 {
		t.Errorf("Grep(%q) counts = (%d, %d), want (2, 3)", "NEEDLE", result.FilesSearched, result.TotalHits)
	}
	if len(result.Groups) != 3 {
		t.Fatalf("Grep(%q) groups = %d, want 3", "NEEDLE", len(result.Groups))
	}
	if got := result.Groups[0].Symbol.Name; got != "root" {
		t.Errorf("Grep(%q) first group = %q, want root", "NEEDLE", got)
	}
	if got := result.Groups[0].InDegree; got != 2 {
		t.Errorf("Grep(%q) root inDegree = %d, want 2", "NEEDLE", got)
	}
	if result.Groups[1].Symbol != nil || result.Groups[1].Path != "src/a.ts" {
		t.Errorf("Grep(%q) module group = %#v, want src/a.ts with nil symbol", "NEEDLE", result.Groups[1])
	}
	if result.Saved == nil || result.Saved.Files != 2 || result.Saved.BaselineChars != rootChars+otherChars {
		t.Errorf("Grep(%q) saved = %#v, want two files and %d chars", "NEEDLE", result.Saved, rootChars+otherChars)
	}

	filtered, err := Grep(wiring, dir, "needle", GrepOptions{IgnoreCase: true, In: "src/a.ts"})
	if err != nil {
		t.Fatalf("Grep(%q) filtered error = %v, want nil", "needle", err)
	}
	if filtered.FilesSearched != 1 || filtered.TotalHits != 2 || len(filtered.Groups) != 2 {
		t.Errorf("Grep(%q) filtered = %#v, want one file and two hits in two groups", "needle", filtered)
	}

	fixedDir := t.TempDir()
	writeGrepFile(t, fixedDir, "x.txt", "axb\na.b literal\n")
	fixedWiring := GraphV1{Nodes: []NodeV1{grepContractNode("x.txt", "x.txt", "file", "x.txt", "L1-L2", 0)}}
	regexResult, err := Grep(fixedWiring, fixedDir, "a.b", GrepOptions{})
	if err != nil || regexResult.TotalHits != 2 {
		t.Errorf("Grep(%q) regex = (%#v, %v), want two hits and nil error", "a.b", regexResult, err)
	}
	fixedResult, err := Grep(fixedWiring, fixedDir, "a.b", GrepOptions{Fixed: true})
	if err != nil || fixedResult.TotalHits != 1 {
		t.Errorf("Grep(%q) fixed = (%#v, %v), want one hit and nil error", "a.b", fixedResult, err)
	}

	truncated, err := Grep(fixedWiring, fixedDir, "a", GrepOptions{MaxHits: 1})
	if err != nil || truncated.TotalHits != 1 || truncated.Truncated.Hits != 1 {
		t.Errorf("Grep(%q) truncation = (%#v, %v), want one collected and one dropped", "a", truncated, err)
	}

	empty, err := Grep(fixedWiring, fixedDir, "absent", GrepOptions{})
	if err != nil {
		t.Fatalf("Grep(%q) empty error = %v, want nil", "absent", err)
	}
	if empty.Groups == nil || len(empty.Groups) != 0 || empty.Saved != nil {
		t.Errorf("Grep(%q) empty = %#v, want non-nil empty groups and no saved value", "absent", empty)
	}

	_, err = Grep(wiring, dir, "[", GrepOptions{})
	if _, ok := errors.AsType[*GrepPatternError](err); !ok {
		t.Errorf("Grep(%q) error = %v, want GrepPatternError", "[", err)
	}
	_, err = Grep(wiring, dir, "NEEDLE", GrepOptions{In: "missing"})
	if err == nil {
		t.Errorf("Grep(%q) missing prefix error = nil, want error", "NEEDLE")
	}

	missingWiring := GraphV1{Nodes: []NodeV1{
		grepContractNode("real.txt", "real.txt", "file", "real.txt", "L1-L1", 0),
		grepContractNode("missing.txt", "missing.txt", "file", "missing.txt", "L1-L1", 0),
	}}
	writeGrepFile(t, dir, "real.txt", "no match\n")
	missing, err := Grep(missingWiring, dir, "absent", GrepOptions{})
	if err != nil || missing.FilesSearched != 2 || missing.Truncated.Files != 1 {
		t.Errorf("Grep(%q) unreadable = (%#v, %v), want two searched and one unreadable", "absent", missing, err)
	}

	if !reflect.DeepEqual(filtered.Groups[0].Hits, []GrepHit{{Line: 1, Text: "NEEDLE module"}}) && !reflect.DeepEqual(filtered.Groups[0].Hits, []GrepHit{{Line: 3, Text: "NEEDLE root"}}) {
		t.Errorf("Grep(%q) filtered hit = %#v, want a decoded matching line", "needle", filtered.Groups[0].Hits)
	}
}

func grepContractNode(id, name, kind, path, span string, chars int) NodeV1 {
	node := NodeV1{ID: id, Name: name, Kind: Kind(kind), Path: path, Span: span}
	if chars > 0 {
		node.Chars = &chars
	}
	return node
}

func writeGrepFile(t *testing.T, root, path, text string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(absolute, []byte(text), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
