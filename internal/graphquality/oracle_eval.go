package graphquality

import (
	"cmp"
	"fmt"
	"slices"
)

// OracleReport contains accuracy results only for declared closed-world scopes.
type OracleReport struct {
	Partitions          []OracleScore `json:"partitions"`
	UnassessedRelations []string      `json:"unassessedRelations"`
	Limitations         []string      `json:"limitations"`
}

// OracleScore is the fact comparison for one oracle partition.
type OracleScore struct {
	Name            string   `json:"name"`
	Language        string   `json:"language"`
	Relation        string   `json:"relation"`
	Files           []string `json:"files"`
	TP              int      `json:"tp"`
	FP              int      `json:"fp"`
	FN              int      `json:"fn"`
	Precision       *float64 `json:"precision"`
	Recall          *float64 `json:"recall"`
	FalsePositives  []Fact   `json:"falsePositives"`
	FalseNegatives  []Fact   `json:"falseNegatives"`
	DuplicateActual []Fact   `json:"duplicateActual"`
}

// OK reports whether every assessed fact matches and no duplicate was found.
func (report OracleReport) OK() bool {
	for _, score := range report.Partitions {
		if score.FP != 0 || score.FN != 0 || len(score.DuplicateActual) != 0 {
			return false
		}
	}
	return true
}

// EvaluateOracle compares graph edges against a validated manifest.
func EvaluateOracle(g Graph, manifest Manifest) (OracleReport, error) {
	if err := manifest.Validate(); err != nil {
		return OracleReport{}, fmt.Errorf("validate oracle: %w", err)
	}
	report := OracleReport{UnassessedRelations: []string{}, Limitations: slices.Clone(manifest.Limitations)}
	assessedRelations := make(map[string]bool)
	for _, partition := range manifest.Partitions {
		assessedRelations[partition.Relation] = true
		score := OracleScore{
			Name: partition.Name, Language: partition.Language, Relation: partition.Relation,
			Files: slices.Clone(partition.Files), FalsePositives: []Fact{}, FalseNegatives: []Fact{}, DuplicateActual: []Fact{},
		}
		slices.Sort(score.Files)
		files := make(map[string]bool, len(partition.Files))
		for _, file := range partition.Files {
			files[file] = true
		}
		expected := make(map[Fact]bool, len(partition.Expected))
		for _, fact := range partition.Expected {
			expected[fact] = true
		}
		actual := make(map[Fact]bool)
		for _, edge := range g.Edges {
			if string(edge.Relation) != partition.Relation || !factSourceInFiles(edge.Source, files) {
				continue
			}
			fact := Fact{Source: edge.Source, Relation: string(edge.Relation), Target: edge.Target}
			if actual[fact] {
				score.DuplicateActual = append(score.DuplicateActual, fact)
				continue
			}
			actual[fact] = true
			if expected[fact] {
				score.TP++
			} else {
				score.FP++
				score.FalsePositives = append(score.FalsePositives, fact)
			}
		}
		for fact := range expected {
			if !actual[fact] {
				score.FN++
				score.FalseNegatives = append(score.FalseNegatives, fact)
			}
		}
		sortFacts(score.FalsePositives)
		sortFacts(score.FalseNegatives)
		sortFacts(score.DuplicateActual)
		score.Precision = ratio(score.TP, score.TP+score.FP)
		score.Recall = ratio(score.TP, score.TP+score.FN)
		report.Partitions = append(report.Partitions, score)
	}
	unassessed := make(map[string]bool)
	for _, edge := range g.Edges {
		relation := string(edge.Relation)
		if !assessedRelations[relation] {
			unassessed[relation] = true
		}
	}
	for relation := range unassessed {
		report.UnassessedRelations = append(report.UnassessedRelations, relation)
	}
	slices.Sort(report.UnassessedRelations)
	return report, nil
}

func ratio(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func sortFacts(facts []Fact) {
	slices.SortFunc(facts, func(a, b Fact) int {
		if order := cmp.Compare(a.Source, b.Source); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Relation, b.Relation); order != 0 {
			return order
		}
		return cmp.Compare(a.Target, b.Target)
	})
}
