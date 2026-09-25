package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func TestAskExpandsNamedTopHit(t *testing.T) {
	root := t.TempDir()
	body := "func ProbeDrift(paths []string) int {\n" + strings.Repeat("\tpaths = append(paths, \"drift probe fast path\")\n", 14) + "\treturn len(paths)\n}"
	files := map[string]string{
		"go.mod":        "module example.com/m\n\ngo 1.27\n",
		"drift/pkg.go":  "package drift\n\n" + body + "\n",
		"drift/note.go": "package drift\n\nfunc noteDrift() string { return \"drift\" }\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "graft")
	built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Write(built.Graph, dir); err != nil {
		t.Fatal(err)
	}
	if err := graph.WriteFingerprint(dir, graph.ExtractorID, built.Fingerprints, nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		query    string
		budget   string
		wantFull bool
	}{
		{name: "query names the function", query: "probedrift fast paths", budget: "2000", wantFull: true},
		{name: "qualified mention", query: "how does drift.ProbeDrift work", budget: "2000", wantFull: true},
		{name: "descriptive query", query: "how does drift probing work", budget: "2000", wantFull: false},
		{name: "definition exceeds half the budget", query: "ProbeDrift fast paths", budget: "256", wantFull: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if status := run([]string{"ask", tc.query, root, "--source", "--json", "--budget", tc.budget}, &out, &diagnostic); status != 0 {
				t.Fatalf("run(ask %q) = %d, %q", tc.query, status, diagnostic.String())
			}
			var result struct {
				Hits []struct{ Title, Code string }
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("ask(%q) output %q: %v", tc.query, out.String(), err)
			}
			if len(result.Hits) == 0 || !strings.HasPrefix(result.Hits[0].Title, "ProbeDrift") {
				t.Fatalf("ask(%q) hits = %+v, want ProbeDrift first", tc.query, result.Hits)
			}
			if got := result.Hits[0].Code == body; got != tc.wantFull {
				t.Errorf("ask(%q, budget %s) top hit complete = %t, want %t; code %q", tc.query, tc.budget, got, tc.wantFull, result.Hits[0].Code)
			}
		})
	}
}
