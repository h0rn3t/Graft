package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestFitGrepResultContract(t *testing.T) {
	text := strings.Repeat("x", 100)
	result := graph.GrepResult{Pattern: "x", TotalHits: 6, FilesSearched: 3}
	for _, file := range []string{"a.go", "b.go", "c.go"} {
		result.Groups = append(result.Groups, graph.GrepGroup{Path: file, Hits: []graph.GrepHit{{Line: 1, Text: text}, {Line: 2, Text: text}}})
	}
	result.Truncated.Hits = 4
	original := fmt.Sprintf("%#v", result)

	if got := fitGrepResult(result, 1<<20); !reflect.DeepEqual(got, result) {
		t.Errorf("fitGrepResult(result, 1 MiB) = %#v, want result unchanged", got)
	}

	const budget = 400
	got := fitGrepResult(result, budget)
	var kept []string
	var body strings.Builder
	for _, group := range got.Groups {
		body.WriteString(grepGroupHeader(group))
		for _, hit := range group.Hits {
			kept = append(kept, fmt.Sprintf("%s:%d", group.Path, hit.Line))
			fmt.Fprintf(&body, "\n  L%d: %s", hit.Line, hit.Text)
		}
	}
	if want := []string{"a.go:1", "a.go:2", "b.go:1"}; !reflect.DeepEqual(kept, want) {
		t.Errorf("fitGrepResult(result, %d) kept hits = %q, want the top-ranked %q", budget, kept, want)
	}
	if body.Len() > budget {
		t.Errorf("fitGrepResult(result, %d) renders %d bytes of groups, want at most %d", budget, body.Len(), budget)
	}
	if got.TotalHits != 3 || got.Truncated.Hits != 7 {
		t.Errorf("fitGrepResult(result, %d) = (total %d, truncated %d), want (3, 7)", budget, got.TotalHits, got.Truncated.Hits)
	}
	if note := "(truncated: 7 more hits beyond the cap"; !strings.Contains(formatGrepResult(got), note) {
		t.Errorf("formatGrepResult(fitGrepResult(result, %d)) = %q, want %q", budget, formatGrepResult(got), note)
	}
	if fmt.Sprintf("%#v", result) != original {
		t.Errorf("fitGrepResult(result, %d) mutated its input to %#v", budget, result)
	}
}
