// Package graphquality validates and summarizes a graft wiring graph.
package graphquality

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	graphmodel "github.com/h0rn3t/Graft/internal/graph"
)

// Node is the subset of a graph node used by the quality report.
type Node = graphmodel.NodeV1

// Edge is the subset of a graph edge used by the quality report.
type Edge = graphmodel.EdgeV1

// Graph is the graph JSON shape consumed by the quality report.
type Graph = graphmodel.GraphV1

// Resolution contains call-edge resolution metrics.
type Resolution struct {
	Calls                     int  `json:"calls"`
	ResolvedToNode            int  `json:"resolvedToNode"`
	ResolvedPercent           *int `json:"resolvedPct"`
	UnresolvedExternalImports int  `json:"unresolvedExternalImports"`
}

// Connectivity contains graph connectivity metrics for symbol nodes.
type Connectivity struct {
	OrphanSymbolNodes int  `json:"orphanSymbolNodes"`
	OrphanPercent     *int `json:"orphanPct"`
}

// Invariants contains the structural quality checks for a graph.
type Invariants struct {
	OK            bool     `json:"ok"`
	DanglingEdges int      `json:"danglingEdges"`
	SelfLoopCalls int      `json:"selfLoopCalls"`
	Violations    int      `json:"violations"`
	Sample        []string `json:"sample"`
}

// Report is the JSON and human-readable graph-quality report.
type Report struct {
	Graph           string         `json:"graph"`
	Nodes           int            `json:"nodes"`
	SymbolNodes     int            `json:"symbolNodes"`
	Edges           int            `json:"edges"`
	ByKind          map[string]int `json:"byKind"`
	ByOrigin        map[string]int `json:"byOrigin"`
	ByRelation      map[string]int `json:"byRelation"`
	ByConfidence    map[string]int `json:"byConfidence"`
	Resolution      Resolution     `json:"resolution"`
	Connectivity    Connectivity   `json:"connectivity"`
	Invariants      Invariants     `json:"invariants"`
	kindOrder       []string
	originOrder     []string
	relationOrder   []string
	confidenceOrder []string
}

// Load reads a graph JSON file.
func Load(path string) (Graph, error) {
	loaded, err := graphmodel.Read(path)
	if err != nil {
		return Graph{}, err
	}
	return *loaded, nil
}

// ResolvePath finds the wiring graph from a repository directory or JSON path.
func ResolvePath(arg string) (string, bool) {
	if strings.HasSuffix(arg, ".json") {
		_, err := os.Stat(arg)
		return arg, err == nil
	}
	for _, candidate := range []string{
		filepath.Join(arg, "graft", ".graph", "wiring.json"),
		filepath.Join(arg, "graft", "wiring.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// Analyze computes the graph-quality report without reading from disk.
func Analyze(graph Graph, path string) Report {
	byKind, kindOrder := countNodes(graph.Nodes, func(node Node) string { return string(node.Kind) })
	byOrigin, originOrder := countNodes(graph.Nodes, func(node Node) string {
		if node.Origin == "" {
			return "?"
		}
		return string(node.Origin)
	})
	byRelation, relationOrder := countEdges(graph.Edges, func(edge Edge) string { return string(edge.Relation) })
	byConfidence, confidenceOrder := countEdges(graph.Edges, func(edge Edge) string { return string(edge.Confidence) })

	ids := make(map[string]bool, len(graph.Nodes))
	for _, node := range graph.Nodes {
		ids[node.ID] = true
	}
	invariants := graphmodel.CheckInvariants(graph)
	problems := make([]string, len(invariants.Problems))
	for i, problem := range invariants.Problems {
		problems[i] = problem
		if id, ok := strings.CutPrefix(problem, "duplicate node id: "); ok {
			problems[i] = "dup id: " + id
			continue
		}
		if prefix, _, ok := strings.Cut(problem, "': "); ok &&
			(strings.HasPrefix(problem, "bad relation ") || strings.HasPrefix(problem, "bad confidence ")) {
			problems[i] = prefix + "'"
		}
	}

	calls := 0
	resolvedCalls := 0
	unresolvedExternalImports := 0
	withEdge := make(map[string]bool)
	for _, edge := range graph.Edges {
		if edge.Relation == "calls" {
			calls++
			if ids[edge.Target] {
				resolvedCalls++
			}
		}
		if edge.Relation != "contains" {
			withEdge[edge.Source] = true
			if ids[edge.Target] {
				withEdge[edge.Target] = true
			}
		}
		if !ids[edge.Target] && isExternalTarget(edge.Relation) {
			unresolvedExternalImports++
		}
	}

	symbolNodes := 0
	orphanSymbolNodes := 0
	for _, node := range graph.Nodes {
		if node.Kind == "file" {
			continue
		}
		symbolNodes++
		if !withEdge[node.ID] {
			orphanSymbolNodes++
		}
	}

	return Report{
		Graph:        path,
		Nodes:        len(graph.Nodes),
		SymbolNodes:  symbolNodes,
		Edges:        len(graph.Edges),
		ByKind:       byKind,
		ByOrigin:     byOrigin,
		ByRelation:   byRelation,
		ByConfidence: byConfidence,
		Resolution: Resolution{
			Calls:                     calls,
			ResolvedToNode:            resolvedCalls,
			ResolvedPercent:           percentage(resolvedCalls, calls),
			UnresolvedExternalImports: unresolvedExternalImports,
		},
		Connectivity: Connectivity{
			OrphanSymbolNodes: orphanSymbolNodes,
			OrphanPercent:     percentage(orphanSymbolNodes, symbolNodes),
		},
		Invariants: Invariants{
			OK:            len(problems) == 0,
			DanglingEdges: invariants.DanglingEdges,
			SelfLoopCalls: invariants.SelfLoopCalls,
			Violations:    len(problems),
			Sample:        problems[:min(len(problems), 15)],
		},
		kindOrder:       kindOrder,
		originOrder:     originOrder,
		relationOrder:   relationOrder,
		confidenceOrder: confidenceOrder,
	}
}

// JSON returns the report in the machine-readable format used by --json. The
// count objects keep their keys in first-seen order, as the human report does.
func (report Report) JSON() ([]byte, error) {
	return json.MarshalIndent(struct {
		Graph        string        `json:"graph"`
		Nodes        int           `json:"nodes"`
		SymbolNodes  int           `json:"symbolNodes"`
		Edges        int           `json:"edges"`
		ByKind       orderedCounts `json:"byKind"`
		ByOrigin     orderedCounts `json:"byOrigin"`
		ByRelation   orderedCounts `json:"byRelation"`
		ByConfidence orderedCounts `json:"byConfidence"`
		Resolution   Resolution    `json:"resolution"`
		Connectivity Connectivity  `json:"connectivity"`
		Invariants   Invariants    `json:"invariants"`
	}{
		Graph:        report.Graph,
		Nodes:        report.Nodes,
		SymbolNodes:  report.SymbolNodes,
		Edges:        report.Edges,
		ByKind:       orderedCounts{report.ByKind, report.kindOrder},
		ByOrigin:     orderedCounts{report.ByOrigin, report.originOrder},
		ByRelation:   orderedCounts{report.ByRelation, report.relationOrder},
		ByConfidence: orderedCounts{report.ByConfidence, report.confidenceOrder},
		Resolution:   report.Resolution,
		Connectivity: report.Connectivity,
		Invariants:   report.Invariants,
	}, "", "  ")
}

// orderedCounts encodes counts as a JSON object with its keys in order. A
// report not built by Analyze has no order, and its keys come out sorted.
type orderedCounts struct {
	counts map[string]int
	order  []string
}

// MarshalJSON writes the counts in order.
func (c orderedCounts) MarshalJSON() ([]byte, error) {
	order := c.order
	if len(order) != len(c.counts) {
		order = slices.Sorted(maps.Keys(c.counts))
	}
	data := []byte{'{'}
	for i, key := range order {
		if i > 0 {
			data = append(data, ',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		data = append(data, name...)
		data = append(data, ':')
		data = strconv.AppendInt(data, int64(c.counts[key]), 10)
	}
	return append(data, '}'), nil
}

// Human returns the report in the human-readable format used by the command.
func (report Report) Human() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "graph-quality — %s\n", report.Graph)
	fmt.Fprintf(&builder, "  nodes %d (%d symbols) · edges %d\n", report.Nodes, report.SymbolNodes, report.Edges)
	fmt.Fprintf(&builder, "  kinds:      %s\n", formatCounts(report.ByKind, report.kindOrder, true))
	fmt.Fprintf(&builder, "  origin:     %s\n", formatCounts(report.ByOrigin, report.originOrder, false))
	fmt.Fprintf(&builder, "  relations:  %s\n", formatCounts(report.ByRelation, report.relationOrder, false))
	fmt.Fprintf(&builder, "  confidence: %s\n", formatCounts(report.ByConfidence, report.confidenceOrder, false))
	fmt.Fprintf(&builder, "  calls resolved: %d/%d (%s)\n", report.Resolution.ResolvedToNode, report.Resolution.Calls, formatPercentage(report.Resolution.ResolvedPercent))
	fmt.Fprintf(&builder, "  orphan symbols: %d/%d (%s)\n", report.Connectivity.OrphanSymbolNodes, report.SymbolNodes, formatPercentage(report.Connectivity.OrphanPercent))
	if report.Invariants.OK {
		builder.WriteString("  INVARIANTS: OK ✓\n")
	} else {
		fmt.Fprintf(&builder, "  INVARIANTS: FAIL ✗ (%d violations, %d dangling)\n", report.Invariants.Violations, report.Invariants.DanglingEdges)
		for _, problem := range report.Invariants.Sample {
			fmt.Fprintf(&builder, "     - %s\n", problem)
		}
	}
	if report.Invariants.SelfLoopCalls > 0 {
		fmt.Fprintf(&builder, "  note: %d self-loop calls\n", report.Invariants.SelfLoopCalls)
	}
	return builder.String()
}

func isExternalTarget(relation graphmodel.Relation) bool {
	switch relation {
	case "imports", "extends", "implements", "references":
		return true
	default:
		return false
	}
}

func countNodes(nodes []Node, key func(Node) string) (map[string]int, []string) {
	counts := make(map[string]int)
	order := make([]string, 0)
	for _, node := range nodes {
		value := key(node)
		if _, ok := counts[value]; !ok {
			order = append(order, value)
		}
		counts[value]++
	}
	return counts, order
}

func countEdges(edges []Edge, key func(Edge) string) (map[string]int, []string) {
	counts := make(map[string]int)
	order := make([]string, 0)
	for _, edge := range edges {
		value := key(edge)
		if _, ok := counts[value]; !ok {
			order = append(order, value)
		}
		counts[value]++
	}
	return counts, order
}

func percentage(part, total int) *int {
	if total == 0 {
		return nil
	}
	value := (part*100 + total/2) / total
	return &value
}

func formatPercentage(value *int) string {
	if value == nil {
		return "null%"
	}
	return strconv.Itoa(*value) + "%"
}

func formatCounts(counts map[string]int, order []string, rankByCount bool) string {
	keys := slices.Clone(order)
	if rankByCount {
		slices.SortStableFunc(keys, func(left, right string) int {
			return cmp.Compare(counts[right], counts[left])
		})
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+strconv.Itoa(counts[key]))
	}
	return strings.Join(parts, " ")
}
