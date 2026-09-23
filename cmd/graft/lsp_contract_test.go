package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

func TestBuildLSPAddsCompilerResolvedCallEdge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake language server launcher uses a POSIX shell")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	t.Setenv("GRAFT_DIR", "")

	root := t.TempDir()
	sourcePath := filepath.Join(root, "main.go")
	source := "package main\n\nfunc target() {}\nfunc caller() { receiver.Call() }\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	installFakeLSPServer(t, "gopls", sourcePath)
	var stdout, stderr strings.Builder
	args := []string{"build", root, "--lsp"}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	loaded, err := graph.Read(graph.WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatalf("Read(%q) error = %v", graph.WiringPath(filepath.Join(root, "graft")), err)
	}
	_, progress, ok := strings.Cut(stderr.String(), "\rsummarizing 2/2: ")
	if !ok {
		t.Errorf("run(%v) stderr = %q, want LSP progress", args, stderr.String())
	} else if line := strings.TrimSuffix(progress, "\n"); len([]rune(line)) != 50 {
		t.Errorf("run(%v) LSP progress label length = %d, want 50; stderr = %q", args, len([]rune(line)), stderr.String())
	}
	for _, edge := range loaded.Edges {
		if edge.Source == "main.go#caller" && edge.Target == "main.go#target" &&
			edge.Relation == "calls" && edge.Confidence == "lsp_resolved" {
			return
		}
	}
	t.Errorf("run(%v) graph edges = %#v, want LSP-resolved caller to target edge; stdout = %q; stderr = %q", args, loaded.Edges, stdout.String(), stderr.String())
}

func TestBuildLSPWithoutMatchingLanguageSucceeds(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	var stdout, stderr strings.Builder
	args := []string{"build", root, "--lsp"}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	if strings.Contains(stdout.String(), "lsp_resolved") || strings.Contains(stderr.String(), "unknown option") {
		t.Errorf("run(%v) output = (%q, %q), want successful no-op without a matching language", args, stdout.String(), stderr.String())
	}
}

func TestBuildLSPUsesUTF16CharacterPositions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake language server launcher uses a POSIX shell")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	sourcePath := filepath.Join(root, "main.go")
	source := "package main\n\nfunc target() {}\ntype T struct{}\nfunc (μ T) caller() { receiver.Call() }\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	installFakeLSPServer(t, "gopls", sourcePath)
	t.Setenv("GRAFT_TEST_LSP_WARM_LINE", "2")
	t.Setenv("GRAFT_TEST_LSP_EXPECT_LINE", "4")
	t.Setenv("GRAFT_TEST_LSP_EXPECT_CHARACTER", "11")
	var stdout, stderr strings.Builder
	args := []string{"build", root, "--lsp"}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	loaded, err := graph.Read(graph.WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatal(err)
	}
	var callerID, targetID string
	for _, node := range loaded.Nodes {
		if node.Name == "caller" && node.Kind == "method" {
			callerID = node.ID
		}
		if node.Name == "target" && node.Kind == "function" {
			targetID = node.ID
		}
	}
	for _, edge := range loaded.Edges {
		if edge.Source == callerID && edge.Target == targetID && edge.Confidence == "lsp_resolved" {
			return
		}
	}
	t.Errorf("run(%v) graph edges = %#v, want UTF-16-positioned LSP edge from %q to %q", args, loaded.Edges, callerID, targetID)
}

func installFakeLSPServer(t *testing.T, name, sourcePath string) {
	t.Helper()
	t.Setenv("GRAFT_TEST_LSP_SERVER", "")
	serverDir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	canonicalSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(canonicalSource)}).String()
	t.Setenv("GRAFT_TEST_LSP_BINARY", binary)
	t.Setenv("GRAFT_TEST_LSP_URI", uri)
	t.Setenv("PATH", serverDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	serverPath := filepath.Join(serverDir, name)
	script := "#!/bin/sh\nexport GRAFT_TEST_LSP_SERVER=1\nexec \"$GRAFT_TEST_LSP_BINARY\" -test.run='^TestLSPTestServer$'\n"
	if err := os.WriteFile(serverPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLSPTestServer(t *testing.T) {
	if os.Getenv("GRAFT_TEST_LSP_SERVER") != "1" {
		return
	}
	if err := serveTestLSP(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func serveTestLSP() error {
	reader := bufio.NewReader(os.Stdin)
	for {
		request, err := readTestLSPMessage(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		id, hasID := request["id"]
		if !hasID {
			continue
		}
		var method string
		if err := json.Unmarshal(request["method"], &method); err != nil {
			continue
		}
		var result any
		switch method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{"callHierarchyProvider": true}}
		case "textDocument/prepareCallHierarchy":
			result = []any{testCallHierarchyItem("caller", 1)}
			if warmLine := os.Getenv("GRAFT_TEST_LSP_WARM_LINE"); warmLine != "" {
				var params struct {
					Position struct {
						Line      int `json:"line"`
						Character int `json:"character"`
					} `json:"position"`
				}
				if err := json.Unmarshal(request["params"], &params); err == nil {
					warm, _ := strconv.Atoi(warmLine)
					line, _ := strconv.Atoi(os.Getenv("GRAFT_TEST_LSP_EXPECT_LINE"))
					character, _ := strconv.Atoi(os.Getenv("GRAFT_TEST_LSP_EXPECT_CHARACTER"))
					if params.Position.Line != warm && (params.Position.Line != line || params.Position.Character != character) {
						result = []any{}
					}
				}
			}
		case "callHierarchy/outgoingCalls":
			result = []any{map[string]any{"to": testCallHierarchyItem("target", 2)}}
		default:
			result = nil
		}
		if err := writeTestLSPResponse(id, result); err != nil {
			return err
		}
	}
}

func readTestLSPMessage(reader *bufio.Reader) (map[string]json.RawMessage, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(name, "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("missing content length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, err
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, err
	}
	return request, nil
}

func writeTestLSPResponse(id json.RawMessage, result any) error {
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func testCallHierarchyItem(name string, line int) map[string]any {
	return map[string]any{
		"name": name, "kind": 12, "uri": os.Getenv("GRAFT_TEST_LSP_URI"),
		"range": map[string]any{
			"start": map[string]any{"line": line, "character": 0},
			"end":   map[string]any{"line": line, "character": 30},
		},
		"selectionRange": map[string]any{
			"start": map[string]any{"line": line, "character": 5},
			"end":   map[string]any{"line": line, "character": len(name) + 5},
		},
	}
}
