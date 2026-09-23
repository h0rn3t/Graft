package graph

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
)

// WriteAskIndex stores the lexical body tokens omitted from the persisted graph.
func WriteAskIndex(outDir string, graph GraphV1) error {
	type document struct {
		ID   string  `json:"id"`
		Name [][]any `json:"name"`
		Path [][]any `json:"path"`
		Body [][]any `json:"body"`
	}
	pairs := func(counts map[string]int) [][]any {
		terms := slices.Sorted(maps.Keys(counts))
		out := make([][]any, 0, len(terms))
		for _, term := range terms {
			out = append(out, []any{term, counts[term]})
		}
		return out
	}
	nodes := slices.Clone(graph.Nodes)
	slices.SortFunc(nodes, func(a, b NodeV1) int {
		return cmp.Compare(a.ID, b.ID)
	})
	docs := make([]document, 0, len(nodes))
	df := make(map[string]int)
	length := 0
	for _, node := range nodes {
		name := askCounts(node.Name)
		path := askCounts(node.Path)
		body := askCounts(askNodeBody(node))
		docs = append(docs, document{ID: node.ID, Name: pairs(name), Path: pairs(path), Body: pairs(body)})
		seen := make(map[string]struct{}, len(name)+len(path)+len(body))
		for term := range name {
			seen[term] = struct{}{}
		}
		for term := range path {
			seen[term] = struct{}{}
		}
		for term, count := range body {
			seen[term] = struct{}{}
			length += count
		}
		for term := range seen {
			df[term]++
		}
	}
	average := 0.0
	if len(docs) > 0 {
		average = float64(length) / float64(len(docs))
	}
	data, err := json.Marshal(struct {
		Version    int        `json:"version"`
		AvgBodyLen float64    `json:"avgBodyLen"`
		DF         [][]any    `json:"df"`
		DocCount   int        `json:"docCount"`
		Docs       []document `json:"docs"`
	}{Version: 1, AvgBodyLen: average, DF: pairs(df), DocCount: len(docs), Docs: docs})
	if err != nil {
		return fmt.Errorf("encode ask index: %w", err)
	}
	if err := writeAtomicSidecar(filepath.Join(outDir, ".cache", "ask-index.json"), append(data, '\n')); err != nil {
		return fmt.Errorf("write ask index: %w", err)
	}
	return nil
}
