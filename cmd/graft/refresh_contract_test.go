package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func TestQueryRefreshContract(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "app.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("export function oldName() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "graft")
	built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: outDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Write(built.Graph, outDir); err != nil {
		t.Fatal(err)
	}
	if err := graph.WriteFingerprint(outDir, graph.ExtractorID, built.Fingerprints, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("export function newName() { return 'freshmarker'; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		args       []string
		want       string
		wantAbsent string
		wantNote   bool
	}{
		{name: "disabled", args: []string{"skeleton", "src/app.ts", root, "--no-refresh"}, want: "oldName", wantAbsent: "newName"},
		{name: "automatic", args: []string{"skeleton", "src/app.ts", root}, want: "newName", wantAbsent: "oldName", wantNote: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if status := run(tt.args, &stdout, &stderr); status != 0 {
				t.Fatalf("run(%v) status = %d, stderr = %q", tt.args, status, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.want) || strings.Contains(stdout.String(), tt.wantAbsent) {
				t.Errorf("run(%v) stdout = %q, want %q without %q", tt.args, stdout.String(), tt.want, tt.wantAbsent)
			}
			if got := strings.Contains(stderr.String(), "[graft] refreshed the graph"); got != tt.wantNote {
				t.Errorf("run(%v) refresh note = %t, want %t; stderr = %q", tt.args, got, tt.wantNote, stderr.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"ask", "freshmarker", root, "--json"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(ask freshmarker) status = %d, stderr = %q", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "newName · function") {
		t.Errorf("run(ask freshmarker) stdout = %q, want newName body-text hit", stdout.String())
	}
}

func TestWorkspaceQueryPreservesUnsupportedChildGraph(t *testing.T) {
	root := t.TempDir()
	childRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(filepath.Join(childRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "src", "app.ts"), []byte("export function current() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "src", "other.zig"), []byte("fn other() void {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childContext := filepath.Join(childRoot, "graft")
	legacy := graph.GraphV1{Meta: graph.GraphMeta{Version: 1}, Nodes: []graph.NodeV1{{ID: "legacy", Name: "legacy", Kind: "function", Path: "src/app.ts", Span: "L1-L1"}}}
	path, err := graph.Write(legacy, childContext)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parentContext := filepath.Join(root, "graft")
	if err := os.MkdirAll(parentContext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentContext, "workspace.json"), []byte(`{"version":1,"children":["web"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"map", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(map %q) status = %d, stderr = %q", root, status, stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("run(map %q) replaced a child graph containing unsupported Go source", root)
	}
}
