package graphquality

import (
	"os"
	"reflect"
	"testing"
)

func TestOracleGoFixture(t *testing.T) {
	file, err := os.Open("testdata/oracle-go/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close oracle manifest: %v", err)
		}
	})
	manifest, err := DecodeManifest(file)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := BuildOracleFixture("testdata/oracle-go", manifest)
	if err != nil {
		t.Fatal(err)
	}
	report, err := EvaluateOracle(graph, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Partitions) != 2 || report.Partitions[0].TP != 3 || report.Partitions[0].FP != 0 || report.Partitions[0].FN != 1 || report.Partitions[1].TP != 10 {
		t.Errorf("fixture oracle report = %+v, want calls TP=3 FP=0 FN=1 and contains TP=10", report)
	}
	wantMissing := []Fact{{Source: "a.go#Direct", Relation: "calls", Target: "a.go#A.Save"}}
	if !reflect.DeepEqual(report.Partitions[0].FalseNegatives, wantMissing) {
		t.Errorf("fixture missing = %+v, want %+v", report.Partitions[0].FalseNegatives, wantMissing)
	}
	unassessed := graph
	unassessed.Edges = append(append([]Edge{}, graph.Edges...), Edge{Source: "dynamic.go#Use", Relation: "calls", Target: "a.go#A.Save"})
	unassessedReport, err := EvaluateOracle(unassessed, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unassessedReport.Partitions[0], report.Partitions[0]) {
		t.Errorf("dynamic.go call affected assessed call score: got %+v, want %+v", unassessedReport.Partitions[0], report.Partitions[0])
	}
	graph.Edges = append(graph.Edges, Edge{Source: "a.go#Direct", Relation: "calls", Target: "b.go#B.Save"})
	for i, edge := range graph.Edges {
		if edge.Source == "b.go#Right" && edge.Relation == "calls" {
			graph.Edges = append(graph.Edges[:i], graph.Edges[i+1:]...)
			break
		}
	}
	bad, err := EvaluateOracle(graph, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if bad.Partitions[0].FP != 1 || bad.Partitions[0].FN != 2 || bad.OK() {
		t.Errorf("mutated fixture report = %+v, want FP=1 FN=2", bad.Partitions[0])
	}
}
