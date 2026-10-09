package graphquality

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestOracleLanguageFixtures pins today's score of each reviewed fixture, so a
// resolver change that fixes or breaks a case shows up in review. Each FP and
// FN is explained in the fixture manifest's limitations.
func TestOracleLanguageFixtures(t *testing.T) {
	type partitionScore struct {
		Name       string
		TP, FP, FN int
	}
	tests := []struct {
		fixture string
		want    []partitionScore
	}{
		{"oracle-python", []partitionScore{
			{"python-calls", 11, 0, 5},
			{"python-contains", 27, 0, 0},
			{"python-extends", 1, 0, 1},
		}},
		{"oracle-ts", []partitionScore{
			{"typescript-calls", 12, 0, 5},
			{"typescript-contains", 21, 0, 1},
			{"typescript-extends", 1, 0, 0},
			{"typescript-implements", 1, 0, 1},
		}},
		{"oracle-java", []partitionScore{
			{"java-calls", 10, 1, 5},
			{"java-contains", 32, 0, 0},
			{"java-extends", 1, 0, 0},
			{"java-implements", 2, 0, 2},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			root := filepath.Join("testdata", tt.fixture)
			data, err := os.ReadFile(filepath.Join(root, "oracle.json"))
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := DecodeManifest(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("DecodeManifest(%s) error = %v", tt.fixture, err)
			}
			graph, err := BuildOracleFixture(root, manifest)
			if err != nil {
				t.Fatalf("BuildOracleFixture(%s) error = %v", tt.fixture, err)
			}
			report, err := EvaluateOracle(graph, manifest)
			if err != nil {
				t.Fatalf("EvaluateOracle(%s) error = %v", tt.fixture, err)
			}
			var got []partitionScore
			for _, score := range report.Partitions {
				got = append(got, partitionScore{Name: score.Name, TP: score.TP, FP: score.FP, FN: score.FN})
				if len(score.DuplicateActual) != 0 {
					t.Errorf("EvaluateOracle(%s) %s duplicates = %v, want none", tt.fixture, score.Name, score.DuplicateActual)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("EvaluateOracle(%s) partitions = %+v, want %+v", tt.fixture, got, tt.want)
			}
		})
	}
}
