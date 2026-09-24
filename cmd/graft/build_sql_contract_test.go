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

func TestBuildSQLParseFailureDegradesToFileNodeContract(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{
		"schema.sql": "CREATE TABLE app.users (id bigint;\n",
		"main.ts":    "export function run() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 || !strings.Contains(stderr.String(), "schema.sql: 1 of 1 SQL statements not indexed") {
		t.Fatalf("run(build %q) = (status %d, stderr %q), want 0 and a schema.sql limitation", root, status, stderr.String())
	}
	wiring, err := graph.Read(graph.WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatalf("graph.Read(wiring) error = %v", err)
	}
	var ids []string
	for _, node := range wiring.Nodes {
		ids = append(ids, node.ID)
	}
	if want := []string{"main.ts", "main.ts#run", "schema.sql"}; !slices.Equal(ids, want) {
		t.Errorf("run(build %q) node ids = %q, want %q", root, ids, want)
	}
}

func TestBuildReusesStoredOnlyDirsContract(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"src/inside.ts", "outside.ts"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("export function mark() {}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{"build", root, "--only-dir", "src"}, {"build", root}} {
		stdout.Reset()
		stderr.Reset()
		if status := run(args, &stdout, &stderr); status != 0 {
			t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
		}
	}
	outDir := filepath.Join(root, "graft")
	fingerprint, err := graph.ReadFingerprint(outDir, graph.ExtractorID)
	if err != nil || fingerprint == nil || !slices.Equal(fingerprint.OnlyDirs, []string{"src"}) || len(fingerprint.Files) != 1 {
		t.Errorf("ReadFingerprint(%q) = %#v, %v, want persisted src scope", outDir, fingerprint, err)
	}
	loaded, err := graph.Read(graph.WiringPath(outDir))
	if err != nil {
		t.Fatalf("Read(%q) error = %v", graph.WiringPath(outDir), err)
	}
	if slices.ContainsFunc(loaded.Nodes, func(node graph.NodeV1) bool { return node.Path == "outside.ts" }) {
		t.Errorf("run(build %q) nodes = %#v, want stored src scope", root, loaded.Nodes)
	}
}

func TestBuildNoReuseReparsesUnchangedSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("export function run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"build", root}, "parsed: 1 of 1 files (0 replayed"},
		{[]string{"build", root}, "parsed: 0 of 1 files (1 replayed"},
		{[]string{"build", root, "--no-reuse"}, "parsed: 1 of 1 files (0 replayed"},
	} {
		var stdout, stderr bytes.Buffer
		if status := run(tt.args, &stdout, &stderr); status != 0 {
			t.Fatalf("run(%v) status = %d, want 0; stderr = %q", tt.args, status, stderr.String())
		}
		if !strings.Contains(stdout.String(), tt.want) {
			t.Errorf("run(%v) stdout = %q, want substring %q", tt.args, stdout.String(), tt.want)
		}
	}
}
