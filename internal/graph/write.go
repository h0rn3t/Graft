package graph

import (
	"cmp"
	"encoding/json"
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
	var graph GraphV1
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, err
	}
	return &graph, nil
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
	slices.SortStableFunc(sorted.Nodes, func(a, b NodeV1) int {
		return cmp.Compare(a.ID, b.ID)
	})
	sorted.Edges = make([]EdgeV1, len(graph.Edges))
	copy(sorted.Edges, graph.Edges)
	slices.SortStableFunc(sorted.Edges, func(a, b EdgeV1) int {
		return cmp.Or(
			cmp.Compare(a.Source, b.Source),
			cmp.Compare(a.Relation, b.Relation),
			cmp.Compare(a.Target, b.Target),
		)
	})

	path := WiringPath(outDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create graph directory: %w", err)
	}
	data, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode graph: %w", err)
	}
	data = append(data, '\n')
	temporary := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		_ = os.Remove(temporary)
		return "", fmt.Errorf("write temporary graph: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return "", fmt.Errorf("replace graph: %w", err)
	}
	return path, nil
}
