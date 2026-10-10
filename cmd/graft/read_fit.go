package main

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
)

// fitReadResult cuts a definition over the budget down to what fits, so a large
// class or function still answers in one round instead of asking for a retry.
// A container (any kind but a function or method, with members it contains)
// becomes an outline of its members; any other definition keeps its longest
// whole-line head. needed is the budget the whole definition takes, and fits
// reports whether a candidate fits the response. ok is false when not even the
// declaration line fits.
func fitReadResult(wiring graph.GraphV1, node graph.NodeV1, result readResult, needed int, mcp bool, fits func(readResult) bool) (readResult, bool) {
	flag, also, fileAPI := "--budget", "--also", "graft skeleton"
	if mcp {
		flag, also, fileAPI = "budget", "also", "graft_file_api"
	}
	nodes := make(map[string]graph.NodeV1, len(wiring.Nodes))
	for _, candidate := range wiring.Nodes {
		nodes[candidate.ID] = candidate
	}
	var members []graph.NodeV1
	var callees []string
	for _, edge := range wiring.Edges {
		target, ok := nodes[edge.Target]
		if !ok || edge.Source != node.ID {
			continue
		}
		switch {
		case edge.Relation == "contains" && target.Span != "":
			members = append(members, target)
		// Type uses are references too; the gap names only what runs.
		case (edge.Relation == "calls" || edge.Relation == "references") && (target.Kind == "function" || target.Kind == "method") &&
			!slices.Contains(callees, target.Name):
			callees = append(callees, target.Name)
		}
	}
	if len(members) > 0 && node.Kind != "function" && node.Kind != "method" {
		note := fmt.Sprintf("the whole %s needs %d estimated tokens; its members follow as ⋮ lines: read them by name, several at once with %s", node.Kind, needed, also)
		return fitReadOutline(withReadNote(result, note), node, members, fileAPI, fits)
	}
	whole := fmt.Sprintf("the whole %s needs %d estimated tokens: %s %d", node.Kind, needed, flag, needed)
	if mcp && needed > mcpBudgetCeiling {
		// A larger budget would come back cut the same way.
		whole = fmt.Sprintf("the whole %s needs %d, more than the %d estimated tokens one answer holds", node.Kind, needed, mcpBudgetCeiling)
	}
	return fitReadHead(result, node, callees, whole, fits)
}

// fitReadOutline keeps a container's head and lists every member as a gap line
// with the selector to read it by and its signature. When that does not fit it
// drops the signatures, then lists the members that fit and points at the
// file's skeleton for the rest.
func fitReadOutline(result readResult, node graph.NodeV1, members []graph.NodeV1, fileAPI string, fits func(readResult) bool) (readResult, bool) {
	start := func(member graph.NodeV1) int {
		line, _, _ := spanLines(member.Span)
		return line
	}
	slices.SortFunc(members, func(a, b graph.NodeV1) int { return cmp.Compare(start(a), start(b)) })
	gaps := func(signatures bool) []string {
		out := make([]string, len(members))
		for i, member := range members {
			_, selector, _ := strings.Cut(member.ID, "#")
			out[i] = "⋮ " + member.Span + " " + selector
			if signatures && member.Signature != nil {
				out[i] += " · " + strings.Join(strings.Fields(*member.Signature), " ")
			}
		}
		return out
	}
	lines := strings.Split(result.Code, "\n")
	outline := func(head int, shown []string, rest int) readResult {
		fitted := result
		fitted.Code = strings.Join(slices.Concat(lines[:head], shown), "\n")
		if rest > 0 {
			fitted.Code += fmt.Sprintf("\n⋮ +%d more members: %s %s", rest, fileAPI, node.Path)
		}
		return fitted
	}
	// The head is the declaration plus whatever precedes the first member.
	from, _, _ := spanLines(node.Span)
	headMax := min(max(start(members[0])-from, 1), len(lines))
	for _, signatures := range []bool{true, false} {
		shown := gaps(signatures)
		if head := sort.Search(headMax, func(i int) bool { return !fits(outline(i+1, shown, 0)) }); head > 0 {
			return outline(head, shown, 0), true
		}
	}
	names := gaps(false)
	if count := sort.Search(len(names)-1, func(i int) bool { return !fits(outline(1, names[:i+1], len(names)-i-1)) }); count > 0 {
		return outline(1, names[:count], len(names)-count), true
	}
	return result, false
}

// fitReadHead keeps the longest whole-line head of a definition and closes it
// with a gap line naming, in order of appearance, up to six callees the hidden
// rest mentions.
func fitReadHead(result readResult, node graph.NodeV1, callees []string, whole string, fits func(readResult) bool) (readResult, bool) {
	lines := strings.Split(result.Code, "\n")
	// Each callee's lines inside the definition, to name the ones a cut hides.
	type mention struct {
		name  string
		lines []int
	}
	mentions := make([]mention, 0, len(callees))
	for _, name := range callees {
		word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		found := mention{name: name}
		for i, line := range lines {
			if word.MatchString(line) {
				found.lines = append(found.lines, i)
			}
		}
		mentions = append(mentions, found)
	}
	from, to, _ := spanLines(node.Span)
	head := func(shown int) readResult {
		var hidden []mention
		for _, m := range mentions {
			if i := slices.IndexFunc(m.lines, func(line int) bool { return line >= shown }); i >= 0 {
				hidden = append(hidden, mention{m.name, m.lines[i:]})
			}
		}
		slices.SortStableFunc(hidden, func(a, b mention) int { return cmp.Compare(a.lines[0], b.lines[0]) })
		gap := fmt.Sprintf("⋮ L%d-L%d", from+shown, to)
		if len(hidden) > 0 {
			names := make([]string, 0, 7)
			for _, m := range hidden[:min(len(hidden), 6)] {
				names = append(names, m.name)
			}
			if len(hidden) > 6 {
				names = append(names, fmt.Sprintf("+%d more", len(hidden)-6))
			}
			gap += " · calls " + strings.Join(names, ", ")
		}
		fitted := withReadNote(result, fmt.Sprintf("shows L%d-L%d of %s; %s", from, from+shown-1, node.Span, whole))
		fitted.Code = strings.Join(lines[:shown], "\n") + "\n" + gap
		return fitted
	}
	shown := sort.Search(len(lines)-1, func(i int) bool { return !fits(head(i + 1)) })
	if shown == 0 {
		return result, false
	}
	return head(shown), true
}

// withReadNote appends note to the resolution note a read already carries.
func withReadNote(result readResult, note string) readResult {
	if result.Note != "" {
		note = result.Note + "; " + note
	}
	result.Note = note
	return result
}
