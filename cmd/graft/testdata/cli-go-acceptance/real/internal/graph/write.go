package graph

import (
	"cmp"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	sorted.Nodes = make([]NodeV1, len(graph.Nodes))
	copy(sorted.Nodes, graph.Nodes)
	for i := range sorted.Nodes {
		sorted.Nodes[i].BodyText = nil
	}
	compare := localeCompare()
	slices.SortStableFunc(sorted.Nodes, func(a, b NodeV1) int {
		return compare(a.ID, b.ID)
	})
	sorted.Edges = make([]EdgeV1, len(graph.Edges))
	copy(sorted.Edges, graph.Edges)
	slices.SortStableFunc(sorted.Edges, func(a, b EdgeV1) int {
		return cmp.Or(
			compare(a.Source, b.Source),
			compare(string(a.Relation), string(b.Relation)),
			compare(a.Target, b.Target),
		)
	})

	path := WiringPath(outDir)
	// json/v2 escapes neither HTML characters nor U+2028/U+2029, matching the
	// JSON.stringify output TypeScript builds write.
	data, err := jsonv2.Marshal(sorted, jsontext.WithIndent("  "))
	if err != nil {
		return "", fmt.Errorf("encode graph: %w", err)
	}
	data = append(data, '\n')
	if err := writeAtomicSidecar(path, data); err != nil {
		return "", fmt.Errorf("write graph: %w", err)
	}
	return path, nil
}
