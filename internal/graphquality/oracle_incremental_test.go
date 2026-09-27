package graphquality

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func TestOracleFixtureIncrementalMatchesCold(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "dynamic.go"} {
		data, err := os.ReadFile(filepath.Join("testdata", "oracle-go", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	incrementalOptions := sourcefiles.Options{OutDir: t.TempDir(), Extensions: []string{".go"}}
	if _, err := graph.BuildGraph(root, incrementalOptions); err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(root, "a.go")
	file, err := os.OpenFile(aPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\nfunc Extra() { Recur(1) }\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	compareBuilds := func(state string) Graph {
		t.Helper()
		incremental, err := graph.BuildGraph(root, incrementalOptions)
		if err != nil {
			t.Fatal(err)
		}
		if incremental.Reused == 0 {
			t.Errorf("%s incremental build reused no files", state)
		}
		cold, err := graph.BuildGraph(root, sourcefiles.Options{
			OutDir: t.TempDir(), Extensions: []string{".go"}, NoReuse: true, NoCacheWrite: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(graphFacts(incremental.Graph), graphFacts(cold.Graph)) {
			t.Errorf("%s incremental facts differ from cold: incremental=%v cold=%v", state, graphFacts(incremental.Graph), graphFacts(cold.Graph))
		}
		return incremental.Graph
	}
	compareBuilds("edit")
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	afterRemoval := compareBuilds("removal")
	for _, fact := range graphFacts(afterRemoval) {
		if fact.Source == "b.go" || fact.Source == "b.go#Left" || fact.Source == "b.go#Right" {
			t.Errorf("removed file retained active fact %+v", fact)
		}
	}
	corrupt := afterRemoval
	corrupt.Edges = append(slices.Clone(corrupt.Edges), Edge{Source: "b.go#Left", Relation: "calls", Target: "b.go#Right"})
	if reflect.DeepEqual(graphFacts(corrupt), graphFacts(afterRemoval)) {
		t.Error("fact comparison failed to detect a deliberately retained stale edge")
	}
}

func graphFacts(g Graph) []Fact {
	facts := make([]Fact, 0, len(g.Edges))
	for _, edge := range g.Edges {
		facts = append(facts, Fact{Source: edge.Source, Relation: string(edge.Relation), Target: edge.Target})
	}
	sortFacts(facts)
	return facts
}
