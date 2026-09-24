package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

// flagLikeFixture is a repository whose source mentions strings a CLI parser
// would read as flags.
func flagLikeFixture(t *testing.T, contextDir string) string {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, "src/flags.ts", "export function flags() {\n  run(\"-i\")\n  run(\"--version\")\n  run(\"--dir=/x\")\n}\n")
	file := graphNode("src/flags.ts", "flags.ts", "file", "src/flags.ts")
	file.Chars = new(80)
	flags := graphNode("src/flags.ts#flags", "flags", "function", "src/flags.ts")
	flags.Span = "L1-L5"
	fixture := graph.GraphV1{
		Meta:  graph.GraphMeta{Version: 1, NodeCount: 2, Languages: []string{"ts"}},
		Nodes: []graph.NodeV1{file, flags},
	}
	if contextDir == "" {
		contextDir = filepath.Join(root, "graft")
	}
	if _, err := graph.Write(fixture, contextDir); err != nil {
		t.Fatalf("graph.Write(%q) error = %v", contextDir, err)
	}
	return root
}

func TestMCPToolArgumentsNeverBecomeFlags(t *testing.T) {
	root := flagLikeFixture(t, "")
	contextDir := filepath.Join(root, "graft")
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{tool: "graft_find_all", args: map[string]any{"pattern": "-i"}, want: `run("-i")`},
		{tool: "graft_find_all", args: map[string]any{"pattern": "--version"}, want: `run("--version")`},
		{tool: "graft_find_all", args: map[string]any{"pattern": "--dir=/x"}, want: `run("--dir=/x")`},
		{tool: "graft_find_code", args: map[string]any{"query": "--dir=/x"}, want: "no matching nodes"},
		{tool: "graft_find_code", args: map[string]any{"query": "--version"}, want: "no matching nodes"},
		{tool: "graft_find_code", args: map[string]any{"query": "--help me"}, want: "no matching nodes"},
		{tool: "graft_find_code", args: map[string]any{"query": "-i"}, want: "no matching nodes"},
	}
	for _, tt := range tests {
		got := mcpCall(t.Context(), root, contextDir, "", tt.tool, tt.args)
		if got.isError || !strings.Contains(got.text, tt.want) {
			t.Errorf("mcpCall(%s, %v) = (%q, isError %t), want text containing %q", tt.tool, tt.args, got.text, got.isError, tt.want)
		}
	}
}

func TestMCPFindAllHonorsServerDir(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "elsewhere")
	root := flagLikeFixture(t, custom)
	got := mcpCall(t.Context(), root, custom, custom, "graft_find_all", map[string]any{"pattern": "version"})
	if got.isError || !strings.Contains(got.text, `run("--version")`) {
		t.Errorf("mcpCall(graft_find_all) with --dir %q = (%q, isError %t), want the hit from that graph", custom, got.text, got.isError)
	}
}

func TestMCPCallStopsWhenCancelled(t *testing.T) {
	root := flagLikeFixture(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got := mcpCall(ctx, root, filepath.Join(root, "graft"), "", "graft_find_code", map[string]any{"query": "flags"})
	if !got.isError || !strings.Contains(got.text, context.Canceled.Error()) {
		t.Errorf("mcpCall(cancelled, graft_find_code) = (%q, isError %t), want a cancellation error", got.text, got.isError)
	}
}

func TestMCPDepthMatchesCLI(t *testing.T) {
	maxDepth := int(^uint(0) >> 1)
	tests := []struct {
		value any
		want  int
	}{
		{value: nil, want: 1},
		{value: 3.0, want: 3},
		{value: 2.7, want: 2},
		{value: "3", want: 3},
		{value: " 4 ", want: 4},
		{value: "max", want: maxDepth},
		{value: "ALL", want: maxDepth},
		{value: "full", want: maxDepth},
		{value: math.MaxFloat64, want: maxDepth},
		{value: "0", want: 1},
		{value: -2.0, want: 1},
		{value: "deep", want: 1},
		{value: true, want: 1},
	}
	for _, tt := range tests {
		if got := mcpDepthValue(tt.value); got != tt.want {
			t.Errorf("mcpDepthValue(%#v) = %d, want %d", tt.value, got, tt.want)
		}
	}
}

func TestMCPServerSurvivesLongLines(t *testing.T) {
	dir := t.TempDir()
	// Longer than the old 1 MiB scanner limit, well under the line cap.
	long := strings.Repeat("x", 2<<20)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"graft_find_code","arguments":{"query":"` + long + `"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
	}, "\n") + "\n"
	var stdout, stderr bytes.Buffer
	if status := runMCP(t.Context(), callersOptions{command: "mcp", root: dir, rootSet: true}, strings.NewReader(input), &stdout, &stderr); status != 0 {
		t.Fatalf("runMCP(2 MiB line) status = %d, want 0; stderr = %q", status, stderr.String())
	}
	ids := mcpResponseIDs(t, stdout.String())
	for _, id := range []string{"1", "2", "3"} {
		if _, ok := ids[id]; !ok {
			t.Errorf("runMCP(2 MiB line) answered ids %v, want id %s answered", ids, id)
		}
	}
}

func TestMCPServerSkipsOversizeLine(t *testing.T) {
	dir := t.TempDir()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"graft_find_code","arguments":{"query":"` + strings.Repeat("x", 64<<10) + `"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
	}, "\n")
	var stdout bytes.Buffer
	server := newMCPServer(t.Context(), callersOptions{}, dir, dir)
	err := server.Run(t.Context(), &mcpTransport{reader: strings.NewReader(input), writer: &stdout, maxLine: 4 << 10})
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Server.Run(oversize line) error = %v, want nil or EOF", err)
	}
	ids := mcpResponseIDs(t, stdout.String())
	if ids["null"] != -32700 {
		t.Errorf("Server.Run(oversize line) responses = %v, want a -32700 parse error without id", ids)
	}
	if _, ok := ids["2"]; ok {
		t.Errorf("Server.Run(oversize line) answered id 2, want the oversize request skipped")
	}
	if _, ok := ids["3"]; !ok {
		t.Errorf("Server.Run(oversize line) responses = %v, want the next request answered", ids)
	}
}

// mcpResponseIDs maps each NDJSON response id (JSON text, "null" for none) to
// its error code, zero for a result.
func mcpResponseIDs(t *testing.T, output string) map[string]int {
	t.Helper()
	ids := make(map[string]int)
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		var response struct {
			ID    json.RawMessage `json:"id"`
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("response %.80q is not JSON: %v", line, err)
		}
		id := string(response.ID)
		if id == "" {
			id = "null"
		}
		ids[id] = 0
		if response.Error != nil {
			ids[id] = response.Error.Code
		}
	}
	return ids
}

// TestMCPConcurrentToolCalls pipelines tool calls that the SDK runs at once;
// under -race it proves the calls share no unguarded state.
func TestMCPConcurrentToolCalls(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	t.Setenv("GRAFT_NO_REFRESH", "")
	root := t.TempDir()
	writeFixtureFile(t, root, "main.go", "package main\n\nfunc main() { helper() }\n\nfunc helper() {}\n")
	writeFixtureFile(t, root, "util.go", "package main\n\nfunc util() { helper() }\n")
	if status := runBuild(callersOptions{command: "build", root: root, rootSet: true}, io.Discard, io.Discard); status != 0 {
		t.Fatalf("runBuild(%q) status = %d, want 0", root, status)
	}
	// A drifted file makes every call race to refresh the graph first.
	if err := os.WriteFile(filepath.Join(root, "util.go"), []byte("package main\n\nfunc util() { helper(); helper() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := []string{
		`{"name":"graft_find_code","arguments":{"query":"helper function"}}`,
		`{"name":"graft_find_all","arguments":{"pattern":"helper"}}`,
		`{"name":"graft_trace_calls","arguments":{"symbol":"helper","depth":"2"}}`,
		`{"name":"graft_repo_map","arguments":{}}`,
		`{"name":"graft_file_api","arguments":{"file":"main.go"}}`,
		`{"name":"graft_check_freshness","arguments":{}}`,
		`{"name":"graft_find_code","arguments":{"query":"util"}}`,
		`{"name":"graft_find_all","arguments":{"pattern":"func","ignore_case":true}}`,
	}
	lines := []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`}
	for index, call := range calls {
		lines = append(lines, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":%s}`, index+2, call))
	}
	var stdout lockedBuffer
	var stderr bytes.Buffer
	status := runMCP(t.Context(), callersOptions{command: "mcp", root: root, rootSet: true}, strings.NewReader(strings.Join(lines, "\n")+"\n"), &stdout, &stderr)
	if status != 0 {
		t.Fatalf("runMCP(concurrent calls) status = %d, want 0; stderr = %q", status, stderr.String())
	}
	ids := mcpResponseIDs(t, stdout.String())
	for index := range calls {
		id := strconv.Itoa(index + 2)
		if code, ok := ids[id]; !ok || code != 0 {
			t.Errorf("runMCP(concurrent calls) id %s = (code %d, answered %t), want a result", id, code, ok)
		}
	}
}

// lockedBuffer is a bytes.Buffer safe for the concurrent writes a server makes.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
