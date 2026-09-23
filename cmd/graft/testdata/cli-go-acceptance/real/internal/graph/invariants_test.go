package graph

import (
	"strings"
	"testing"
)

func TestCheckInvariantsValidGraph(t *testing.T) {
	graph := GraphV1{
		Nodes: []NodeV1{
			{ID: "a.ts", Name: "a.ts", Kind: "file", Span: "L1-L3"},
			{ID: "a.ts#main", Name: "main", Kind: "function", Span: "L1-L3"},
		},
		Edges: []EdgeV1{{
			Source:     "a.ts",
			Target:     "a.ts#main",
			Relation:   "contains",
			Confidence: "extracted",
		}},
	}

	result := CheckInvariants(graph)
	if len(result.Problems) != 0 {
		t.Errorf("CheckInvariants(valid graph).Problems = %v, want []", result.Problems)
	}
	if result.SelfLoopCalls != 0 {
		t.Errorf("CheckInvariants(valid graph).SelfLoopCalls = %d, want 0", result.SelfLoopCalls)
	}
}

func TestCheckInvariantsReportsMalformedGraph(t *testing.T) {
	graph := GraphV1{
		Nodes: []NodeV1{
			{ID: "a.ts#Foo", Name: "Foo", Kind: "class", Span: "L1-L3"},
			{ID: "a.ts#Foo", Name: "  ", Kind: "widget", Span: "L9-L2"},
		},
		Edges: []EdgeV1{
			{Source: "a.ts#Foo", Target: "a.ts#Ghost", Relation: "calls", Confidence: "guessed"},
			{Source: "a.ts#Missing", Target: "a.ts#Foo", Relation: "references", Confidence: "extracted"},
			{Source: "a.ts#Foo", Target: "a.ts#Foo", Relation: "calls", Confidence: "extracted"},
		},
	}

	result := CheckInvariants(graph)
	for _, want := range []string{
		"duplicate node id",
		"empty name",
		"bad kind",
		"inverted span",
		"dangling calls target",
		"dangling source",
		"bad confidence",
	} {
		found := false
		for _, problem := range result.Problems {
			if strings.Contains(problem, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CheckInvariants(malformed graph) did not report %q; Problems = %v", want, result.Problems)
		}
	}
	if result.SelfLoopCalls != 1 {
		t.Errorf("CheckInvariants(malformed graph).SelfLoopCalls = %d, want 1", result.SelfLoopCalls)
	}
}

func TestCheckInvariantsAllowsExternalTargets(t *testing.T) {
	graph := GraphV1{
		Nodes: []NodeV1{{ID: "a.ts#Foo", Name: "Foo", Kind: "class", Span: "L1-L3"}},
		Edges: []EdgeV1{
			{Source: "a.ts#Foo", Target: "com.external.Base", Relation: "extends", Confidence: "inferred"},
			{Source: "a.ts#Foo", Target: "Override", Relation: "references", Confidence: "inferred"},
		},
	}

	result := CheckInvariants(graph)
	if len(result.Problems) != 0 {
		t.Errorf("CheckInvariants(external targets).Problems = %v, want []", result.Problems)
	}
}
