package graph

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var spanPattern = regexp.MustCompile(`^L(\d+)-L(\d+)$`)

// InvariantResult contains structural violations and informational recursion
// edges found in a graph.
type InvariantResult struct {
	Problems      []string
	SelfLoopCalls int
	DanglingEdges int
}

// CheckInvariants checks the structural invariants of a graph.
func CheckInvariants(graph GraphV1) InvariantResult {
	problems := make([]string, 0)
	ids := make(map[string]struct{}, len(graph.Nodes))
	seen := make(map[string]struct{}, len(graph.Nodes))

	for _, node := range graph.Nodes {
		if _, ok := seen[node.ID]; ok {
			problems = append(problems, fmt.Sprintf("duplicate node id: %s", node.ID))
		}
		seen[node.ID] = struct{}{}
		ids[node.ID] = struct{}{}
		if strings.TrimSpace(node.Name) == "" {
			problems = append(problems, fmt.Sprintf("empty name: %s", node.ID))
		}
		if !validKind(node.Kind) {
			problems = append(problems, fmt.Sprintf("bad kind '%s': %s", node.Kind, node.ID))
		}
		match := spanPattern.FindStringSubmatch(node.Span)
		if len(match) == 0 {
			problems = append(problems, fmt.Sprintf("bad span '%s': %s", node.Span, node.ID))
		} else {
			start, startErr := strconv.ParseUint(match[1], 10, 64)
			end, endErr := strconv.ParseUint(match[2], 10, 64)
			if startErr != nil || endErr != nil || start > end {
				problems = append(problems, fmt.Sprintf("inverted span %s: %s", node.Span, node.ID))
			}
		}
	}

	danglingEdges := 0
	selfLoopCalls := 0
	for _, edge := range graph.Edges {
		if !validRelation(edge.Relation) {
			problems = append(problems, fmt.Sprintf("bad relation '%s': %s", edge.Relation, edge.Source))
		}
		if !validConfidence(edge.Confidence) {
			problems = append(problems, fmt.Sprintf("bad confidence '%s': %s → %s", edge.Confidence, edge.Source, edge.Target))
		}
		if _, ok := ids[edge.Source]; !ok {
			problems = append(problems, fmt.Sprintf("dangling source: %s", edge.Source))
			danglingEdges++
		}
		if _, ok := ids[edge.Target]; !ok && !externalTarget(edge.Relation) {
			problems = append(problems, fmt.Sprintf("dangling %s target: %s → %s", edge.Relation, edge.Source, edge.Target))
			danglingEdges++
		}
		if edge.Relation == Relation("calls") && edge.Source == edge.Target {
			selfLoopCalls++
		}
	}

	return InvariantResult{
		Problems:      problems,
		SelfLoopCalls: selfLoopCalls,
		DanglingEdges: danglingEdges,
	}
}

func validKind(kind Kind) bool {
	switch kind {
	case "file", "class", "function", "method", "interface", "type", "enum", "struct", "module", "constant", "variable":
		return true
	default:
		return false
	}
}

func validRelation(relation Relation) bool {
	switch relation {
	case "contains", "calls", "imports", "references", "implements", "extends":
		return true
	default:
		return false
	}
}

func validConfidence(confidence Confidence) bool {
	switch confidence {
	case "lsp_resolved", "lsp_dispatch", "extracted", "inferred":
		return true
	default:
		return false
	}
}

func externalTarget(relation Relation) bool {
	switch relation {
	case "imports", "extends", "implements", "references":
		return true
	default:
		return false
	}
}
