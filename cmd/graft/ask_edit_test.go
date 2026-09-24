package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func TestAskEditDirectContext(t *testing.T) {
	root := t.TempDir()
	buildAskEditFixture(t, root)
	plain := runAskEditJSON(t, root, "processInvoice", "--limit", "1")
	if len(plain.Hits) != 1 || !strings.Contains(plain.Hits[0].Title, "processInvoice") || plain.Hits[0].Code != "" {
		t.Fatalf("ask processInvoice = %+v, want one primary hit without source", plain)
	}
	lookup := runAskEditJSON(t, root, "processInvoice", "--intent", "lookup", "--limit", "1")
	if !reflect.DeepEqual(plain, lookup) {
		t.Errorf("ask --intent lookup = %+v, want default lookup %+v", lookup, plain)
	}
	for _, limit := range []string{"1", "10"} {
		t.Run("limit="+limit, func(t *testing.T) {
			result := runAskEditJSON(t, root, "processInvoice", "--intent", "edit", "--limit", limit, "--budget", "64000")
			if len(result.Hits) < 4 || result.Hits[0].Pointer != plain.Hits[0].Pointer {
				t.Fatalf("ask --intent edit = %+v, want primary followed by direct context", result)
			}
			want := map[string]string{"TestProcessInvoice": "test", "serveInvoice": "caller", "normalizeTotal": "dependency"}
			pointers := make(map[string]bool)
			for _, hit := range result.Hits {
				if pointers[hit.Pointer] {
					t.Errorf("ask --intent edit repeats pointer %q", hit.Pointer)
				}
				pointers[hit.Pointer] = true
				if kind, ok := want[hit.Title]; ok {
					if hit.Kind != kind || hit.Code == "" {
						t.Errorf("ask --intent edit hit %q = %+v, want %s with source", hit.Title, hit, kind)
					}
					delete(want, hit.Title)
				}
			}
			if len(want) != 0 {
				t.Errorf("ask --intent edit missing direct context %v; hits = %+v", want, result.Hits)
			}
		})
	}
}

func TestAskEditIndexedTypeReference(t *testing.T) {
	root := t.TempDir()
	buildAskEditFixture(t, root)
	outDir := filepath.Join(root, "graft")
	wiring, err := graph.Read(graph.WiringPath(outDir))
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string)
	for _, node := range wiring.Nodes {
		ids[node.Name] = node.ID
	}
	// Go composite literals currently have no type-reference edge; exercise an indexed reference explicitly.
	wiring.Edges = append(wiring.Edges, graph.EdgeV1{Source: ids["processInvoice"], Target: ids["Invoice"], Relation: "references"})
	if _, err := graph.Write(*wiring, outDir); err != nil {
		t.Fatal(err)
	}
	result := runAskEditJSON(t, root, "processInvoice", "--intent", "edit", "--limit", "1", "--budget", "64000")
	for _, hit := range result.Hits {
		if hit.Title == "Invoice" && hit.Kind == "dependency" && hit.Relation == "references" && strings.Contains(hit.Code, "type Invoice struct") {
			return
		}
	}
	t.Errorf("ask --intent edit with indexed type reference = %+v, want Invoice definition", result)
}

func TestAskEditFocusedSourceAndFullExpansion(t *testing.T) {
	root := t.TempDir()
	buildAskEditFixture(t, root)
	compact := runAskEditJSON(t, root, "processInvoice focusneedle", "--source", "--limit", "1")
	full := runAskEditJSON(t, root, "processInvoice focusneedle", "--full", "--limit", "1", "--budget", "64000")
	if len(compact.Hits) != 1 || len(full.Hits) != 1 {
		t.Fatalf("ask source hits = (%d, %d), want (1, 1)", len(compact.Hits), len(full.Hits))
	}
	if code := compact.Hits[0].Code; !strings.Contains(code, "func processInvoice") || !strings.Contains(code, "focusneedle") || !strings.Contains(code, "excerpt") || strings.Contains(code, "expansiontail") {
		t.Errorf("ask --source code = %q, want signature and query-focused excerpt without tail", code)
	}
	if code := full.Hits[0].Code; !strings.Contains(code, "expansiontail") || strings.Contains(code, "excerpt") || len(code) <= len(compact.Hits[0].Code) {
		t.Errorf("ask --full code = %q, want entire definition", code)
	}
}

func TestAskEditBudgetCoversWholeResponse(t *testing.T) {
	root := t.TempDir()
	buildAskEditFixture(t, root)
	for _, format := range []string{"human", "json"} {
		t.Run(format, func(t *testing.T) {
			args := []string{"ask", "processInvoice", root, "--no-refresh", "--intent", "edit", "--full", "--budget", "256"}
			if format == "json" {
				args = append(args, "--json")
			}
			var stdout, stderr bytes.Buffer
			if status := run(args, &stdout, &stderr); status != 0 {
				t.Fatalf("run(%v) = %d, stderr %q", args, status, stderr.String())
			}
			if tokens := savings.Tokens(savings.Length(stdout.String())); tokens > 256 {
				t.Errorf("run(%v) = %d estimated tokens, want <= 256; output %q", args, tokens, stdout.String())
			}
			if !strings.Contains(stdout.String(), "processInvoice") || !strings.Contains(stdout.String(), "budget") {
				t.Errorf("run(%v) = %q, want primary hit and budget coverage note", args, stdout.String())
			}
		})
	}
	result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_find_code", map[string]any{
		"query": "processInvoice", "intent": "edit", "full": true, "budget": float64(256),
	})
	if result.isError || savings.Tokens(savings.Length(result.text)) > 256 || !strings.Contains(result.text, "budget") {
		t.Errorf("mcpCall(budget=256) = %+v, want complete response within budget with coverage note", result)
	}
}

func TestAskEditRejectsInvalidOptions(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		flag, value string
	}{
		{flag: "--budget", value: "127"},
		{flag: "--budget", value: "64001"},
		{flag: "--budget", value: "256.5"},
		{flag: "--budget", value: "NaN"},
		{flag: "--intent", value: "rewrite"},
	} {
		t.Run(tc.flag+"="+tc.value, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := []string{"ask", "processInvoice", root, tc.flag, tc.value}
			if status := run(args, &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), strings.TrimPrefix(tc.flag, "--")) {
				t.Errorf("run(%v) = %d, stderr %q, want option error", args, status, stderr.String())
			}
		})
	}
	for _, tc := range []struct {
		name  string
		key   string
		value any
	}{
		{name: "small budget", key: "budget", value: float64(127)},
		{name: "fractional budget", key: "budget", value: 256.5},
		{name: "string budget", key: "budget", value: "256"},
		{name: "unknown intent", key: "intent", value: "rewrite"},
		{name: "non-string intent", key: "intent", value: true},
		{name: "non-array seen", key: "seen", value: "abc"},
		{name: "non-string seen entry", key: "seen", value: []any{true}},
		{name: "short seen entry", key: "seen", value: []any{"abc"}},
		{name: "non-hex seen entry", key: "seen", value: []any{strings.Repeat("g", 24)}},
		{name: "oversized seen", key: "seen", value: make([]any, 257)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"query": "processInvoice", tc.key: tc.value}
			result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_find_code", args)
			if !result.isError || result.text == "" || strings.Contains(result.text, "no graph found") {
				t.Errorf("mcpCall(%v) = %+v, want %s option error before graph loading", args, result, tc.key)
			}
		})
	}
}

func TestMCPAskEditBudgetIncludesRefreshNote(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "0")
	for _, budget := range []int{128, 160, 192, 256} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			root := t.TempDir()
			buildAskEditFixture(t, root)
			sourcePath := filepath.Join(root, "src", "invoice.go")
			source, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sourcePath, append(source, []byte("\n// refresh-trigger\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
			result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_find_code", map[string]any{
				"query": "processInvoice", "intent": "edit", "full": true, "budget": float64(budget),
			})
			if result.isError {
				t.Fatalf("mcpCall(budget=%d, changed source) = %+v, want success", budget, result)
			}
			if !strings.Contains(result.text, "refreshed") {
				t.Fatalf("mcpCall(changed source) = %q, want refresh coverage", result.text)
			}
			if tokens := savings.Tokens(savings.Length(result.text)); tokens > budget {
				t.Errorf("mcpCall(budget=%d, changed source) = %d estimated tokens, want <= %d; output %q", budget, tokens, budget, result.text)
			}
		})
	}
}

func TestAskEditBudgetRejectsOversizedQueryMetadata(t *testing.T) {
	root := t.TempDir()
	for _, format := range []string{"human", "json"} {
		t.Run(format, func(t *testing.T) {
			args := []string{"ask", strings.Repeat("unmatched-query ", 80), root, "--no-refresh", "--budget", "128"}
			if format == "json" {
				args = append(args, "--json")
			}
			var stdout, stderr bytes.Buffer
			if status := run(args, &stdout, &stderr); status == 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "budget") {
				t.Errorf("ask oversized query without graph = (status %d, stdout %q, stderr %q), want budget error", status, stdout.String(), stderr.String())
			}
		})
	}
}

func TestMCPAskEditReferenceReplay(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root := t.TempDir()
	buildAskEditFixture(t, root)
	var cache queryCache
	call := func(args map[string]any) string {
		t.Helper()
		result := mcpCallWithCache(t.Context(), root, filepath.Join(root, "graft"), "", "graft_find_code", args, &cache)
		if result.isError {
			t.Fatalf("mcpCall(%v) = %+v, want success", args, result)
		}
		return result.text
	}
	args := map[string]any{"query": "processInvoice focusneedle", "limit": float64(1)}
	plain := call(args)
	if strings.Contains(plain, "ref:") || !strings.Contains(plain, "focusneedle :=") {
		t.Errorf("mcpCall without seen = %q, want source without references", plain)
	}
	args["seen"] = []any{}
	initial := call(args)
	refPattern := regexp.MustCompile(`(?m)^\s+ref: ([a-f0-9]{24})$`)
	match := refPattern.FindStringSubmatch(initial)
	if len(match) != 2 || !strings.Contains(initial, "focusneedle :=") {
		t.Fatalf("mcpCall(seen=[]) = %q, want reference and source", initial)
	}
	args["seen"] = []any{match[1]}
	replay := call(args)
	if !strings.Contains(replay, "unchanged; source already supplied") || strings.Contains(replay, "focusneedle :=") || !strings.Contains(replay, match[1]) {
		t.Errorf("mcpCall(seen=ref) = %q, want pointer reference without repeated source", replay)
	}
	args["full"] = true
	full := call(args)
	if strings.Contains(full, "unchanged;") || !strings.Contains(full, "expansiontail") {
		t.Errorf("mcpCall(full=true, seen=compact ref) = %q, want full source", full)
	}
	delete(args, "full")
	args["seen"] = []any{}
	reset := call(args)
	if strings.Contains(reset, "unchanged;") || !strings.Contains(reset, "focusneedle :=") || !strings.Contains(reset, match[1]) {
		t.Errorf("mcpCall(seen reset) = %q, want original reference and source", reset)
	}
	sourcePath := filepath.Join(root, "src", "invoice.go")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, bytes.ReplaceAll(source, []byte("expansiontail"), []byte("changedtail")), 0o644); err != nil {
		t.Fatal(err)
	}
	args["seen"] = []any{match[1]}
	changed := call(args)
	if strings.Contains(changed, "unchanged;") || strings.Contains(changed, match[1]) || !strings.Contains(changed, "focusneedle :=") {
		t.Errorf("mcpCall(after change outside excerpt) = %q, want new reference and source", changed)
	}
	args["seen"] = []any{}
	args["full"] = true
	args["budget"] = float64(128)
	truncated := call(args)
	if strings.Contains(truncated, "ref:") || !strings.Contains(truncated, "budget") || savings.Tokens(savings.Length(truncated)) > 128 {
		t.Errorf("mcpCall(full source, budget=128) = %q, want bounded partial source without complete-content reference", truncated)
	}
}

func TestAskEditWorkspaceScope(t *testing.T) {
	root := t.TempDir()
	for _, child := range []string{"billing", "shipping"} {
		buildAskEditFixture(t, filepath.Join(root, child))
	}
	writeFixtureFile(t, root, "graft/workspace.json", `{"version":1,"children":["billing","shipping"]}`)
	result := runAskEditJSON(t, root, "processInvoice", "--intent", "edit", "--in", "billing/src", "--limit", "1", "--budget", "64000")
	if len(result.Hits) < 4 {
		t.Fatalf("workspace ask --intent edit --in billing/src = %+v, want direct context", result)
	}
	for _, hit := range result.Hits {
		if !strings.HasPrefix(hit.Pointer, "billing/src/") || hit.Scope == nil || *hit.Scope != "billing" {
			t.Errorf("workspace edit hit = %+v, want billing/src pointer and billing scope", hit)
		}
	}
}

func buildAskEditFixture(t *testing.T, root string) {
	t.Helper()
	source := "package invoice\n\ntype Invoice struct { Total int }\n\nfunc processInvoice() int {\n\tinvoice := Invoice{Total: 1}\n" +
		strings.Repeat("\tinvoice.Total += 1\n", 14) +
		"\tfocusneedle := normalizeTotal(invoice.Total)\n" +
		strings.Repeat("\tinvoice.Total += 2\n", 14) +
		"\t// expansiontail\n\treturn focusneedle + invoice.Total\n}\n\n" +
		"func normalizeTotal(total int) int { return total * 2 }\n\n" +
		"func serveInvoice() int { return processInvoice() }\n"
	writeFixtureFile(t, root, "src/invoice.go", source)
	writeFixtureFile(t, root, "src/invoice_test.go", "package invoice\n\nimport \"testing\"\n\nfunc TestProcessInvoice(t *testing.T) {\n\tif processInvoice() == 0 { t.Fatal(\"empty invoice\") }\n}\n")
	outDir := filepath.Join(root, "graft")
	built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: outDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Write(built.Graph, outDir); err != nil {
		t.Fatal(err)
	}
	if err := graph.WriteAskIndex(outDir, built.Graph); err != nil {
		t.Fatal(err)
	}
	if err := graph.WriteFingerprint(outDir, graph.ExtractorID, built.Fingerprints, nil); err != nil {
		t.Fatal(err)
	}
}

func runAskEditJSON(t *testing.T, root, query string, flags ...string) graph.AskResult {
	t.Helper()
	args := append([]string{"ask", query, root, "--json", "--no-refresh"}, flags...)
	var stdout, stderr bytes.Buffer
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) = %d, stderr %q", args, status, stderr.String())
	}
	var result graph.AskResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("run(%v) output %q: %v", args, stdout.String(), err)
	}
	return result
}
