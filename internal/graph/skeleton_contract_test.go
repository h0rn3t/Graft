package graph

import (
	"reflect"
	"strings"
	"testing"
)

func TestSkeletonContract(t *testing.T) {
	firstSignature := "first(a: number): number"
	secondSignature := "second(b: string): string"
	firstSummary := "first summary\nmore detail"
	firstSummaryLine := "first summary"
	fileChars := 120
	first := traversalNode("src/api.ts#first", "first", "function", "src/api.ts")
	first.Span = "L1-L2"
	first.Signature = &firstSignature
	first.Summary = &firstSummary
	second := traversalNode("src/api.ts#second", "second", "function", "src/api.ts")
	second.Span = "L4-L5"
	second.Signature = &secondSignature
	file := traversalNode("src/api.ts", "api.ts", "file", "src/api.ts")
	file.Chars = &fileChars
	fixture := traversalGraph([]NodeV1{second, first, file}, nil)

	tests := []struct {
		name string
		file string
		want SkeletonResult
	}{
		{
			name: "exact path",
			file: "src/api.ts",
			want: SkeletonResult{
				File: "src/api.ts",
				Entries: []SkeletonEntry{
					{Name: "first", Kind: Kind("function"), Span: "L1-L2", Signature: &firstSignature, Summary: &firstSummaryLine},
					{Name: "second", Kind: Kind("function"), Span: "L4-L5", Signature: &secondSignature},
				},
				Saved: &SkeletonSavings{Files: 1, BaselineChars: fileChars},
			},
		},
		{
			name: "unique basename",
			file: "api.ts",
			want: SkeletonResult{
				File: "src/api.ts",
				Entries: []SkeletonEntry{
					{Name: "first", Kind: Kind("function"), Span: "L1-L2", Signature: &firstSignature, Summary: &firstSummaryLine},
					{Name: "second", Kind: Kind("function"), Span: "L4-L5", Signature: &secondSignature},
				},
				Saved: &SkeletonSavings{Files: 1, BaselineChars: fileChars},
			},
		},
		{
			name: "no definitions",
			file: "missing.ts",
			want: SkeletonResult{
				File:    "missing.ts",
				Entries: make([]SkeletonEntry, 0),
				Note:    "no definitions indexed for this file",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Skeleton(fixture, tt.file)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Skeleton(%q) = %#v, want %#v", tt.file, got, tt.want)
			}
		})
	}
}

func TestSkeletonAmbiguousBasenameContract(t *testing.T) {
	first := traversalNode("src/a/api.ts#first", "first", "function", "src/a/api.ts")
	first.Span = "L1-L2"
	second := traversalNode("src/b/api.ts#second", "second", "function", "src/b/api.ts")
	second.Span = "L1-L2"
	fixture := traversalGraph([]NodeV1{second, first}, nil)

	got := Skeleton(fixture, "api.ts")
	if len(got.Entries) != 0 || got.Note != "ambiguous — matches: src/a/api.ts, src/b/api.ts" {
		t.Errorf("Skeleton(%q) = %#v, want ambiguous basename note", "api.ts", got)
	}
	if !strings.Contains(got.Note, "src/a/api.ts") || !strings.Contains(got.Note, "src/b/api.ts") {
		t.Errorf("Skeleton(%q) note = %q, want both matching paths", "api.ts", got.Note)
	}
}
