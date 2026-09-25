package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func writeReadFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"one.ts": "export function alpha() {\n  return 1;\n}\nexport function shared() { return alpha(); }\n",
		"two.ts": "export function shared() { return 2; }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "graft")
	built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Write(built.Graph, dir); err != nil {
		t.Fatal(err)
	}
	if err := graph.WriteFingerprint(dir, graph.ExtractorID, built.Fingerprints, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadSymbolContract(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	for _, tc := range []struct {
		selector string
		want     string
		wantErr  bool
	}{
		{"alpha", "return 1;", false},
		{"src/one.ts::alpha", "return 1;", false},
		{"src/one.ts#alpha", "return 1;", false},
		{"src/two.ts::shared", "return 2;", false},
		{"shared", "ambiguous", true},
		{"Alpha", "no exact symbol", true},
		{"wrong.alpha", "no exact symbol", true},
		{"src/two.ts::alpha", "alpha is not in src/two.ts; resolved by name", false},
		{"src/one.ts", "no exact symbol", true},
		{"", "requires a", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			status := run([]string{"read", tc.selector, root}, &out, &diagnostic)
			if (status != 0) != tc.wantErr || !strings.Contains(out.String()+diagnostic.String(), tc.want) {
				t.Errorf("run(read %q) = (%d, %q, %q), want error %t and %q", tc.selector, status, out.String(), diagnostic.String(), tc.wantErr, tc.want)
			}
			result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", map[string]any{"symbol": tc.selector})
			if result.isError != tc.wantErr || !strings.Contains(result.text, tc.want) {
				t.Errorf("mcpCall(read %q) = %+v, want error %t and %q", tc.selector, result, tc.wantErr, tc.want)
			}
			if tc.wantErr && (out.Len() != 0 || strings.Contains(result.text, "return 1")) {
				t.Errorf("read(%q) leaked source on error: %q, %+v", tc.selector, out.String(), result)
			}
		})
	}
	var out, diagnostic bytes.Buffer
	if status := run([]string{"read", "alpha", root, "--json"}, &out, &diagnostic); status != 0 {
		t.Fatalf("run(read alpha --json) = %d, %q", status, diagnostic.String())
	}
	var result struct {
		ID, Name, Pointer, SourceHash, Code string
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	wantCode := "export function alpha() {\n  return 1;\n}"
	if result.Code != wantCode || result.Pointer != "src/one.ts:L1-L3" || result.SourceHash != sourcefiles.Hash(wantCode) || result.ID != "src/one.ts#alpha" {
		t.Errorf("read(alpha) = %+v, want exact code, current span, ID and matching hash", result)
	}
}

func TestReadSymbolFreshness(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	path := filepath.Join(root, "src", "one.ts")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte("return 1"), []byte("return 9")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if status := run([]string{"read", "alpha", root, "--no-refresh"}, &out, &diagnostic); status == 0 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "changed") {
		t.Fatalf("read(stale alpha) = (%d, %q, %q), want explicit stale-source error", status, out.String(), diagnostic.String())
	}
	t.Setenv("GRAFT_REFRESH", "hash")
	result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", map[string]any{"symbol": "alpha"})
	if result.isError || !strings.Contains(result.text, "return 9") {
		t.Errorf("read(refreshed alpha) = %+v, want current source", result)
	}
}

func TestReadSymbolBudget(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	path := filepath.Join(root, "src", "one.ts")
	body := "export function large() {\n" + strings.Repeat("  alpha();\n", 200) + "}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []string{"127", "64001", "1.5", "128"} {
		var out, diagnostic bytes.Buffer
		if status := run([]string{"read", "large", root, "--budget", budget}, &out, &diagnostic); status == 0 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "budget") {
			t.Errorf("read(large, budget=%s) = (%d, %q, %q), want budget error without partial source", budget, status, out.String(), diagnostic.String())
		}
	}
	result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", map[string]any{"symbol": "large", "budget": float64(2000)})
	if result.isError || !strings.Contains(result.text, strings.TrimSuffix(body, "\n")) {
		t.Errorf("read(large, budget=2000) = %+v, want complete definition", result)
	}
}

func TestReadSymbolWorkspace(t *testing.T) {
	root := t.TempDir()
	for _, child := range []string{"api", "web"} {
		writeReadFixture(t, filepath.Join(root, child))
	}
	if err := graph.WriteWorkspace(filepath.Join(root, "graft"), []string{"api", "web"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		selector string
		want     string
		wantErr  bool
	}{
		{"alpha", "ambiguous", true},
		{"api/src/one.ts::alpha", "api/src/one.ts:L1-L3", false},
		{"web/src/one.ts#alpha", "web/src/one.ts:L1-L3", false},
	} {
		result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", map[string]any{"symbol": tc.selector})
		if result.isError != tc.wantErr || !strings.Contains(result.text, tc.want) {
			t.Errorf("read(workspace %q) = %+v, want error %t and %q", tc.selector, result, tc.wantErr, tc.want)
		}
	}
}

func TestReadSymbolRejectsEscapingSource(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root := t.TempDir()
	writeReadFixture(t, root)
	outside := filepath.Join(t.TempDir(), "outside.ts")
	if err := os.WriteFile(outside, []byte("export function alpha() { return 'secret'; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "src", "one.ts")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", map[string]any{"symbol": "alpha"})
	if !result.isError || strings.Contains(result.text, "secret") {
		t.Errorf("read(escaping symlink) = %+v, want confined read error", result)
	}
}

func TestReadSymbolSourceFailures(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	for _, tc := range []struct {
		name   string
		modify func(*graph.GraphV1, string) error
	}{
		{"missing source", func(_ *graph.GraphV1, root string) error { return os.Remove(filepath.Join(root, "src", "one.ts")) }},
		{"missing file hash", func(wiring *graph.GraphV1, _ string) error {
			for i := range wiring.Nodes {
				if wiring.Nodes[i].Kind == "file" {
					wiring.Nodes[i].BodyHash = ""
				}
			}
			return nil
		}},
		{"invalid span", func(wiring *graph.GraphV1, _ string) error {
			for i := range wiring.Nodes {
				if wiring.Nodes[i].Name == "alpha" {
					wiring.Nodes[i].Span = "L1-L900"
				}
			}
			return nil
		}},
		{"escaping path", func(wiring *graph.GraphV1, _ string) error {
			for i := range wiring.Nodes {
				if wiring.Nodes[i].Name == "alpha" {
					wiring.Nodes[i].Path = "../outside.ts"
				}
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeReadFixture(t, root)
			dir := filepath.Join(root, "graft")
			wiring, err := graph.Read(graph.WiringPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.modify(wiring, root); err != nil {
				t.Fatal(err)
			}
			if _, err := graph.Write(*wiring, dir); err != nil {
				t.Fatal(err)
			}
			result := mcpCall(t.Context(), root, dir, "", "graft_read_symbol", map[string]any{"symbol": "alpha"})
			if !result.isError || strings.Contains(result.text, "return 1") {
				t.Errorf("read(%s) = %+v, want error without source", tc.name, result)
			}
		})
	}
}

func TestReadSymbolUTF16(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	body := "export function encoded() {\r\n  return 'hello';\r\n}"
	data := []byte{0xff, 0xfe}
	for _, unit := range utf16.Encode([]rune(body)) {
		data = binary.LittleEndian.AppendUint16(data, unit)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "one.ts"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", map[string]any{"symbol": "encoded"})
	if result.isError || !strings.Contains(result.text, body) {
		t.Errorf("read(UTF-16LE CRLF source) = %+v, want complete decoded source", result)
	}
}

func TestMCPReadPreservesWorkspaceCoverage(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, filepath.Join(root, "api"))
	dir := filepath.Join(root, "graft")
	if err := graph.WriteWorkspace(dir, []string{"api", "missing"}); err != nil {
		t.Fatal(err)
	}
	result := mcpCall(t.Context(), root, dir, "", "graft_read_symbol", map[string]any{"symbol": "alpha"})
	if result.isError || !strings.Contains(result.text, "workspace graphs are unavailable") || !strings.Contains(result.text, "return 1") {
		t.Errorf("MCP read(partial workspace) = %+v, want source and coverage warning", result)
	}
}

func TestMCPReadRefreshBudgetBoundary(t *testing.T) {
	t.Setenv("GRAFT_REFRESH", "hash")
	for padding := range 4 {
		root := t.TempDir()
		writeReadFixture(t, root)
		path := filepath.Join(root, "src", "one.ts")
		body := "export function alpha() {\n // " + strings.Repeat("x", 500+padding) + "\n return 2;\n}\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"symbol": "alpha"}
		result := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", args)
		if result.isError {
			t.Fatal(result.text)
		}
		tokens := savings.Tokens(savings.Length(result.text))
		args["budget"] = float64(tokens - 1)
		body = strings.ReplaceAll(body, "return 2", "return 3")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		result = mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", args)
		if !result.isError {
			t.Errorf("MCP read(padding=%d, budget=%d) = %d tokens, want oversized-answer error", padding, tokens-1, savings.Tokens(savings.Length(result.text)))
		}
	}
}

func TestMCPReadSharedCache(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root)
	dir := filepath.Join(root, "graft")
	var cache queryCache
	args := map[string]any{"symbol": "alpha"}
	result := mcpCallWithCache(t.Context(), root, dir, "", "graft_read_symbol", args, &cache)
	if result.isError || len(cache.entries) != 1 || cache.entries[0].index != nil {
		t.Fatalf("cached exact read = %+v, entries = %+v, want graph-only snapshot", result, cache.entries)
	}
	wiring := cache.entries[0].wiring
	if err := graph.WriteAskIndex(dir, *wiring); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"graft_read_symbol", args},
		{"graft_file_api", map[string]any{"file": "src/one.ts"}},
		{"graft_trace_calls", args},
		{"graft_find_code", map[string]any{"query": "alpha"}},
	} {
		wg.Go(func() {
			result := mcpCallWithCache(t.Context(), root, dir, "", tc.tool, tc.args, &cache)
			if result.isError || !strings.Contains(result.text, "alpha") {
				t.Errorf("mixed cached %s = %+v, want alpha result", tc.tool, result)
			}
		})
	}
	wg.Wait()
	if cache.entries[0].wiring != wiring || cache.entries[0].index == nil {
		t.Errorf("mixed MCP cache = %+v, want original graph and lazy ranked index", cache.entries[0])
	}
	path := filepath.Join(root, "src", "one.ts")
	if err := os.WriteFile(path, []byte("export function alpha() { return 'updated'; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = mcpCallWithCache(t.Context(), root, dir, "", "graft_read_symbol", args, &cache)
	if result.isError || !strings.Contains(result.text, "updated") || cache.entries[0].wiring == wiring {
		t.Errorf("cached read after source edit = %+v, want refreshed source and graph", result)
	}
}
