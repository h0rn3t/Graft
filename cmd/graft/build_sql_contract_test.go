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

func TestBuildSQLParseFailurePreservesGraphContract(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "schema.sql")
	if err := os.WriteFile(file, []byte("CREATE TABLE app.users (id bigint);\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", file, err)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	outDir := filepath.Join(root, "graft")
	graphPath := graph.WiringPath(outDir)
	fingerprintPath, err := graph.FingerprintPath(outDir, graph.ExtractorID)
	if err != nil {
		t.Fatalf("FingerprintPath(%q, %q) error = %v", outDir, graph.ExtractorID, err)
	}
	beforeGraph, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", graphPath, err)
	}
	beforeFingerprint, err := os.ReadFile(fingerprintPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", fingerprintPath, err)
	}
	if err := os.WriteFile(file, []byte("CREATE TABLE app.users (id bigint;\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", file, err)
	}
	stdout.Reset()
	stderr.Reset()
	if status := run([]string{"build", root}, &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), "schema.sql") {
		t.Errorf("run(build %q) = (status %d, stderr %q), want nonzero and SQL diagnostic", root, status, stderr.String())
	}
	for _, item := range []struct {
		path string
		want []byte
	}{{graphPath, beforeGraph}, {fingerprintPath, beforeFingerprint}} {
		got, err := os.ReadFile(item.path)
		if err != nil {
			t.Errorf("ReadFile(%q) error = %v", item.path, err)
			continue
		}
		if !bytes.Equal(got, item.want) {
			t.Errorf("run(build %q) changed %q after SQL parse failure", root, item.path)
		}
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
