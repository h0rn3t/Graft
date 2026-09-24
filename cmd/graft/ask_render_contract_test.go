package main

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
)

func TestAskTextRenderContract(t *testing.T) {
	excerpt := strings.Join([]string{
		"L10: func run(a int) error {",
		"…",
		"L20: \tif a > 0 {",
		"L21: \t\treturn check(a)",
		"L22: \t}",
		"L23: ",
		"L24: \t})",
		"L25: \tcleanup()",
		"L26: \treturn nil",
		"… (excerpt; full definition at pkg/run.go:L10-L40; rerun with --full)",
	}, "\n")
	full := "func tiny() {\n\twork()\n}"
	tests := []struct {
		name    string
		result  graph.AskResult
		mcp     bool
		want    []string
		notWant []string
	}{
		{
			name:    "query is not echoed",
			result:  graph.AskResult{Query: "needle query", Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Code: full}}},
			want:    []string{"1. run · function\n   pkg/run.go:L10-L40"},
			notWant: []string{"graft ask", "needle query", "(lexical)"},
		},
		{
			name:    "kind tag repeating the title is dropped",
			result:  graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40"}}},
			notWant: []string{"[symbol]"},
		},
		{
			name:   "other kind tag is kept",
			result: graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "card", Title: "Ask pipeline", Pointer: "graft/ask.md"}}},
			want:   []string{"1. Ask pipeline  [card]"},
		},
		{
			name:    "bare file hit is one line",
			result:  graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "sync_unix.go · file", Pointer: "cmd/graft/sync_unix.go"}}},
			want:    []string{"1. cmd/graft/sync_unix.go (file)"},
			notWant: []string{"sync_unix.go · file"},
		},
		{
			name:    "signature repeated by the excerpt is omitted",
			result:  graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Snippet: "func run(a int)  error", Code: excerpt}}},
			want:    []string{"   pkg/run.go:L10-L40\n\n```\nL10: func run(a int) error {"},
			notWant: []string{"   func run(a int)  error"},
		},
		{
			name:   "signature is kept when the excerpt does not start with it",
			result: graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Snippet: "func run(a int) error", Code: "L30: \tcleanup()"}}},
			want:   []string{"   func run(a int) error"},
		},
		{
			name:    "truncated excerpt drops closer-only lines and counts omitted lines on the CLI",
			result:  graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Code: excerpt}}},
			want:    []string{"L21: \t\treturn check(a)\nL25: \tcleanup()\nL26: \treturn nil\n… +26 lines (--full)\n```"},
			notWant: []string{"L22:", "L23:", "L24:", "full definition at"},
		},
		{
			name:   "truncated excerpt names the MCP option over MCP",
			result: graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Code: excerpt}}},
			mcp:    true,
			want:   []string{"… +26 lines (full: true)"},
		},
		{
			name:   "whole definitions are printed verbatim",
			result: graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "tiny · function", Pointer: "pkg/tiny.go:L1-L3", Code: full}}},
			want:   []string{"```\n" + full + "\n```"},
		},
		{
			name:    "structural hits compact their excerpts too",
			result:  graph.AskResult{Mode: "structural", Hits: []graph.AskHit{{Title: "run", Pointer: "pkg/run.go:L10-L40", Relation: "calls", Code: excerpt}}},
			want:    []string{"- run  pkg/run.go:L10-L40  (calls)", "… +26 lines (--full)"},
			notWant: []string{"graft ask", "(structural)"},
		},
		{
			name:    "no hits keeps the no-match text",
			result:  graph.AskResult{Query: "absent", Mode: "empty"},
			want:    []string{"no matches."},
			notWant: []string{"graft ask", "absent"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatAskText(tt.result, tt.mcp)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("formatAskText(%s, mcp=%t) = %q, want it to contain %q", tt.name, tt.mcp, got, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("formatAskText(%s, mcp=%t) = %q, want no %q", tt.name, tt.mcp, got, notWant)
				}
			}
		})
	}
}

func TestSkeletonTextRenderContract(t *testing.T) {
	signature := "func readHookInput(stdin io.Reader) hookInput"
	summary := "decodes one hook event"
	result := graph.SkeletonResult{File: "cmd/graft/hook_runtime.go", Entries: []graph.SkeletonEntry{
		{Name: "readHookInput", Kind: "function", Span: "L60-L70", Signature: &signature},
		{Name: "readHookInput", Kind: "function", Span: "L60-L70", Signature: &signature, Summary: &summary},
		{Name: "hookInput", Kind: "type", Span: "L58-L58"},
	}}
	var out bytes.Buffer
	if code := writeSkeletonHuman(&out, result); code != 0 {
		t.Fatalf("writeSkeletonHuman() = %d, want 0", code)
	}
	want := "graft skeleton — cmd/graft/hook_runtime.go\n" +
		"- L60-L70 " + signature + "\n" +
		"- L60-L70 " + signature + " — " + summary + "\n" +
		"- L58-L58  type hookInput\n"
	if got := out.String(); got != want {
		t.Errorf("writeSkeletonHuman() = %q, want %q", got, want)
	}
}

func TestAskWeakMatchNoticeContract(t *testing.T) {
	coverage := func(value float64) *float64 { return &value }
	weakHits := make([]graph.AskHit, 8)
	for i := range weakHits {
		weakHits[i] = graph.AskHit{Kind: "symbol", Title: "unrelated · function", Pointer: "pkg/a.go:L1-L2"}
	}
	weak := graph.AskResult{Mode: "lexical", Hits: weakHits, Coverage: coverage(0.2), CoverageStrong: coverage(0), Distinctive: "timeout"}
	strong := graph.AskResult{Mode: "lexical", Hits: weakHits[:2], Coverage: coverage(1), CoverageStrong: coverage(1), Distinctive: "timeout"}
	tests := []struct {
		name    string
		result  graph.AskResult
		mcp     bool
		want    []string
		notWant []string
	}{
		{name: "many weak hits on the CLI", result: weak, want: []string{"[graft] weak match", "`graft grep -i \"timeout\"`"}, notWant: []string{"graft_find_all"}},
		{name: "many weak hits over MCP", result: weak, mcp: true, want: []string{"[graft] weak match", "graft_find_all {\"pattern\":\"timeout\",\"ignore_case\":true}", "graft_file_api", "graft_trace_calls"}, notWant: []string{"graft grep", "graft skeleton", "graft callers"}},
		{name: "strong answer", result: strong, notWant: []string{"[graft]"}},
		{name: "few hits without coverage", result: graph.AskResult{Mode: "lexical", Hits: weakHits[:2]}, notWant: []string{"[graft]"}},
		{name: "structural answer", result: graph.AskResult{Mode: "structural", Hits: []graph.AskHit{{Title: "run", Pointer: "pkg/run.go:L1-L2", Relation: "calls"}}}, notWant: []string{"[graft]"}},
		{name: "no hits", result: graph.AskResult{Mode: "empty", Distinctive: "timeout"}, want: []string{"[graft] no hits", "`graft grep -i \"timeout\"`"}},
		{name: "no hits without a usable term", result: graph.AskResult{Mode: "empty"}, want: []string{"`graft grep -i \"<literal>\"`"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatAskText(tt.result, tt.mcp)
			if n := strings.Count(got, "[graft]"); n > 1 {
				t.Errorf("formatAskText(%s) = %q, want at most one notice, got %d", tt.name, got, n)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("formatAskText(%s, mcp=%t) = %q, want it to contain %q", tt.name, tt.mcp, got, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("formatAskText(%s, mcp=%t) = %q, want no %q", tt.name, tt.mcp, got, notWant)
				}
			}
		})
	}
	withTerm, _ := jsonjs.Marshal(weak, "  ")
	weak.Distinctive = ""
	withoutTerm, _ := jsonjs.Marshal(weak, "  ")
	if !bytes.Equal(withTerm, withoutTerm) {
		t.Errorf("jsonjs.Marshal(AskResult with Distinctive) = %s, want the JSON without it %s", withTerm, withoutTerm)
	}
}

func TestAskCompactSourceMatchesInflectedQuery(t *testing.T) {
	lines := []string{"func setup() {"}
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("\tstep%d()", i))
	}
	lines = append(lines, "\tapplyReadTimeout()", "}")
	got := compactAskSource(lines, 1, "timeouts", "pkg/setup.go:L1-L23")
	if !strings.Contains(got, "applyReadTimeout") {
		t.Errorf("compactAskSource(query %q) = %q, want the window around applyReadTimeout", "timeouts", got)
	}
}

func TestTierWithinScopes(t *testing.T) {
	scope := func(name string) *string { return &name }
	hits := []graph.AskHit{
		{Title: "a1", Scope: scope("api"), NameTerms: 1},
		{Title: "b1", Scope: scope("web"), NameTerms: 0},
		{Title: "a2", Scope: scope("api"), NameTerms: 2},
		{Title: "b2", Scope: scope("web"), NameTerms: 2},
		{Title: "a3", Scope: scope("api"), NameTerms: 2},
	}
	tierWithinScopes(hits)
	var got []string
	for _, hit := range hits {
		got = append(got, hit.Title)
	}
	if want := []string{"a2", "b2", "a3", "b1", "a1"}; !slices.Equal(got, want) {
		t.Errorf("tierWithinScopes() order = %v, want %v", got, want)
	}
}
