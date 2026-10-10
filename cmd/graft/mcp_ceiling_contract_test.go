package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
)

// writeCeilingFixture adds sources whose answers run past what one MCP answer
// holds: giant0 is about 33,000 characters, giant1-3 about 9,000 each,
// wide.ts declares 1,200 functions, and src holds 500 long-named directories.
// journal.ts holds a 124-line method, which find_code shows as an excerpt.
func writeCeilingFixture(t *testing.T, root string) {
	t.Helper()
	writeReadFixture(t, root)
	files := make(map[string]string)
	var giants strings.Builder
	for g, lines := range []int{1500, 400, 400, 400} {
		fmt.Fprintf(&giants, "export function giant%d(): number {\n", g)
		for i := range lines {
			fmt.Fprintf(&giants, "  const step%d = %d;\n", i, i)
		}
		fmt.Fprintf(&giants, "  return step%d;\n}\n", lines-1)
	}
	files["giant.ts"] = giants.String()
	var journal strings.Builder
	journal.WriteString("export class Journal {\n  replay(entries: number[]): number {\n    let total = 0;\n")
	for i := range 120 {
		fmt.Fprintf(&journal, "    total += entries[%d];\n", i)
	}
	journal.WriteString("    return total;\n  }\n}\n")
	files["journal.ts"] = journal.String()
	var wide strings.Builder
	for i := range 1200 {
		fmt.Fprintf(&wide, "/** wide%d documents a long parameter list. */\nexport function wide%d(first: string, second: number): string {\n  return first + second;\n}\n", i, i)
	}
	files["wide.ts"] = wide.String()
	for i := range 500 {
		files[fmt.Sprintf("a_directory_with_a_rather_long_name_%03d/mod.ts", i)] = fmt.Sprintf("export function dir%dEntry(): number { return %d; }\n", i, i)
	}
	for name, body := range files {
		path := filepath.Join(root, "src", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPAnswersStayUnderTheCeiling(t *testing.T) {
	root := t.TempDir()
	writeCeilingFixture(t, root)
	dir := filepath.Join(root, "graft")
	budgetAdvice := regexp.MustCompile(`budget (\d+)`)
	tests := []struct {
		name          string
		tool          string
		args          map[string]any
		want, notWant []string
	}{
		{
			name:    "find_code inlines whole bodies at the largest budget",
			tool:    "graft_find_code",
			args:    map[string]any{"query": "giant step", "full": true, "budget": float64(64000), "limit": float64(10)},
			want:    []string{"giant0", "… +"},
			notWant: []string{"increase budget"},
		},
		{
			name: "an excerpt points at its method by Owner.member",
			tool: "graft_find_code",
			args: map[string]any{"query": "replay entries", "limit": float64(2), "budget": float64(300)},
			want: []string{"lines (graft_read_symbol Journal.replay)"},
		},
		{
			name: "read_symbol fits a definition past the ceiling",
			tool: "graft_read_symbol",
			args: map[string]any{"symbol": "giant0", "budget": float64(64000)},
			want: []string{"export function giant0(): number {", "⋮ L", "more than the 6000 estimated tokens one answer holds"},
		},
		{
			name:    "file_api names the definitions it leaves out",
			tool:    "graft_file_api",
			args:    map[string]any{"file": "src/wide.ts"},
			want:    []string{"function wide0", "more definitions from L", "+"},
			notWant: []string{"documents a long parameter list"},
		},
		{
			name: "repo_map keeps its hotspots",
			tool: "graft_repo_map",
			args: map[string]any{"max_dirs": float64(100000)},
			want: []string{"hotspots:", "more directories not shown"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mcpCall(t.Context(), root, dir, "", tt.tool, tt.args)
			if got.isError {
				t.Fatalf("mcpCall(%s, %v) = error %q, want an answer", tt.tool, tt.args, got.text)
			}
			// Hosts count UTF-16 code units, as savings.Length does.
			if length := savings.Length(got.text); length > mcpResultCeiling {
				t.Errorf("mcpCall(%s, %v) = %d characters, want at most %d", tt.tool, tt.args, length, mcpResultCeiling)
			}
			for _, want := range tt.want {
				if !strings.Contains(got.text, want) {
					t.Errorf("mcpCall(%s, %v) = %q, want it to contain %q", tt.tool, tt.args, got.text, want)
				}
			}
			for _, notWant := range append(tt.notWant, "range directly", "the Read tool") {
				if strings.Contains(got.text, notWant) {
					t.Errorf("mcpCall(%s, %v) = %q, want no %q", tt.tool, tt.args, got.text, notWant)
				}
			}
			// A budget past the ceiling would only come back cut the same way.
			for _, match := range budgetAdvice.FindAllStringSubmatch(got.text, -1) {
				if advised, _ := strconv.Atoi(match[1]); advised > mcpBudgetCeiling {
					t.Errorf("mcpCall(%s, %v) advises %q, want no budget above %d", tt.tool, tt.args, match[0], mcpBudgetCeiling)
				}
			}
		})
	}
}

func TestFitMCPText(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d %s", i, strings.Repeat("x", 40))
	}
	long := strings.Join(lines, "\n")
	tests := []struct {
		name, text string
		ceiling    int
		want       string
	}{
		{name: "nil text", text: "", ceiling: 100, want: ""},
		{name: "under the ceiling", text: "short\nanswer", ceiling: 100, want: "short\nanswer"},
		{name: "at the ceiling", text: long, ceiling: len(long), want: long},
		{name: "over the ceiling", text: long, ceiling: 1000, want: strings.Join(lines[:19], "\n") + "\n⋮ +81 more lines past the 1000 characters one answer holds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fitMCPText(tt.text, tt.ceiling); got != tt.want {
				t.Errorf("fitMCPText(%d characters, %d) = %q, want %q", len(tt.text), tt.ceiling, got, tt.want)
			}
		})
	}
}

func TestFitSkeletonText(t *testing.T) {
	result := graph.SkeletonResult{File: "src/wide.ts"}
	for i := range 20 {
		result.Entries = append(result.Entries, graph.SkeletonEntry{
			Span:      fmt.Sprintf("L%d-L%d", 4*i+2, 4*i+4),
			Kind:      "function",
			Name:      fmt.Sprintf("wide%d", i),
			Signature: new(fmt.Sprintf("function wide%d(first: string, second: number): string", i)),
			Summary:   new(fmt.Sprintf("wide%d documents a long parameter list.", i)),
		})
	}
	tests := []struct {
		name          string
		ceiling       int
		want, notWant []string
	}{
		{
			name:    "whole when it fits",
			ceiling: 10000,
			want:    []string{"graft skeleton — src/wide.ts\n- L2-L4 function wide0(first: string, second: number): string — wide0 documents"},
			notWant: []string{"left out"},
		},
		{
			name:    "drops docs first",
			ceiling: 1600,
			want:    []string{"(docs left out to fit one answer)", "- L2-L4 function wide0(first: string, second: number): string\n", "wide19(first"},
			notWant: []string{"documents"},
		},
		{
			name:    "then signatures",
			ceiling: 700,
			want:    []string{"(signatures and docs left out to fit one answer)", "- L2-L4  function wide0\n", "function wide19"},
			notWant: []string{"(first"},
		},
		{
			name:    "then names the definitions past the ceiling",
			ceiling: 400,
			want:    []string{"- L2-L4  function wide0\n", "more definitions from L", ": wide", ", +", "graft_read_symbol"},
			notWant: []string{"function wide19"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fitSkeletonText(result, tt.ceiling)
			if length := savings.Length(got); length > tt.ceiling {
				t.Errorf("fitSkeletonText(20 entries, %d) = %d characters, want at most %d", tt.ceiling, length, tt.ceiling)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("fitSkeletonText(20 entries, %d) = %q, want it to contain %q", tt.ceiling, got, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("fitSkeletonText(20 entries, %d) = %q, want no %q", tt.ceiling, got, notWant)
				}
			}
		})
	}
}

// TestAskBudgetMarksWhatItCuts: a body cut to fit the budget keeps a marker
// counting the lines it lost, so it never reads as a whole definition, and
// the hits dropped whole are named with their spans.
func TestAskBudgetMarksWhatItCuts(t *testing.T) {
	numbered := func(from, count int) string {
		lines := make([]string, count)
		for i := range lines {
			lines[i] = fmt.Sprintf("L%d: \tstep%d()", from+i, i)
		}
		return strings.Join(lines, "\n")
	}
	// hitsOf returns three leading hits, one of them an excerpt, then extras
	// hits of lines lines each.
	hitsOf := func(extras, lines int) []graph.AskHit {
		hits := []graph.AskHit{
			{Kind: "symbol", Title: "first · function", Pointer: "a.go:L1-L40", Code: numbered(1, 40)},
			{Kind: "symbol", Title: "second · function", Pointer: "b.go:L1-L40", Code: numbered(1, 40)},
			{Kind: "symbol", Title: "third · function", Pointer: "c.go:L5-L44", Code: numbered(5, 8) + "\n… (excerpt; full definition at c.go:L5-L44; rerun with --full)"},
		}
		for i := range extras {
			from := 1000*i + 1
			hits = append(hits, graph.AskHit{Kind: "symbol", Title: fmt.Sprintf("extra%d · function", i), Pointer: fmt.Sprintf("x.go:L%d-L%d", from, from+lines-1), Code: numbered(from, lines)})
		}
		return hits
	}
	tests := []struct {
		name    string
		hits    []graph.AskHit
		budget  int
		mcp     bool
		want    []string
		notWant []string
	}{
		{
			name:   "drops trailing hits and names them",
			hits:   hitsOf(8, 3),
			budget: 400,
			want:   []string{"not shown: extra0 (x.go:L1-L3), ", ", +2 more; increase --budget or narrow --in."},
		},
		{
			name:    "MCP at the ceiling only narrows",
			hits:    hitsOf(10, 300),
			budget:  mcpBudgetCeiling,
			mcp:     true,
			want:    []string{"not shown: ", "narrow in"},
			notWant: []string{"increase budget"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := graph.AskResult{Query: "steps", Mode: "lexical", Hits: tt.hits}
			got, err := fitAskBudget(result, tt.budget, false, tt.mcp)
			if err != nil {
				t.Fatalf("fitAskBudget(%d) error = %v, want nil", tt.budget, err)
			}
			text := renderAskBudget(got, false, tt.mcp)
			if tokens := savings.Tokens(savings.Length(text)); tokens > tt.budget {
				t.Errorf("fitAskBudget(%d) = %d estimated tokens, want at most %d", tt.budget, tokens, tt.budget)
			}
			for _, hit := range got.Hits {
				original := tt.hits[slices.IndexFunc(tt.hits, func(h graph.AskHit) bool { return h.Title == hit.Title })]
				// A body the budget cut must announce the lines it lost.
				if hit.Code != "" && hit.Code != original.Code && !strings.Contains(hit.Code, "… (excerpt; full definition at "+original.Pointer) {
					t.Errorf("fitAskBudget(%d) hit %s code = %q, want the original or an excerpt marker for %s", tt.budget, hit.Title, hit.Code, original.Pointer)
				}
			}
			if len(got.Hits) < len(tt.hits) && len(got.Hits) > 0 {
				dropped := tt.hits[len(got.Hits)]
				if name, _, _ := strings.Cut(dropped.Title, " · "); !strings.Contains(got.Note, name+" ("+dropped.Pointer+")") {
					t.Errorf("fitAskBudget(%d) note = %q, want it to name %s (%s)", tt.budget, got.Note, name, dropped.Pointer)
				}
			}
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Errorf("fitAskBudget(%d) = %q, want it to contain %q", tt.budget, text, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(text, notWant) {
					t.Errorf("fitAskBudget(%d, mcp) = %q, want no %q", tt.budget, text, notWant)
				}
			}
		})
	}
}

// TestAskBudgetHalvedBodyRendersItsGap: a whole body, inlined unnumbered as
// full: true and the budget fill inline it, is cut into a numbered excerpt.
func TestAskBudgetHalvedBodyRendersItsGap(t *testing.T) {
	lines := make([]string, 80)
	for i := range lines {
		lines[i] = fmt.Sprintf("\tstep%d()", i)
	}
	result := graph.AskResult{Query: "steps", Mode: "lexical", Hits: []graph.AskHit{
		{Kind: "symbol", Title: "first · function", Pointer: "a.go:L11-L90", Code: strings.Join(lines, "\n")},
	}}
	got, err := fitAskBudget(result, 150, false, true)
	if err != nil {
		t.Fatalf("fitAskBudget(150) error = %v, want nil", err)
	}
	text := renderAskBudget(got, false, true)
	if !strings.Contains(text, "L11: \tstep0()") || !regexp.MustCompile(`(?m)^… \+\d+ lines`).MatchString(text) || strings.Contains(text, "step79()") {
		t.Errorf("fitAskBudget(150) = %q, want the head of first numbered from L11 and closed by a … +N lines marker", text)
	}
}
