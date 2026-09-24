package graph

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/h0rn3t/Graft/internal/fsutil"
)

const (
	// GraphDir is the hidden graph directory under a context directory.
	GraphDir = ".graph"
	// GraphFile is the persisted wiring graph filename.
	GraphFile = "wiring.json"
)

// WiringPath returns the persisted wiring graph path for a context directory.
func WiringPath(outDir string) string {
	return filepath.Join(outDir, GraphDir, GraphFile)
}

// Read loads a wiring graph from path.
func Read(path string) (*GraphV1, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var graph *GraphV1
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, err
	}
	if graph == nil {
		return nil, errors.New("null graph")
	}
	return graph, nil
}

// Write sorts and atomically persists graph under outDir. The serialized copy
// omits body_text, which is stored separately for query-time indexing.
func Write(graph GraphV1, outDir string) (string, error) {
	sorted := graph
	sorted.Nodes = sortNodesByID(graph.Nodes)
	for i := range sorted.Nodes {
		sorted.Nodes[i].BodyText = nil
	}
	// Edge endpoints repeat, so each distinct text gets one collation key.
	texts := make([]string, 0, len(graph.Nodes)+2)
	textIndex := make(map[string]int, len(graph.Nodes)+2)
	indexOf := func(text string) int {
		index, ok := textIndex[text]
		if !ok {
			index = len(texts)
			textIndex[text] = index
			texts = append(texts, text)
		}
		return index
	}
	type edgeTexts struct{ source, relation, target int }
	edgeRefs := make([]edgeTexts, len(graph.Edges))
	for i, edge := range graph.Edges {
		edgeRefs[i] = edgeTexts{indexOf(edge.Source), indexOf(string(edge.Relation)), indexOf(edge.Target)}
	}
	keys := localeSortKeys(texts)
	order := localeOrder(len(graph.Edges), func(a, b int) int {
		left, right := edgeRefs[a], edgeRefs[b]
		return cmp.Or(
			bytes.Compare(keys[left.source], keys[right.source]),
			bytes.Compare(keys[left.relation], keys[right.relation]),
			bytes.Compare(keys[left.target], keys[right.target]),
		)
	})
	sorted.Edges = make([]EdgeV1, len(graph.Edges))
	for i, index := range order {
		sorted.Edges[i] = graph.Edges[index]
	}

	path := WiringPath(outDir)
	// json/v2 escapes neither HTML characters nor U+2028/U+2029, matching the
	// JSON.stringify output TypeScript builds write.
	data, err := jsonv2.Marshal(sorted, jsontext.WithIndent("  "))
	if err != nil {
		return "", fmt.Errorf("encode graph: %w", err)
	}
	data = append(data, '\n')
	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write graph: %w", err)
	}
	return path, nil
}

// sortNodesByID returns a copy of nodes in localeCompare order of their ids;
// nodes with equal ids keep their input order.
func sortNodesByID(nodes []NodeV1) []NodeV1 {
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
	}
	keys := localeSortKeys(ids)
	order := localeOrder(len(nodes), func(a, b int) int { return bytes.Compare(keys[a], keys[b]) })
	sorted := make([]NodeV1, len(nodes))
	for i, index := range order {
		sorted[i] = nodes[index]
	}
	return sorted
}
