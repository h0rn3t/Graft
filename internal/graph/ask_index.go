package graph

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

// askBag is a term-count bag that remembers first-occurrence order.
type askBag struct {
	terms  []string
	counts map[string]int
}

func (bag *askBag) add(term string, count int) {
	if _, ok := bag.counts[term]; !ok {
		bag.terms = append(bag.terms, term)
	}
	bag.counts[term] += count
}

// askOrderedCounts tokenizes text like askTermCounts, keeping term order.
func askOrderedCounts(text string) askBag {
	bag := askBag{counts: make(map[string]int)}
	for _, term := range askTerms(text) {
		bag.add(term, 1)
	}
	return bag
}

// WriteAskIndex stores the lexical body tokens omitted from the persisted graph.
func WriteAskIndex(outDir string, graph GraphV1) error {
	type document struct {
		ID   string  `json:"id"`
		Name [][]any `json:"name"`
		Path [][]any `json:"path"`
		Body [][]any `json:"body"`
	}
	// Every bag keeps first-occurrence order, as the TypeScript Maps do.
	pairs := func(bag askBag) [][]any {
		out := make([][]any, 0, len(bag.terms))
		for _, term := range bag.terms {
			out = append(out, []any{term, bag.counts[term]})
		}
		return out
	}
	nodes := slices.Clone(graph.Nodes)
	compare := localeCompare()
	slices.SortStableFunc(nodes, func(a, b NodeV1) int {
		return compare(a.ID, b.ID)
	})
	docs := make([]document, 0, len(nodes))
	df := askBag{counts: make(map[string]int)}
	length := 0
	for _, node := range nodes {
		name := askOrderedCounts(node.Name)
		path := askOrderedCounts(node.Path)
		body := askOrderedCounts(askNodeBody(node))
		docs = append(docs, document{ID: node.ID, Name: pairs(name), Path: pairs(path), Body: pairs(body)})
		seen := make(map[string]struct{}, len(name.terms)+len(path.terms)+len(body.terms))
		for _, bag := range []askBag{name, path, body} {
			for _, term := range bag.terms {
				if _, dup := seen[term]; dup {
					continue
				}
				seen[term] = struct{}{}
				df.add(term, 1)
			}
		}
		for _, count := range body.counts {
			length += count
		}
	}
	average := 0.0
	if len(docs) > 0 {
		average = float64(length) / float64(len(docs))
	}
	data, err := jsonjs.Marshal(struct {
		Version    int        `json:"version"`
		AvgBodyLen float64    `json:"avgBodyLen"`
		DF         [][]any    `json:"df"`
		DocCount   int        `json:"docCount"`
		Docs       []document `json:"docs"`
	}{Version: 1, AvgBodyLen: average, DF: pairs(df), DocCount: len(docs), Docs: docs}, "")
	if err != nil {
		return fmt.Errorf("encode ask index: %w", err)
	}
	if err := writeAtomicSidecar(filepath.Join(outDir, ".cache", "ask-index.json"), append(data, '\n')); err != nil {
		return fmt.Errorf("write ask index: %w", err)
	}
	return nil
}
