package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestRunWorkspaceBuildSplitsChildrenDeterministically(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	for _, child := range []string{"web", "api"} {
		writeWorkspaceChildFixture(t, root, child)
	}
	if _, err := graph.Write(graph.GraphV1{Meta: graph.GraphMeta{Version: 1}, Nodes: []graph.NodeV1{}, Edges: []graph.EdgeV1{}}, filepath.Join(root, "graft")); err != nil {
		t.Fatalf("Write(parent mega graph) error = %v", err)
	}
	if err := graph.WriteWorkspace(filepath.Join(root, "graft"), []string{"api"}); err != nil {
		t.Fatalf("WriteWorkspace(parent index) error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"build", root}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	parentDir := filepath.Join(root, "graft")
	index, err := os.ReadFile(filepath.Join(parentDir, "workspace.json"))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want workspace index", filepath.Join(parentDir, "workspace.json"), err)
	}
	wantIndex := "{\n  \"version\": 1,\n  \"children\": [\n    \"api\",\n    \"web\"\n  ]\n}\n"
	if string(index) != wantIndex {
		t.Errorf("run(%v) workspace index = %q, want deterministic sorted index %q", args, index, wantIndex)
	}
	if _, err := os.Stat(graph.WiringPath(parentDir)); !os.IsNotExist(err) {
		t.Errorf("run(%v) parent wiring graph Stat error = %v, want absent", args, err)
	}
	for _, child := range []string{"api", "web"} {
		childGraph, err := graph.Read(graph.WiringPath(filepath.Join(root, child, "graft")))
		if err != nil {
			t.Errorf("Read(%q) error = %v, want child graph", graph.WiringPath(filepath.Join(root, child, "graft")), err)
			continue
		}
		if !slices.ContainsFunc(childGraph.Nodes, func(node graph.NodeV1) bool { return node.Path == "src/app.ts" }) {
			t.Errorf("run(%v) child %s graph nodes = %#v, want its own src/app.ts", args, child, childGraph.Nodes)
		}
	}
	if !strings.Contains(stderr.String(), "building 2 workspace repos: api, web") {
		t.Errorf("run(%v) stderr = %q, want sorted workspace start notice", args, stderr.String())
	}
	if !strings.Contains(stderr.String(), "this folder contains 2 separate git repos — splitting") {
		t.Errorf("run(%v) stderr = %q, want one-time mega-graph migration notice", args, stderr.String())
	}
	if !strings.Contains(stdout.String(), "✓ api/: ") || !strings.Contains(stdout.String(), "✓ web/: ") ||
		!strings.Contains(stdout.String(), "✓ workspace: 2 repos federated → graft/workspace.json") {
		t.Errorf("run(%v) stdout = %q, want child summaries and workspace index summary", args, stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".ignore")); !os.IsNotExist(err) {
		t.Errorf("run(%v) parent .ignore Stat error = %v, want absent without parent cards", args, err)
	}
}

func TestRunWorkspaceCheckReportsMissingCoverageAndChildDrift(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	api := writeWorkspaceChildFixture(t, root, "api")
	web := writeWorkspaceChildFixture(t, root, "web")
	for _, child := range []string{api, web} {
		var stdout, stderr bytes.Buffer
		args := []string{"build", child}
		if status := run(args, &stdout, &stderr); status != 0 {
			t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
		}
	}
	parentContext := filepath.Join(root, "graft")
	if err := graph.WriteWorkspace(parentContext, []string{"web", "missing", "api"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"check", root, "--json"}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0 for missing child coverage; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	for _, want := range []string{"workspace check — 3 repo(s)", "api/: OK", "web/: OK", "missing/: not built (run graft build)", "2 of 3 workspace repos have graphs; run graft build to cover missing"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("run(%v) stdout = %q, want %q", args, stdout.String(), want)
		}
	}
	if err := os.WriteFile(filepath.Join(web, "src", "app.ts"), []byte("export function app() {}\nexport function next() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if status := run(args, &stdout, &stderr); status != 1 {
		t.Fatalf("run(%v) status = %d, want 1 for stale child; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "web/: STALE (") || !strings.Contains(stdout.String(), "1 added") || !strings.Contains(stdout.String(), "1 changed") {
		t.Errorf("run(%v) stdout = %q, want web child drift summary", args, stdout.String())
	}
}

func TestRunWorkspaceCheckWithoutDirUsesNearestWorkspaceAncestor(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	for _, child := range []string{"api", "web"} {
		childDir := writeWorkspaceChildFixture(t, root, child)
		var stdout, stderr bytes.Buffer
		if status := run([]string{"build", childDir}, &stdout, &stderr); status != 0 {
			t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", childDir, status, stderr.String())
		}
	}
	if err := graph.WriteWorkspace(filepath.Join(root, "graft"), []string{"api", "web"}); err != nil {
		t.Fatal(err)
	}
	workingDir := filepath.Join(root, "tools", "nested")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workingDir)
	var stdout, stderr bytes.Buffer
	if status := run([]string{"check"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(check in %q) status = %d, want 0; stdout = %q; stderr = %q", workingDir, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "workspace check — 2 repo(s)") || !strings.Contains(stdout.String(), "api/: OK") || !strings.Contains(stdout.String(), "web/: OK") {
		t.Errorf("run(check in %q) stdout = %q, want nearest workspace report", workingDir, stdout.String())
	}
}

func TestRunWorkspaceBuildPreservesChildGraphsOnUnsupportedExtension(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	api := writeWorkspaceChildFixture(t, root, "api")
	web := writeWorkspaceChildFixture(t, root, "web")
	for _, child := range []string{api, web} {
		var stdout, stderr bytes.Buffer
		args := []string{"build", child}
		if status := run(args, &stdout, &stderr); status != 0 {
			t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
		}
	}
	unsupported := filepath.Join(api, "opaque.xyz")
	if err := os.WriteFile(unsupported, []byte("opaque\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parentContext := filepath.Join(root, "graft")
	if err := graph.WriteWorkspace(parentContext, []string{"api", "web"}); err != nil {
		t.Fatal(err)
	}
	parentIndex, err := os.ReadFile(filepath.Join(parentContext, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	apiGraphPath := graph.WiringPath(filepath.Join(api, "graft"))
	webGraphPath := graph.WiringPath(filepath.Join(web, "graft"))
	beforeAPI, err := os.ReadFile(apiGraphPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeWeb, err := os.ReadFile(webGraphPath)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"build", root, "--extensions", ".xyz"}
	if status := run(args, &stdout, &stderr); status != 1 {
		t.Fatalf("run(%v) status = %d, want 1; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "api/: ") || !strings.Contains(stderr.String(), "opaque.xyz") {
		t.Errorf("run(%v) stderr = %q, want unsupported api child diagnostic", args, stderr.String())
	}
	for _, item := range []struct {
		path string
		want []byte
	}{{filepath.Join(parentContext, "workspace.json"), parentIndex}, {apiGraphPath, beforeAPI}, {webGraphPath, beforeWeb}} {
		got, err := os.ReadFile(item.path)
		if err != nil || !bytes.Equal(got, item.want) {
			t.Errorf("run(%v) changed %q; read error = %v", args, item.path, err)
		}
	}
}

func writeWorkspaceChildFixture(t *testing.T, root, name string) string {
	t.Helper()
	child := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Join(child, ".git"), err)
	}
	source := filepath.Join(child, "src", "app.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(source), err)
	}
	if err := os.WriteFile(source, []byte("export function app() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", source, err)
	}
	return child
}
