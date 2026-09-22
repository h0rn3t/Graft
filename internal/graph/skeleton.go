package graph

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// SkeletonEntry is the signatures-only representation of one graph definition.
type SkeletonEntry struct {
	Name      string  `json:"name"`
	Kind      Kind    `json:"kind"`
	Span      string  `json:"span"`
	Signature *string `json:"signature"`
	Summary   *string `json:"summary,omitempty"`
}

// SkeletonSavings records the known whole-file baseline for a skeleton.
type SkeletonSavings struct {
	Files         int `json:"files"`
	BaselineChars int `json:"baselineChars"`
}

// SkeletonResult is the signatures-only view of one repository file.
type SkeletonResult struct {
	File    string           `json:"file"`
	Entries []SkeletonEntry  `json:"entries"`
	Note    string           `json:"note,omitempty"`
	Saved   *SkeletonSavings `json:"saved,omitempty"`
}

// Skeleton selects definitions by repository path or unique basename and orders them by span.
func Skeleton(wiring GraphV1, file string) SkeletonResult {
	definitions := definitionsAtPath(wiring.Nodes, file)
	selectedPath := file
	if len(definitions) == 0 {
		paths := matchingPaths(wiring.Nodes, file)
		if len(paths) > 1 {
			return SkeletonResult{
				File:    file,
				Entries: make([]SkeletonEntry, 0),
				Note:    "ambiguous — matches: " + strings.Join(slices.Sorted(maps.Keys(paths)), ", "),
			}
		}
		if len(paths) == 1 {
			selectedPath = slices.Sorted(maps.Keys(paths))[0]
			definitions = definitionsAtPath(wiring.Nodes, selectedPath)
		}
	}
	if len(definitions) == 0 {
		return SkeletonResult{File: file, Entries: make([]SkeletonEntry, 0), Note: "no definitions indexed for this file"}
	}

	slices.SortStableFunc(definitions, func(left, right NodeV1) int {
		return cmp.Compare(spanStart(left.Span), spanStart(right.Span))
	})
	entries := make([]SkeletonEntry, 0, len(definitions))
	for _, node := range definitions {
		entries = append(entries, SkeletonEntry{
			Name:      node.Name,
			Kind:      node.Kind,
			Span:      node.Span,
			Signature: node.Signature,
			Summary:   firstSummaryLine(node.Summary),
		})
	}
	return SkeletonResult{
		File:    selectedPath,
		Entries: entries,
		Saved:   skeletonSavings(wiring.Nodes, selectedPath),
	}
}

func definitionsAtPath(nodes []NodeV1, path string) []NodeV1 {
	definitions := make([]NodeV1, 0)
	for _, node := range nodes {
		if node.Kind != Kind("file") && node.Path == path {
			definitions = append(definitions, node)
		}
	}
	return definitions
}

func matchingPaths(nodes []NodeV1, file string) map[string]struct{} {
	paths := make(map[string]struct{})
	for _, node := range nodes {
		if node.Path == file || strings.HasSuffix(node.Path, "/"+file) {
			paths[node.Path] = struct{}{}
		}
	}
	return paths
}

func spanStart(span string) int {
	start, _, ok := strings.Cut(span, "-")
	if !ok {
		return 0
	}
	start, ok = strings.CutPrefix(start, "L")
	if !ok {
		return 0
	}
	line, err := strconv.Atoi(start)
	if err != nil {
		return 0
	}
	return line
}

func firstSummaryLine(summary *string) *string {
	if summary == nil {
		return nil
	}
	line, _, _ := strings.Cut(*summary, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	return &line
}

func skeletonSavings(nodes []NodeV1, path string) *SkeletonSavings {
	for _, node := range nodes {
		if node.Kind == Kind("file") && node.Path == path && node.Chars != nil {
			return &SkeletonSavings{Files: 1, BaselineChars: *node.Chars}
		}
	}
	return nil
}
