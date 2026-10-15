package graph

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// askIndexGraphs builds a graph before and after an edit that changes a body,
// removes the symbol that first used a term, and adds a file, so both the
// document list and the order of the df array shift.
func askIndexGraphs(t *testing.T) (before, after GraphV1) {
	t.Helper()
	root := t.TempDir()
	build := func() GraphV1 {
		t.Helper()
		built, err := BuildGraph(root, sourcefiles.Options{Extensions: []string{".go", ".ts"}})
		if err != nil {
			t.Fatalf("BuildGraph(%q) error = %v, want nil", root, err)
		}
		return built.Graph
	}
	writeRefreshSource(t, root, "pkg/queue/queue.go", "package queue\n\n// Queue holds pending jobs.\ntype Queue struct{ jobs []string }\n\n"+
		"// Push appends a job; retries reuse the same job.\nfunc (q *Queue) Push(job string) { q.jobs = append(q.jobs, job) }\n")
	writeRefreshSource(t, root, "src/app.ts", "// Formats the Ünïcode greeting shown on start.\nexport function greet(name: string): string {\n"+
		"  return formatGreeting(name) + \" welcome\";\n}\n\nexport function scheduleJob(job: string) {\n  return job;\n}\n")
	writeRefreshSource(t, root, "src/format.ts", "export function formatGreeting(name: string) {\n  return `hello ${name}`;\n}\n")
	before = build()
	writeRefreshSource(t, root, "pkg/queue/queue.go", "package queue\n\n// Queue holds pending jobs.\ntype Queue struct{ jobs []string }\n")
	writeRefreshSource(t, root, "src/app.ts", "// Formats the Ünïcode farewell shown on exit.\nexport function greet(name: string): string {\n"+
		"  return formatGreeting(name) + \" farewell\";\n}\n\nexport function scheduleJob(job: string) {\n  return job;\n}\n")
	writeRefreshSource(t, root, "src/extra.ts", "export function extra() {\n  return \"welcome extra\";\n}\n")
	return before, build()
}

func readAskIndexFile(t *testing.T, outDir, name string) []byte {
	t.Helper()
	path := filepath.Join(outDir, ".cache", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v, want nil", path, err)
	}
	return data
}

func TestWriteAskIndexIncrementalMatchesFull(t *testing.T) {
	before, after := askIndexGraphs(t)
	incremental := t.TempDir()
	for _, wiring := range []GraphV1{before, after} {
		if err := WriteAskIndex(incremental, wiring); err != nil {
			t.Fatalf("WriteAskIndex(%q) error = %v, want nil", incremental, err)
		}
	}
	full := t.TempDir()
	if err := WriteAskIndex(full, after); err != nil {
		t.Fatalf("WriteAskIndex(%q) error = %v, want nil", full, err)
	}
	for _, name := range []string{"ask-index.json", "ask-terms.json"} {
		if got, want := readAskIndexFile(t, incremental, name), readAskIndexFile(t, full, name); !bytes.Equal(got, want) {
			t.Errorf("%s after an incremental write =\n%s\nwant the full write's\n%s", name, got, want)
		}
	}
}

func TestWriteAskIndexTermCache(t *testing.T) {
	_, after := askIndexGraphs(t)
	full := t.TempDir()
	if err := WriteAskIndex(full, after); err != nil {
		t.Fatalf("WriteAskIndex(%q) error = %v, want nil", full, err)
	}
	want := readAskIndexFile(t, full, "ask-index.json")
	key := sourcefiles.Hash("greet")
	tests := []struct {
		name   string
		cache  string
		reused bool
	}{
		{name: "not JSON", cache: `{"version":`},
		{name: "another version", cache: fmt.Sprintf(`{"version":%d,"bags":{%q:{"terms":["planted"],"counts":[1]}}}`, AskIndexVersion+1, key)},
		{name: "uneven bag", cache: fmt.Sprintf(`{"version":%d,"bags":{%q:{"terms":["planted"],"counts":[]}}}`, AskIndexVersion, key)},
		{name: "current bag", cache: fmt.Sprintf(`{"version":%d,"bags":{%q:{"terms":["planted"],"counts":[1]}}}`, AskIndexVersion, key), reused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()
			writeRefreshSource(t, outDir, ".cache/ask-terms.json", tt.cache)
			if err := WriteAskIndex(outDir, after); err != nil {
				t.Fatalf("WriteAskIndex(%q) with cache %s error = %v, want nil", outDir, tt.cache, err)
			}
			got := readAskIndexFile(t, outDir, "ask-index.json")
			if tt.reused {
				if !bytes.Contains(got, []byte(`"name":[["planted",1]]`)) {
					t.Errorf("WriteAskIndex(%q) with cache %s = %s, want the cached bag for greet", outDir, tt.cache, got)
				}
				return
			}
			if !bytes.Equal(got, want) {
				t.Errorf("WriteAskIndex(%q) with cache %s =\n%s\nwant the full write's\n%s", outDir, tt.cache, got, want)
			}
		})
	}
}

func TestWriteAskIndexEncodesLikeJSONJS(t *testing.T) {
	// The index is written by hand; jsonjs.Marshal of the documented shape is
	// the format it must keep, down to the escapes.
	for _, tc := range []struct {
		name, id, body string
	}{
		{name: "plain", id: "src/app.ts#greet", body: "greet the user by name"},
		{name: "quotes and backslashes", id: `src/a"b\c.ts#q`, body: `say "hi" \ twice`},
		{name: "control characters", id: "src/\x01\b\f\n\r\t\x1f.ts#c", body: "tab\tand\x00null"},
		{name: "html and line separators", id: "src/<a>&  .ts#h", body: "a < b && c > d next"},
		{name: "non-ASCII", id: "src/Ünïcode🙂.ts#u", body: "Ünïcode greeting 🙂"},
		{name: "invalid UTF-8", id: "src/bad\xff\xfe.ts#x", body: "bad \xff bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			node := NodeV1{ID: tc.id, Name: "symbol", Kind: "function", Path: "src/app.ts", BodyText: &body}
			outDir := t.TempDir()
			if err := WriteAskIndex(outDir, GraphV1{Nodes: []NodeV1{node}}); err != nil {
				t.Fatalf("WriteAskIndex(node %q) error = %v, want nil", tc.id, err)
			}
			pairs := func(bag askBag) [][]any {
				out := make([][]any, 0, len(bag.Terms))
				for i, term := range bag.Terms {
					out = append(out, []any{term, bag.Counts[i]})
				}
				return out
			}
			name, path, bodyBag := askOrderedCounts(node.Name), askOrderedCounts(node.Path), askOrderedCounts(askNodeBody(node))
			df := askBag{at: make(map[string]int)}
			for _, bag := range []askBag{name, path, bodyBag} {
				for _, term := range bag.Terms {
					if _, seen := df.at[term]; !seen {
						df.add(term, 1)
					}
				}
			}
			length := 0
			for _, count := range bodyBag.Counts {
				length += count
			}
			type document struct {
				ID   string  `json:"id"`
				Name [][]any `json:"name"`
				Path [][]any `json:"path"`
				Body [][]any `json:"body"`
			}
			want, err := jsonjs.Marshal(struct {
				Version    int        `json:"version"`
				AvgBodyLen float64    `json:"avgBodyLen"`
				DF         [][]any    `json:"df"`
				DocCount   int        `json:"docCount"`
				Docs       []document `json:"docs"`
			}{AskIndexVersion, float64(length), pairs(df), 1, []document{{tc.id, pairs(name), pairs(path), pairs(bodyBag)}}}, "")
			if err != nil {
				t.Fatalf("jsonjs.Marshal(index of %q) error = %v, want nil", tc.id, err)
			}
			if got := readAskIndexFile(t, outDir, "ask-index.json"); !bytes.Equal(got, append(want, '\n')) {
				t.Errorf("WriteAskIndex(node %q) wrote\n%q\nwant\n%q", tc.id, got, append(want, '\n'))
			}
		})
	}
}
