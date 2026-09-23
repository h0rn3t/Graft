package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

func TestRunCallersContract(t *testing.T) {
	dir := callersFixture(t)

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		check      func(t *testing.T, stdout, stderr string)
	}{
		{
			name:       "dotted resolution and bfs depth in json",
			args:       []string{"callers", "pkg.root", dir, "--depth", "2", "--json"},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stderr != "" {
					t.Errorf("run(%v) stderr = %q, want empty stderr", []string{"callers", "pkg.root"}, stderr)
				}
				var payload struct {
					Query   string `json:"query"`
					Matches []struct {
						Symbol struct {
							Name string `json:"name"`
						} `json:"symbol"`
						Hits []struct {
							ID    string `json:"id"`
							Depth int    `json:"depth"`
						} `json:"hits"`
					} `json:"matches"`
				}
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"callers", "pkg.root"}, stdout, err)
				}
				if payload.Query != "pkg.root" {
					t.Errorf("run(%v) query = %q, want %q", []string{"callers", "pkg.root"}, payload.Query, "pkg.root")
				}
				if len(payload.Matches) != 1 || payload.Matches[0].Symbol.Name != "root" {
					t.Errorf("run(%v) matches = %#v, want one root match", []string{"callers", "pkg.root"}, payload.Matches)
				}
				if len(payload.Matches[0].Hits) != 2 {
					t.Errorf("run(%v) hits = %#v, want caller and transitive caller", []string{"callers", "pkg.root"}, payload.Matches[0].Hits)
				}
				if got := payload.Matches[0].Hits[0].Depth; got != 1 {
					t.Errorf("run(%v) first hit depth = %d, want 1", []string{"callers", "pkg.root"}, got)
				}
				if got := payload.Matches[0].Hits[1].Depth; got != 2 {
					t.Errorf("run(%v) second hit depth = %d, want 2", []string{"callers", "pkg.root"}, got)
				}
			},
		},
		{
			name:       "missing graph",
			args:       []string{"callers", "root", t.TempDir()},
			wantStatus: 1,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stdout != "" {
					t.Errorf("run(%v) stdout = %q, want empty stdout", []string{"callers", "root"}, stdout)
				}
				if !strings.Contains(stderr, "run `graft build` first") {
					t.Errorf("run(%v) stderr = %q, want graft build guidance", []string{"callers", "root"}, stderr)
				}
			},
		},
		{
			name:       "invalid direction",
			args:       []string{"callers", "root", dir, "--direction", "sideways"},
			wantStatus: 1,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if !strings.Contains(stderr, `--direction must be "in" or "out"`) {
					t.Errorf("run(%v) stderr = %q, want direction validation", []string{"callers", "root"}, stderr)
				}
			},
		},
		{
			name:       "invalid depth",
			args:       []string{"callers", "root", dir, "--depth", "banana"},
			wantStatus: 1,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if !strings.Contains(stderr, `--depth must be a positive number or "all"`) {
					t.Errorf("run(%v) stderr = %q, want depth validation", []string{"callers", "root"}, stderr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tt.args, &stdout, &stderr)
			if status != tt.wantStatus {
				t.Errorf("run(%v) status = %d, want %d", tt.args, status, tt.wantStatus)
			}
			tt.check(t, stdout.String(), stderr.String())
		})
	}
}

func callersFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := graphNode("src/root.ts#root", "root", "function", "src/root.ts")
	caller := graphNode("src/caller.ts#caller", "caller", "function", "src/caller.ts")
	top := graphNode("src/top.ts#top", "top", "function", "src/top.ts")
	fixture := graph.GraphV1{
		Meta:  graph.GraphMeta{Version: 1, NodeCount: 3, EdgeCount: 2, Languages: []string{"ts"}},
		Nodes: []graph.NodeV1{root, caller, top},
		Edges: []graph.EdgeV1{
			graphEdge(caller.ID, root.ID),
			graphEdge(top.ID, caller.ID),
		},
	}
	if _, err := graph.Write(fixture, filepath.Join(dir, "graft")); err != nil {
		t.Fatalf("graph.Write(%q) error = %v", dir, err)
	}
	return dir
}

func graphNode(id, name, kind, path string) graph.NodeV1 {
	return graph.NodeV1{ID: id, Name: name, Kind: graph.Kind(kind), Path: path, Span: "L1-L1"}
}

func graphEdge(source, target string) graph.EdgeV1 {
	return graph.EdgeV1{Source: source, Target: target, Relation: "calls", Confidence: "extracted"}
}

func workspaceMapFixture(t *testing.T) (root, contextDir string) {
	t.Helper()
	root = t.TempDir()
	contextDir = filepath.Join(root, "graft")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", contextDir, err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "workspace.json"), []byte(`{"version":1,"children":["web","missing","api"]}`), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(contextDir, "workspace.json"), err)
	}
	for _, child := range []string{"api", "web"} {
		childRoot := filepath.Join(root, child)
		sourcePath := filepath.Join(childRoot, "src", "app.ts")
		if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(sourcePath), err)
		}
		if err := os.WriteFile(sourcePath, []byte("export function worker() {}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v, want nil", sourcePath, err)
		}
		outDir := filepath.Join(childRoot, "graft")
		built, err := graph.BuildGraph(childRoot, sourcefiles.Options{OutDir: outDir})
		if err != nil {
			t.Fatalf("BuildGraph(%q) error = %v, want nil", childRoot, err)
		}
		if _, err := graph.Write(built.Graph, outDir); err != nil {
			t.Fatalf("Write(BuildGraph(%q), %q) error = %v, want nil", childRoot, outDir, err)
		}
		if err := graph.WriteFingerprint(outDir, graph.ExtractorID, built.Fingerprints, nil); err != nil {
			t.Fatalf("WriteFingerprint(%q, go-v1, files, nil) error = %v, want nil", outDir, err)
		}
	}
	return root, contextDir
}

func TestRunSkeletonContract(t *testing.T) {
	dir := skeletonFixture(t)

	tests := []struct {
		name      string
		args      []string
		wantFile  string
		wantNames []string
		wantNote  string
	}{
		{
			name:      "exact path",
			args:      []string{"skeleton", "src/api.ts", dir, "--json"},
			wantFile:  "src/api.ts",
			wantNames: []string{"first", "second"},
		},
		{
			name:      "unique basename",
			args:      []string{"skeleton", "api.ts", dir, "--json"},
			wantFile:  "src/api.ts",
			wantNames: []string{"first", "second"},
		},
		{
			name:     "empty definitions",
			args:     []string{"skeleton", "missing.ts", dir, "--json"},
			wantFile: "missing.ts",
			wantNote: "no definitions indexed for this file",
		},
		{
			name:     "missing graph",
			args:     []string{"skeleton", "api.ts", t.TempDir(), "--json"},
			wantFile: "api.ts",
			wantNote: "no wiring graph — run `graft build` first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tt.args, &stdout, &stderr)
			if status != 0 {
				t.Errorf("run(%v) status = %d, want 0", tt.args, status)
			}
			if stderr.String() != "" {
				t.Errorf("run(%v) stderr = %q, want empty stderr", tt.args, stderr.String())
			}
			var payload struct {
				File    string `json:"file"`
				Entries []struct {
					Name string `json:"name"`
				} `json:"entries"`
				Note string `json:"note"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("run(%v) stdout = %q, json error = %v", tt.args, stdout.String(), err)
			}
			if payload.File != tt.wantFile {
				t.Errorf("run(%v) file = %q, want %q", tt.args, payload.File, tt.wantFile)
			}
			if len(payload.Entries) != len(tt.wantNames) {
				t.Errorf("run(%v) entries = %#v, want %v", tt.args, payload.Entries, tt.wantNames)
			}
			for index, want := range tt.wantNames {
				if index < len(payload.Entries) && payload.Entries[index].Name != want {
					t.Errorf("run(%v) entry %d = %q, want %q", tt.args, index, payload.Entries[index].Name, want)
				}
			}
			if payload.Note != tt.wantNote {
				t.Errorf("run(%v) note = %q, want %q", tt.args, payload.Note, tt.wantNote)
			}
		})
	}

	ambiguous := skeletonAmbiguousFixture(t)
	var stdout, stderr bytes.Buffer
	if status := run([]string{"skeleton", "api.ts", ambiguous, "--json"}, &stdout, &stderr); status != 0 {
		t.Errorf("run(%v) status = %d, want 0", []string{"skeleton", "api.ts"}, status)
	}
	var payload struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"skeleton", "api.ts"}, stdout.String(), err)
	}
	if payload.Note != "ambiguous — matches: src/a/api.ts, src/b/api.ts" {
		t.Errorf("run(%v) note = %q, want ambiguous basename note", []string{"skeleton", "api.ts"}, payload.Note)
	}
}

func TestRunGrepContract(t *testing.T) {
	dir := grepFixture(t)

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		check      func(t *testing.T, stdout, stderr string)
	}{
		{
			name:       "json success",
			args:       []string{"grep", "NEEDLE", dir, "--json"},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stderr != "" {
					t.Errorf("run(%v) stderr = %q, want empty stderr", []string{"grep", "NEEDLE"}, stderr)
				}
				var payload graph.GrepResult
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"grep", "NEEDLE"}, stdout, err)
				}
				if payload.Pattern != "NEEDLE" || payload.FilesSearched != 2 || payload.TotalHits != 3 {
					t.Errorf("run(%v) payload = %#v, want NEEDLE, two files, three hits", []string{"grep", "NEEDLE"}, payload)
				}
				if len(payload.Groups) != 3 || payload.Groups[0].Symbol == nil || payload.Groups[0].Symbol.Name != "root" {
					t.Errorf("run(%v) groups = %#v, want root first and three groups", []string{"grep", "NEEDLE"}, payload.Groups)
				}
			},
		},
		{
			name:       "json empty",
			args:       []string{"grep", "ABSENT", dir, "--json"},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				var payload graph.GrepResult
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"grep", "ABSENT"}, stdout, err)
				}
				if payload.Groups == nil || len(payload.Groups) != 0 || payload.TotalHits != 0 || stderr != "" {
					t.Errorf("run(%v) = (%#v, %q), want empty groups, zero hits, empty stderr", []string{"grep", "ABSENT"}, payload, stderr)
				}
			},
		},
		{
			name:       "fixed case insensitive path filter",
			args:       []string{"grep", "needle", dir, "--fixed", "--ignore-case", "--in", "src/a.ts", "--json"},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				var payload graph.GrepResult
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"grep", "needle"}, stdout, err)
				}
				if payload.FilesSearched != 1 || payload.TotalHits != 2 || stderr != "" {
					t.Errorf("run(%v) = (%#v, %q), want one file, two hits, empty stderr", []string{"grep", "needle"}, payload, stderr)
				}
			},
		},
		{
			name:       "missing graph",
			args:       []string{"grep", "NEEDLE", t.TempDir(), "--json"},
			wantStatus: 1,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stdout != "" || stderr != "✗ no graph — run graft build first\n" {
					t.Errorf("run(%v) = (%q, %q), want empty stdout and missing graph diagnostic", []string{"grep", "NEEDLE"}, stdout, stderr)
				}
			},
		},
		{
			name:       "invalid pattern",
			args:       []string{"grep", "[", dir, "--json"},
			wantStatus: 1,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stdout != "" || !strings.Contains(stderr, `✗ invalid pattern "[":`) {
					t.Errorf("run(%v) = (%q, %q), want invalid pattern diagnostic", []string{"grep", "["}, stdout, stderr)
				}
			},
		},
		{
			name:       "unknown prefix",
			args:       []string{"grep", "NEEDLE", dir, "--in", "missing", "--json"},
			wantStatus: 1,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stdout != "" || !strings.Contains(stderr, `✗ nothing indexed under "missing/"`) {
					t.Errorf("run(%v) = (%q, %q), want unknown prefix diagnostic", []string{"grep", "NEEDLE"}, stdout, stderr)
				}
			},
		},
		{
			name:       "human zero hit",
			args:       []string{"grep", "ABSENT", dir},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stdout != "" || !strings.Contains(stderr, `no hits for "ABSENT" in 2 indexed files`) {
					t.Errorf("run(%v) = (%q, %q), want zero-hit diagnostic on stderr", []string{"grep", "ABSENT"}, stdout, stderr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tt.args, &stdout, &stderr)
			if status != tt.wantStatus {
				t.Errorf("run(%v) status = %d, want %d", tt.args, status, tt.wantStatus)
			}
			tt.check(t, stdout.String(), stderr.String())
		})
	}
}

func TestRunMapContract(t *testing.T) {
	dir := mapCLIFixture(t)

	var stdout, stderr bytes.Buffer
	status := run([]string{"map", dir, "--json"}, &stdout, &stderr)
	if status != 0 || stderr.String() != "" {
		t.Errorf("run(%v) = status %d, stderr %q, want 0 and empty stderr", []string{"map", dir}, status, stderr.String())
	}
	var payload graph.RepoMap
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"map", dir}, stdout.String(), err)
	}
	if payload.Totals.Files != 3 || payload.Totals.Symbols != 3 || payload.Totals.Edges != 2 {
		t.Errorf("run(%v) totals = %#v, want 3 files, 3 symbols, 2 edges", []string{"map", dir}, payload.Totals)
	}
	if len(payload.Dirs) != 3 || payload.Dirs[0].Path != "docs" {
		t.Errorf("run(%v) dirs = %#v, want docs first and three groups", []string{"map", dir}, payload.Dirs)
	}

	stdout.Reset()
	stderr.Reset()
	status = run([]string{"map", dir, "--max-dirs", "1", "--json"}, &stdout, &stderr)
	if status != 0 || stderr.String() != "" {
		t.Errorf("run(%v) = status %d, stderr %q, want 0 and empty stderr", []string{"map", dir, "--max-dirs", "1"}, status, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"map", dir, "--max-dirs", "1"}, stdout.String(), err)
	}
	if len(payload.Dirs) != 1 || payload.Dropped != 2 {
		t.Errorf("run(%v) dirs/dropped = (%d, %d), want (1, 2)", []string{"map", dir, "--max-dirs", "1"}, len(payload.Dirs), payload.Dropped)
	}

	stdout.Reset()
	stderr.Reset()
	status = run([]string{"map", dir, "--max-dirs", "0"}, &stdout, &stderr)
	if status != 1 || stdout.String() != "" || stderr.String() != "✗ --max-dirs must be a positive integer, got \"0\"\n" {
		t.Errorf("run(%v) = (%d, %q, %q), want invalid max-dirs diagnostic", []string{"map", dir, "--max-dirs", "0"}, status, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	status = run([]string{"map", dir}, &stdout, &stderr)
	if status != 0 || stderr.String() != "" || !strings.Contains(stdout.String(), "repo map — 3 files") || !strings.Contains(stdout.String(), "hotspots:") {
		t.Errorf("run(%v) = (%d, %q, %q), want human report", []string{"map", dir}, status, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	status = run([]string{"map", t.TempDir(), "--json"}, &stdout, &stderr)
	if status != 1 || stdout.String() != "" || stderr.String() != "✗ no graph — run graft build first\n" {
		t.Errorf("run(%v) = (%d, %q, %q), want missing graph diagnostic", []string{"map"}, status, stdout.String(), stderr.String())
	}
}

func TestRunWorkspaceMapContract(t *testing.T) {
	root, _ := workspaceMapFixture(t)
	var stdout, stderr bytes.Buffer
	status := run([]string{"map", root, "--json", "--max-dirs", "2"}, &stdout, &stderr)
	if status != 0 || stderr.String() != "" {
		t.Fatalf("run(%v) = (%d, %q), want success and empty stderr", []string{"map", root, "--json"}, status, stderr.String())
	}
	got := stdout.String()
	alpha := strings.Index(got, "## api/")
	web := strings.Index(got, "## web/")
	if !strings.HasPrefix(got, "workspace map — 2 repo(s)\n") || alpha < 0 || web <= alpha ||
		!strings.Contains(got, "2 of 3 workspace repos have graphs; run graft build to cover missing") {
		t.Errorf("run(%v) stdout = %q, want sorted child map sections and missing-graph coverage", []string{"map", root, "--json"}, got)
	}
}

func TestRunAskContract(t *testing.T) {
	dir := callersFixture(t)

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		check      func(t *testing.T, stdout, stderr string)
	}{
		{
			name:       "structural json",
			args:       []string{"ask", "who calls root", dir, "--json"},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stderr != "" {
					t.Errorf("run(%v) stderr = %q, want empty stderr", []string{"ask", "who calls root"}, stderr)
				}
				var payload struct {
					Mode    string `json:"mode"`
					Subject string `json:"subject"`
					Hits    []struct {
						Title string `json:"title"`
					} `json:"hits"`
				}
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"ask", "who calls root"}, stdout, err)
				}
				if payload.Mode != "structural" || payload.Subject != "root" || len(payload.Hits) != 1 || payload.Hits[0].Title != "caller" {
					t.Errorf("run(%v) payload = %#v, want structural root with caller hit", []string{"ask", "who calls root"}, payload)
				}
			},
		},
		{
			name:       "lexical json",
			args:       []string{"ask", "root", dir, "--json", "--no-graph-rank"},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				var payload graph.AskResult
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"ask", "root"}, stdout, err)
				}
				if payload.Mode != "lexical" || len(payload.Hits) == 0 || payload.Hits[0].Title != "root · function" || stderr != "" {
					t.Errorf("run(%v) payload = %#v, stderr = %q, want lexical root hit and empty stderr", []string{"ask", "root"}, payload, stderr)
				}
			},
		},
		{
			name:       "missing graph",
			args:       []string{"ask", "root", t.TempDir(), "--json"},
			wantStatus: 0,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				var payload graph.AskResult
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("run(%v) stdout = %q, json error = %v", []string{"ask", "root"}, stdout, err)
				}
				if payload.Mode != "empty" || len(payload.Hits) != 0 || stderr != "" {
					t.Errorf("run(%v) = (%#v, %q), want empty result and empty stderr", []string{"ask", "root"}, payload, stderr)
				}
			},
		},
		{
			name:       "unknown prefix",
			args:       []string{"ask", "root", dir, "--in", "missing", "--json"},
			wantStatus: 1,
			check: func(t *testing.T, stdout, stderr string) {
				t.Helper()
				if stdout != "" || !strings.Contains(stderr, `✗ nothing indexed under "missing/"`) {
					t.Errorf("run(%v) = (%q, %q), want unknown-prefix diagnostic", []string{"ask", "root"}, stdout, stderr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tt.args, &stdout, &stderr)
			if status != tt.wantStatus {
				t.Errorf("run(%v) status = %d, want %d", tt.args, status, tt.wantStatus)
			}
			tt.check(t, stdout.String(), stderr.String())
		})
	}
}

func grepFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixtureFile(t, dir, "src/a.ts", "NEEDLE module\nexport function root() {\n  NEEDLE root\n}\n")
	writeFixtureFile(t, dir, "src/b.ts", "export function other() {\n  NEEDLE other\n}\n")
	root := graphNode("src/a.ts#root", "root", "function", "src/a.ts")
	root.Span = "L2-L4"
	other := graphNode("src/b.ts#other", "other", "function", "src/b.ts")
	other.Span = "L1-L3"
	fileA := graphNode("src/a.ts", "a.ts", "file", "src/a.ts")
	fileA.Chars = new(59)
	fileB := graphNode("src/b.ts", "b.ts", "file", "src/b.ts")
	fileB.Chars = new(51)
	fixture := graph.GraphV1{
		Meta:  graph.GraphMeta{Version: 1, NodeCount: 4, EdgeCount: 2, Languages: []string{"ts"}},
		Nodes: []graph.NodeV1{fileA, root, fileB, other},
		Edges: []graph.EdgeV1{
			graphEdge("src/caller.ts#one", root.ID),
			graphEdge("src/caller.ts#two", root.ID),
		},
	}
	if _, err := graph.Write(fixture, filepath.Join(dir, "graft")); err != nil {
		t.Fatalf("graph.Write(%q) error = %v", dir, err)
	}
	return dir
}

func mapCLIFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fileA := graphNode("src/a.ts", "a.ts", "file", "src/a.ts")
	fileA.Chars = new(60)
	fileB := graphNode("tests/b.ts", "b.ts", "file", "tests/b.ts")
	fileB.Chars = new(50)
	fileC := graphNode("docs/c.py", "c.py", "file", "docs/c.py")
	fileC.Chars = new(40)
	one := graphNode("src/a.ts#one", "one", "function", "src/a.ts")
	two := graphNode("tests/b.ts#two", "two", "function", "tests/b.ts")
	three := graphNode("docs/c.py#three", "three", "function", "docs/c.py")
	fixture := graph.GraphV1{
		Meta:  graph.GraphMeta{Version: 1, NodeCount: 6, EdgeCount: 2, Languages: []string{"typescript", "python"}},
		Nodes: []graph.NodeV1{fileA, one, fileB, two, fileC, three},
		Edges: []graph.EdgeV1{graphEdge("caller#one", one.ID), graphEdge("caller#two", two.ID)},
	}
	if _, err := graph.Write(fixture, filepath.Join(dir, "graft")); err != nil {
		t.Fatalf("graph.Write(%q) error = %v", dir, err)
	}
	return dir
}

func writeFixtureFile(t *testing.T, root, path, text string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(absolute, []byte(text), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func skeletonFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	firstSignature := "first(a: number): number"
	firstSummary := "first summary\nmore detail"
	first := graphNode("src/api.ts#first", "first", "function", "src/api.ts")
	first.Span = "L1-L2"
	first.Signature = &firstSignature
	first.Summary = &firstSummary
	secondSignature := "second(b: string): string"
	second := graphNode("src/api.ts#second", "second", "function", "src/api.ts")
	second.Span = "L4-L5"
	second.Signature = &secondSignature
	file := graphNode("src/api.ts", "api.ts", "file", "src/api.ts")
	fileChars := 120
	file.Chars = &fileChars
	fixture := graph.GraphV1{
		Meta:  graph.GraphMeta{Version: 1, NodeCount: 3, EdgeCount: 0, Languages: []string{"ts"}},
		Nodes: []graph.NodeV1{second, first, file},
	}
	if _, err := graph.Write(fixture, filepath.Join(dir, "graft")); err != nil {
		t.Fatalf("graph.Write(%q) error = %v", dir, err)
	}
	return dir
}

func skeletonAmbiguousFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	first := graphNode("src/a/api.ts#first", "first", "function", "src/a/api.ts")
	second := graphNode("src/b/api.ts#second", "second", "function", "src/b/api.ts")
	fixture := graph.GraphV1{
		Meta:  graph.GraphMeta{Version: 1, NodeCount: 2, EdgeCount: 0, Languages: []string{"ts"}},
		Nodes: []graph.NodeV1{second, first},
	}
	if _, err := graph.Write(fixture, filepath.Join(dir, "graft")); err != nil {
		t.Fatalf("graph.Write(%q) error = %v", dir, err)
	}
	return dir
}

func TestRunBrainRefreshContract(t *testing.T) {
	type observedRequest struct {
		path          string
		authorization string
	}
	initial := []byte(`{"brainId":"brain/1","fetchedAt":1,"checkedAt":2,"rules":[{"ruleId":"old","symbol":"old#Symbol","fingerprint":"old-hash","rule":"old rule"}]}`)
	for _, tt := range []struct {
		name         string
		config       bool
		status       int
		response     string
		initialCache []byte
		wantCache    bool
		wantRequest  bool
		wantRule     graph.BrainRule
	}{
		{
			name:   "fetches and stores brain rules",
			config: true, status: http.StatusOK,
			response:  `{"anchors":[{"rule_id":"rule-1","symbol":"src/app.ts#Run","fingerprint":"body-hash","rule":"keep errors wrapped","source_url":"https://example.com/commit/1"}]}`,
			wantCache: true, wantRequest: true,
			wantRule: graph.BrainRule{RuleID: "rule-1", Symbol: "src/app.ts#Run", Fingerprint: "body-hash", Rule: "keep errors wrapped", SourceURL: "https://example.com/commit/1"},
		},
		{
			name:   "empty anchors are cached as an empty array",
			config: true, status: http.StatusOK, response: `{"anchors":[]}`,
			wantCache: true, wantRequest: true,
		},
		{
			name:   "upstream failure preserves the old cache",
			config: true, status: http.StatusServiceUnavailable, response: `unavailable`,
			initialCache: initial, wantCache: true, wantRequest: true,
		},
		{
			name:   "invalid response preserves the old cache",
			config: true, status: http.StatusOK, response: `{"anchors":{}}`,
			initialCache: initial, wantCache: true, wantRequest: true,
		},
		{
			name:   "unlinked repository is a silent no-op",
			status: http.StatusOK, response: `{"anchors":[]}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			contextDir := filepath.Join(root, "graft")
			cachePath := filepath.Join(contextDir, ".cache", "brain-rules.json")
			if tt.config {
				if err := os.MkdirAll(filepath.Join(root, ".graft"), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Join(root, ".graft"), err)
				}
				config := []byte(`{"brain":{"brainId":"brain/1","token":"test-token","baseUrl":"https://configured.invalid"}}`)
				if err := os.WriteFile(filepath.Join(root, ".graft", "config.json"), config, 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(root, ".graft", "config.json"), err)
				}
			}
			if tt.initialCache != nil {
				if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(cachePath), err)
				}
				if err := os.WriteFile(cachePath, tt.initialCache, 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", cachePath, err)
				}
			}
			requests := make(chan observedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- observedRequest{path: r.URL.EscapedPath(), authorization: r.Header.Get("Authorization")}
				w.WriteHeader(tt.status)
				if _, err := io.WriteString(w, tt.response); err != nil {
					t.Errorf("WriteString(response) error = %v, want nil", err)
				}
			}))
			t.Cleanup(server.Close)
			t.Setenv("GRAFT_BRAIN_ID", "")
			t.Setenv("GRAFT_BRAIN_TOKEN", "")
			t.Setenv("GRAFT_BRAIN_URL", server.URL)
			t.Setenv("GRAFT_DIR", "")

			var stdout, stderr bytes.Buffer
			if got := run([]string{"_brain-refresh", root, contextDir}, &stdout, &stderr); got != 0 {
				t.Errorf("run(_brain-refresh %q %q) = %d, want 0; stderr = %q", root, contextDir, got, stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("run(_brain-refresh %q %q) output = %q, %q, want empty", root, contextDir, stdout.String(), stderr.String())
			}
			if tt.wantRequest {
				select {
				case got := <-requests:
					if got.path != "/api/public/brains/brain%2F1/rules/anchors" || got.authorization != "Bearer test-token" {
						t.Errorf("brain refresh request = %#v, want encoded brain path and bearer token", got)
					}
				default:
					t.Errorf("run(_brain-refresh %q %q) made no request, want one", root, contextDir)
				}
			} else {
				select {
				case got := <-requests:
					t.Errorf("run(_brain-refresh %q %q) request = %#v, want none", root, contextDir, got)
				default:
				}
			}
			data, err := os.ReadFile(cachePath)
			if !tt.wantCache {
				if !os.IsNotExist(err) {
					t.Errorf("ReadFile(%q) error = %v, want not exist", cachePath, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v, want nil", cachePath, err)
			}
			if tt.initialCache != nil {
				if !bytes.Equal(data, tt.initialCache) {
					t.Errorf("brain cache after failed refresh = %s, want unchanged %s", data, tt.initialCache)
				}
				return
			}
			var cache struct {
				Rules     []graph.BrainRule `json:"rules"`
				FetchedAt int64             `json:"fetchedAt"`
			}
			if err := json.Unmarshal(data, &cache); err != nil {
				t.Fatalf("json.Unmarshal(brain cache) error = %v, want nil", err)
			}
			if cache.FetchedAt <= 0 {
				t.Errorf("brain cache fetchedAt = %d, want positive timestamp", cache.FetchedAt)
			}
			if tt.wantRule.RuleID != "" && (len(cache.Rules) != 1 || cache.Rules[0] != tt.wantRule) {
				t.Errorf("brain cache rules = %#v, want [%#v]", cache.Rules, tt.wantRule)
			}
			if tt.wantRule.RuleID == "" && (!strings.Contains(string(data), `"rules":[]`) || len(cache.Rules) != 0) {
				t.Errorf("brain cache = %s, want an encoded empty rules array", data)
			}
		})
	}
}
