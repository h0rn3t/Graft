package graph

import "slices"

// PathStep is one edge of a dependency path, walked from From to To.
type PathStep struct {
	From       string
	To         string
	Relation   Relation
	Confidence Confidence
	// Dispatch marks a step from an interface method to an implementation:
	// an implements edge walked against its direction.
	Dispatch bool
}

// ShortestPath returns a shortest chain of calls, references and imports from
// any source to any target, at most maxDepth steps long, or nil when there is
// none. A step into an interface method may continue to the methods that
// implement it, the way a call through the interface dispatches. A source that
// is itself a target yields nil: there is nothing to walk.
func ShortestPath(graph GraphV1, sources, targets []NodeV1, maxDepth int) []PathStep {
	adjacency := make(map[string][]PathStep)
	for _, edge := range graph.Edges {
		switch edge.Relation {
		case "calls", "references", "imports":
			adjacency[edge.Source] = append(adjacency[edge.Source], PathStep{From: edge.Source, To: edge.Target, Relation: edge.Relation, Confidence: edge.Confidence})
		case "implements":
			adjacency[edge.Target] = append(adjacency[edge.Target], PathStep{From: edge.Target, To: edge.Source, Relation: edge.Relation, Confidence: edge.Confidence, Dispatch: true})
		}
	}
	wanted := make(map[string]bool, len(targets))
	for _, target := range targets {
		wanted[target.ID] = true
	}
	reachedBy := make(map[string]PathStep)
	visited := make(map[string]bool, len(sources))
	var frontier []string
	for _, source := range sources {
		if !visited[source.ID] {
			visited[source.ID] = true
			frontier = append(frontier, source.ID)
		}
	}
	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, current := range frontier {
			for _, step := range adjacency[current] {
				if visited[step.To] {
					continue
				}
				visited[step.To] = true
				reachedBy[step.To] = step
				if wanted[step.To] {
					return walkBack(reachedBy, step.To)
				}
				next = append(next, step.To)
			}
		}
		frontier = next
	}
	return nil
}

// walkBack follows reachedBy from end to the source the search started at.
func walkBack(reachedBy map[string]PathStep, end string) []PathStep {
	var steps []PathStep
	for at := end; ; {
		step, ok := reachedBy[at]
		if !ok {
			break
		}
		steps = append(steps, step)
		at = step.From
	}
	slices.Reverse(steps)
	return steps
}
