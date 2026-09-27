package graphquality

import (
	"reflect"
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		Version: 1,
		Sources: []OracleSource{{Path: "a.go", SHA256: strings.Repeat("a", 64)}},
		Build:   OracleBuild{Extensions: []string{".go"}},
		Partitions: []OraclePartition{{
			Name: "go-calls", Language: "go", Relation: "calls", Files: []string{"a.go"},
			Expected: []Fact{{Source: "a.go#Run", Relation: "calls", Target: "a.go#Save"}},
		}},
	}
}

func TestManifestValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Manifest)
	}{
		{"duplicate source", func(m *Manifest) { m.Sources = append(m.Sources, m.Sources[0]) }},
		{"unsafe source", func(m *Manifest) { m.Sources[0].Path = "../a.go" }},
		{"bad hash", func(m *Manifest) { m.Sources[0].SHA256 = "abc" }},
		{"unknown relation", func(m *Manifest) { m.Partitions[0].Relation = "unknown" }},
		{"wrong language", func(m *Manifest) { m.Partitions[0].Language = "python" }},
		{"duplicate fact", func(m *Manifest) {
			m.Partitions[0].Expected = append(m.Partitions[0].Expected, m.Partitions[0].Expected[0])
		}},
		{"out of scope fact", func(m *Manifest) { m.Partitions[0].Expected[0].Source = "b.go#Run" }},
		{"overlap", func(m *Manifest) { m.Partitions = append(m.Partitions, m.Partitions[0]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validManifest()
			tt.change(&m)
			if err := m.Validate(); err == nil {
				t.Errorf("Validate(%s) error = nil, want non-nil", tt.name)
			}
		})
	}
	if err := validManifest().Validate(); err != nil {
		t.Errorf("Validate(valid) error = %v", err)
	}
}

func TestDecodeManifestRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{`{`, `{}`, `{"version":1,"unknown":1}`, `{"version":1} {"version":1}`} {
		if _, err := DecodeManifest(strings.NewReader(input)); err == nil {
			t.Errorf("DecodeManifest(%q) error = nil, want non-nil", input)
		}
	}
}

func TestEvaluateOracleCountsAndScopes(t *testing.T) {
	m := validManifest()
	m.Sources = append(m.Sources, OracleSource{Path: "b.go", SHA256: strings.Repeat("b", 64)})
	m.Partitions[0].Files = []string{"a.go", "b.go"}
	m.Partitions[0].Expected = []Fact{
		{Source: "a.go#Run", Relation: "calls", Target: "a.go#Save"},
		{Source: "b.go#Run", Relation: "calls", Target: "b.go#Save"},
	}
	g := Graph{Edges: []Edge{
		{Source: "a.go#Run", Relation: "calls", Target: "a.go#Save"},
		{Source: "a.go#Run", Relation: "calls", Target: "b.go#Save"},
		{Source: "a.go#Run", Relation: "references", Target: "b.go#Save"},
	}}
	got, err := EvaluateOracle(g, m)
	if err != nil {
		t.Fatal(err)
	}
	result := got.Partitions[0]
	if result.TP != 1 || result.FP != 1 || result.FN != 1 || result.Precision == nil || *result.Precision != 0.5 || result.Recall == nil || *result.Recall != 0.5 {
		t.Errorf("EvaluateOracle counts = %+v, want TP=1 FP=1 FN=1 precision=recall=0.5", result)
	}
	if !reflect.DeepEqual(result.FalsePositives, []Fact{{Source: "a.go#Run", Relation: "calls", Target: "b.go#Save"}}) {
		t.Errorf("FalsePositives = %+v", result.FalsePositives)
	}
	if !reflect.DeepEqual(result.FalseNegatives, []Fact{{Source: "b.go#Run", Relation: "calls", Target: "b.go#Save"}}) {
		t.Errorf("FalseNegatives = %+v", result.FalseNegatives)
	}
	if !reflect.DeepEqual(got.UnassessedRelations, []string{"references"}) {
		t.Errorf("UnassessedRelations = %v", got.UnassessedRelations)
	}
}

func TestEvaluateOracleEmptyAndDuplicateFacts(t *testing.T) {
	m := validManifest()
	m.Partitions[0].Expected = nil
	got, err := EvaluateOracle(Graph{}, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.Partitions[0].Precision != nil || got.Partitions[0].Recall != nil {
		t.Errorf("empty metrics = %+v, want null", got.Partitions[0])
	}
	fact := Edge{Source: "a.go#Run", Relation: "calls", Target: "a.go#Save"}
	got, err = EvaluateOracle(Graph{Edges: []Edge{fact, fact}}, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.Partitions[0].FP != 1 || len(got.Partitions[0].DuplicateActual) != 1 || got.OK() {
		t.Errorf("duplicate report = %+v, want one FP and duplicate violation", got)
	}
}
