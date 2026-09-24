package graph

import (
	"slices"
	"testing"
	"unicode/utf16"
)

func TestGenericTagsCharsAndEnclosingCalls(t *testing.T) {
	const source = "// café 😀\n" +
		"fn outer() {\n" +
		"    fn inner() { leaf(); }\n" +
		"    helper();\n" +
		"}\n" +
		"fn after() { leaf(); }\n" +
		"fn leaf() {}\n" +
		"fn helper() {}\n"
	got, err := extractFile("src/lib.rs", source)
	if err != nil {
		t.Fatalf("extractFile(%q, source) error = %v, want nil", "src/lib.rs", err)
	}
	if want := len(utf16.Encode([]rune(source))); got.nodes[0].Chars == nil || *got.nodes[0].Chars != want {
		t.Errorf("extractFile(%q) file chars = %v, want %d UTF-16 units", "src/lib.rs", got.nodes[0].Chars, want)
	}
	var calls []rawEdge
	for _, edge := range got.rawEdges {
		if edge.relation == "calls" {
			calls = append(calls, edge)
		}
	}
	want := []rawEdge{
		{source: "src/lib.rs#inner", relation: "calls", name: "leaf", file: "src/lib.rs"},
		{source: "src/lib.rs#outer", relation: "calls", name: "helper", file: "src/lib.rs"},
		{source: "src/lib.rs#after", relation: "calls", name: "leaf", file: "src/lib.rs"},
	}
	for _, edge := range want {
		if !slices.ContainsFunc(calls, func(got rawEdge) bool { return got.source == edge.source && got.name == edge.name }) {
			t.Errorf("extractFile(%q) calls = %#v, want %#v", "src/lib.rs", calls, edge)
		}
	}
	if len(calls) != len(want) {
		t.Errorf("extractFile(%q) calls = %#v, want %d", "src/lib.rs", calls, len(want))
	}
}

func TestGenericTagsQueryIsShared(t *testing.T) {
	grammar := genericNativeGrammars["cpp"]
	first, err := grammar.query()
	if err != nil {
		t.Fatalf("cpp query() error = %v, want nil", err)
	}
	second, err := grammar.query()
	if err != nil || second != first {
		t.Errorf("cpp query() second = (%p, %v), want the cached %p", second, err, first)
	}
}
