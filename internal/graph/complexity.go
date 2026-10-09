package graph

import (
	"cmp"
	"math"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// branchKinds lists, per grammar, the syntax nodes that always add a path
// through a function. Conditional cases (a logical operator, a switch label
// that is not the default) are decided in isBranch.
var branchKinds = map[string][]string{
	"go":         {"if_statement", "for_statement", "expression_case", "type_case", "communication_case"},
	"python":     {"if_statement", "elif_clause", "for_statement", "while_statement", "except_clause", "conditional_expression", "boolean_operator", "case_clause", "for_in_clause", "if_clause"},
	"typescript": {"if_statement", "for_statement", "for_in_statement", "while_statement", "do_statement", "switch_case", "catch_clause", "ternary_expression"},
	"java":       {"if_statement", "for_statement", "enhanced_for_statement", "while_statement", "do_statement", "catch_clause", "ternary_expression"},
	"rust":       {"if_expression", "while_expression", "for_expression"},
	"c":          {"if_statement", "for_statement", "while_statement", "do_statement", "conditional_expression"},
	"cpp":        {"if_statement", "for_statement", "for_range_loop", "while_statement", "do_statement", "conditional_expression", "catch_clause"},
}

// cyclomatic is the McCabe complexity of a function definition: one plus each
// decision point in it. A nested definition that is a graph node of its own is
// skipped; closures count toward the function that holds them.
func cyclomatic(definition *sitter.Node, grammar string, source []byte, nested func(*sitter.Node) bool) int {
	complexity := 1
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		for _, child := range namedChildren(node) {
			if nested(child) {
				continue
			}
			if isBranch(child, grammar, source) {
				complexity++
			}
			visit(child)
		}
	}
	visit(definition)
	return complexity
}

func isBranch(node *sitter.Node, grammar string, source []byte) bool {
	kind := node.Kind()
	if slices.Contains(branchKinds[grammar], kind) {
		return true
	}
	switch kind {
	case "binary_expression":
		for index := range node.ChildCount() {
			switch node.Child(index).Kind() {
			case "&&", "||", "??":
				return true
			}
		}
	case "switch_label": // java: `case X:` and `case X ->`, never `default`
		return strings.HasPrefix(nodeText(node, source), "case")
	case "case_statement": // c, cpp: `default:` has no value
		return node.ChildByFieldName("value") != nil
	case "match_arm": // rust: the `_` arm is the default
		return nodeText(node.ChildByFieldName("pattern"), source) != "_"
	}
	return false
}

// MostComplex returns the production functions and methods under the in
// prefix that carry a complexity, most complex first, then by path and span.
func MostComplex(graph GraphV1, in string) ([]NodeV1, error) {
	prefix := normalizePathPrefix(in)
	if in != "" {
		if err := assertPrefixIndexed(graph, prefix); err != nil {
			return nil, err
		}
	}
	var functions []NodeV1
	for _, node := range graph.Nodes {
		if node.Complexity != nil && !IsCopyPath(node.Path) && (in == "" || pathUnderPrefix(node.Path, prefix)) {
			functions = append(functions, node)
		}
	}
	slices.SortStableFunc(functions, func(a, b NodeV1) int {
		aLine, _, _ := parseLineSpan(a.Span)
		bLine, _, _ := parseLineSpan(b.Span)
		return cmp.Or(cmp.Compare(*b.Complexity, *a.Complexity), cmp.Compare(a.Path, b.Path), cmp.Compare(aLine, bLine))
	})
	return functions, nil
}

// Hotspot is a file that both changes often and holds complex code.
type Hotspot struct {
	Path    string
	Commits int
	// Complexity sums the complexity of the file's functions and methods.
	Complexity int
	// Score is 100 × the file's share of the busiest file's commits × its
	// share of the most complex file's complexity, rounded.
	Score int
	// Top is the file's most complex function or method.
	Top NodeV1
}

// Hotspots ranks the production files under the in prefix that have both
// commits and complexity, highest score first. churn maps repository-relative
// paths to the number of commits that touched them.
func Hotspots(graph GraphV1, churn map[string]int, in string) ([]Hotspot, error) {
	functions, err := MostComplex(graph, in)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]*Hotspot)
	var order []string
	for _, function := range functions {
		commits := churn[function.Path]
		if commits == 0 {
			continue
		}
		spot, seen := byPath[function.Path]
		if !seen {
			spot = &Hotspot{Path: function.Path, Commits: commits, Top: function} // functions come most complex first
			byPath[function.Path] = spot
			order = append(order, function.Path)
		}
		spot.Complexity += *function.Complexity
	}
	maxCommits, maxComplexity := 0, 0
	for _, spot := range byPath {
		maxCommits = max(maxCommits, spot.Commits)
		maxComplexity = max(maxComplexity, spot.Complexity)
	}
	spots := make([]Hotspot, 0, len(order))
	for _, file := range order {
		spot := *byPath[file]
		spot.Score = int(math.Round(100 * float64(spot.Commits) / float64(maxCommits) * float64(spot.Complexity) / float64(maxComplexity)))
		spots = append(spots, spot)
	}
	slices.SortStableFunc(spots, func(a, b Hotspot) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(b.Commits, a.Commits), cmp.Compare(a.Path, b.Path))
	})
	return spots, nil
}

// complexityGrammar maps a depth-tier grammar onto its branchKinds key.
func complexityGrammar(lang language) string {
	if lang == langTSX {
		return "typescript"
	}
	return string(lang)
}
