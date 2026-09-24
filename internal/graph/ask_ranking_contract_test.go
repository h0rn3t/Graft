package graph

import (
	"strings"
	"testing"
)

func TestAskDistinctiveTerm(t *testing.T) {
	wiring := traversalGraph([]NodeV1{
		traversalNode("src/a.go#hookStart", "hookStart", "function", "src/a.go"),
		traversalNode("src/a.go#hookStop", "hookStop", "function", "src/a.go"),
		traversalNode("src/b.go#hookRun", "hookRun", "function", "src/b.go"),
		traversalNode("src/c.go#readTimeout", "readTimeout", "function", "src/c.go"),
	}, nil)
	for query, want := range map[string]string{"hook timeout": "timeout", "hook timeouts": "timeouts"} {
		result, err := Ask(wiring, query, AskOptions{})
		if err != nil {
			t.Fatalf("Ask(%q) error = %v, want nil", query, err)
		}
		if result.Distinctive != want {
			t.Errorf("Ask(%q).Distinctive = %q, want %q", query, result.Distinctive, want)
		}
	}
}

func TestAskFold(t *testing.T) {
	tests := []struct{ term, want string }{
		{"timeouts", "timeout"},
		{"timeout", "timeout"},
		{"queries", "query"},
		{"query", "query"},
		{"matches", "match"},
		{"matched", "match"},
		{"matching", "match"},
		{"boxes", "box"},
		{"configure", "configur"},
		{"configured", "configur"},
		{"configuring", "configur"},
		{"cache", "cach"},
		{"caches", "cach"},
		{"caching", "cach"},
		{"running", "run"},
		{"mapping", "map"},
		{"indexed", "index"},
		{"indexes", "index"},
		{"added", "add"},
		{"status", "status"},
		{"statuses", "status"},
		{"class", "class"},
		{"classes", "class"},
		{"process", "process"},
		{"analysis", "analysis"},
		{"strings", "str"},
		{"string", "str"},
		{"its", "its"},
		{"use", "use"},
		{"need", "need"},
		{"ring", "ring"},
		{"flies", "fly"},
	}
	for _, tt := range tests {
		if got := AskFold(tt.term); got != tt.want {
			t.Errorf("AskFold(%q) = %q, want %q", tt.term, got, tt.want)
		}
	}
}

func TestAskRankingContract(t *testing.T) {
	node := traversalNode
	body := func(n NodeV1, text string) NodeV1 {
		n.BodyText = &text
		return n
	}
	var helperCallers []NodeV1
	var helperEdges []EdgeV1
	for _, name := range []string{"Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta"} {
		caller := node("src/callers.go#hook"+name, "hook"+name, "function", "src/callers.go")
		helperCallers = append(helperCallers, caller)
		helperEdges = append(helperEdges, traversalEdge(caller.ID, "src/hook.go#hookContextDir", "calls"))
	}
	tests := []struct {
		name  string
		nodes []NodeV1
		edges []EdgeV1
		query string
		order [][]string // groups of "<title prefix> <path>": each group listed before the next, the top hit from the first
	}{
		{
			name:  "plural query word matches a singular name",
			nodes: []NodeV1{node("src/a.go#readTimeout", "readTimeout", "function", "src/a.go"), node("src/b.go#parseFlags", "parseFlags", "function", "src/b.go")},
			query: "timeouts",
			order: [][]string{{"readTimeout · function src/a.go"}},
		},
		{
			name:  "exact match is not outranked by a folded match",
			nodes: []NodeV1{node("src/b.go#readTimeouts", "readTimeouts", "function", "src/b.go"), node("src/a.go#readTimeout", "readTimeout", "function", "src/a.go")},
			query: "read timeout",
			order: [][]string{{"readTimeout · function src/a.go"}},
		},
		{
			name: "second symbol of a file against another file's leader",
			nodes: append([]NodeV1{
				node("src/hook.go#hookContextDir", "hookContextDir", "function", "src/hook.go"),
				node("src/timeouts.go#hookPromptAskTimeout", "hookPromptAskTimeout", "function", "src/timeouts.go"),
				node("src/timeouts.go#hookInstalledTimeout", "hookInstalledTimeout", "function", "src/timeouts.go"),
			}, helperCallers...),
			edges: helperEdges,
			query: "hook timeout",
			order: [][]string{{"hookPromptAskTimeout", "hookInstalledTimeout"}, {"hookContextDir"}},
		},
		{
			name: "test with more name matches stays below production",
			nodes: []NodeV1{
				node("src/config_test.go#TestReadConfigTimeout", "TestReadConfigTimeout", "function", "src/config_test.go"),
				node("src/config.go#readConfig", "readConfig", "function", "src/config.go"),
			},
			query: "read config timeout",
			order: [][]string{{"readConfig"}, {"TestReadConfigTimeout"}},
		},
		{
			name: "concept query without name matches still ranks",
			nodes: []NodeV1{
				body(node("src/billing.go#run", "run", "function", "src/billing.go"), "computes invoice totals for every customer"),
				body(node("src/other.go#start", "start", "function", "src/other.go"), "opens the socket"),
			},
			query: "invoice totals",
			order: [][]string{{"run · function"}},
		},
		{
			name: "production outranks an equal-coverage testdata copy",
			nodes: []NodeV1{
				node("testdata/a.go#readTimeout", "readTimeout", "function", "testdata/a.go"),
				node("src/a.go#readTimeout", "readTimeout", "function", "src/a.go"),
			},
			query: "read timeout",
			order: [][]string{{"readTimeout · function src/a.go"}, {"readTimeout · function testdata/a.go"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Ask(traversalGraph(tt.nodes, tt.edges), tt.query, AskOptions{})
			if err != nil {
				t.Fatalf("Ask(%q) error = %v, want nil", tt.query, err)
			}
			titles := make([]string, len(result.Hits))
			for i, hit := range result.Hits {
				path, _, _ := strings.Cut(hit.Pointer, ":")
				titles[i] = hit.Title + " " + path
			}
			groupOf := func(title string) int {
				for group, prefixes := range tt.order {
					for _, prefix := range prefixes {
						if strings.HasPrefix(title, prefix) {
							return group
						}
					}
				}
				return -1
			}
			found := make([]int, len(tt.order))
			last := 0
			ordered := len(titles) > 0 && groupOf(titles[0]) == 0
			for _, title := range titles {
				group := groupOf(title)
				if group < 0 {
					continue
				}
				ordered = ordered && group >= last
				last = max(last, group)
				found[group]++
			}
			for group, prefixes := range tt.order {
				ordered = ordered && found[group] >= len(prefixes)
			}
			if !ordered {
				t.Errorf("Ask(%q) hits = %q, want groups %q in this order, the top hit from the first", tt.query, titles, tt.order)
			}
		})
	}
}

func TestAskIgnoresIndexOfAnotherVersion(t *testing.T) {
	wiring := traversalGraph([]NodeV1{traversalNode("src/a.go#readTimeout", "readTimeout", "function", "src/a.go")}, nil)
	stale := &AskIndex{Version: 1, DocCount: 1, Docs: map[string]AskIndexDoc{
		"src/a.go#readTimeout": {Name: map[string]int{"unrelated": 1}, Path: map[string]int{}, Body: map[string]int{}},
	}, DF: map[string]int{"unrelated": 1}}
	result, err := Ask(wiring, "timeouts", AskOptions{Index: stale})
	if err != nil {
		t.Fatalf("Ask(%q, index v1) error = %v, want nil", "timeouts", err)
	}
	if len(result.Hits) == 0 || !strings.HasPrefix(result.Hits[0].Title, "readTimeout") {
		t.Errorf("Ask(%q, index v1) hits = %+v, want readTimeout from the graph, the stale index ignored", "timeouts", result.Hits)
	}
}
