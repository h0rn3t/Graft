package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
)

func TestAskCompactSource(t *testing.T) {
	root := t.TempDir()
	var code strings.Builder
	code.WriteString("func example() {\n")
	for i := range 30 {
		fmt.Fprintf(&code, "  step%d()\n", i)
	}
	code.WriteString("  importantFailure()\n}\n")
	if err := os.WriteFile(filepath.Join(root, "example.go"), []byte(code.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	hits := []graph.AskHit{{Pointer: "example.go:L1-L33"}}
	inlineAskHits(root, nil, hits, false)
	if len(strings.Split(hits[0].Code, "\n")) > 12 || !strings.Contains(hits[0].Code, "--full") {
		t.Errorf("inlineAskHits(compact) = %q, want bounded excerpt and expansion hint", hits[0].Code)
	}
}

func TestAskHumanOmitsSavingsBanner(t *testing.T) {
	result := graph.AskResult{Query: "example", Mode: "lexical", Hits: []graph.AskHit{{Title: "example", Pointer: "example.go:L1-L3", Code: "func example() {}"}}, Saved: &graph.AskSavings{Files: 1, BaselineChars: 40000}}
	if got := formatAskText(result); strings.Contains(got, "tokens saved") || strings.Contains(got, "At the end of your reply") {
		t.Errorf("formatAskText() = %q, want source without savings instructions", got)
	}
}

func TestAskBudgetAndSeen(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprint(asJSON), func(t *testing.T) {
			result := graph.AskResult{Query: "example", Mode: "lexical", Hits: []graph.AskHit{
				{Title: "example", Pointer: "example.go:L1-L80", Code: strings.Repeat("  doWork(\"世界😀\")\n", 80)},
				{Title: "caller", Pointer: "caller.go:L1-L80", Code: strings.Repeat("  example()\n", 80)},
			}}
			got, err := fitAskBudget(result, 256, asJSON)
			if err != nil {
				t.Fatal(err)
			}
			body := renderAskBudget(got, asJSON)
			if savings.Tokens(savings.Length(body)) > 256 || !strings.Contains(got.Note, "budget") || len(got.Hits) == 0 {
				t.Errorf("fitAskBudget(256) = %s, want bounded answer retaining primary hit and truncation note", body)
			}
		})
	}
}

func TestAskSeenReferences(t *testing.T) {
	result := graph.AskResult{Hits: []graph.AskHit{{Pointer: "a.go:L1-L2", Code: "body", SourceHash: "revision-1"}}}
	applyAskSeen(&result, nil)
	ref := result.Hits[0].ContentRef
	if ref == "" {
		t.Fatal("applyAskSeen() returned no content reference")
	}
	applyAskSeen(&result, []string{ref})
	if result.Hits[0].Code != "" || !result.Hits[0].Unchanged {
		t.Errorf("applyAskSeen(seen) = %+v, want unchanged pointer without repeated code", result.Hits[0])
	}
	result.Hits[0].Code = "body"
	result.Hits[0].SourceHash = "revision-2"
	applyAskSeen(&result, []string{ref})
	if result.Hits[0].Code == "" || result.Hits[0].Unchanged {
		t.Errorf("applyAskSeen(changed) = %+v, want code again", result.Hits[0])
	}
}

func TestAskBudgetIncludesCLIRefreshNotice(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GRAFT_NO_REFRESH", "")
	path := filepath.Join(root, "auth.go")
	source := "package auth\nfunc verify() string { return \"" + strings.Repeat("x", 2000) + "\" }\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("os.WriteFile(auth.go) = %v, want nil", err)
	}
	var stderr bytes.Buffer
	if code := runBuild(callersOptions{root: root, rootSet: true}, io.Discard, &stderr); code != 0 {
		t.Fatalf("runBuild(auth.go) = %d, want 0; stderr=%q", code, stderr.String())
	}
	if err := os.WriteFile(path, []byte(strings.Replace(source, "xxx", "changed", 1)), 0o600); err != nil {
		t.Fatalf("os.WriteFile(changed auth.go) = %v, want nil", err)
	}
	stderr.Reset()
	var stdout bytes.Buffer
	options := callersOptions{root: root, rootSet: true, query: strings.Repeat("verify ", 12), source: true, budget: "128"}
	if code := runAsk(options, &stdout, &stderr); code != 0 {
		t.Fatalf("runAsk(refresh, budget=128) = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "[graft] refreshed the graph") {
		t.Errorf("runAsk(refresh) stderr=%q, want refresh notice", stderr.String())
	}
	if got := savings.Tokens(savings.Length(stdout.String() + stderr.String())); got > 128 {
		t.Errorf("runAsk(refresh, budget=128) combined output=%d tokens, want <=128; stdout=%q; stderr=%q", got, stdout.String(), stderr.String())
	}
}
