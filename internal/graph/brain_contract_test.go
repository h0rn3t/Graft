package graph

import (
	"fmt"
	"reflect"
	"testing"
)

func TestApplyBrainRules(t *testing.T) {
	current := GraphV1{
		Nodes: []NodeV1{
			{ID: "src/root.ts#root", Path: "src/root.ts", Span: "L1-L3", BodyHash: "current"},
			{ID: "src/other.ts#other", Path: "src/other.ts", Span: "L2-L4", BodyHash: "changed"},
			{ID: "src/legacy.ts#legacy", Path: "src/legacy.ts", Span: "L5-L6", BodyHash: "anything"},
		},
	}
	rules := []BrainRule{
		{RuleID: "r1", Symbol: "src/root.ts#root", Fingerprint: "current", Rule: "Keep the root boundary", SourceURL: "https://example.test/r1"},
		{RuleID: "r2", Symbol: "src/other.ts#other", Fingerprint: "before", Rule: "Validate the other path"},
		{RuleID: "r3", Symbol: "src/legacy.ts#legacy", Rule: "Preserve the legacy behavior"},
		{RuleID: "r4", Symbol: "src/root.ts#root", Fingerprint: "current", Rule: "Keep the root boundary", SourceURL: "https://example.test/duplicate"},
		{RuleID: "r5", Symbol: "src/missing.ts#missing", Fingerprint: "missing", Rule: "Never attach this"},
	}

	tests := []struct {
		name     string
		pointers []string
		rules    []BrainRule
		graph    *GraphV1
		want     []AppliedRule
	}{
		{
			name:     "matches current pointers in cache order and deduplicates rule text",
			pointers: []string{"src/root.ts:L1-L3", "src/other.ts:L2-L4", "src/legacy.ts:L5-L6"},
			rules:    rules,
			graph:    &current,
			want: []AppliedRule{
				{Rule: "Keep the root boundary", Pointer: "src/root.ts:L1-L3", SourceURL: "https://example.test/r1"},
				{Rule: "Validate the other path", Pointer: "src/other.ts:L2-L4", Stale: true},
				{Rule: "Preserve the legacy behavior", Pointer: "src/legacy.ts:L5-L6"},
			},
		},
		{
			name:     "unmatched pointer returns no rules",
			pointers: []string{"src/unrelated.ts:L1-L2"},
			rules:    rules,
			graph:    &current,
			want:     nil,
		},
		{
			name:     "missing graph returns no rules",
			pointers: []string{"src/root.ts:L1-L3"},
			rules:    rules,
			graph:    nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyBrainRules(tt.pointers, tt.rules, tt.graph)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ApplyBrainRules(%q) = %#v, want %#v", tt.name, got, tt.want)
			}
		})
	}
}

func TestApplyBrainRulesCapsResults(t *testing.T) {
	var wiring GraphV1
	var rules []BrainRule
	var pointers []string
	for index := range 7 {
		path := fmt.Sprintf("src/file%d.ts", index)
		span := fmt.Sprintf("L%d-L%d", index+1, index+1)
		id := fmt.Sprintf("%s#function%d", path, index)
		wiring.Nodes = append(wiring.Nodes, NodeV1{ID: id, Path: path, Span: span})
		rules = append(rules, BrainRule{Symbol: id, Rule: fmt.Sprintf("Rule %d", index)})
		pointers = append(pointers, path+":"+span)
	}

	got := ApplyBrainRules(pointers, rules, &wiring)
	if len(got) != 6 {
		t.Errorf("ApplyBrainRules(%q) returned %d rules, want 6", "seven matching rules", len(got))
	}
}
