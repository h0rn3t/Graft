package graph

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAskContract(t *testing.T) {
	wiring := askContractGraph()
	tests := []struct {
		name        string
		query       string
		options     AskOptions
		wantMode    string
		wantSubject string
		wantTitles  []string
		wantErr     string
	}{
		{
			name:        "structural callers",
			query:       "who calls root",
			wantMode:    "structural",
			wantSubject: "root",
			wantTitles:  []string{"caller"},
		},
		{
			name:       "lexical body match",
			query:      "needle",
			options:    AskOptions{NoGraphRank: true},
			wantMode:   "lexical",
			wantTitles: []string{"root · function"},
		},
		{
			name:     "empty result",
			query:    "absent",
			wantMode: "empty",
		},
		{
			name:    "unknown prefix",
			query:   "root",
			options: AskOptions{In: "missing"},
			wantErr: `nothing indexed under "missing/"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Ask(wiring, tt.query, tt.options)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Ask(%q) error = %v, want substring %q", tt.query, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Ask(%q) error = %v, want nil", tt.query, err)
			}
			if result.Mode != tt.wantMode {
				t.Errorf("Ask(%q).Mode = %q, want %q", tt.query, result.Mode, tt.wantMode)
			}
			if result.Subject != tt.wantSubject {
				t.Errorf("Ask(%q).Subject = %q, want %q", tt.query, result.Subject, tt.wantSubject)
			}
			if len(result.Hits) != len(tt.wantTitles) {
				t.Fatalf("Ask(%q).Hits = %#v, want %d hits", tt.query, result.Hits, len(tt.wantTitles))
			}
			for index, want := range tt.wantTitles {
				if result.Hits[index].Title != want {
					t.Errorf("Ask(%q).Hits[%d].Title = %q, want %q", tt.query, index, result.Hits[index].Title, want)
				}
			}
		})
	}
}

func TestAskRankingMetadataPreservesFileLeadersAndBaseline(t *testing.T) {
	amberBody := "return amber"
	cobaltBody := "return cobalt"
	bothBody := "return amber cobalt"
	wiring := GraphV1{
		Nodes: []NodeV1{
			{ID: "a.ts", Name: "a.ts", Kind: "file", Path: "a.ts", Span: "L1-L2"},
			{ID: "a.ts#amber", Name: "amberShard", Kind: "function", Path: "a.ts", Span: "L1-L1", BodyText: &amberBody},
			{ID: "a.ts#cobalt", Name: "cobaltShard", Kind: "function", Path: "a.ts", Span: "L2-L2", BodyText: &cobaltBody},
			{ID: "b.ts", Name: "b.ts", Kind: "file", Path: "b.ts", Span: "L1-L1"},
			{ID: "b.ts#both", Name: "amberCobalt", Kind: "function", Path: "b.ts", Span: "L1-L1", BodyText: &bothBody},
		},
	}
	fileFirst := false
	result, err := Ask(wiring, "amber cobalt", AskOptions{
		Limit:                  new(8.0),
		NoGraphRank:            true,
		FileFirst:              &fileFirst,
		FileComplement:         true,
		IncludeRankingMetadata: true,
	})
	if err != nil {
		t.Fatalf("Ask(%q) error = %v, want nil", "amber cobalt", err)
	}
	if result.Ranking == nil {
		t.Fatalf("Ask(%q).Ranking = nil, want file ranking metadata", "amber cobalt")
	}
	if len(result.Ranking.Groups) != 2 {
		t.Fatalf("Ask(%q).Ranking.Groups = %d, want 2 file groups", "amber cobalt", len(result.Ranking.Groups))
	}
	for _, group := range result.Ranking.Groups {
		if len(group.Hits) == 0 || len(group.BaselineHits) == 0 {
			t.Errorf("Ask(%q).Ranking group %q = %#v, want leader and baseline queues", "amber cobalt", group.Key, group)
		}
	}
	if len(result.Ranking.Baseline) == 0 {
		t.Fatalf("Ask(%q).Ranking.Baseline = empty, want baseline candidates", "amber cobalt")
	}
	if result.Ranking.Baseline[0].Hit.Pointer != "b.ts:L1-L1" {
		t.Errorf("Ask(%q).Ranking.Baseline[0].Hit.Pointer = %q, want b.ts:L1-L1", "amber cobalt", result.Ranking.Baseline[0].Hit.Pointer)
	}
	if data, err := resultJSON(result); err != nil {
		t.Fatalf("resultJSON(Ask(%q)) error = %v", "amber cobalt", err)
	} else if strings.Contains(string(data), "ranking") {
		t.Errorf("resultJSON(Ask(%q)) = %s, must hide internal ranking metadata", "amber cobalt", data)
	}
}

func TestFuseAskGatesWeakWorkspaceScope(t *testing.T) {
	result := FuseAsk("query", []AskWorkspaceRun{
		{Scope: "strong", Hits: []AskHit{{Kind: "symbol", Title: "strong", Pointer: "strong.ts:L1-L1", Score: 1}}},
		{Scope: "weak", Hits: []AskHit{{Kind: "symbol", Title: "weak", Pointer: "weak.ts:L1-L1", Score: 0.1}}},
	}, 0)
	if len(result.Hits) != 1 || result.Hits[0].Scope == nil || *result.Hits[0].Scope != "strong" {
		t.Errorf("FuseAsk(%q).Hits = %#v, want only strong scope", "query", result.Hits)
	}
	if result.Scopes == nil || len(result.Scopes.Federated) != 1 || result.Scopes.Federated[0] != "strong" {
		t.Errorf("FuseAsk(%q).Scopes.Federated = %#v, want [strong]", "query", result.Scopes)
	}
	if len(result.Scopes.AlsoMatched) != 1 || result.Scopes.AlsoMatched[0].Scope != "weak" {
		t.Errorf("FuseAsk(%q).Scopes.AlsoMatched = %#v, want weak scope", "query", result.Scopes.AlsoMatched)
	}
}

func resultJSON(result AskResult) ([]byte, error) {
	return json.Marshal(result)
}

func askContractGraph() GraphV1 {
	rootBody := "return needle"
	callerBody := "return root()"
	return GraphV1{
		Nodes: []NodeV1{
			{ID: "src/root.ts", Name: "root.ts", Kind: "file", Path: "src/root.ts", Span: "L1-L3"},
			{ID: "src/root.ts#root", Name: "root", Kind: "function", Path: "src/root.ts", Span: "L1-L3", BodyText: &rootBody},
			{ID: "src/caller.ts", Name: "caller.ts", Kind: "file", Path: "src/caller.ts", Span: "L1-L3"},
			{ID: "src/caller.ts#caller", Name: "caller", Kind: "function", Path: "src/caller.ts", Span: "L1-L3", BodyText: &callerBody},
		},
		Edges: []EdgeV1{{Source: "src/caller.ts#caller", Target: "src/root.ts#root", Relation: "calls"}},
	}
}
