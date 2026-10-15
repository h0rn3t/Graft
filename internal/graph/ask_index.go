package graph

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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
	file, bags := countAskIndex(graph, readAskTermBags(outDir))
	data, err := file.encode()
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(askIndexPath(outDir), data, 0o644); err != nil {
		return fmt.Errorf("write ask index: %w", err)
	}
	// A cache that cannot be written only costs tokenizing every text next time.
	cache := struct {
		Version int               `json:"version"`
		Bags    map[string]askBag `json:"bags"`
	}{Version: AskIndexVersion, Bags: bags}
	if data, err := jsonv2.Marshal(cache, jsonv2.Deterministic(true)); err == nil {
		_ = fsutil.WriteFileAtomic(askTermsPath(outDir), data, 0o644)
	}
	return nil
}

// askIndexFile is ask-index.json before it is encoded: the term bags of each
// ranked node in id order, and how many of them hold each term.
type askIndexFile struct {
	docs    []askIndexDocument
	df      askBag
	average float64
}

type askIndexDocument struct {
	id               string
	name, path, body askBag
}

// countAskIndex counts the terms of graph's ranked nodes, taking the bag of a
// text counted before from bags, and returns the bags of this graph's texts.
func countAskIndex(graph GraphV1, bags map[string]askBag) (askIndexFile, map[string]askBag) {
	next := make(map[string]askBag)
	counts := func(text string) askBag {
		key := sourcefiles.Hash(text)
		bag, ok := bags[key]
		if !ok || len(bag.Terms) != len(bag.Counts) {
			bag = askOrderedCounts(text)
		}
		next[key] = bag
		return bag
	}
	nodes := sortNodesByID(graph.Nodes)
	file := askIndexFile{docs: make([]askIndexDocument, 0, len(nodes)), df: askBag{at: make(map[string]int)}}
	length := 0
	for _, node := range nodes {
		if !askRanked(node) {
			continue
		}
		doc := askIndexDocument{id: node.ID, name: counts(node.Name), path: counts(node.Path), body: counts(askNodeBody(node))}
		file.docs = append(file.docs, doc)
		seen := make(map[string]struct{}, len(doc.name.Terms)+len(doc.path.Terms)+len(doc.body.Terms))
		for _, bag := range []askBag{doc.name, doc.path, doc.body} {
			for _, term := range bag.Terms {
				if _, dup := seen[term]; dup {
					continue
				}
				seen[term] = struct{}{}
				file.df.add(term, 1)
			}
		}
		for _, count := range doc.body.Counts {
			length += count
		}
	}
	if len(file.docs) > 0 {
		file.average = float64(length) / float64(len(file.docs))
	}
	return file, next
}

// encode writes the file as jsonjs.Marshal writes its JSON shape, which spent
// most of a refresh boxing each term pair.
func (file askIndexFile) encode() ([]byte, error) {
	// Like jsonjs.Marshal, AppendQuote writes invalid UTF-8 as U+FFFD; the
	// error only reports that.
	quote := func(data []byte, text string) []byte {
		data, _ = jsontext.AppendQuote(data, text)
		return data
	}
	// Every bag keeps first-occurrence order, as the TypeScript Maps do.
	pairs := func(data []byte, bag askBag) []byte {
		data = append(data, '[')
		for i, term := range bag.Terms {
			if i > 0 {
				data = append(data, ',')
			}
			data = quote(append(data, '['), term)
			data = append(strconv.AppendInt(append(data, ','), int64(bag.Counts[i]), 10), ']')
		}
		return append(data, ']')
	}
	average, err := jsonjs.Marshal(file.average, "")
	if err != nil {
		return nil, fmt.Errorf("encode ask index: %w", err)
	}
	data := fmt.Appendf(nil, `{"version":%d,"avgBodyLen":%s,"df":`, AskIndexVersion, average)
	data = pairs(data, file.df)
	data = append(strconv.AppendInt(append(data, `,"docCount":`...), int64(len(file.docs)), 10), `,"docs":[`...)
	for i, doc := range file.docs {
		if i > 0 {
			data = append(data, ',')
		}
		data = quote(append(data, `{"id":`...), doc.id)
		data = pairs(append(data, `,"name":`...), doc.name)
		data = pairs(append(data, `,"path":`...), doc.path)
		data = append(pairs(append(data, `,"body":`...), doc.body), '}')
	}
	return append(data, "]}\n"...), nil
}

// index returns the file as a query reads it back: nil when two documents
// share an id, since such an index is dropped.
func (file askIndexFile) index() *AskIndex {
	counts := func(bag askBag) map[string]int {
		out := make(map[string]int, len(bag.Terms))
		for i, term := range bag.Terms {
			out[term] = bag.Counts[i]
		}
		return out
	}
	docs := make(map[string]AskIndexDoc, len(file.docs))
	for _, doc := range file.docs {
		docs[doc.id] = AskIndexDoc{Name: counts(doc.name), Path: counts(doc.path), Body: counts(doc.body)}
	}
	if len(docs) != len(file.docs) {
		return nil
	}
	return &AskIndex{Version: AskIndexVersion, AvgBodyLen: file.average, DF: counts(file.df), DocCount: len(file.docs), Docs: docs}
}

// readAskTermBags returns the term cache's bags, keyed by the SHA-256 of the
// text each counts, or an empty map when there is no usable cache.
func readAskTermBags(outDir string) map[string]askBag {
	var cache struct {
		Version int               `json:"version"`
		Bags    map[string]askBag `json:"bags"`
	}
	data, err := os.ReadFile(askTermsPath(outDir))
	if err != nil || jsonv2.Unmarshal(data, &cache) != nil || cache.Version != AskIndexVersion || cache.Bags == nil {
		return make(map[string]askBag)
	}
	return cache.Bags
}

func askTermsPath(outDir string) string {
	return filepath.Join(outDir, ".cache", "ask-terms.json")
}

func askIndexPath(outDir string) string {
	return filepath.Join(outDir, ".cache", "ask-index.json")
}
