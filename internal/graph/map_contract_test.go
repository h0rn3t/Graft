package graph

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestMapLanguagesCoverEveryNativeLanguage(t *testing.T) {
	paths := []string{"a.ts", "b.TSX", "c.py", "d.go", "e.java", "f.rs", "g.c", "h.hpp", "i.cc", "j.SQL", "k.md"}
	want := []string{"c", "cpp", "go", "java", "python", "rust", "sql", "tsx", "typescript"}
	if got := mapSortedLanguages(paths); !slices.Equal(got, want) {
		t.Errorf("mapSortedLanguages(%q) = %q, want %q", paths, got, want)
	}
}

func TestBuildRepoMapContract(t *testing.T) {
	graph := mapContractGraph()
	result := BuildRepoMap(graph, RepoMapOptions{MaxDirs: 2, Hotspots: 2})

	if result.Totals.Files != 6 || result.Totals.Symbols != 6 || result.Totals.Edges != 4 {
		t.Errorf("BuildRepoMap(totals) = %#v, want 6 files, 6 symbols, 4 edges", result.Totals)
	}
	if got := strings.Join(result.Totals.Languages, ","); got != "python,typescript" {
		t.Errorf("BuildRepoMap(totals.languages) = %q, want %q", got, "python,typescript")
	}
	if got := []string{result.Dirs[0].Path, result.Dirs[1].Path}; !sameStrings(got, []string{"src/ask", "src/graph"}) {
		t.Errorf("BuildRepoMap(dirs) = %v, want [src/ask src/graph]", got)
	}
	if result.Dropped != 2 {
		t.Errorf("BuildRepoMap(dropped) = %d, want 2", result.Dropped)
	}
	if result.Dirs[0].Files != 2 || result.Dirs[0].Symbols != 2 {
		t.Errorf("BuildRepoMap(src/ask) = %#v, want 2 files and 2 symbols", result.Dirs[0])
	}
	if len(result.Dirs[0].Hubs) != 2 || result.Dirs[0].Hubs[0].Name != "alpha" || result.Dirs[0].Hubs[0].InDegree != 2 {
		t.Errorf("BuildRepoMap(src/ask hubs) = %#v, want alpha first with degree 2", result.Dirs[0].Hubs)
	}
	if len(result.Hotspots) != 2 || result.Hotspots[0].Name != "alpha" || result.Hotspots[1].Name != "beta" {
		t.Errorf("BuildRepoMap(hotspots) = %#v, want alpha then beta", result.Hotspots)
	}
	if result.Saved == nil || result.Saved.Files != 6 || result.Saved.BaselineChars != 600 {
		t.Errorf("BuildRepoMap(saved) = %#v, want six files and 600 chars", result.Saved)
	}

	scopeGraph := graph
	scopeGraph.Meta.Scopes = &[]ScopeV1{
		{Prefix: "backend", Label: "backend", Markers: []string{"go.mod"}},
		{Prefix: "frontend", Label: "frontend", Markers: []string{"package.json"}},
	}
	scoped := BuildRepoMap(scopeGraph, RepoMapOptions{})
	if len(scoped.Dirs) != 0 || len(scoped.Scopes) != 2 {
		t.Errorf("BuildRepoMap(scopes) = dirs %v, scopes %v, want empty dirs and two scopes", scoped.Dirs, scoped.Scopes)
	}
	if scoped.Scopes[0].Scope != "backend/" || scoped.Scopes[1].Scope != "frontend/" {
		t.Errorf("BuildRepoMap(scope labels) = %#v, want backend/ then frontend/", scoped.Scopes)
	}

	empty := BuildRepoMap(GraphV1{}, RepoMapOptions{})
	if empty.Dirs == nil || empty.Hotspots == nil || empty.Scopes != nil {
		t.Errorf("BuildRepoMap(empty) = %#v, want [] dirs/hotspots and omitted scopes", empty)
	}
	data, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("json.Marshal(BuildRepoMap(empty)) error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"dirs":[]`) || !strings.Contains(text, `"hotspots":[]`) || strings.Contains(text, `"scopes"`) {
		t.Errorf("json.Marshal(BuildRepoMap(empty)) = %s, want dirs/hotspots arrays and no scopes", text)
	}

	formatted := FormatRepoMap(empty)
	if !strings.Contains(formatted, "repo map — 0 files · 0 symbols · 0 edges") || !strings.Contains(formatted, "hotspots:") {
		t.Errorf("FormatRepoMap(empty) = %q, want header and hotspots", formatted)
	}
}

func mapContractGraph() GraphV1 {
	fileA := mapFileNode("src/ask/a.ts", 100)
	fileB := mapFileNode("src/ask/b.ts", 100)
	fileC := mapFileNode("src/graph/c.ts", 100)
	fileF := mapFileNode("src/graph/f.ts", 100)
	fileD := mapFileNode("tests/d.ts", 100)
	fileE := mapFileNode("docs/readme.py", 100)
	alpha := mapSymbolNode("src/ask/a.ts", "alpha")
	beta := mapSymbolNode("src/ask/b.ts", "beta")
	gamma := mapSymbolNode("src/graph/c.ts", "gamma")
	zeta := mapSymbolNode("src/graph/f.ts", "zeta")
	delta := mapSymbolNode("tests/d.ts", "delta")
	epsilon := mapSymbolNode("docs/readme.py", "epsilon")
	return GraphV1{
		Meta:  GraphMeta{Version: 1, NodeCount: 10, EdgeCount: 4, Languages: []string{"typescript", "python"}},
		Nodes: []NodeV1{fileA, alpha, fileB, beta, fileC, gamma, fileF, zeta, fileD, delta, fileE, epsilon},
		Edges: []EdgeV1{
			{Source: "caller#one", Target: alpha.ID, Relation: "calls"},
			{Source: "caller#two", Target: alpha.ID, Relation: "calls"},
			{Source: "caller#one", Target: beta.ID, Relation: "calls"},
			{Source: "caller#two", Target: gamma.ID, Relation: "calls"},
		},
	}
}

func mapFileNode(path string, chars int) NodeV1 {
	return NodeV1{ID: path, Name: path, Kind: "file", Path: path, Span: "L1-L1", Chars: new(chars)}
}

func mapSymbolNode(path, name string) NodeV1 {
	return NodeV1{ID: path + "#" + name, Name: name, Kind: "function", Path: path, Span: "L1-L3"}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
