package graph

import (
	"strings"
	"testing"
)

func askDocGraph() GraphV1 {
	node := func(id, name, signature, summary string) NodeV1 {
		path, _, _ := strings.Cut(id, "#")
		body := signature + " {}"
		n := NodeV1{ID: id, Name: name, Kind: "function", Path: path, Span: "L2-L2", Signature: &signature, BodyText: &body}
		if summary != "" {
			n.Summary = &summary
		}
		return n
	}
	return GraphV1{Nodes: []NodeV1{
		{ID: "a.go", Name: "a.go", Kind: "file", Path: "a.go", Span: "L1-L2"},
		node("a.go#load", "load", "func load() error", "Reads the persisted settings from disk.\nMissing files are not an error."),
		{ID: "b.go", Name: "b.go", Kind: "file", Path: "b.go", Span: "L1-L2"},
		node("b.go#persistedSettings", "persistedSettings", "func persistedSettings()", ""),
		{ID: "c.go", Name: "c.go", Kind: "file", Path: "c.go", Span: "L1-L2"},
		node("c.go#other", "other", "func other()", ""),
	}}
}

func TestAskMatchesSymbolDocumentation(t *testing.T) {
	result, err := Ask(askDocGraph(), "disk", AskOptions{NoGraphRank: true})
	if err != nil {
		t.Fatalf("Ask(%q) error = %v, want nil", "disk", err)
	}
	if len(result.Hits) == 0 {
		t.Fatalf("Ask(%q).Hits = none, want load", "disk")
	}
	hit := result.Hits[0]
	if hit.Title != "load · function" || hit.Snippet != "func load() error" || hit.Doc != "Reads the persisted settings from disk." {
		t.Errorf("Ask(%q).Hits[0] = (title %q, snippet %q, doc %q), want (%q, %q, %q)", "disk", hit.Title, hit.Snippet, hit.Doc,
			"load · function", "func load() error", "Reads the persisted settings from disk.")
	}
	data, err := hit.MarshalJSON()
	if err != nil {
		t.Fatalf("Ask(%q).Hits[0].MarshalJSON() error = %v", "disk", err)
	}
	if want := `"snippet":"func load() error","doc":"Reads the persisted settings from disk."`; !strings.Contains(string(data), want) {
		t.Errorf("Ask(%q).Hits[0].MarshalJSON() = %s, want it to contain %s", "disk", data, want)
	}
	hit.ScopeAfterCode = true
	if data, _ := hit.MarshalJSON(); !strings.Contains(string(data), `"doc":"Reads the persisted settings from disk."`) {
		t.Errorf("workspace Ask(%q).Hits[0].MarshalJSON() = %s, want a doc field", "disk", data)
	}
}

func TestAskUndocumentedHitHasNoDoc(t *testing.T) {
	result, err := Ask(askDocGraph(), "other", AskOptions{NoGraphRank: true})
	if err != nil {
		t.Fatalf("Ask(%q) error = %v, want nil", "other", err)
	}
	if len(result.Hits) == 0 || result.Hits[0].Title != "other · function" {
		t.Fatalf("Ask(%q).Hits = %#v, want other first", "other", result.Hits)
	}
	if data, _ := result.Hits[0].MarshalJSON(); strings.Contains(string(data), `"doc"`) {
		t.Errorf("Ask(%q).Hits[0].MarshalJSON() = %s, want no doc field", "other", data)
	}
}

func TestAskNameMatchOutranksDocMatch(t *testing.T) {
	result, err := Ask(askDocGraph(), "persisted settings", AskOptions{NoGraphRank: true})
	if err != nil {
		t.Fatalf("Ask(%q) error = %v, want nil", "persisted settings", err)
	}
	rank := map[string]int{}
	for index, hit := range result.Hits {
		if _, seen := rank[hit.Title]; !seen {
			rank[hit.Title] = index
		}
	}
	name, nameOK := rank["persistedSettings · function"]
	doc, docOK := rank["load · function"]
	if !nameOK || !docOK || name > doc {
		t.Errorf("Ask(%q) ranks = %v, want persistedSettings at or above load, both present", "persisted settings", rank)
	}
}

func TestAskStructuralHitHasNoDoc(t *testing.T) {
	wiring := askDocGraph()
	wiring.Edges = []EdgeV1{{Source: "a.go#load", Target: "c.go#other", Relation: "calls"}}
	result, err := Ask(wiring, "who calls other", AskOptions{NoGraphRank: true})
	if err != nil {
		t.Fatalf("Ask(%q) error = %v, want nil", "who calls other", err)
	}
	if result.Mode != "structural" || len(result.Hits) != 1 {
		t.Fatalf("Ask(%q) = (mode %q, %d hits), want one structural hit", "who calls other", result.Mode, len(result.Hits))
	}
	if hit := result.Hits[0]; hit.Title != "load" || hit.Snippet != "func load() error" || hit.Doc != "" {
		t.Errorf("Ask(%q).Hits[0] = (title %q, snippet %q, doc %q), want (%q, %q, no doc)", "who calls other", hit.Title, hit.Snippet, hit.Doc, "load", "func load() error")
	}
}
