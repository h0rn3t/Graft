package main

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// askWholeFileLines is the longest file shown whole: an agent shown most of
// such a file in pieces reads all of it next.
const askWholeFileLines = 220

// askWholeFileShare is the share of a file's lines its hits must span before
// the whole file replaces their definitions; codegraph buys a whole file at
// the same fraction of its size.
const askWholeFileShare = 0.6

// askInterchangeable is the number of implementations past which an unnamed
// implementation shows only its signature: any one of them explains the rest.
const askInterchangeable = 3

// askHitShare caps a definition completed for any hit but the top one at this
// fraction of the budget: nine in ten definitions agents read next fit in a
// fifth of the default budget, and a long weak hit would crowd out the rest.
const askHitShare = 5

// fillAskBudget spends what the budget leaves on source the agent would fetch
// in another round. An unnamed implementation of a widely implemented
// interface off the call flow shrinks to its signature. Then the hits on the
// call flow come first, the rest after, each group best hit first: a small
// file whose hits span most of it is shown once, whole, and any other partial
// hit becomes its complete definition, each only while the answer still fits
// and, below the top hit and off the flow, only within askHitShare.
func fillAskBudget(root string, wiring graph.GraphV1, result *graph.AskResult, nodes []graph.NodeV1, chain []string, budget int, opts callersOptions) {
	overhead := opts.budgetOverhead + opts.queryNote
	// Measured as writeAskResult will render it, content references included.
	fits := func() bool {
		probe := *result
		if opts.references {
			probe.Hits = slices.Clone(result.Hits)
			applyAskSeen(&probe, opts.seen)
		}
		return savings.Tokens(savings.Length(overhead+renderAskBudget(probe, opts.jsonOutput, opts.mcp))) <= budget
	}
	hits := result.Hits
	if len(hits) == 0 || !fits() {
		return
	}
	partial := make(map[int]askDefinition)
	members := make(map[string][]int)
	for index, hit := range hits {
		if hit.Kind == "file" || hit.Code == "" {
			continue
		}
		path, from, to, ok := parseAskPointer(hit.Pointer)
		if !ok {
			continue
		}
		members[path] = append(members[path], index)
		body, hash, exists := sliceAskSpan(filepath.Join(root, filepath.FromSlash(path)), from, to, path, true)
		if exists && body != hit.Code {
			partial[index] = askDefinition{path: path, from: from, body: body, hash: hash}
		}
	}

	onChain := func(index int) bool { return nodes[index].ID != "" && slices.Contains(chain, nodes[index].ID) }
	showAskSignatures(wiring, hits, nodes, partial, func(index int) bool {
		return onChain(index) || askNamesHit(hits[index], opts.query)
	})

	order := make([]int, 0, len(hits))
	for index := range hits {
		if onChain(index) {
			order = append(order, index)
		}
	}
	for index := range hits {
		if !onChain(index) {
			order = append(order, index)
		}
	}
	shown := make(map[int]bool)
	for _, index := range order {
		def, ok := partial[index]
		if !ok || shown[index] {
			continue
		}
		if group := members[def.path]; showWholeAskFile(root, def.path, hits, group, fits) {
			for _, member := range group {
				shown[member] = true
			}
			continue
		}
		if index > 0 && !onChain(index) && savings.Tokens(savings.Length(def.body)) > budget/askHitShare {
			continue
		}
		excerpt := hits[index].Code
		hits[index].Code, hits[index].SourceHash = def.body, def.hash
		if !fits() {
			hits[index].Code = excerpt
		}
	}
}

// askDefinition is the complete definition of a hit that shows only part of it.
type askDefinition struct {
	path       string
	from       int
	body, hash string
}

// showAskSignatures shrinks each partial hit below the top one that
// implements an interface with askInterchangeable or more implementations to
// its signature, unless keep spares it, and drops it from partial.
func showAskSignatures(wiring graph.GraphV1, hits []graph.AskHit, nodes []graph.NodeV1, partial map[int]askDefinition, keep func(int) bool) {
	implementations := make(map[string]int)
	implemented := make(map[string][]string)
	for _, edge := range wiring.Edges {
		if edge.Relation == "implements" {
			implementations[edge.Target]++
			implemented[edge.Source] = append(implemented[edge.Source], edge.Target)
		}
	}
	interfaces := make(map[string]string)
	for _, node := range wiring.Nodes {
		if implementations[node.ID] >= askInterchangeable {
			interfaces[node.ID] = askNodeName(node)
		}
	}
	// The top hit keeps its source: it is the answer more often than not.
	for index := 1; index < len(hits); index++ {
		def, ok := partial[index]
		node := nodes[index]
		if !ok || node.ID == "" || keep(index) {
			continue
		}
		ids := []string{node.ID}
		if node.Owner != nil {
			ids = append(ids, node.Path+"#"+*node.Owner)
		}
		for _, id := range ids {
			at := slices.IndexFunc(implemented[id], func(target string) bool { return interfaces[target] != "" })
			if at < 0 {
				continue
			}
			iface := implemented[id][at]
			signature, _, _ := strings.Cut(def.body, "\n")
			hits[index].Code = fmt.Sprintf("L%d: %s\n… (signature only: one of %d implementations of %s)", def.from, signature, implementations[iface], interfaces[iface])
			delete(partial, index)
			break
		}
	}
}

// showWholeAskFile replaces the source of a file's hits with the whole file,
// shown once on the best of them, when the file is small, their spans cover
// askWholeFileShare of it, and the answer still fits; it reports whether it did.
func showWholeAskFile(root, path string, hits []graph.AskHit, group []int, fits func() bool) bool {
	data, readable, err := sourcefiles.Read(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil || !readable {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	if len(lines) > askWholeFileLines {
		return false
	}
	covered := make(map[int]bool)
	for _, member := range group {
		if _, from, to, ok := parseAskPointer(hits[member].Pointer); ok {
			for line := from; line <= min(to, len(lines)); line++ {
				covered[line] = true
			}
		}
	}
	if float64(len(covered)) < askWholeFileShare*float64(len(lines)) {
		return false
	}
	var whole strings.Builder
	for number, line := range lines {
		fmt.Fprintf(&whole, "L%d: %s\n", number+1, line)
	}
	fmt.Fprintf(&whole, "… (whole file %s, %d lines", path, len(lines))
	if len(group) > 1 {
		others := make([]string, 0, len(group)-1)
		for _, member := range group[1:] {
			others = append(others, strconv.Itoa(member+1))
		}
		fmt.Fprintf(&whole, "; also holds hits %s", strings.Join(others, ", "))
	}
	whole.WriteString(")")
	codes := make([]string, len(group))
	for i, member := range group {
		codes[i], hits[member].Code = hits[member].Code, ""
	}
	hits[group[0]].Code = whole.String()
	if fits() {
		return true
	}
	for i, member := range group {
		hits[member].Code = codes[i]
	}
	return false
}

// askHitNodes returns the graph node each hit names, the zero node for a hit
// it cannot find. It keys by title as well as pointer: a one-line class shares
// its span with its methods.
func askHitNodes(wiring graph.GraphV1, hits []graph.AskHit) []graph.NodeV1 {
	wanted := make(map[string]int, len(hits))
	for index, hit := range hits {
		wanted[hit.Title+"\x00"+hit.Pointer] = index
	}
	nodes := make([]graph.NodeV1, len(hits))
	for _, node := range wiring.Nodes {
		if index, ok := wanted[node.Name+" · "+string(node.Kind)+"\x00"+node.Path+":"+node.Span]; ok {
			nodes[index] = node
		}
	}
	return nodes
}

// askNamesHit reports whether the query names the hit's symbol as a word.
func askNamesHit(hit graph.AskHit, query string) bool {
	name, _, _ := strings.Cut(hit.Title, " · ")
	name = strings.ToLower(name)
	if _, last, ok := strings.CutLast(name, "."); ok {
		name = last
	}
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	return name != "" && slices.Contains(words, name)
}

func askNodeName(node graph.NodeV1) string {
	if node.Owner != nil {
		return *node.Owner + "." + node.Name
	}
	return node.Name
}

// callFlow returns the longest chain of calls through the given callables as
// one "call flow:" line, with the chain's node IDs, or nothing for a chain of
// fewer than three nodes, which the source beside it already shows. A step
// may pass through one callable outside the set, shown in brackets, so a
// helper between two named symbols does not break the chain, and it may
// dispatch from an interface method to an implementation, marked as such.
func callFlow(wiring graph.GraphV1, named []graph.NodeV1) (string, []string) {
	const maxNamed = 8
	in := make(map[string]bool)
	var ids []string
	for _, node := range named {
		if (node.Kind == "function" || node.Kind == "method") && !in[node.ID] && len(ids) < maxNamed {
			in[node.ID] = true
			ids = append(ids, node.ID)
		}
	}
	if len(ids) < 2 {
		return "", nil
	}
	callable := make(map[string]bool)
	for _, node := range wiring.Nodes {
		if node.Kind == "function" || node.Kind == "method" {
			callable[node.ID] = true
		}
	}
	// steps maps a caller to its callees, each labeled "" for a call the
	// extractor saw, "inferred", or "dispatch" for an implements edge walked
	// from the interface method to its implementation.
	steps := make(map[string]map[string]string)
	step := func(from, to, label string) {
		if steps[from] == nil {
			steps[from] = make(map[string]string)
		}
		if previous, ok := steps[from][to]; !ok || previous == "dispatch" {
			steps[from][to] = label
		}
	}
	for _, edge := range wiring.Edges {
		switch {
		case edge.Source == edge.Target:
		case edge.Relation == "calls" && edge.Confidence == "inferred":
			step(edge.Source, edge.Target, "inferred")
		case edge.Relation == "calls":
			step(edge.Source, edge.Target, "")
		case edge.Relation == "implements" && callable[edge.Source] && callable[edge.Target]:
			step(edge.Target, edge.Source, "dispatch")
		}
	}
	chain := longestCallChain(ids, in, steps)
	if len(chain) < 3 {
		return "", nil
	}
	labels := make(map[string]string, len(chain))
	for _, node := range wiring.Nodes {
		if slices.Contains(chain, node.ID) {
			start, _, _ := strings.Cut(node.Span, "-")
			labels[node.ID] = fmt.Sprintf("%s (%s:%s)", askNodeName(node), node.Path, start)
		}
	}
	var line strings.Builder
	line.WriteString("call flow: ")
	for i, id := range chain {
		label := cmp.Or(labels[id], id)
		if !in[id] {
			label = "[" + label + "]"
		}
		if i > 0 {
			line.WriteString(" → ")
			if kind := steps[chain[i-1]][id]; kind != "" {
				line.WriteString("(" + kind + ") ")
			}
		}
		line.WriteString(label)
	}
	return line.String(), chain
}

// longestCallChain returns the chain of calls that passes through the most of
// ids, the shorter of two such chains, and the earlier id breaks a tie; it
// returns nil when no two ids connect. Between two ids a step may pass through
// one callee outside them, and a chain holds at most seven nodes, so it never
// wanders into a large function's fan-out.
func longestCallChain(ids []string, in map[string]bool, calls map[string]map[string]string) []string {
	const maxChain = 7
	type link struct{ to, bridge string }
	links := make(map[string][]link)
	for _, from := range ids {
		for _, to := range ids {
			if from == to {
				continue
			}
			if _, direct := calls[from][to]; direct {
				links[from] = append(links[from], link{to: to})
				continue
			}
			for _, bridge := range slices.Sorted(maps.Keys(calls[from])) {
				if _, ok := calls[bridge][to]; ok && !in[bridge] {
					links[from] = append(links[from], link{to: to, bridge: bridge})
					break
				}
			}
		}
	}
	var best []string
	bestNamed := 1
	visited := make(map[string]bool)
	var walk func(chain []string, named int)
	walk = func(chain []string, named int) {
		if named > bestNamed || named == bestNamed && len(chain) < len(best) {
			best, bestNamed = slices.Clone(chain), named
		}
		for _, next := range links[chain[len(chain)-1]] {
			grown := slices.Clip(chain)
			if next.bridge != "" {
				grown = append(grown, next.bridge)
			}
			grown = append(grown, next.to)
			if visited[next.to] || next.bridge != "" && visited[next.bridge] || len(grown) > maxChain {
				continue
			}
			steps := []string{next.to}
			if next.bridge != "" {
				steps = append(steps, next.bridge)
			}
			for _, id := range steps {
				visited[id] = true
			}
			walk(grown, named+1)
			for _, id := range steps {
				visited[id] = false
			}
		}
	}
	for _, id := range ids {
		visited[id] = true
		walk([]string{id}, 1)
		visited[id] = false
	}
	return best
}
