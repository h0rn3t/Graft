package main

import (
	"bytes"
	"fmt"
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
		{
			name:   "doc line follows the signature without source",
			result: graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Snippet: "func run(a int) error", Doc: "Runs the job once."}}},
			want:   []string{"1. run · function\n   pkg/run.go:L10-L40\n   func run(a int) error\n   Runs the job once.\n"},
		},
		{
			name:    "doc line precedes the excerpt that repeats the signature",
			result:  graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Snippet: "func run(a int)  error", Doc: "Runs the job once.", Code: excerpt}}},
			want:    []string{"   pkg/run.go:L10-L40\n   Runs the job once.\n\n```\nL10: func run(a int) error {"},
			notWant: []string{"   func run(a int)  error"},
		},
		{
			name:   "doc line over MCP",
			result: graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "run · function", Pointer: "pkg/run.go:L10-L40", Snippet: "func run(a int) error", Doc: "Runs the job once.", Code: full}}},
			mcp:    true,
			want:   []string{"   func run(a int) error\n   Runs the job once.\n\n```\nfunc tiny() {"},
		},
		{
			name:    "structural answers carry no doc line",
			result:  graph.AskResult{Mode: "structural", Hits: []graph.AskHit{{Kind: "symbol", Title: "run", Pointer: "pkg/run.go:L10-L40", Relation: "calls", Snippet: "func run(a int) error", Doc: "Runs the job once."}}},
			want:    []string{"- run  pkg/run.go:L10-L40  (calls) — func run(a int) error"},
			notWant: []string{"Runs the job once."},
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

func TestAskTextUndocumentedHitIsUnchanged(t *testing.T) {
	result := graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{{Kind: "symbol", Title: "tiny · function", Pointer: "pkg/tiny.go:L1-L3", Snippet: "func tiny(a int)", Code: "L2: \twork()"}}}
	want := "1. tiny · function\n   pkg/tiny.go:L1-L3\n   func tiny(a int)\n\n```\nL2: \twork()\n```\n"
	for _, mcp := range []bool{false, true} {
		if got := formatAskText(result, mcp); got != want {
			t.Errorf("formatAskText(undocumented, mcp=%t) = %q, want %q", mcp, got, want)
		}
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

func TestSetAskMethodsContract(t *testing.T) {
	node := func(path, name, kind, span, owner string) graph.NodeV1 {
		n := graph.NodeV1{ID: path + "#" + name, Name: name, Kind: graph.Kind(kind), Path: path, Span: span}
		if owner != "" {
			n.Owner = &owner
		}
		return n
	}
	nodes := []graph.NodeV1{
		node("pkg/cache.go", "cache.go", "file", "L1-L40", ""),
		node("pkg/cache.go", "queryCache", "struct", "L3-L6", ""),
		node("pkg/cache.go", "load", "method", "L8-L10", "queryCache"),
		node("pkg/cache_extra.go", "snapshot", "method", "L2-L20", "queryCache"),
		node("other/cache.go", "drop", "method", "L5-L7", "queryCache"), // another package's queryCache
		node("pkg/cache.go", "sameFile", "function", "L30-L35", ""),
		node("pkg/big.go", "Big", "struct", "L1-L2", ""),
		node("java/Worker.java", "Worker", "class", "L1-L1", ""),
		node("java/Worker.java", "run", "method", "L1-L1", "Worker"),
	}
	for i := range 12 {
		nodes = append(nodes, node("pkg/big.go", fmt.Sprintf("m%02d", i), "method", fmt.Sprintf("L%d-L%d", 10+i, 10+i), "Big"))
	}
	tests := []struct {
		name    string
		title   string
		pointer string
		want    string
	}{
		{name: "type with methods across its package", title: "queryCache · struct", pointer: "pkg/cache.go:L3-L6", want: "load L8-L10 · snapshot pkg/cache_extra.go:L2-L20"},
		{name: "long list keeps ten", title: "Big · struct", pointer: "pkg/big.go:L1-L2", want: "m00 L10-L10 · m01 L11-L11 · m02 L12-L12 · m03 L13-L13 · m04 L14-L14 · m05 L15-L15 · m06 L16-L16 · m07 L17-L17 · m08 L18-L18 · m09 L19-L19 · +2 more"},
		{name: "one-line class", title: "Worker · class", pointer: "java/Worker.java:L1-L1", want: "run L1-L1"},
		{name: "method on the class line", title: "run · method", pointer: "java/Worker.java:L1-L1", want: ""},
		{name: "function", title: "sameFile · function", pointer: "pkg/cache.go:L30-L35", want: ""},
		{name: "method", title: "load · method", pointer: "pkg/cache.go:L8-L10", want: ""},
		{name: "file", title: "cache.go · file", pointer: "pkg/cache.go", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := []graph.AskHit{{Kind: "symbol", Title: tt.title, Pointer: tt.pointer}}
			setAskMethods(graph.GraphV1{Nodes: nodes}, hits)
			if hits[0].Methods != tt.want {
				t.Errorf("setAskMethods(%q %q).Methods = %q, want %q", tt.title, tt.pointer, hits[0].Methods, tt.want)
			}
		})
	}
	hit := graph.AskHit{Kind: "symbol", Title: "queryCache · struct", Pointer: "pkg/cache.go:L3-L6", Methods: "load L8-L10"}
	text := formatAskText(graph.AskResult{Mode: "lexical", Hits: []graph.AskHit{hit}}, true)
	if !strings.Contains(text, "\n   methods: load L8-L10\n") {
		t.Errorf("formatAskText(type hit) = %q, want a methods line under the pointer", text)
	}
}
