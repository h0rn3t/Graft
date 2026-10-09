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

func TestFitGrepResultCountsCopiesBehindProduction(t *testing.T) {
	group := func(path string, hits int) graph.GrepGroup {
		g := graph.GrepGroup{Path: path}
		for line := range hits {
			g.Hits = append(g.Hits, graph.GrepHit{Line: line + 1, Text: "inputUSDPerMtok"})
		}
		return g
	}
	mixed := []graph.GrepGroup{group("cmd/tally.go", 2), group("cmd/tally_test.go", 3), group("testdata/real/cmd/tally.go", 4)}
	tests := []struct {
		name       string
		pattern    string
		groups     []graph.GrepGroup
		wantPaths  []string
		wantTotal  int
		wantRemain string
	}{
		{name: "production and copies", pattern: "inputUSDPerMtok", groups: mixed, wantPaths: []string{"cmd/tally.go"}, wantTotal: 2,
			wantRemain: "more hits in: testdata/real/cmd/tally.go (4), cmd/tally_test.go (3)"},
		{name: "pattern asks for tests", pattern: "TestInputUSDPerMtok|inputUSDPerMtok", groups: mixed, wantPaths: []string{"cmd/tally.go", "cmd/tally_test.go", "testdata/real/cmd/tally.go"}, wantTotal: 9},
		{name: "only copies match", pattern: "inputUSDPerMtok", groups: mixed[1:], wantPaths: []string{"cmd/tally_test.go", "testdata/real/cmd/tally.go"}, wantTotal: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := graph.GrepResult{Pattern: tt.pattern, Groups: tt.groups}
			for _, g := range tt.groups {
				result.TotalHits += len(g.Hits)
			}
			got := fitGrepResult(result, 1<<20)
			var paths []string
			for _, g := range got.Groups {
				paths = append(paths, g.Path)
			}
			if !reflect.DeepEqual(paths, tt.wantPaths) || got.TotalHits != tt.wantTotal {
				t.Errorf("fitGrepResult(%q) = (groups %q, total %d), want (%q, %d)", tt.pattern, paths, got.TotalHits, tt.wantPaths, tt.wantTotal)
			}
			if note := grepRemainderNote(result, got); !strings.HasPrefix(note, tt.wantRemain) || (tt.wantRemain == "") != (note == "") {
				t.Errorf("grepRemainderNote(%q) = %q, want prefix %q", tt.pattern, note, tt.wantRemain)
			}
		})
	}
}
