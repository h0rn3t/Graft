package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildAnalyzeRepo writes a small Go and Python repository and builds its graph.
func buildAnalyzeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, source := range map[string]string{
		"go.mod": "module example.com/app\n",
		"store/store.go": "package store\n\ntype Getter interface{ Get(k string) string }\n\n" +
			"type Store struct{}\n\nfunc NewStore() *Store { return &Store{} }\n\n" +
			"func (s *Store) Get(k string) string {\n\tif k == \"\" || k == \"-\" {\n\t\treturn \"\"\n\t}\n\treturn k\n}\n",
		"cmd/main.go": "package main\n\nimport (\n\t\"net/http\"\n\n\t\"example.com/app/store\"\n)\n\n" +
			"func read(g store.Getter) string { return g.Get(\"k\") }\n\n" +
			"func main() {\n\tread(store.NewStore())\n\thttp.HandleFunc(\"GET /items\", items)\n}\n\n" +
			"func items(w http.ResponseWriter, r *http.Request) {}\n\nfunc unused() {}\n",
	} {
		writeCheckSource(t, root, filepath.FromSlash(rel), source)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root, "--no-lsp"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	return root
}

func TestAnalyzeCommandsContract(t *testing.T) {
	root := buildAnalyzeRepo(t)
	tests := []struct {
		name       string
		args       []string
		wantStatus int
		want       []string // substrings of stdout, or of stderr when the status is 1 and stdout is empty
		notWant    []string
	}{
		{
			name: "path through an interface",
			args: []string{"path", "main", "Store.Get", root},
			want: []string{"main → Store.Get · 3 steps\n", "→ calls  read · function", "→ calls (inferred)  Get · method · store/store.go", "→ dispatches to (inferred)  Get · method · store/store.go"},
		},
		{
			name: "path json",
			args: []string{"path", "main", "NewStore", root, "--json"},
			want: []string{`"from": "main"`, `"relation": "calls"`, `"name": "NewStore"`},
		},
		{
			name: "no path is an answer, not a failure",
			args: []string{"path", "NewStore", "main", root},
			want: []string{"no path from NewStore to main within 10 steps"},
		},
		{name: "path to an unknown symbol", args: []string{"path", "main", "nothing", root}, wantStatus: 1, want: []string{`no symbol "nothing"`}},
		{name: "path with a bad depth", args: []string{"path", "main", "read", root, "-d", "0"}, wantStatus: 1, want: []string{"--depth must be a positive number"}},
		{
			name:    "dead hides low confidence",
			args:    []string{"dead", root},
			want:    []string{"dead code — 1 high, 0 medium confidence", "high · nothing calls or references it", "unused · function · cmd/main.go"},
			notWant: []string{"items"},
		},
		{
			name: "dead json counts every class",
			args: []string{"dead", root, "--json", "--all"},
			want: []string{`"high": 1`, `"name": "unused"`, `"confidence": "high"`},
		},
		{
			name: "complexity lists the most complex first",
			args: []string{"complexity", root, "-n", "1"},
			want: []string{"complexity — top 1 of", "     3  Get · method · store/store.go"},
		},
		{name: "complexity under the threshold passes", args: []string{"complexity", root, "--threshold", "3"}, want: []string{"no function above 3"}},
		{name: "complexity over the threshold fails", args: []string{"complexity", root, "--threshold", "2"}, wantStatus: 1, want: []string{"1 of", "functions above 2", "Get · method"}},
		{name: "complexity rejects a bad limit", args: []string{"complexity", root, "-n", "x"}, wantStatus: 1, want: []string{`--limit must be a positive integer, got "x"`}},
		{name: "no cycles", args: []string{"cycles", root}, want: []string{"no dependency cycles between directories"}},
		{name: "cycles rejects a level", args: []string{"cycles", root, "--level", "pkg"}, wantStatus: 1, want: []string{`--level must be "dir" or "file"`}},
		{name: "routes", args: []string{"routes", root}, want: []string{"1 HTTP routes", "GET  /items  cmd/main.go:13 → items · function · cmd/main.go"}},
		{name: "hotspots need git history", args: []string{"hotspots", root}, wantStatus: 1, want: []string{"could not read git history"}},
		{name: "an unknown prefix fails", args: []string{"dead", root, "--in", "nowhere/"}, wantStatus: 1, want: []string{"nowhere"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(append(tt.args, "--no-refresh"), &stdout, &stderr)
			if status != tt.wantStatus {
				t.Fatalf("run(%v) status = %d, want %d; stdout = %q, stderr = %q", tt.args, status, tt.wantStatus, stdout.String(), stderr.String())
			}
			output := stdout.String()
			if status == 1 && output == "" {
				output = stderr.String()
			}
			for _, want := range tt.want {
				if !strings.Contains(output, want) {
					t.Errorf("run(%v) = %q, want it to contain %q", tt.args, output, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(output, notWant) {
					t.Errorf("run(%v) = %q, want it without %q", tt.args, output, notWant)
				}
			}
		})
	}
}

func TestHotspotsReadsGitHistory(t *testing.T) {
	root := buildAnalyzeRepo(t)
	commit := []string{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-a", "-m", "change"}
	for index, args := range [][]string{{"init", "-q"}, {"add", "store", "cmd"}, commit, nil, commit} {
		if args == nil { // a second commit that touches store.go alone
			writeCheckSource(t, root, filepath.Join("store", "store.go"), "// Package store keeps values.\n"+mustReadFile(t, filepath.Join(root, "store", "store.go")))
			continue
		}
		git := exec.Command("git", args...)
		git.Dir = root
		if output, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git step %d %v error = %v, output = %q", index, args, err, output)
		}
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"hotspots", root, "--no-refresh", "--json"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(hotspots) status = %d, want 0; stderr = %q", status, stderr.String())
	}
	var result struct {
		Files []struct {
			Path    string `json:"path"`
			Score   int    `json:"score"`
			Commits int    `json:"commits"`
		} `json:"files"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal(hotspots) error = %v, output = %q", err, stdout.String())
	}
	// Both files sum to complexity 4; store.go changed twice, main.go once.
	want := "[{store/store.go 100 2} {cmd/main.go 50 1}]"
	if got := fmt.Sprint(result.Files); got != want {
		t.Errorf("run(hotspots) files = %s, want %s", got, want)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	return string(data)
}

func TestMCPTraceCallsTo(t *testing.T) {
	root := buildAnalyzeRepo(t)
	contextDir := filepath.Join(root, "graft")
	got := mcpCall(t.Context(), root, contextDir, "", "graft_trace_calls", map[string]any{"symbol": "main", "to": "NewStore"})
	if got.isError || !strings.Contains(got.text, "main → NewStore · 1 steps") {
		t.Errorf("mcpCall(graft_trace_calls, to) = %#v, want the one-step path", got)
	}
	got = mcpCall(t.Context(), root, contextDir, "", "graft_trace_calls", map[string]any{"symbol": "main", "to": "missing"})
	if !got.isError || !strings.Contains(got.text, `no symbol "missing"`) {
		t.Errorf("mcpCall(graft_trace_calls, to missing) = %#v, want an unknown-symbol error", got)
	}
}
