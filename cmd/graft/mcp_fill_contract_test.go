package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
)

// These contracts spend what a budget leaves on source an agent would
// otherwise fetch in another round: complete definitions, a small file the
// hits crowd into, the bodies around a narrow search, and the call flow
// among the symbols an answer names.

func buildFillFixture(t *testing.T, files map[string]string) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	for path, text := range files {
		writeFixtureFile(t, root, path, text)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root, "--no-lsp"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	return root, filepath.Join(root, "graft")
}

func fillFixtureFiles() map[string]string {
	var tally, ledger strings.Builder
	tally.WriteString("package billing\n\n// Tally adds the invoice totals line by line.\nfunc Tally(lines []int) int {\n\ttotal := 0\n")
	for i := range 40 {
		fmt.Fprintf(&tally, "\ttotal += lines[%d]\n", i)
	}
	tally.WriteString("\treturn total\n}\n\nfunc tallyPadding(n int) int {\n")
	tally.WriteString(strings.Repeat("\tn++\n", 60) + "\treturn n\n}\n")
	var codes strings.Builder
	codes.WriteString("package billing\n\n")
	for _, region := range []string{"alpha", "beta"} {
		fmt.Fprintf(&codes, "// Code%[1]s maps the invoice region %[1]s.\nfunc Code%[1]s(n int) int {\n%[2]s\treturn n // region %[1]s end\n}\n\n", region, strings.Repeat("\tn += 1\n", 10))
	}
	codes.WriteString("func codesPadding(n int) int {\n" + strings.Repeat("\tn++\n", 100) + "\treturn n\n}\n")
	ledger.WriteString("package billing\n\n// Ledger replays every invoice entry.\nfunc Ledger(entries []int) int {\n\tsum := 0\n")
	for i := range 600 {
		fmt.Fprintf(&ledger, "\tsum += entries[%d] // entry %d\n", i, i)
	}
	ledger.WriteString("\treturn sum\n}\n")
	return map[string]string{
		"go.mod":            "module example.com/fx\n",
		"billing/tally.go":  tally.String(),
		"billing/ledger.go": ledger.String(),
		"billing/codes.go":  codes.String(),
		"billing/rate.go": "package billing\n\nimport \"strings\"\n\n// Rate converts a currency code to its rate.\nfunc Rate(code string) float64 {\n" +
			"\tswitch strings.ToUpper(code) {\n\tcase \"EUR\":\n\t\treturn 1.1\n\tcase \"GBP\":\n\t\treturn 1.3\n\tcase \"JPY\":\n\t\treturn 0.007\n" +
			"\tcase \"CHF\":\n\t\treturn 1.2\n\t}\n\treturn 1\n}\n",
		"flow/flow.go": "package flow\n\n" +
			"// Start runs the pipeline.\nfunc Start(input string) string {\n\tvalue := prepare(input)\n\treturn value\n}\n\n" +
			"// prepare readies the pipeline input.\nfunc prepare(input string) string {\n\tif input == \"\" {\n\t\treturn \"\"\n\t}\n" +
			"\tvalue := normalize(input)\n\tvalue = value + \"!\"\n\tvalue = value + \"?\"\n\tvalue = value + \".\"\n\tvalue = value + \",\"\n\treturn value\n}\n\n" +
			"func normalize(input string) string { return finish(input) }\n\n" +
			"// finish closes the pipeline.\nfunc finish(input string) string { return input }\n",
	}
}

func TestMCPFindCodeFillsBudgetContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root, dir := buildFillFixture(t, fillFixtureFiles())
	flow := regexp.MustCompile(`call flow: Start \(flow/flow\.go:L4\) → prepare \(flow/flow\.go:L10\) → \[?normalize \(flow/flow\.go:L22\)\]? → finish \(flow/flow\.go:L25\)`)
	for _, tc := range []struct {
		name   string
		args   map[string]any
		want   []string
		reject []string
		flow   bool
	}{
		{
			name:   "an excerpt that fits is completed",
			args:   map[string]any{"query": "invoice totals line by line", "limit": float64(1)},
			want:   []string{"total += lines[39]", "return total"},
			reject: []string{"… +", "whole file billing/tally.go"},
		},
		{
			name: "a hit covering most of a small file brings the whole file",
			args: map[string]any{"query": "currency", "limit": float64(1)},
			want: []string{"L3: import \"strings\"", "whole file billing/rate.go"},
		},
		{
			name:   "hits covering little of their file come as definitions",
			args:   map[string]any{"query": "maps the invoice region alpha beta"},
			want:   []string{"return n // region alpha end", "return n // region beta end"},
			reject: []string{"whole file billing/codes.go"},
		},
		{
			name: "a definition larger than the budget stays an excerpt",
			args: map[string]any{"query": "replays every invoice entry", "limit": float64(1)},
			want: []string{"Ledger", "lines (graft_read_symbol Ledger)"},
		},
		{
			name:   "a small file holding several partial hits is shown once, whole",
			args:   map[string]any{"query": "pipeline input"},
			want:   []string{"L1: package flow", "whole file flow/flow.go"},
			reject: []string{"… +"},
		},
		{
			name: "the call flow among the hits leads the answer",
			args: map[string]any{"query": "pipeline input"},
			flow: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpCall(t.Context(), root, dir, "", "graft_find_code", tc.args)
			if got.isError || !containsAll(got.text, tc.want) || containsAny(got.text, tc.reject) || (tc.flow && !flow.MatchString(got.text)) {
				t.Errorf("mcpCall(graft_find_code, %v) = (%q, isError %t), want text containing %q, none of %q, call flow %t", tc.args, got.text, got.isError, tc.want, tc.reject, tc.flow)
			}
			if tokens := savings.Tokens(savings.Length(got.text)); tokens > 2000 {
				t.Errorf("mcpCall(graft_find_code, %v) = %d estimated tokens, want at most the default budget 2000", tc.args, tokens)
			}
			if n := strings.Count(got.text, "L1: package flow"); n > 1 {
				t.Errorf("mcpCall(graft_find_code, %v) shows flow/flow.go %d times, want at most once", tc.args, n)
			}
		})
	}
}

func TestMCPFindCodeFillRespectsSmallBudgetContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root, dir := buildFillFixture(t, fillFixtureFiles())
	args := map[string]any{"query": "invoice totals line by line", "budget": float64(150)}
	got := mcpCall(t.Context(), root, dir, "", "graft_find_code", args)
	if got.isError || strings.Contains(got.text, "total += lines[39]") {
		t.Errorf("mcpCall(graft_find_code, %v) = (%q, isError %t), want an answer without the complete Tally body", args, got.text, got.isError)
	}
	if tokens := savings.Tokens(savings.Length(got.text)); tokens > 150 {
		t.Errorf("mcpCall(graft_find_code, %v) = %d estimated tokens, want at most 150", args, tokens)
	}
}

func TestMCPFindCodeImplementationsContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	files := map[string]string{
		"go.mod":          "module example.com/fx\n",
		"store/getter.go": "package store\n\n// Getter reads a stored value.\ntype Getter interface {\n\tGet(key string) string\n}\n",
	}
	for _, name := range []string{"Mem", "Disk", "Net"} {
		files["store/"+strings.ToLower(name)+".go"] = fmt.Sprintf("package store\n\ntype %[1]s struct{ prefix string }\n\n"+
			"// Get reads the stored value from %[1]s.\nfunc (s *%[1]s) Get(key string) string {\n"+
			"\tif key == \"\" {\n\t\treturn \"\"\n\t}\n\tvalue := s.prefix + key\n\tvalue = value + \"-%[1]s\"\n"+
			"\tvalue = value + \"!\"\n\tvalue = value + \"?\"\n\tvalue = value + \".\"\n\treturn value\n}\n", name)
	}
	root, dir := buildFillFixture(t, files)
	for _, tc := range []struct {
		name      string
		query     string
		wantShort bool
	}{
		{name: "unnamed interchangeable implementations show signatures", query: "reads the stored value", wantShort: true},
		{name: "a named method keeps its source", query: "get reads the stored value", wantShort: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"query": tc.query}
			got := mcpCall(t.Context(), root, dir, "", "graft_find_code", args)
			short := strings.Count(got.text, "signature only: one of 3 implementations of Getter.Get")
			if got.isError || (short >= 2) != tc.wantShort || (!tc.wantShort && short != 0) {
				t.Errorf("mcpCall(graft_find_code, %v) = (%q, isError %t) with %d signature-only hits, want signature-only hits %t", args, got.text, got.isError, short, tc.wantShort)
			}
		})
	}
}

func TestMCPFindAllInlinesNarrowBodiesContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	files := fillFixtureFiles()
	// Settle calls Reconcile, which ranks it first; together their bodies
	// pass the budget, each alone fits.
	var reconcile, settle, fees strings.Builder
	reconcile.WriteString("package billing\n\nfunc Reconcile(n int) int {\n\tn += auditMark // reconcile\n")
	for i := range 150 {
		fmt.Fprintf(&reconcile, "\tn += %d // reconcile row %d of the ledger table\n", i, i)
	}
	reconcile.WriteString("\treturn n // reconcile end\n}\n")
	settle.WriteString("package billing\n\nconst auditMark = 1\n\nfunc Settle(n int) int {\n\tn = Reconcile(n) + auditMark // settle\n")
	for i := range 60 {
		fmt.Fprintf(&settle, "\tn -= %d // settle row %d of the ledger table\n", i, i)
	}
	settle.WriteString("\treturn n // settle end\n}\n")
	// Five short definitions use feeRate in a file too long to show whole.
	fees.WriteString("package fees\n\nconst feeRate = 2\n")
	for _, name := range []string{"Card", "Wire", "Cash", "Check", "Crypto"} {
		fmt.Fprintf(&fees, "\nfunc %[1]s(n int) int {\n\tn *= feeRate\n\treturn n // %[1]s fee end\n}\n", name)
	}
	fees.WriteString("\nfunc feesPadding(n int) int {\n" + strings.Repeat("\tn++\n", 240) + "\treturn n\n}\n")
	files["billing/reconcile.go"] = reconcile.String()
	files["billing/settle.go"] = settle.String()
	files["fees/fees.go"] = fees.String()
	root, dir := buildFillFixture(t, files)
	for _, tc := range []struct {
		name   string
		args   map[string]any
		want   []string
		reject []string
	}{
		{
			name: "one enclosing definition comes whole, matches marked",
			args: map[string]any{"pattern": `lines\[39\]`},
			want: []string{"▸ L45: \ttotal += lines[39]", "  L46: \treturn total"},
		},
		{
			name: "hits crowded into one small file show that file once",
			args: map[string]any{"pattern": "input"},
			want: []string{"Start · function · flow/flow.go:L4-L7 · 0 in-edges · L4, L5", "L1: package flow", "whole file flow/flow.go", "▸ L4: func Start(input string) string {"},
		},
		{
			name: "five definitions in a long file come whole",
			args: map[string]any{"pattern": `\*= feeRate`},
			want: []string{"return n // Card fee end", "return n // Wire fee end", "return n // Cash fee end", "return n // Check fee end", "return n // Crypto fee end"},
		},
		{
			name:   "of two definitions that fit only apart, the most called comes whole and the other shows a window",
			args:   map[string]any{"pattern": `auditMark //`},
			want:   []string{"return n // reconcile end", "▸ L6: \tn = Reconcile(n) + auditMark // settle", "  ⋮ L"},
			reject: []string{"return n // settle end"},
		},
		{
			name:   "a definition larger than the budget shows its first line and a window around its hit",
			args:   map[string]any{"pattern": "entry 599"},
			want:   []string{"  L4: func Ledger(entries []int) int {", "  ⋮ L5-L", "▸ L605: \tsum += entries[599] // entry 599", "  L607: }"},
			reject: []string{"L6: \tsum += entries[0] "},
		},
		{
			name:   "a wide search keeps hit lines only",
			args:   map[string]any{"pattern": "func"},
			want:   []string{"L4: func Tally(lines []int) int {"},
			reject: []string{"▸", "return total"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpCall(t.Context(), root, dir, "", "graft_find_all", tc.args)
			if got.isError || !containsAll(got.text, tc.want) || containsAny(got.text, tc.reject) {
				t.Errorf("mcpCall(graft_find_all, %v) = (%q, isError %t), want text containing %q and none of %q", tc.args, got.text, got.isError, tc.want, tc.reject)
			}
			if len(got.text) > mcpGrepSourceBudget+500 {
				t.Errorf("mcpCall(graft_find_all, %v) = %d bytes, want at most about %d", tc.args, len(got.text), mcpGrepSourceBudget)
			}
		})
	}
}

func TestMCPReadSymbolAlsoCallFlowContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root, dir := buildFillFixture(t, fillFixtureFiles())
	flow := regexp.MustCompile(`call flow: Start \(flow/flow\.go:L4\) → prepare \(flow/flow\.go:L10\) → \[normalize \(flow/flow\.go:L22\)\] → finish \(flow/flow\.go:L25\)`)
	for _, tc := range []struct {
		name     string
		args     map[string]any
		wantFlow bool
	}{
		{name: "connected symbols", args: map[string]any{"symbol": "finish", "also": []any{"Start", "prepare"}}, wantFlow: true},
		{name: "unconnected symbols", args: map[string]any{"symbol": "Tally", "also": []any{"finish"}}, wantFlow: false},
		{name: "one symbol", args: map[string]any{"symbol": "Start"}, wantFlow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpCall(t.Context(), root, dir, "", "graft_read_symbol", tc.args)
			if got.isError || flow.MatchString(got.text) != tc.wantFlow || (!tc.wantFlow && strings.Contains(got.text, "call flow")) {
				t.Errorf("mcpCall(graft_read_symbol, %v) = (%q, isError %t), want call flow %t", tc.args, got.text, got.isError, tc.wantFlow)
			}
		})
	}
}

func containsAny(text string, parts []string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

func TestFillAskBudgetChainContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	body := func(name, call string) string {
		var text strings.Builder
		fmt.Fprintf(&text, "package p\n\nfunc %s() int {\n\tn := 0\n", name)
		for i := range 60 {
			fmt.Fprintf(&text, "\tn += %d // %s step\n", i, name)
		}
		fmt.Fprintf(&text, "\treturn n + %s // %s end\n}\n\nfunc %sPadding(n int) int {\n%s\treturn n\n}\n", call, name, strings.ToLower(name), strings.Repeat("\tn++\n", 200))
		return text.String()
	}
	root, dir := buildFillFixture(t, map[string]string{
		"go.mod":     "module example.com/fx\n",
		"p/top.go":   "package p\n\nfunc Top() int { return Mid() }\n",
		"p/mid.go":   body("Mid", "End()"),
		"p/end.go":   "package p\n\nfunc End() int { return 1 }\n",
		"p/other.go": body("Other", "1"),
	})
	var cache queryCache
	wiring, err := cache.loadGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	var hits []graph.AskHit
	for _, name := range []string{"Top", "Other", "Mid", "End"} {
		at := slices.IndexFunc(wiring.Nodes, func(node graph.NodeV1) bool { return node.Name == name })
		if at < 0 {
			t.Fatalf("graph has no node %q", name)
		}
		node := wiring.Nodes[at]
		hits = append(hits, graph.AskHit{Kind: string(node.Kind), Title: node.Name + " · " + string(node.Kind), Pointer: node.Path + ":" + node.Span})
	}
	inlineAskHits(root, nil, hits, false, "step")
	nodes := askHitNodes(*wiring, hits)
	flow, chain := callFlow(*wiring, nodes)
	result := graph.AskResult{Mode: "lexical", Hits: hits, Flow: flow}
	fillAskBudget(root, *wiring, &result, nodes, chain, 1000, callersOptions{mcp: true})
	// Both bodies exceed a fifth of the budget; only the one on the chain is completed.
	if code := result.Hits[2].Code; !strings.Contains(code, "Mid end") {
		t.Errorf("fillAskBudget(chain Top → Mid → End) Mid code = %q, want its complete body", code)
	}
	if code := result.Hits[1].Code; strings.Contains(code, "Other end") {
		t.Errorf("fillAskBudget(chain Top → Mid → End) Other code = %q, want an excerpt", code)
	}
}

func TestCallFlowContract(t *testing.T) {
	node := func(id, name, owner string) graph.NodeV1 {
		path, _, _ := strings.Cut(id, "#")
		n := graph.NodeV1{ID: id, Name: name, Kind: "function", Path: path, Span: "L1-L2"}
		if owner != "" {
			n.Kind, n.Owner = "method", &owner
		}
		return n
	}
	a, iface, impl, helper, b := node("a.go#A", "A", ""), node("i.go#I.M", "M", "I"), node("t.go#T.M", "M", "T"), node("h.go#H", "H", ""), node("b.go#B", "B", "")
	wiring := graph.GraphV1{Nodes: []graph.NodeV1{a, iface, impl, helper, b}}
	for _, tc := range []struct {
		name  string
		edges []graph.EdgeV1
		named []graph.NodeV1
		want  string
	}{
		{
			name: "an interface call dispatches to the named implementation",
			edges: []graph.EdgeV1{
				{Source: a.ID, Target: iface.ID, Relation: "calls", Confidence: "extracted"},
				{Source: impl.ID, Target: iface.ID, Relation: "implements", Confidence: "inferred"},
				{Source: impl.ID, Target: helper.ID, Relation: "calls", Confidence: "extracted"},
			},
			named: []graph.NodeV1{a, impl, helper},
			want:  "call flow: A (a.go:L1) → [I.M (i.go:L1)] → (dispatch) T.M (t.go:L1) → H (h.go:L1)",
		},
		{
			name: "an inferred call is marked",
			edges: []graph.EdgeV1{
				{Source: a.ID, Target: b.ID, Relation: "calls", Confidence: "inferred"},
				{Source: b.ID, Target: helper.ID, Relation: "calls", Confidence: "extracted"},
			},
			named: []graph.NodeV1{a, b, helper},
			want:  "call flow: A (a.go:L1) → (inferred) B (b.go:L1) → H (h.go:L1)",
		},
		{
			name:  "a two-node chain is left to the source",
			edges: []graph.EdgeV1{{Source: impl.ID, Target: helper.ID, Relation: "calls", Confidence: "extracted"}},
			named: []graph.NodeV1{a, impl, helper},
			want:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wiring.Edges = tc.edges
			if got, _ := callFlow(wiring, tc.named); got != tc.want {
				t.Errorf("callFlow(%v) = %q, want %q", tc.edges, got, tc.want)
			}
		})
	}
}
