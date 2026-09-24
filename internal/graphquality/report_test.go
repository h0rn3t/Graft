package graphquality

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAnalyzeValidGraph(t *testing.T) {
	report := Analyze(Graph{
		Nodes: []Node{
			{ID: "a.ts#main", Name: "main", Kind: "function", Span: "L1-L3", Origin: "ast"},
			{ID: "a.ts", Name: "a.ts", Kind: "file", Span: "L1-L3", Origin: "ast"},
		},
		Edges: []Edge{
			{Source: "a.ts", Target: "a.ts#main", Relation: "contains", Confidence: "extracted"},
		},
	}, "graft/.graph/wiring.json")

	if !report.Invariants.OK {
		t.Errorf("Analyze(valid graph).Invariants.OK = false, want true; problems = %v", report.Invariants.Sample)
	}
	if report.SymbolNodes != 1 {
		t.Errorf("Analyze(valid graph).SymbolNodes = %d, want 1", report.SymbolNodes)
	}
	if report.Connectivity.OrphanSymbolNodes != 1 {
		t.Errorf("Analyze(valid graph).Connectivity.OrphanSymbolNodes = %d, want 1", report.Connectivity.OrphanSymbolNodes)
	}
}

func TestAnalyzeCatchesScriptQualityViolations(t *testing.T) {
	report := Analyze(Graph{
		Nodes: []Node{
			{ID: "a.ts#Foo", Name: "Foo", Kind: "class", Span: "L1-L3", Origin: "ast"},
			{ID: "a.ts#Foo", Name: " ", Kind: "widget", Span: "L9-L2", Origin: "ast"},
		},
		Edges: []Edge{
			{Source: "a.ts#Foo", Target: "a.ts#Ghost", Relation: "calls", Confidence: "guessed"},
			{Source: "a.ts#Missing", Target: "a.ts#Foo", Relation: "references", Confidence: "extracted"},
			{Source: "a.ts#Foo", Target: "a.ts#Foo", Relation: "calls", Confidence: "extracted"},
		},
	}, "fixture.json")

	if report.Invariants.OK {
		t.Fatal("Analyze(invalid graph).Invariants.OK = true, want false")
	}
	for _, want := range []string{"dup id", "empty name", "bad kind", "inverted span", "dangling calls target", "dangling source", "bad confidence"} {
		found := false
		for _, problem := range report.Invariants.Sample {
			if strings.Contains(problem, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Analyze(invalid graph) did not report %q; sample = %v", want, report.Invariants.Sample)
		}
	}
	if report.Invariants.SelfLoopCalls != 1 {
		t.Errorf("Analyze(invalid graph).Invariants.SelfLoopCalls = %d, want 1", report.Invariants.SelfLoopCalls)
	}
}

func TestJSONUsesMachineReportShape(t *testing.T) {
	data, err := Analyze(Graph{}, "fixture.json").JSON()
	if err != nil {
		t.Fatalf("Report.JSON() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(Report.JSON()) error = %v", err)
	}
	for _, key := range []string{"graph", "nodes", "symbolNodes", "resolution", "connectivity", "invariants"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("Report.JSON() missing key %q", key)
		}
	}
}

// TestJSONMatchesGoldenBytes compares --json output byte for byte with the CLI
// goldens, which keep count keys in first-seen order.
func TestJSONMatchesGoldenBytes(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "cmd", "graft", "testdata", "goldens", "per-command", "graph-quality-cli", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("filepath.Glob(graph-quality goldens) = %v, %v, want goldens", paths, err)
	}
	checked := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var golden struct {
			Args   []string          `json:"args"`
			Inputs map[string]string `json:"inputs"`
			Stdout string            `json:"stdout"`
		}
		if err := json.Unmarshal(data, &golden); err != nil {
			t.Fatalf("json.Unmarshal(%s) error = %v", path, err)
		}
		if !slices.Contains(golden.Args, "--json") || golden.Inputs["wiring.json"] == "" {
			continue
		}
		wiring := filepath.Join(t.TempDir(), "wiring.json")
		if err := os.WriteFile(wiring, []byte(golden.Inputs["wiring.json"]), 0o600); err != nil {
			t.Fatal(err)
		}
		graph, err := Load(wiring)
		if err != nil {
			t.Fatalf("Load(%s input) error = %v", path, err)
		}
		// "<REPO>" would come out HTML-escaped, so the graph path uses a stand-in.
		got, err := Analyze(graph, "REPO/wiring.json").JSON()
		if err != nil {
			t.Fatalf("Report.JSON() for %s error = %v", path, err)
		}
		if want := strings.ReplaceAll(golden.Stdout, "<REPO>", "REPO"); string(got)+"\n" != want {
			t.Errorf("Report.JSON() for %s =\n%s\nwant\n%s", filepath.Base(path), got, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no --json golden with a wiring.json input")
	}
}

func TestResolvePathFindsCurrentAndLegacyLocations(t *testing.T) {
	repository := t.TempDir()
	current := filepath.Join(repository, "graft", ".graph", "wiring.json")
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(`{"nodes":[],"edges":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := ResolvePath(repository); !ok || got != current {
		t.Errorf("ResolvePath(%q) = %q, %t, want %q, true", repository, got, ok, current)
	}

	legacyRepository := t.TempDir()
	legacy := filepath.Join(legacyRepository, "graft", "wiring.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"nodes":[],"edges":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := ResolvePath(legacyRepository); !ok || got != legacy {
		t.Errorf("ResolvePath(%q) = %q, %t, want %q, true", legacyRepository, got, ok, legacy)
	}
}
