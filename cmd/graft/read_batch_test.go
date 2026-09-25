package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func TestReadBatchContract(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	var out, diagnostic bytes.Buffer
	status := run([]string{"read", "alpha", root, "--also", "src/two.ts::shared", "--also", "missing", "--also", "shared", "--json"}, &out, &diagnostic)
	if status != 0 {
		t.Fatalf("read(batch) = %d, %s", status, diagnostic.String())
	}
	var got struct {
		Results []struct {
			Selector, Status, Error string
			Result                  *readResult
		}
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 4 {
		t.Fatalf("read(batch) = %s, want four item statuses", out.String())
	}
	for _, item := range got.Results {
		switch item.Selector {
		case "alpha", "src/two.ts::shared":
			if item.Status != "ok" || item.Result == nil || !strings.Contains(item.Result.Code, "return ") {
				t.Errorf("read(%s) = %+v, want complete source", item.Selector, item)
			}
		default:
			if item.Status != "error" || item.Error == "" {
				t.Errorf("read(%s) = %+v, want explicit error", item.Selector, item)
			}
		}
	}
	if text, failed := readBatchText(t, root, 0, "alpha", "src/two.ts::shared"); failed || !strings.Contains(text, "return 1") || !strings.Contains(text, "return 2") {
		t.Errorf("read(batch) = %q, want both definitions", text)
	}
}

func TestReadBatchInputs(t *testing.T) {
	for _, args := range []map[string]any{
		{"symbols": []any{"alpha", "shared"}}, {"symbol": " "}, {},
	} {
		result := mcpCall(t.Context(), t.TempDir(), "missing", "", "graft_read_symbol", args)
		if !result.isError || !strings.Contains(result.text, "requires a symbol") {
			t.Errorf("mcpCall(read_symbol %v) = %+v, want argument validation before graph access", args, result)
		}
	}
	if result := mcpCall(t.Context(), t.TempDir(), "missing", "", "graft_find_code", map[string]any{"symbol": "alpha"}); !result.isError || !strings.Contains(result.text, "requires a query") {
		t.Errorf("mcpCall(find_code symbol) = %+v, want a query requirement: exact reads have their own tool", result)
	}
	var out, diagnostic bytes.Buffer
	args := []string{"read", "a", t.TempDir()}
	for _, selector := range []string{"b", "c", "d", "e", "f", "g", "h", "i"} {
		args = append(args, "--also", selector)
	}
	if status := run(args, &out, &diagnostic); status == 0 || !strings.Contains(diagnostic.String(), "between 1 and 8") {
		t.Errorf("run(read with nine selectors) = (%d, %q), want selector validation", status, diagnostic.String())
	}
}

// readBatchText runs a CLI batch read of selectors and returns its combined
// output and whether it failed; a zero budget keeps the default.
func readBatchText(t *testing.T, root string, budget int, selectors ...string) (string, bool) {
	t.Helper()
	args := []string{"read", selectors[0], root}
	for _, selector := range selectors[1:] {
		args = append(args, "--also", selector)
	}
	if budget > 0 {
		args = append(args, "--budget", strconv.Itoa(budget))
	}
	var out, diagnostic bytes.Buffer
	status := run(args, &out, &diagnostic)
	return diagnostic.String() + out.String(), status != 0
}

func TestReadBatchContainedSource(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	path := filepath.Join(root, "src", "one.ts")
	if err := os.WriteFile(path, []byte("export class Service {\n  run() { return 42; }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, selectors := range [][]string{{"run", "Service"}, {"Service", "run", "src/one.ts::run"}} {
		text, failed := readBatchText(t, root, 0, selectors...)
		if failed || strings.Count(text, "return 42") != 1 || !strings.Contains(text, "covered") {
			t.Errorf("read(batch %v) = %q, want source once with explicit coverage", selectors, text)
		}
	}
}

func TestReadBatchCoverageBudgetBoundary(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "src", "one.ts"), []byte("export class Service {\n  run() { return 42; }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var refreshed, diagnostic bytes.Buffer
	if status := run([]string{"read", "Service", root}, &refreshed, &diagnostic); status != 0 {
		t.Fatal(diagnostic.String())
	}
	for budget := 128; budget < 260; budget++ {
		var out, diagnostic bytes.Buffer
		status := runRead(callersOptions{root: root, symbols: []string{"run", "Service"}, budget: strconv.Itoa(budget), jsonOutput: true, noRefresh: true}, &out, &diagnostic)
		if status != 0 {
			continue
		}
		var response struct{ Results []readBatchItem }
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if savings.Tokens(savings.Length(out.String()+diagnostic.String())) > budget {
			t.Fatalf("read(batch,budget=%d) exceeded budget: %s", budget, out.String())
		}
		if response.Results[0].Status == "ok" && response.Results[1].Status != "covered" {
			t.Fatalf("read(batch,budget=%d) = %s, want included parent's child covered", budget, out.String())
		}
	}
}

func TestReadBatchOmittedParentAndDuplicateChild(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	body := "export class Service {\n  run() { return 42; }\n" + strings.Repeat("  // unrelated implementation detail\n", 300) + "}\n"
	if err := os.WriteFile(filepath.Join(root, "src", "one.ts"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	text, failed := readBatchText(t, root, 400, "Service", "run", "run")
	if failed || !strings.Contains(text, "Service · src/one.ts:L1-L303 — omitted") || strings.Count(text, "return 42") != 1 || !strings.Contains(text, "covered by") {
		t.Errorf("read(omitted parent, duplicate child) = %q, want full child once and covered duplicate", text)
	}
}

func TestReadBatchBudget(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	body := "export function alpha() {\n" + strings.Repeat("  shared();\n", 300) + "}\n"
	if err := os.WriteFile(filepath.Join(root, "src", "one.ts"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	text, failed := readBatchText(t, root, 350, "alpha", "src/two.ts::shared")
	if failed || !strings.Contains(text, "omitted") || !strings.Contains(text, "return 2") || strings.Contains(text, "shared();") || savings.Tokens(savings.Length(text)) > 350 {
		t.Errorf("MCP read(batch,budget=350) = %q, want omitted large definition, complete small one, bounded response", text)
	}
}

func TestReadBatchWorkspaceAndFreshness(t *testing.T) {
	root := t.TempDir()
	for _, child := range []string{"api", "web"} {
		writeReadFixture(t, filepath.Join(root, child))
	}
	dir := filepath.Join(root, "graft")
	if err := graph.WriteWorkspace(dir, []string{"api", "web"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "api", "src", "one.ts")
	if err := os.WriteFile(path, []byte("export function alpha() { return 'changed'; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, failed := readBatchText(t, root, 0, "api/src/one.ts::alpha", "web/src/one.ts::alpha")
	if failed || !strings.Contains(text, "changed") || !strings.Contains(text, "return 1") {
		t.Errorf("MCP read(workspace batch) = %q, want both fresh scoped definitions", text)
	}
	// A malformed span must not poison another definition from the same file.
	t.Setenv("GRAFT_NO_REFRESH", "1")
	childDir := filepath.Join(root, "web", "graft")
	wiring, err := graph.Read(graph.WiringPath(childDir))
	if err != nil {
		t.Fatal(err)
	}
	for i := range wiring.Nodes {
		if wiring.Nodes[i].Name == "alpha" {
			wiring.Nodes[i].Span = "L1-L999"
		}
	}
	if _, err := graph.Write(*wiring, childDir); err != nil {
		t.Fatal(err)
	}
	text, failed = readBatchText(t, root, 0, "web/src/one.ts::alpha", "web/src/one.ts::shared")
	if failed || !strings.Contains(text, "error") || !strings.Contains(text, "return alpha()") {
		t.Errorf("MCP read(mixed stale span) = %q, want item error and valid source", text)
	}
}

func TestReadBatchRejectsEscapingSource(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	outer := t.TempDir()
	root := filepath.Join(outer, "repo")
	writeReadFixture(t, root)
	outside := "export function alpha() { return 'secret'; }\n"
	if err := os.WriteFile(filepath.Join(outer, "outside.ts"), []byte(outside), 0o600); err != nil {
		t.Fatal(err)
	}
	wiring, err := graph.Read(graph.WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatal(err)
	}
	for i := range wiring.Nodes {
		if wiring.Nodes[i].Name == "alpha" {
			wiring.Nodes[i].Path = "../outside.ts"
			wiring.Nodes[i].Span = "L1-L1"
		}
	}
	wiring.Nodes = append(wiring.Nodes, graph.NodeV1{ID: "../outside.ts", Path: "../outside.ts", Kind: "file", BodyHash: sourcefiles.Hash(outside)})
	if _, err := graph.Write(*wiring, filepath.Join(root, "graft")); err != nil {
		t.Fatal(err)
	}
	text, failed := readBatchText(t, root, 0, "alpha", "src/two.ts::shared")
	if failed || strings.Contains(text, "secret") || !strings.Contains(text, "error") || !strings.Contains(text, "return 2") {
		t.Errorf("MCP read(escaping source) = %q, want confined item failure and valid source", text)
	}
}
