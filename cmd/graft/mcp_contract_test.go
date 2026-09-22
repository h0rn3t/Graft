package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServerSpeaksOfficialSDK(t *testing.T) {
	dir := t.TempDir()
	server := newMCPServer(callersOptions{}, dir, dir)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("Server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "graft-contract", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Client.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	tools, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 0 {
		t.Errorf("ListTools() returned %d tools, want 0 for an unbuilt repo", len(tools.Tools))
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "graft_ask",
		Arguments: map[string]any{"query": "missing"},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if !result.IsError {
		t.Error("CallTool() IsError = false, want true for an unbuilt repo")
	}
	if len(result.Content) != 1 {
		t.Fatalf("CallTool() returned %d content items, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool() content type = %T, want *mcp.TextContent", result.Content[0])
	}
	if text.Text != "no graph found — run `graft build` first" {
		t.Errorf("CallTool() text = %q, want missing-graph guidance", text.Text)
	}
}

func TestRunMCPPreservesNDJSONBoundary(t *testing.T) {
	dir := t.TempDir()
	input := strings.Join([]string{
		"{",
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
	}, "\n")
	var stdout, stderr bytes.Buffer
	status := runMCP(callersOptions{command: "mcp", root: dir, rootSet: true}, strings.NewReader(input), &stdout, &stderr)
	if status != 0 {
		t.Fatalf("runMCP() status = %d, want 0; stderr = %q", status, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("runMCP() stderr = %q, want empty stderr", stderr.String())
	}

	responses := make([]map[string]any, 0, 4)
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("runMCP() response %q is invalid JSON: %v", line, err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 4 {
		t.Fatalf("runMCP() emitted %d responses, want 4: %s", len(responses), stdout.String())
	}
	var parseError, initialize, list, unknown map[string]any
	for _, response := range responses {
		if response["id"] == nil {
			parseError = response
			continue
		}
		switch response["id"] {
		case float64(1):
			initialize = response
		case float64(2):
			list = response
		case float64(3):
			unknown = response
		}
	}
	if parseError["error"].(map[string]any)["code"] != float64(-32700) {
		t.Errorf("parse error = %#v, want code -32700", parseError)
	}
	if initialize["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Errorf("initialize protocolVersion = %#v, want 2025-03-26", initialize)
	}
	listResult := list["result"].(map[string]any)
	if _, ok := listResult["ttlMs"]; ok {
		t.Errorf("tools/list result contains SDK-only ttlMs field: %#v", listResult)
	}
	if unknown["error"].(map[string]any)["code"] != float64(-32601) {
		t.Errorf("unknown method = %#v, want code -32601", unknown)
	}
}

func TestRunMCPAcceptsLegacyCallBeforeInitialize(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	status := runMCP(
		callersOptions{command: "mcp", root: dir, rootSet: true},
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"graft_ask","arguments":{"query":"missing"}}}`+"\n"),
		&stdout,
		&stderr,
	)
	if status != 0 {
		t.Fatalf("runMCP() status = %d, want 0; stderr = %q", status, stderr.String())
	}
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("runMCP() response = %q, JSON error = %v", stdout.String(), err)
	}
	if !response.Result.IsError || len(response.Result.Content) != 1 || response.Result.Content[0].Text != "no graph found — run `graft build` first" {
		t.Errorf("runMCP() result = %#v, want legacy soft error", response.Result)
	}
}

func TestMCPCallWorkspaceRepoMap(t *testing.T) {
	root, contextDir := workspaceMapFixture(t)
	got := mcpCall(root, contextDir, "", "graft_repo_map", map[string]any{"max_dirs": 2})
	if got.isError {
		t.Fatalf("mcpCall(%q, %q, graft_repo_map) = %#v, want success", root, contextDir, got)
	}
	alpha := strings.Index(got.text, "## api/")
	web := strings.Index(got.text, "## web/")
	if !strings.HasPrefix(got.text, "workspace map — 2 repo(s)\n") || alpha < 0 || web <= alpha ||
		!strings.Contains(got.text, "2 of 3 workspace repos have graphs; run graft build to cover missing") {
		t.Errorf("mcpCall(%q, %q, graft_repo_map) text = %q, want federated map and coverage", root, contextDir, got.text)
	}
}

func TestMCPRefreshContract(t *testing.T) {
	for _, tt := range []struct {
		name            string
		tool            string
		editSource      bool
		addUnsupported  bool
		wantRefresh     bool
		wantStale       bool
		wantUnsupported bool
	}{
		{name: "refresh before retrieval", tool: "graft_file_api", editSource: true, wantRefresh: true},
		{name: "freshness check reports without repairing", tool: "graft_check_freshness", editSource: true, wantStale: true},
		{name: "unsupported source is never called clean", tool: "graft_check_freshness", addUnsupported: true, wantUnsupported: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAFT_NO_REFRESH", "false")
			root := t.TempDir()
			contextDir := filepath.Join(root, "graft")
			sourcePath := filepath.Join(root, "src", "app.ts")
			if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(sourcePath), err)
			}
			if err := os.WriteFile(sourcePath, []byte("export function before() {}"), 0o644); err != nil {
				t.Fatalf("WriteFile(%q) error = %v, want nil", sourcePath, err)
			}
			if tt.addUnsupported {
				if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) for unsupported source error = %v, want nil", filepath.Join(root, "src"), err)
				}
				if err := os.WriteFile(filepath.Join(root, "src", "other.go"), []byte("package other"), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) for unsupported source error = %v, want nil", filepath.Join(root, "src", "other.go"), err)
				}
			}
			built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: contextDir})
			if err != nil {
				t.Fatalf("BuildGraph(%q) error = %v, want nil", root, err)
			}
			if _, err := graph.Write(built.Graph, contextDir); err != nil {
				t.Fatalf("Write(BuildGraph(%q), %q) error = %v, want nil", root, contextDir, err)
			}
			if err := graph.WriteFingerprint(contextDir, "go-v1", built.Fingerprints, nil); err != nil {
				t.Fatalf("WriteFingerprint(%q, go-v1) error = %v, want nil", contextDir, err)
			}
			if tt.editSource {
				if err := os.WriteFile(sourcePath, []byte("export function after() {}"), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) after edit error = %v, want nil", sourcePath, err)
				}
			}

			got := mcpCall(root, contextDir, "", tt.tool, map[string]any{"file": "src/app.ts"})
			if got.isError {
				t.Fatalf("mcpCall(%q, %q, %q) = %#v, want success", root, contextDir, tt.tool, got)
			}
			loaded, err := graph.Read(graph.WiringPath(contextDir))
			if err != nil {
				t.Fatalf("Read(%q) after mcpCall error = %v, want nil", graph.WiringPath(contextDir), err)
			}
			if tt.wantRefresh {
				if !strings.HasPrefix(got.text, "[graft] refreshed the graph (1 file changed) before answering") || !strings.Contains(got.text, "after") {
					t.Errorf("mcpCall(%q, %q, %q) text = %q, want refresh note and new source symbol", root, contextDir, tt.tool, got.text)
				}
				if !slices.ContainsFunc(loaded.Nodes, func(node graph.NodeV1) bool { return node.Name == "after" }) {
					t.Errorf("Read(%q) nodes = %v, want refreshed after function", graph.WiringPath(contextDir), loaded.Nodes)
				}
				return
			}
			if strings.HasPrefix(got.text, "[graft]") {
				t.Errorf("mcpCall(%q, %q, %q) text = %q, want check without a refresh note", root, contextDir, tt.tool, got.text)
			}
			if tt.wantStale && !strings.Contains(got.text, "graph check: STALE") {
				t.Errorf("mcpCall(%q, %q, %q) text = %q, want stale graph report", root, contextDir, tt.tool, got.text)
			}
			if tt.wantStale && (!strings.Contains(got.text, "src/app.ts#after") || !strings.Contains(got.text, "src/app.ts#before")) {
				t.Errorf("mcpCall(%q, %q, %q) text = %q, want added and removed symbol IDs", root, contextDir, tt.tool, got.text)
			}
			if tt.wantUnsupported && (!strings.Contains(got.text, "graph check: PARTIAL") || !strings.Contains(got.text, "src/other.go")) {
				t.Errorf("mcpCall(%q, %q, %q) text = %q, want an explicit unsupported-source limitation", root, contextDir, tt.tool, got.text)
			}
			if slices.ContainsFunc(loaded.Nodes, func(node graph.NodeV1) bool { return node.Name == "after" }) {
				t.Errorf("Read(%q) nodes = %v, want freshness check to leave the stale graph unchanged", graph.WiringPath(contextDir), loaded.Nodes)
			}
		})
	}
}
