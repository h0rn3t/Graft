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

// writeReadContextFixture builds a Go repository where Run calls two helpers
// in its directory, one in another directory, and itself, next to a testdata
// copy of Run and two production definitions named Dup.
func writeReadContextFixture(t *testing.T, root string, bigHelper bool) {
	t.Helper()
	helperB := "func helperB() int {\n\treturn 2\n}\n"
	if bigHelper {
		helperB = "func helperB() int {\n" + strings.Repeat("\t_ = \"padding padding padding padding\"\n", 60) + "\treturn 2\n}\n"
	}
	files := map[string]string{
		"go.mod":                 "module example.com/m\n\ngo 1.27\n",
		"pkg/main.go":            "package pkg\n\nimport \"example.com/m/pkg/other\"\n\nfunc Run(n int) int {\n\tif n == 0 {\n\t\treturn helperA() + helperB() + other.Far()\n\t}\n\treturn Run(n - 1)\n}\n",
		"pkg/helpers.go":         "package pkg\n\nfunc helperA() int {\n\treturn 1\n}\n\n" + helperB,
		"pkg/other/far.go":       "package other\n\nfunc Far() int { return 3 }\n\nfunc Dup() int { return 4 }\n",
		"pkg/dup.go":             "package pkg\n\nfunc Dup() int { return 5 }\n",
		"testdata/pkg/copy.go":   "package pkg\n\nfunc Run(n int) int { return n }\n",
		"testdata/pkg/copy2.go":  "package pkg\n\nfunc Only() int { return 6 }\n",
		"testdata/pkg2/copy3.go": "package pkg2\n\nfunc Only() int { return 7 }\n",
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
}

type readContextJSON struct {
	ID, Pointer, Code, Note string
	Callees                 []struct{ Name, Pointer, SourceHash, Code string }
}

func readContext(t *testing.T, root string, args ...string) (readContextJSON, int, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	status := run(append([]string{"read"}, append(args, root, "--json")...), &out, &diagnostic)
	var result readContextJSON
	if status == 0 {
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("read %v output %q: %v", args, out.String(), err)
		}
	}
	return result, status, diagnostic.String()
}

func TestReadSymbolSelectorResolution(t *testing.T) {
	root := t.TempDir()
	writeReadContextFixture(t, root, false)
	for _, tc := range []struct {
		selector string
		wantID   string
		wantNote string
		wantErr  string
	}{
		{selector: "Run", wantID: "pkg/main.go#Run", wantNote: "testdata/pkg/copy.go#Run"},
		{selector: "pkg/helpers.go::Run", wantID: "pkg/main.go#Run", wantNote: "not in pkg/helpers.go"},
		{selector: "pkg/nowhere.go::helperA", wantID: "pkg/helpers.go#helperA", wantNote: "not in pkg/nowhere.go"},
		{selector: "pkg/main.go::Run", wantID: "pkg/main.go#Run"},
		{selector: "Dup", wantErr: "ambiguous"},
		{selector: "Only", wantErr: "ambiguous"},
		{selector: "pkg/helpers.go::Missing", wantErr: "no exact symbol"},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			result, status, diagnostic := readContext(t, root, tc.selector)
			if tc.wantErr != "" {
				if status == 0 || !strings.Contains(diagnostic, tc.wantErr) {
					t.Errorf("read(%q) = (%d, %+v, %q), want error %q", tc.selector, status, result, diagnostic, tc.wantErr)
				}
				return
			}
			if status != 0 || result.ID != tc.wantID {
				t.Fatalf("read(%q) = (%d, id %q, %q), want id %q", tc.selector, status, result.ID, diagnostic, tc.wantID)
			}
			if tc.wantNote == "" && result.Note != "" || !strings.Contains(result.Note, tc.wantNote) {
				t.Errorf("read(%q) note = %q, want %q", tc.selector, result.Note, tc.wantNote)
			}
		})
	}
	var out, diagnostic bytes.Buffer
	if status := run([]string{"read", "Dup", root}, &out, &diagnostic); status == 0 ||
		!strings.Contains(diagnostic.String(), "pkg/dup.go#Dup") || !strings.Contains(diagnostic.String(), "pkg/other/far.go#Dup") {
		t.Errorf("read(Dup) = (%d, %q), want both production candidates", status, diagnostic.String())
	}
}

func TestReadSymbolIncludesDirectCallees(t *testing.T) {
	root := t.TempDir()
	writeReadContextFixture(t, root, false)
	result, status, diagnostic := readContext(t, root, "Run")
	if status != 0 {
		t.Fatalf("read(Run) = (%d, %q), want success", status, diagnostic)
	}
	names := make([]string, 0, len(result.Callees))
	for _, callee := range result.Callees {
		names = append(names, callee.Name)
		if callee.Code == "" || callee.SourceHash != sourcefiles.Hash(callee.Code) {
			t.Errorf("read(Run) callee %s = %+v, want complete source with its hash", callee.Name, callee)
		}
	}
	if got := strings.Join(names, ","); got != "helperA,helperB" {
		t.Errorf("read(Run) callees = %q, want helperA,helperB (same directory, production, not itself)", got)
	}

	var out bytes.Buffer
	if status := run([]string{"read", "Run", root}, &out, &bytes.Buffer{}); status != 0 ||
		!strings.Contains(out.String(), "return 1") || !strings.Contains(out.String(), "helperB · function · pkg/helpers.go") {
		t.Errorf("read(Run) text = %q, want inlined callees", out.String())
	}

	mcp := mcpCall(t.Context(), root, filepath.Join(root, "graft"), "", "graft_read_symbol", map[string]any{"symbol": "Run"})
	if mcp.isError || !strings.Contains(mcp.text, "func Run") || !strings.Contains(mcp.text, "Direct callees in this directory") {
		t.Errorf("mcpCall(read_symbol Run) = %+v, want the exact read with callees", mcp)
	}

	batch, status, diagnostic := readContext(t, root, "Run", "--also", "helperA")
	if status != 0 || len(batch.Callees) != 0 {
		t.Errorf("read(Run --also helperA) = (%d, %+v, %q), want a batch without callees", status, batch, diagnostic)
	}
}

func TestReadSymbolCalleesFollowBudgetAndFreshness(t *testing.T) {
	root := t.TempDir()
	writeReadContextFixture(t, root, true)
	result, status, diagnostic := readContext(t, root, "Run", "--budget", "400")
	if status != 0 || len(result.Callees) != 2 {
		t.Fatalf("read(Run, budget 400) = (%d, %+v, %q), want two callees", status, result, diagnostic)
	}
	if result.Callees[0].Code == "" || result.Callees[1].Code != "" || result.Callees[1].Pointer == "" {
		t.Errorf("read(Run, budget 400) callees = %+v, want helperA inlined and helperB as a pointer", result.Callees)
	}

	plain, status, diagnostic := readContext(t, root, "Run", "--budget", "128")
	if status != 0 || !strings.Contains(plain.Code, "func Run") {
		t.Fatalf("read(Run, budget 128) = (%d, %+v, %q), want the definition", status, plain, diagnostic)
	}
	for _, callee := range plain.Callees {
		if callee.Code != "" {
			t.Errorf("read(Run, budget 128) callee %s code = %q, want pointers only", callee.Name, callee.Code)
		}
	}

	path := filepath.Join(root, "pkg", "helpers.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(data, []byte("return 1"), []byte("return 8"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	stale, status, diagnostic := readContext(t, root, "Run", "--no-refresh")
	if status != 0 || len(stale.Callees) != 2 {
		t.Fatalf("read(Run, stale helpers) = (%d, %+v, %q), want the definition with callee pointers", status, stale, diagnostic)
	}
	for _, callee := range stale.Callees {
		if callee.Code != "" {
			t.Errorf("read(Run, stale helpers) callee %s code = %q, want a pointer only", callee.Name, callee.Code)
		}
	}
}
