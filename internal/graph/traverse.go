package graph

import (
	"path/filepath"
	"strings"
)

// Direction selects incoming dependency edges or outgoing dependency edges.
type Direction string

const (
	// DirectionIn walks edges toward their sources, finding callers or dependents.
	DirectionIn Direction = "in"
	// DirectionOut walks edges toward their targets, finding callees or dependencies.
	DirectionOut Direction = "out"
)

// ResolveSymbolOptions narrows symbol resolution to a repository-relative path prefix.
type ResolveSymbolOptions struct {
	In string
}

// EdgeHit describes one dependency edge reached during a graph walk.
type EdgeHit struct {
	Node       *NodeV1
	ID         string
	Relation   Relation
	Confidence Confidence
	Depth      int
	// Line is the edge's line in its source's file: the caller's for an
	// incoming edge, the walked symbol's for an outgoing one; zero if unknown.
	Line int
}

// ResolveSymbol finds all graph nodes matching query and applies an optional path prefix.
func ResolveSymbol(graph GraphV1, query string, opts ResolveSymbolOptions) ([]NodeV1, error) {
	lowerQuery := strings.ToLower(query)
	matches := symbolMatches(graph.Nodes, lowerQuery)
	if len(matches) == 0 && strings.Contains(query, ".") {
		lastSegment := strings.ToLower(query[strings.LastIndex(query, ".")+1:])
		if lastSegment != "" {
			for _, node := range graph.Nodes {
				if node.Kind != Kind("file") && strings.ToLower(node.Name) == lastSegment {
					matches = append(matches, node)
				}
			}
		}
	}
	if len(matches) == 0 && strings.Contains(query, ".") && !strings.Contains(query, "#") {
		for _, node := range graph.Nodes {
			if node.Kind != Kind("file") {
				continue
			}
			path := strings.ToLower(node.Path)
			if strings.ToLower(node.Name) == lowerQuery || path == lowerQuery || strings.HasSuffix(path, "/"+lowerQuery) {
				matches = append(matches, node)
			}
		}
	}
	if opts.In == "" {
		return matches, nil
	}
	prefix := normalizePathPrefix(opts.In)
	if err := assertPrefixIndexed(graph, prefix); err != nil {
		return nil, err
	}
	filtered := make([]NodeV1, 0, len(matches))
	for _, node := range matches {
		if pathUnderPrefix(node.Path, prefix) {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}

// CallersOf returns depth-one incoming dependency edges for symbol.
func CallersOf(graph GraphV1, symbol NodeV1) []EdgeHit {
	byID := nodeIndex(graph)
	hits := make([]EdgeHit, 0)
	for _, edge := range graph.Edges {
		if !isWalkRelation(edge.Relation) || edge.Target != symbol.ID {
			continue
		}
		hits = append(hits, EdgeHit{
			Node:       byID[edge.Source],
			ID:         edge.Source,
			Relation:   edge.Relation,
			Confidence: edge.Confidence,
			Depth:      1,
			Line:       edge.Line,
		})
	}
	return hits
}

// CalleesOf returns depth-one outgoing dependency edges for symbol.
func CalleesOf(graph GraphV1, symbol NodeV1) []EdgeHit {
	byID := nodeIndex(graph)
	hits := make([]EdgeHit, 0)
	for _, edge := range graph.Edges {
		if !isWalkRelation(edge.Relation) || edge.Source != symbol.ID {
			continue
		}
		hits = append(hits, EdgeHit{
			Node:       byID[edge.Target],
			ID:         edge.Target,
			Relation:   edge.Relation,
			Confidence: edge.Confidence,
			Depth:      1,
			Line:       edge.Line,
		})
	}
	return hits
}

// ImpactOf walks incoming dependency edges from symbol. With no maxDepth it uses depth two.
func ImpactOf(graph GraphV1, symbol NodeV1, maxDepth ...int) []EdgeHit {
	depth := 2
	if len(maxDepth) > 0 {
		depth = maxDepth[0]
	}
	return ImpactOfMany(graph, []NodeV1{symbol}, depth, DirectionIn)
}

// ImpactOfMany walks dependency edges from multiple seeds, deduplicating nodes at first reach.
func ImpactOfMany(graph GraphV1, seeds []NodeV1, maxDepth int, directions ...Direction) []EdgeHit {
	direction := DirectionIn
	if len(directions) > 0 {
		direction = directions[0]
	}
	byID := nodeIndex(graph)
	adjacency := make(map[string][]walkEntry)
	for _, edge := range graph.Edges {
		if !isWalkRelation(edge.Relation) {
			continue
		}
		key, other := edge.Source, edge.Target
		if direction == DirectionIn {
			key, other = edge.Target, edge.Source
		}
		adjacency[key] = append(adjacency[key], walkEntry{other: other, relation: edge.Relation, confidence: edge.Confidence, line: edge.Line})
	}

	visited := make(map[string]bool, len(seeds))
	frontier := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		if visited[seed.ID] {
			continue
		}
		visited[seed.ID] = true
		frontier = append(frontier, seed.ID)
	}
	hits := make([]EdgeHit, 0)
	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		next := make([]string, 0)
		for _, current := range frontier {
			for _, entry := range adjacency[current] {
				if visited[entry.other] {
					continue
				}
				visited[entry.other] = true
				hits = append(hits, EdgeHit{
					Node:       byID[entry.other],
					ID:         entry.other,
					Relation:   entry.relation,
					Confidence: entry.confidence,
					Depth:      depth,
					Line:       entry.line,
				})
				next = append(next, entry.other)
			}
		}
		frontier = next
	}
	return hits
}

// ImpactOfFile walks incoming dependency edges from a file and its defined symbols.
func ImpactOfFile(graph GraphV1, fileNode NodeV1, maxDepth int, directions ...Direction) []EdgeHit {
	seeds := make([]NodeV1, 1, 1+len(graph.Nodes))
	seeds[0] = fileNode
	for _, node := range graph.Nodes {
		if node.Kind != Kind("file") && node.Path == fileNode.Path {
			seeds = append(seeds, node)
		}
	}
	return ImpactOfMany(graph, seeds, maxDepth, directions...)
}

// EdgeWalk selects a direct scan for depth one and a breadth-first walk for deeper queries.
func EdgeWalk(graph GraphV1, node NodeV1, direction Direction, depth int) []EdgeHit {
	if depth <= 1 {
		if direction == DirectionIn {
			return CallersOf(graph, node)
		}
		return CalleesOf(graph, node)
	}
	if node.Kind == Kind("file") {
		return ImpactOfFile(graph, node, depth, direction)
	}
	return ImpactOfMany(graph, []NodeV1{node}, depth, direction)
}

type walkEntry struct {
	other      string
	relation   Relation
	confidence Confidence
	line       int
}

func nodeIndex(graph GraphV1) map[string]*NodeV1 {
	byID := make(map[string]*NodeV1, len(graph.Nodes))
	for i := range graph.Nodes {
		byID[graph.Nodes[i].ID] = &graph.Nodes[i]
	}
	return byID
}

func symbolMatches(nodes []NodeV1, lowerQuery string) []NodeV1 {
	suffixHash := "#" + lowerQuery
	suffixDot := "." + lowerQuery
	matches := make([]NodeV1, 0)
	for _, node := range nodes {
		if node.Kind == Kind("file") {
			continue
		}
		if strings.ToLower(node.Name) == lowerQuery {
			matches = append(matches, node)
			continue
		}
		lowerID := strings.ToLower(node.ID)
		candidateID := lowerID
		if hashIndex := strings.IndexByte(lowerID, '#'); hashIndex >= 0 {
			candidateID = lowerID[:hashIndex+1] + stripOrdinals(lowerID[hashIndex+1:])
		}
		if strings.HasSuffix(candidateID, suffixHash) || strings.HasSuffix(candidateID, suffixDot) {
			matches = append(matches, node)
		}
	}
	return matches
}

func stripOrdinals(idTail string) string {
	segments := strings.Split(idTail, ".")
	for i, segment := range segments {
		ordinal := strings.LastIndexByte(segment, '~')
		if ordinal < 0 || ordinal == len(segment)-1 {
			continue
		}
		if allDigits(segment[ordinal+1:]) {
			segments[i] = segment[:ordinal]
		}
	}
	return strings.Join(segments, ".")
}

func allDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func isWalkRelation(relation Relation) bool {
	switch relation {
	case "calls", "references", "writes", "imports", "implements", "extends":
		return true
	default:
		return false
	}
}

func normalizePathPrefix(prefix string) string {
	normalized := filepath.ToSlash(prefix)
	for strings.HasPrefix(normalized, "./") {
		normalized = normalized[2:]
	}
	return strings.TrimRight(normalized, "/")
}

func pathUnderPrefix(path, prefix string) bool {
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

// CallerBound says why the callers the graph lists for a symbol may be fewer
// than the code has. The zero value marks a list nothing is known to miss.
type CallerBound struct {
	// Untyped counts the uses of the symbol's name on a receiver whose type
	// the resolver did not know, `x.Close()`: any of them may be the symbol's.
	Untyped int
	// Ambiguous counts the uses of its name that matched several definitions.
	Ambiguous int
	// Dispatch lists what a call may reach the method through instead: the
	// interface methods it implements, or the types its class extends or
	// implements.
	Dispatch []string
	// Values counts the symbols that name it as a value; a call made later
	// through such a value is no edge.
	Values int
}

// Exact reports whether nothing bounds the callers.
func (bound CallerBound) Exact() bool {
	return bound.Untyped == 0 && bound.Ambiguous == 0 && len(bound.Dispatch) == 0 && bound.Values == 0
}

// CallerBoundOf bounds the callers of a function, method or field; ok is
// false for any other symbol, whose uses the graph does not track the same
// way in every language, for a graph built before graft recorded the uses it
// could not resolve, and for a list it cannot call exact: outside Go a
// function passed as a value leaves no edge.
func CallerBoundOf(graph GraphV1, symbol NodeV1) (bound CallerBound, ok bool) {
	callable := symbol.Kind == "function" || symbol.Kind == "method"
	if (!callable && symbol.Kind != "field") || graph.Meta.UnresolvedNames == nil {
		return bound, false
	}
	goFile := strings.HasSuffix(symbol.Path, ".go")
	counts := graph.Meta.UnresolvedNames[symbol.Name]
	// A Go function is named bare or through its package, never through a
	// receiver; elsewhere `module.f()` may be one.
	if symbol.Kind != "function" || !goFile {
		bound.Untyped = counts.Untyped
	}
	if callable {
		bound.Ambiguous = counts.Ambiguous
	}
	owner := ""
	for _, edge := range graph.Edges {
		switch {
		case edge.Target == symbol.ID && edge.Relation == "contains":
			owner = edge.Source
		case edge.Target == symbol.ID && edge.Relation == "references" && callable:
			bound.Values++
		case edge.Source == symbol.ID && edge.Relation == "implements" && symbol.Kind == "method":
			bound.Dispatch = append(bound.Dispatch, edge.Target)
		}
	}
	if symbol.Kind == "method" && !goFile && owner != "" {
		for _, edge := range graph.Edges {
			if edge.Source == owner && (edge.Relation == "extends" || edge.Relation == "implements") {
				bound.Dispatch = append(bound.Dispatch, edge.Target)
			}
		}
	}
	return bound, goFile || !bound.Exact()
}
