package graph

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

func TestRustGenericExtractorContract(t *testing.T) {
	const source = "pub struct Config { name: String }\n" +
		"pub fn load() -> Config { let c = parse(); Config { name: c } }\n" +
		"fn parse() -> String { helper() }\n" +
		"fn helper() -> String { String::new() }\n" +
		"use crate::util::Thing;\n"
	got, err := extractFile("lib.rs", source)
	if err != nil {
		t.Fatalf("extractFile(%q, source) error = %v, want nil", "lib.rs", err)
	}
	wantIDs := []string{"lib.rs", "lib.rs#Config", "lib.rs#load", "lib.rs#parse", "lib.rs#helper"}
	wantKinds := []Kind{"file", "struct", "function", "function", "function"}
	wantSpans := []string{"L1-L6", "L1-L1", "L2-L2", "L3-L3", "L4-L4"}
	wantSignatures := []string{"", "pub struct Config { name: String }", "pub fn load() -> Config { let c = parse(); Config { name: c } }", "fn parse() -> String { helper() }", "fn helper() -> String { String::new() }"}
	if len(got.nodes) != len(wantIDs) {
		t.Fatalf("extractFile(%q, source) node count = %d, want %d", "lib.rs", len(got.nodes), len(wantIDs))
	}
	var ids []string
	var kinds []Kind
	for index, node := range got.nodes {
		ids = append(ids, node.ID)
		kinds = append(kinds, node.Kind)
		if node.Origin != "generic" {
			t.Errorf("extractFile(%q, source) node %q origin = %q, want generic", "lib.rs", node.ID, node.Origin)
		}
		signature := ""
		if node.Signature != nil {
			signature = *node.Signature
		}
		if node.Span != wantSpans[index] || signature != wantSignatures[index] {
			t.Errorf("extractFile(%q, source) node %q span/signature = (%q, %q), want (%q, %q)", "lib.rs", node.ID, node.Span, signature, wantSpans[index], wantSignatures[index])
		}
	}
	if got.nodes[0].Chars == nil || *got.nodes[0].Chars != len(source) {
		t.Errorf("extractFile(%q, source) file chars = %v, want %d bytes", "lib.rs", got.nodes[0].Chars, len(source))
	}
	if !reflect.DeepEqual(ids, wantIDs) || !reflect.DeepEqual(kinds, wantKinds) {
		t.Errorf("extractFile(%q, source) nodes = (%v, %v), want (%v, %v)", "lib.rs", ids, kinds, wantIDs, wantKinds)
	}
	wantEdges := []rawEdge{
		{source: "lib.rs#load", relation: "calls", name: "parse", file: "lib.rs"},
		{source: "lib.rs#parse", relation: "calls", name: "helper", file: "lib.rs"},
		{source: "lib.rs#helper", relation: "calls", name: "new", file: "lib.rs"},
		{source: "lib.rs", relation: "imports", specifier: "crate/util/Thing", file: "lib.rs"},
	}
	if !reflect.DeepEqual(got.rawEdges, wantEdges) {
		t.Errorf("extractFile(%q, source) raw edges = %#v, want %#v", "lib.rs", got.rawEdges, wantEdges)
	}
}

func TestBuildGraphRustContract(t *testing.T) {
	root := t.TempDir()
	source := "fn helper() -> i32 { 1 }\nfn run() -> i32 { helper() }\n"
	file := filepath.Join(root, "lib.rs")
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", file, err)
	}
	opts := sourcefiles.Options{OutDir: filepath.Join(root, "graft")}
	first, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v", root, opts, err)
	}
	if first.Parsed != 1 || len(first.Unsupported) != 0 || len(first.Errors) != 0 ||
		!reflect.DeepEqual(first.Graph.Meta.Languages, []string{"rust"}) {
		t.Errorf("BuildGraph(%q, %#v) coverage = (parsed %d, unsupported %v, errors %v, languages %v), want complete Rust graph", root, opts, first.Parsed, first.Unsupported, first.Errors, first.Graph.Meta.Languages)
	}
	wantCall := EdgeV1{Source: "lib.rs#run", Target: "lib.rs#helper", Relation: "calls", Confidence: "extracted"}
	found := false
	for _, edge := range first.Graph.Edges {
		found = found || edge == wantCall
	}
	if !found {
		t.Errorf("BuildGraph(%q, %#v) edges = %#v, want %#v", root, opts, first.Graph.Edges, wantCall)
	}
	second, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) second call error = %v", root, opts, err)
	}
	if second.Parsed != 0 || second.Reused != 1 || !reflect.DeepEqual(first.Graph, second.Graph) {
		t.Errorf("BuildGraph(%q, %#v) second result = (parsed %d, reused %d, graph equal %t), want (0, 1, true)", root, opts, second.Parsed, second.Reused, reflect.DeepEqual(first.Graph, second.Graph))
	}
}
