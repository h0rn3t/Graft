package graph

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"

	"github.com/h0rn3t/Graft/internal/fsutil"
	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// askBag is a term-count bag that remembers first-occurrence order:
// Counts[i] counts Terms[i].
type askBag struct {
	Terms  []string       `json:"terms"`
	Counts []int          `json:"counts"`
	at     map[string]int // index into Terms while the bag is filled
}

func (bag *askBag) add(term string, count int) {
	i, ok := bag.at[term]
	if !ok {
		i = len(bag.Terms)
		bag.at[term] = i
		bag.Terms = append(bag.Terms, term)
		bag.Counts = append(bag.Counts, 0)
	}
	bag.Counts[i] += count
}

// askOrderedCounts tokenizes text like askTermCounts, keeping term order.
func askOrderedCounts(text string) askBag {
	bag := askBag{at: make(map[string]int)}
	for _, term := range askTerms(text) {
		bag.add(term, 1)
	}
	return bag
}

// AskIndexVersion is the ask index format. Version 3 stores terms folded by
// AskFold, with acronyms split from the words they start; an index of any
// other version is treated as absent. The term cache WriteAskIndex keeps
// beside the index is versioned by it too, so bump it whenever a text yields
// different terms.
const AskIndexVersion = 3

// WriteAskIndex stores the lexical body tokens omitted from the persisted graph.
// Texts unchanged since the last write take their bags from the term cache in
// .cache/ask-terms.json instead of being tokenized again.
func WriteAskIndex(outDir string, graph GraphV1) error {
	type document struct {
		ID   string  `json:"id"`
		Name [][]any `json:"name"`
		Path [][]any `json:"path"`
		Body [][]any `json:"body"`
	}
	// The cache keys each bag by the SHA-256 of the text it counts.
	type termCache struct {
		Version int               `json:"version"`
		Bags    map[string]askBag `json:"bags"`
	}
	cachePath := filepath.Join(outDir, ".cache", "ask-terms.json")
	var prior termCache
	if data, err := os.ReadFile(cachePath); err == nil && (jsonv2.Unmarshal(data, &prior) != nil || prior.Version != AskIndexVersion) {
		prior = termCache{}
	}
	next := termCache{Version: AskIndexVersion, Bags: make(map[string]askBag)}
	counts := func(text string) askBag {
		key := sourcefiles.Hash(text)
		bag, ok := prior.Bags[key]
		if !ok || len(bag.Terms) != len(bag.Counts) {
			bag = askOrderedCounts(text)
		}
		next.Bags[key] = bag
		return bag
	}
	// Every bag keeps first-occurrence order, as the TypeScript Maps do.
	pairs := func(bag askBag) [][]any {
		out := make([][]any, 0, len(bag.Terms))
		for i, term := range bag.Terms {
			out = append(out, []any{term, bag.Counts[i]})
		}
		return out
	}
	nodes := sortNodesByID(graph.Nodes)
	docs := make([]document, 0, len(nodes))
	df := askBag{at: make(map[string]int)}
	length := 0
	for _, node := range nodes {
		if !askRanked(node) {
			continue
		}
		name := counts(node.Name)
		path := counts(node.Path)
		body := counts(askNodeBody(node))
		docs = append(docs, document{ID: node.ID, Name: pairs(name), Path: pairs(path), Body: pairs(body)})
		seen := make(map[string]struct{}, len(name.Terms)+len(path.Terms)+len(body.Terms))
		for _, bag := range []askBag{name, path, body} {
			for _, term := range bag.Terms {
				if _, dup := seen[term]; dup {
					continue
				}
				seen[term] = struct{}{}
				df.add(term, 1)
			}
		}
		for _, count := range body.Counts {
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
	}{Version: AskIndexVersion, AvgBodyLen: average, DF: pairs(df), DocCount: len(docs), Docs: docs}, "")
	if err != nil {
		return fmt.Errorf("encode ask index: %w", err)
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(outDir, ".cache", "ask-index.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write ask index: %w", err)
	}
	// A cache that cannot be written only costs tokenizing every text next time.
	if data, err := jsonv2.Marshal(next, jsonv2.Deterministic(true)); err == nil {
		_ = fsutil.WriteFileAtomic(cachePath, data, 0o644)
	}
	return nil
}
