package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestRetrievalTextOmitsSavingsNarrative(t *testing.T) {
	root := t.TempDir()
	source := "package cache\nfunc QueryCache() {}\n" + strings.Repeat("// additional source context\n", 1000)
	writeFixtureFile(t, root, "src/cache.go", source)
	wiring := graph.GraphV1{Nodes: []graph.NodeV1{
		{ID: "src/cache.go", Name: "cache.go", Kind: "file", Path: "src/cache.go", Span: "L1-L1002", Chars: new(len(source))},
		{ID: "src/cache.go#QueryCache", Name: "QueryCache", Kind: "function", Path: "src/cache.go", Span: "L2-L2"},
	}}
	if _, err := graph.Write(wiring, filepath.Join(root, "graft")); err != nil {
		t.Fatalf("graph.Write(%q) error = %v, want nil", root, err)
	}

	for _, tt := range []struct {
		name   string
		args   []string
		prefix string
	}{
		{name: "map", args: []string{"map", root}, prefix: "repo map —"},
		{name: "skeleton", args: []string{"skeleton", "src/cache.go", root}, prefix: "graft skeleton —"},
		{name: "callers", args: []string{"callers", "QueryCache", root}, prefix: "QueryCache · function"},
		{name: "grep", args: []string{"grep", "QueryCache", root}, prefix: `"QueryCache" —`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if status := run(tt.args, &stdout, &stderr); status != 0 {
				t.Fatalf("run(%v) status = %d, stderr = %q, want 0", tt.args, status, stderr.String())
			}
			if !strings.HasPrefix(stdout.String(), tt.prefix) {
				t.Errorf("run(%v) stdout = %q, want content starting with %q", tt.args, stdout.String(), tt.prefix)
			}
			if strings.Contains(stdout.String(), "tokens saved") || strings.Contains(stdout.String(), "At the end of your reply") {
				t.Errorf("run(%v) stdout = %q, want retrieval content without savings instructions", tt.args, stdout.String())
			}

			stdout.Reset()
			stderr.Reset()
			args := append(tt.args, "--json")
			if status := run(args, &stdout, &stderr); status != 0 {
				t.Fatalf("run(%v) status = %d, stderr = %q, want 0", args, status, stderr.String())
			}
			var payload struct {
				Saved *struct {
					Files         int `json:"files"`
					BaselineChars int `json:"baselineChars"`
				} `json:"saved"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("run(%v) JSON error = %v, want nil", args, err)
			}
			if payload.Saved == nil || payload.Saved.Files != 1 || payload.Saved.BaselineChars != len(source) {
				t.Errorf("run(%v).saved = %+v, want 1 file and %d baseline chars", args, payload.Saved, len(source))
			}
		})
	}
}
