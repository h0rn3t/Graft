package main

import (
	"cmp"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// federateGrep greps every loaded child of a workspace and merges the groups
// under <child>/ paths, as the TypeScript federateGrep does. A non-empty in
// names one child by its first segment and a path inside it by the rest.
func federateGrep(root, contextDir, pattern string, ignoreCase, fixed bool, in string) (graph.GrepResult, string, error) {
	workspace := graph.LoadWorkspaceGraphs(root, contextDir)
	onlyChild, childIn, _ := strings.Cut(strings.Trim(in, "/"), "/")
	if onlyChild != "" && !slices.ContainsFunc(workspace.Loaded, func(child graph.WorkspaceChild) bool { return child.Name == onlyChild }) {
		names := make([]string, 0, len(workspace.Loaded))
		for _, child := range workspace.Loaded {
			names = append(names, child.Name)
		}
		return graph.GrepResult{}, "", fmt.Errorf("no workspace repo %q - repos: %s", onlyChild, strings.Join(names, ", "))
	}
	result := graph.GrepResult{Pattern: pattern, Groups: make([]graph.GrepGroup, 0)}
	saved := &graph.GrepSavings{}
	for _, child := range workspace.Loaded {
		if onlyChild != "" && child.Name != onlyChild {
			continue
		}
		childResult, err := graph.Grep(child.Graph, filepath.Join(root, child.Name), pattern, graph.GrepOptions{IgnoreCase: ignoreCase, Fixed: fixed, In: childIn})
		if err != nil {
			return graph.GrepResult{}, "", err
		}
		result.FilesSearched += childResult.FilesSearched
		result.TotalHits += childResult.TotalHits
		result.Truncated.Files += childResult.Truncated.Files
		result.Truncated.Hits += childResult.Truncated.Hits
		if childResult.Saved != nil {
			saved.Files += childResult.Saved.Files
			saved.BaselineChars += childResult.Saved.BaselineChars
		}
		for _, group := range childResult.Groups {
			group.Path = child.Name + "/" + group.Path
			if group.Symbol != nil {
				symbol := *group.Symbol
				symbol.Path = child.Name + "/" + symbol.Path
				group.Symbol = &symbol
			}
			result.Groups = append(result.Groups, group)
		}
	}
	compare := graph.LocaleCompare()
	slices.SortStableFunc(result.Groups, func(a, b graph.GrepGroup) int {
		return cmp.Or(cmp.Compare(b.InDegree, a.InDegree), compare(a.Path, b.Path))
	})
	if saved.BaselineChars > 0 {
		result.Saved = saved
	}
	return result, mcpWorkspaceCoverage(workspace), nil
}

// federateCallers walks the symbol's edges in every child that defines it and
// renders one ## <child>/ block per child, as the TypeScript federateCallers does.
func federateCallers(root, contextDir, symbol string, direction graph.Direction, depth int, in string) (string, bool, error) {
	workspace := graph.LoadWorkspaceGraphs(root, contextDir)
	blocks := make([]string, 0, len(workspace.Loaded))
	found := false
	for _, child := range workspace.Loaded {
		matches, err := graph.ResolveSymbol(child.Graph, symbol, graph.ResolveSymbolOptions{In: in})
		if err != nil {
			return "", false, err
		}
		if len(matches) == 0 {
			continue
		}
		found = true
		results := make([]callersResult, len(matches))
		lines := []string{"## " + child.Name + "/"}
		for index, match := range matches {
			results[index] = callersResult{symbol: match, hits: graph.EdgeWalk(child.Graph, match, direction, depth)}
			lines = append(lines, fmt.Sprintf("%s · %s · %s:%s", match.Name, match.Kind, match.Path, match.Span))
			if len(results[index].hits) == 0 {
				lines = append(lines, looseNote(direction, match.Name, len(matches)))
				continue
			}
			for _, hit := range results[index].hits {
				arrow := "←"
				if direction == graph.DirectionOut {
					arrow = "→"
				}
				label := fmt.Sprintf("%s (unresolved import)", hit.ID)
				if hit.Node != nil {
					label = fmt.Sprintf("%s (%s:%s)", hit.Node.Name, hit.Node.Path, hit.Node.Span)
				}
				depthLabel := ""
				if depth > 1 {
					depthLabel = fmt.Sprintf(" [depth %d]", hit.Depth)
				}
				lines = append(lines, fmt.Sprintf("  %s %s %s%s", hit.Relation, arrow, label, depthLabel))
			}
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	coverage := mcpWorkspaceCoverage(workspace)
	if !found {
		text := fmt.Sprintf("no symbol \"%s\" in any of the %d workspace repo(s) — check spelling or run graft build", symbol, len(workspace.Loaded))
		if coverage != "" {
			text += "\n" + coverage
		}
		return text, false, nil
	}
	text := strings.Join(blocks, "\n\n")
	if coverage != "" {
		text += "\n\n" + coverage
	}
	return text, true, nil
}

var workspaceDepthAll = regexp.MustCompile(`(?i)^(all|full|max)$`)

// workspaceCallersDepth reads --depth as the TypeScript workspace path does:
// all/full/max is unbounded, and anything below 1 or not a number is 1.
func workspaceCallersDepth(raw string) int {
	if raw == "" {
		return 1
	}
	if workspaceDepthAll.MatchString(raw) {
		return int(^uint(0) >> 1)
	}
	value := jsonjs.ToNumber(raw)
	if math.IsNaN(value) || value < 1 {
		return 1
	}
	if value >= float64(int(^uint(0)>>1)) {
		return int(^uint(0) >> 1)
	}
	return int(math.Floor(value))
}
