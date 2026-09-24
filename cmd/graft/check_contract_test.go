package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestRunCheckWithoutGraphReportsNoGraph(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"check", root}
	if status := run(args, &stdout, &stderr); status != 1 {
		t.Fatalf("run(%v) status = %d, want 1; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	want := "graph check: NO GRAPH\n\nNo graft/.graph/wiring.json found. Run `graft build` first.\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%v) stdout = %q, want %q", args, got, want)
	}
	if got := stderr.String(); got != "" {
		t.Errorf("run(%v) stderr = %q, want empty", args, got)
	}
}

func TestRunCheckWithoutDirUsesNearestIndexedAncestor(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCheckSource(t, root, "src/app.ts", "export function app() {}\n")
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	t.Chdir(filepath.Join(root, "src"))
	stdout.Reset()
	stderr.Reset()
	if status := run([]string{"check"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(check in %q) status = %d, want 0; stdout = %q; stderr = %q", filepath.Join(root, "src"), status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "graph check: OK") {
		t.Errorf("run(check in %q) stdout = %q, want nearest indexed graph", filepath.Join(root, "src"), stdout.String())
	}
}

func TestRunCheckKeylessBuildReportsGraphOnly(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	writeCheckSource(t, root, "math.ts", "export function add(a: number, b: number) { return a + b; }\n")
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"check", root}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	want := "graph check: OK — the wiring graph is in sync with the code.\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%v) stdout = %q, want %q", args, got, want)
	}
}

func TestRunCheckReportsGraphDrift(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	path := writeCheckSource(t, root, "math.ts", "export function add(a: number, b: number) { return a + b; }\n")
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	if err := os.WriteFile(path, []byte("export function add(a: number, b: number) { return a + b; }\nexport function sub(a: number, b: number) { return a - b; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"check", root}
	if status := run(args, &stdout, &stderr); status != 1 {
		t.Fatalf("run(%v) status = %d, want 1; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "graph check: STALE") || !strings.Contains(stdout.String(), "math.ts#sub") {
		t.Errorf("run(%v) stdout = %q, want stale report naming math.ts#sub", args, stdout.String())
	}
}

func TestRunCheckExcludesCustomContextDirectoryWithoutWriting(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	writeCheckSource(t, root, "main.ts", "export function main() {}\n")
	contextDir := filepath.Join(root, "tools", "context")
	var stdout, stderr bytes.Buffer
	buildArgs := []string{"build", root, "--dir", contextDir}
	if status := run(buildArgs, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", buildArgs, status, stderr.String())
	}
	writeCheckSource(t, contextDir, "generated.ts", "export function generated() {}\n")
	cachePath := filepath.Join(contextDir, ".cache", "extract."+graph.ExtractorID+".json")
	fingerprintPath, err := graph.FingerprintPath(contextDir, graph.ExtractorID)
	if err != nil {
		t.Fatal(err)
	}
	beforeCache, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, err := os.ReadFile(fingerprintPath)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	checkArgs := []string{"check", root, "--dir", contextDir}
	if status := run(checkArgs, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stdout = %q; stderr = %q", checkArgs, status, stdout.String(), stderr.String())
	}
	for _, item := range []struct {
		path string
		want []byte
	}{{cachePath, beforeCache}, {fingerprintPath, beforeFingerprint}} {
		got, err := os.ReadFile(item.path)
		if err != nil || !bytes.Equal(got, item.want) {
			t.Errorf("run(%v) changed %q; read error = %v", checkArgs, item.path, err)
		}
	}
}

func TestRunCheckIgnoresLegacyContextManifest(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	writeCheckSource(t, root, "app.ts", "current\n")
	manifestPath := filepath.Join(root, "graft", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"version":1,"files":[{"path":"app.ts","hash":"legacy"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"check", root}
	if status := run(args, &stdout, &stderr); status != 1 {
		t.Fatalf("run(%v) status = %d, want 1; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	want := "graph check: NO GRAPH\n\nNo graft/.graph/wiring.json found. Run `graft build` first.\n"
	if got := stdout.String(); got != want {
		t.Errorf("run(%v) stdout = %q, want legacy manifest ignored: %q", args, got, want)
	}
}

func TestRunCheckPartialGraphDoesNotRepairIt(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	source := filepath.Join(root, "schema.sql")
	if err := os.WriteFile(source, []byte("CREATE TABLE app.users (id bigint);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	outDir := filepath.Join(root, "graft")
	graphPath := graph.WiringPath(outDir)
	fingerprintPath, err := graph.FingerprintPath(outDir, graph.ExtractorID)
	if err != nil {
		t.Fatal(err)
	}
	beforeGraph, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, err := os.ReadFile(fingerprintPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("CREATE TABLE app.users (id bigint;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"check", root}
	if status := run(args, &stdout, &stderr); status != 1 {
		t.Fatalf("run(%v) status = %d, want 1; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "graph check: PARTIAL") || !strings.Contains(stdout.String(), "schema.sql") {
		t.Errorf("run(%v) stdout = %q, want partial extraction report for schema.sql", args, stdout.String())
	}
	for _, item := range []struct {
		path string
		want []byte
	}{{graphPath, beforeGraph}, {fingerprintPath, beforeFingerprint}} {
		got, err := os.ReadFile(item.path)
		if err != nil || !bytes.Equal(got, item.want) {
			t.Errorf("run(%v) changed %q after a partial check; read error = %v", args, item.path, err)
		}
	}
}

func TestRunCheckMalformedGraphIsMissing(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	graphPath := graph.WiringPath(filepath.Join(root, "graft"))
	if err := os.MkdirAll(filepath.Dir(graphPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(graphPath, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"check", root}
	if status := run(args, &stdout, &stderr); status != 1 {
		t.Fatalf("run(%v) status = %d, want 1; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "graph check: NO GRAPH") {
		t.Errorf("run(%v) stdout = %q, want malformed graph treated as missing", args, stdout.String())
	}
}

func TestRunCheckJSONUsesTypeScriptFieldNames(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	writeCheckSource(t, root, "app.ts", "export function app() {}\n")
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"check", root, "--json"}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal(run(%v)) error = %v; stdout = %q", args, err, stdout.String())
	}
	if _, ok := got["context"]; ok {
		t.Errorf("run(%v) JSON keys = %#v, want context omitted", args, got)
	}
	var graphResult map[string]json.RawMessage
	if err := json.Unmarshal(got["graph"], &graphResult); err != nil {
		t.Fatalf("json.Unmarshal(graph) error = %v", err)
	}
	for _, key := range []string{"ok", "missing", "added", "removed", "changed"} {
		if _, ok := graphResult[key]; !ok {
			t.Errorf("run(%v) graph keys = %#v, want %q", args, graphResult, key)
		}
	}
	for _, key := range []string{"stale", "pending", "pendingIds", "nodes", "unsupported", "errors", "partial"} {
		if _, ok := graphResult[key]; ok {
			t.Errorf("run(%v) graph keys = %#v, want %q omitted for a clean graph", args, graphResult, key)
		}
	}
}

func writeCheckSource(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
