package graph

import (
	"strings"
	"testing"
)

func TestResolveSymbolContract(t *testing.T) {
	cacheGet := traversalNode("src/cache.ts#Cache.get", "get", "method", "src/cache.ts")
	otherGet := traversalNode("src/other.ts#get", "get", "function", "src/other.ts")
	duplicate := traversalNode("src/dup.ts#C~2.m", "m", "method", "src/dup.ts")
	file := traversalNode("src/file.ts", "file.ts", "file", "src/file.ts")
	graph := traversalGraph(
		[]NodeV1{cacheGet, otherGet, duplicate, file},
		nil,
	)

	tests := []struct {
		name  string
		query string
		in    string
		want  []string
	}{
		{name: "bare name is case insensitive", query: "GET", want: []string{cacheGet.ID, otherGet.ID}},
		{name: "qualified name matches id suffix", query: "Cache.get", want: []string{cacheGet.ID}},
		{name: "dotted package name falls back to last segment", query: "pkg.Get", want: []string{cacheGet.ID, otherGet.ID}},
		{name: "ordinal suffix remains qualified", query: "C.m", want: []string{duplicate.ID}},
		{name: "filename is a last resort", query: "file.ts", want: []string{file.ID}},
		{name: "in filters by path prefix", query: "get", in: "src/cache.ts", want: []string{cacheGet.ID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveSymbol(graph, tt.query, ResolveSymbolOptions{In: tt.in})
			if err != nil {
				t.Fatalf("ResolveSymbol(%q, %q) error = %v, want nil", tt.query, tt.in, err)
			}
			if ids := nodeIDs(got); strings.Join(ids, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("ResolveSymbol(%q, %q) = %v, want %v", tt.query, tt.in, ids, tt.want)
			}
		})
	}
	if _, err := ResolveSymbol(graph, "get", ResolveSymbolOptions{In: "cache"}); err == nil || !strings.Contains(err.Error(), `nothing indexed under "cache/"`) {
		t.Errorf(`ResolveSymbol(%q, %q) error = %v, want an indexed-prefix error`, "get", "cache", err)
	}
}

func TestDirectWalkContract(t *testing.T) {
	root := traversalNode("src/root.ts#root", "root", "function", "src/root.ts")
	caller := traversalNode("src/caller.ts#caller", "caller", "function", "src/caller.ts")
	callee := traversalNode("src/callee.ts#callee", "callee", "function", "src/callee.ts")
	graph := traversalGraph(
		[]NodeV1{root, caller, callee},
		[]EdgeV1{
			traversalEdge("src/caller.ts#caller", root.ID, "calls"),
			traversalEdge(root.ID, callee.ID, "calls"),
			traversalEdge(root.ID, "npm:lodash", "imports"),
			traversalEdge("src/root.ts", root.ID, "contains"),
		},
	)

	callers := CallersOf(graph, root)
	if len(callers) != 1 || callers[0].ID != caller.ID || callers[0].Node == nil || callers[0].Relation != Relation("calls") || callers[0].Depth != 1 {
		t.Errorf("CallersOf(root) = %#v, want one depth-1 caller hit", callers)
	}

	callees := CalleesOf(graph, root)
	if len(callees) != 2 || callees[0].ID != callee.ID || callees[1].ID != "npm:lodash" {
		t.Errorf("CalleesOf(root) = %#v, want callee and unresolved import in edge order", callees)
	}
	if callees[1].Node != nil {
		t.Errorf("CalleesOf(root)[1].Node = %#v, want nil for unresolved target", callees[1].Node)
	}
	if got := CallersOf(graph, root); got[0].ID != caller.ID {
		t.Errorf("CallersOf(root) = %#v, want contains edge excluded", got)
	}
}

func TestImpactAndEdgeWalkContract(t *testing.T) {
	x := traversalNode("X", "X", "function", "x.ts")
	a := traversalNode("A", "A", "function", "a.ts")
	b := traversalNode("B", "B", "function", "b.ts")
	c := traversalNode("C", "C", "function", "c.ts")
	graph := traversalGraph(
		[]NodeV1{x, a, b, c},
		[]EdgeV1{
			traversalEdge(a.ID, x.ID, "calls"),
			traversalEdge(b.ID, x.ID, "calls"),
			traversalEdge(c.ID, a.ID, "calls"),
			traversalEdge(c.ID, b.ID, "calls"),
			traversalEdge(x.ID, x.ID, "calls"),
		},
	)

	hits := ImpactOf(graph, x, 2)
	if got := nodeIDsFromHits(hits); strings.Join(got, "\x00") != "A\x00B\x00C" {
		t.Errorf("ImpactOf(x, 2) = %v, want [A B C] in BFS order", got)
	}
	if hits[0].Depth != 1 || hits[1].Depth != 1 || hits[2].Depth != 2 {
		t.Errorf("ImpactOf(x, 2) depths = %#v, want [1 1 2]", hits)
	}
	if len(EdgeWalk(graph, x, DirectionIn, 1)) != 3 {
		t.Errorf("EdgeWalk(x, in, 1) length = %d, want 3 including direct recursion", len(EdgeWalk(graph, x, DirectionIn, 1)))
	}
	if got := nodeIDsFromHits(EdgeWalk(graph, x, DirectionIn, 2)); strings.Join(got, "\x00") != "A\x00B\x00C" {
		t.Errorf("EdgeWalk(x, in, 2) = %v, want [A B C] with seed excluded", got)
	}
	if got := nodeIDsFromHits(EdgeWalk(graph, a, DirectionOut, 2)); strings.Join(got, "\x00") != "X" {
		t.Errorf("EdgeWalk(a, out, 2) = %v, want [X]", got)
	}
}

func TestImpactOfFileContract(t *testing.T) {
	fileA := traversalNode("src/a.ts", "a.ts", "file", "src/a.ts")
	helper := traversalNode("src/a.ts#helper", "helper", "function", "src/a.ts")
	fileB := traversalNode("src/b.ts", "b.ts", "file", "src/b.ts")
	useB := traversalNode("src/b.ts#useB", "useB", "function", "src/b.ts")
	graph := traversalGraph(
		[]NodeV1{fileA, helper, fileB, useB},
		[]EdgeV1{
			traversalEdge(fileB.ID, fileA.ID, "imports"),
			traversalEdge(useB.ID, helper.ID, "calls"),
		},
	)

	hits := ImpactOfFile(graph, fileA, 2)
	if got := nodeIDsFromHits(hits); strings.Join(got, "\x00") != "src/b.ts\x00src/b.ts#useB" {
		t.Errorf("ImpactOfFile(a.ts, 2) = %v, want file and symbol dependents", got)
	}
}

func traversalNode(id, name, kind, path string) NodeV1 {
	return NodeV1{ID: id, Name: name, Kind: Kind(kind), Path: path, Span: "L1-L1"}
}

func traversalEdge(source, target string, relation Relation) EdgeV1 {
	return EdgeV1{Source: source, Target: target, Relation: relation, Confidence: Confidence("extracted")}
}

func traversalGraph(nodes []NodeV1, edges []EdgeV1) GraphV1 {
	return GraphV1{
		Meta:  GraphMeta{Version: 1, NodeCount: len(nodes), EdgeCount: len(edges), Languages: []string{"ts"}},
		Nodes: nodes,
		Edges: edges,
	}
}

func nodeIDs(nodes []NodeV1) []string {
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
	}
	return ids
}

func nodeIDsFromHits(hits []EdgeHit) []string {
	ids := make([]string, len(hits))
	for i, hit := range hits {
		ids[i] = hit.ID
	}
	return ids
}
