package graph

import (
	"cmp"
	"maps"
	"path"
	"slices"
)

// CycleHop is one dependency inside a cycle, with an edge that makes it.
type CycleHop struct {
	From     string
	To       string
	Evidence EdgeV1
}

// Cycle is a set of files or directories that all depend on each other, and
// one shortest loop through them as evidence.
type Cycle struct {
	Members []string
	Loop    []CycleHop
}

// DependencyCycles finds the strongly connected groups of production code
// under the in prefix. At level "file" a dependency is an import between two
// files; at level "dir" it is any call, reference, import, extends or
// implements edge between nodes of two directories. Larger cycles come first.
func DependencyCycles(graph GraphV1, level, in string) ([]Cycle, error) {
	prefix := normalizePathPrefix(in)
	if in != "" {
		if err := assertPrefixIndexed(graph, prefix); err != nil {
			return nil, err
		}
	}
	byID := nodeIndex(graph)
	keyOf := func(id string) (string, bool) {
		node, ok := byID[id]
		if !ok || IsCopy(*node) || (in != "" && !pathUnderPrefix(node.Path, prefix)) {
			return "", false
		}
		if level == "file" {
			return node.Path, node.Kind == "file"
		}
		return path.Dir(node.Path), true
	}
	evidence := make(map[string]map[string]EdgeV1)
	for _, edge := range graph.Edges {
		if level == "file" && edge.Relation != "imports" || level != "file" && !isWalkRelation(edge.Relation) {
			continue
		}
		from, fromOK := keyOf(edge.Source)
		to, toOK := keyOf(edge.Target)
		if !fromOK || !toOK || from == to {
			continue
		}
		if evidence[from] == nil {
			evidence[from] = make(map[string]EdgeV1)
		}
		if _, seen := evidence[from][to]; !seen {
			evidence[from][to] = edge
		}
	}
	successors := make(map[string][]string, len(evidence))
	for from, targets := range evidence {
		successors[from] = slices.Sorted(maps.Keys(targets))
	}

	var cycles []Cycle
	for _, component := range stronglyConnected(successors) {
		if len(component) < 2 {
			continue
		}
		slices.Sort(component)
		cycle := Cycle{Members: component}
		for _, hop := range shortestLoop(successors, component) {
			cycle.Loop = append(cycle.Loop, CycleHop{From: hop[0], To: hop[1], Evidence: evidence[hop[0]][hop[1]]})
		}
		cycles = append(cycles, cycle)
	}
	slices.SortFunc(cycles, func(a, b Cycle) int {
		return cmp.Or(cmp.Compare(len(b.Members), len(a.Members)), cmp.Compare(a.Members[0], b.Members[0]))
	})
	return cycles, nil
}

// stronglyConnected is Tarjan's algorithm, iterative so a long dependency
// chain cannot exhaust the stack. Vertices are visited in sorted order, so
// the result is deterministic.
func stronglyConnected(successors map[string][]string) [][]string {
	vertices := slices.Sorted(maps.Keys(successors))
	index := make(map[string]int)
	low := make(map[string]int)
	onStack := make(map[string]bool)
	var stack []string
	var components [][]string
	type frame struct {
		vertex string
		next   int
	}
	for _, root := range vertices {
		if _, seen := index[root]; seen {
			continue
		}
		calls := []frame{{vertex: root}}
		index[root], low[root] = len(index), len(index)
		stack = append(stack, root)
		onStack[root] = true
		for len(calls) > 0 {
			top := &calls[len(calls)-1]
			if top.next < len(successors[top.vertex]) {
				successor := successors[top.vertex][top.next]
				top.next++
				if _, seen := index[successor]; !seen {
					index[successor], low[successor] = len(index), len(index)
					stack = append(stack, successor)
					onStack[successor] = true
					calls = append(calls, frame{vertex: successor})
				} else if onStack[successor] {
					low[top.vertex] = min(low[top.vertex], index[successor])
				}
				continue
			}
			vertex := top.vertex
			calls = calls[:len(calls)-1]
			if len(calls) > 0 {
				parent := calls[len(calls)-1].vertex
				low[parent] = min(low[parent], low[vertex])
			}
			if low[vertex] != index[vertex] {
				continue
			}
			var component []string
			for {
				member := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[member] = false
				component = append(component, member)
				if member == vertex {
					break
				}
			}
			components = append(components, component)
		}
	}
	return components
}

// shortestLoop is a shortest closed walk from the component's first member
// back to itself, staying inside the component, as [from, to] hops.
func shortestLoop(successors map[string][]string, component []string) [][2]string {
	start := component[0]
	inside := make(map[string]bool, len(component))
	for _, member := range component {
		inside[member] = true
	}
	parent := make(map[string]string)
	frontier := []string{start}
	for len(frontier) > 0 {
		var next []string
		for _, current := range frontier {
			for _, successor := range successors[current] {
				if !inside[successor] {
					continue
				}
				if successor == start {
					hops := [][2]string{{current, start}}
					for at := current; at != start; at = parent[at] {
						hops = append(hops, [2]string{parent[at], at})
					}
					slices.Reverse(hops)
					return hops
				}
				if _, seen := parent[successor]; !seen {
					parent[successor] = current
					next = append(next, successor)
				}
			}
		}
		frontier = next
	}
	return nil
}
