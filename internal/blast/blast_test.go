package blast

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestParseNameStatusAndHunks(t *testing.T) {
	files := parseNameStatus("M\x00src/a.ts\x00R096\x00old.ts\x00new.ts\x00D\x00gone.ts\x00A\x00add.ts\x00")
	patch := strings.Join([]string{
		"diff --git a/src/a.ts b/src/a.ts",
		"--- a/src/a.ts",
		"+++ b/src/a.ts",
		"@@ -3,0 +4,2 @@ func",
		"+one",
		"+two",
		"@@ -10 +11,0 @@",
		"-gone",
		"diff --git a/gone.ts b/gone.ts",
		"--- a/gone.ts",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-x",
	}, "\n")
	applyHunks(files, patch)
	got, err := jsonv2.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"path":"src/a.ts","status":"modified","ranges":[{"start":4,"end":5},{"start":11,"end":11}],` +
		`"hunks":[{"start":4,"end":5,"lines":[{"n":4,"sign":"+","text":"one"},{"n":5,"sign":"+","text":"two"}],"dropped":0},` +
		`{"start":11,"end":11,"lines":[{"n":null,"sign":"-","text":"gone"}],"dropped":0}]},` +
		`{"path":"new.ts","status":"renamed","oldPath":"old.ts","ranges":[],"hunks":[]},` +
		`{"path":"gone.ts","status":"deleted","ranges":[],"hunks":[]},` +
		`{"path":"add.ts","status":"added","ranges":[],"hunks":[]}]`
	if string(got) != want {
		t.Errorf("parsed diff = %s, want %s", got, want)
	}
}

func TestShortLabel(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"  Graph Store  ", "Graph Store"},
		{"Incremental Build via Content Fingerprints", "Incremental Build"},
		{"Reciprocal-Rank Fusion for Workspace Federation", "Reciprocal-Rank Fusion"},
		{"Supercalifragilisticexpialidocious Everywhere", "Supercalifragilisticexpialidoc…"},
		{"Alpha Beta Gamma Delta Epsilon Zeta Eta", "Alpha Beta Gamma Delta Epsilon…"},
	}
	for _, tt := range tests {
		if got := ShortLabel(tt.in); got != tt.want {
			t.Errorf("ShortLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCoarsenFoldsDeepestIntoExistingParent(t *testing.T) {
	groups := []*dirGroup{
		{dir: "src/ai/", paths: []string{"src/ai/a.ts"}},
		{dir: "src/ai/llm/", paths: []string{"src/ai/llm/b.ts"}},
		{dir: "lib/", paths: []string{"lib/c.ts"}},
		{dir: "tools/x/", paths: []string{"tools/x/d.ts"}},
		{dir: "docs/", paths: []string{"docs/e.ts"}},
		{dir: "cmd/", paths: []string{"cmd/f.ts"}},
	}
	got := make([]string, 0)
	for _, group := range coarsen(groups, 5) {
		got = append(got, group.dir+"="+strings.Join(group.paths, ","))
	}
	want := []string{"src/ai/=src/ai/a.ts,src/ai/llm/b.ts", "lib/=lib/c.ts", "tools/x/=tools/x/d.ts", "docs/=docs/e.ts", "cmd/=cmd/f.ts"}
	if !slices.Equal(got, want) {
		t.Errorf("coarsen = %q, want %q", got, want)
	}
}

func TestRadiusSeedsInnermostSymbolAndSerializesFullDepth(t *testing.T) {
	file := func(path string) graph.NodeV1 {
		return graph.NodeV1{ID: path, Name: path, Kind: "file", Path: path, Span: "L1-L20"}
	}
	wiring := graph.GraphV1{
		Nodes: []graph.NodeV1{
			file("src/cache.ts"), file("src/use.ts"),
			{ID: "src/cache.ts#Cache", Name: "Cache", Kind: "class", Path: "src/cache.ts", Span: "L1-L10"},
			{ID: "src/cache.ts#Cache.get", Name: "get", Kind: "method", Path: "src/cache.ts", Span: "L2-L4"},
			{ID: "src/use.ts#use", Name: "use", Kind: "function", Path: "src/use.ts", Span: "L1-L3"},
		},
		Edges: []graph.EdgeV1{{Source: "src/use.ts#use", Target: "src/cache.ts#Cache.get", Relation: "calls"}},
	}
	changed := []*ChangedFile{{Path: "src/cache.ts", Status: StatusModified, Ranges: []LineRange{{Start: 3, End: 3}}, Hunks: []*Hunk{}}}
	report := Radius(wiring, changed, "working tree vs HEAD", FullDepth)
	if len(report.Seeds) != 1 || report.Seeds[0].ID != "src/cache.ts#Cache.get" {
		t.Fatalf("Radius seeds = %+v, want only Cache.get", report.Seeds)
	}
	if len(report.Modules) != 1 || report.Modules[0].Label != "use" {
		t.Fatalf("Radius modules = %+v, want one module labelled use", report.Modules)
	}
	data, err := jsonv2.Marshal(report, jsontext.WithIndent("  "))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"depth": null,`) || strings.Contains(string(data), `"owners"`) {
		t.Errorf("Radius JSON = %s, want depth null and no owners", data)
	}
}
