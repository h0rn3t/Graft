package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/savings"
)

func validateAskOptions(opts callersOptions) (int, error) {
	budget := 2000
	if opts.budget != "" {
		value, err := strconv.Atoi(opts.budget)
		if err != nil || value < 128 || value > 64000 {
			return 0, fmt.Errorf("budget must be an integer from 128 to 64000 estimated tokens")
		}
		budget = value
	}
	if opts.intent != "" && opts.intent != "lookup" && opts.intent != "edit" {
		return 0, fmt.Errorf("intent must be lookup or edit")
	}
	return budget, nil
}

func mcpAskOptions(args map[string]any) (callersOptions, error) {
	var opts callersOptions
	if value, exists := args["budget"]; exists {
		budget, ok := mcpNumber(value)
		if !ok || math.IsNaN(budget) || budget < 128 || budget > 64000 || math.Trunc(budget) != budget {
			return opts, fmt.Errorf("budget must be an integer from 128 to 64000 estimated tokens")
		}
		opts.budget = strconv.Itoa(int(budget))
	}
	if value, exists := args["intent"]; exists {
		intent, ok := value.(string)
		if !ok || (intent != "lookup" && intent != "edit") {
			return opts, fmt.Errorf("intent must be lookup or edit")
		}
		opts.intent = intent
	}
	if value, exists := args["seen"]; exists {
		refs, ok := value.([]any)
		if !ok || len(refs) > 256 {
			return opts, fmt.Errorf("seen must be an array of at most 256 content references")
		}
		opts.references = true
		for _, value := range refs {
			ref, ok := value.(string)
			if !ok || len(ref) != 24 {
				return opts, fmt.Errorf("seen entries must be 24-character hexadecimal content references")
			}
			if _, err := hex.DecodeString(ref); err != nil {
				return opts, fmt.Errorf("invalid content reference %q", ref)
			}
			opts.seen = append(opts.seen, ref)
		}
	}
	return opts, nil
}

func writeAskResult(opts callersOptions, result graph.AskResult, stdout, stderr io.Writer) int {
	budget, err := validateAskOptions(opts)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if opts.references {
		applyAskSeen(&result, opts.seen)
	}
	if opts.queryNote != "" {
		result.Note = strings.TrimSpace(opts.queryNote + "\n" + result.Note)
	}
	result, err = fitAskBudget(result, budget, opts.jsonOutput, opts.mcp, opts.budgetOverhead)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if opts.jsonOutput {
		return writeAskJSON(stdout, stderr, result)
	}
	return writeAskHuman(stdout, result, opts.mcp)
}

func renderAskBudget(result graph.AskResult, asJSON, mcp bool) string {
	if asJSON {
		data, _ := jsonjs.Marshal(result, "  ") // AskResult contains only JSON-safe values from ranking.
		return string(data) + "\n"
	}
	return formatAskText(result, mcp)
}

func fitAskBudget(result graph.AskResult, budget int, asJSON, mcp bool, overhead ...string) (graph.AskResult, error) {
	result.Hits = slices.Clone(result.Hits)
	overheadChars := savings.Length(strings.Join(overhead, ""))
	noted := false
	for savings.Tokens(overheadChars+savings.Length(renderAskBudget(result, asJSON, mcp))) > budget {
		if !noted {
			result.Note = strings.TrimSpace(result.Note + "\nContext omitted to fit the budget; increase --budget or narrow --in.")
			noted = true
		}
		if len(result.Hits) == 0 {
			return result, fmt.Errorf("query and coverage metadata exceed budget; increase --budget or shorten the query")
		}
		last := &result.Hits[len(result.Hits)-1]
		if last.Code != "" {
			lines := strings.Split(last.Code, "\n")
			if len(lines) > 1 {
				last.Code = strings.Join(lines[:len(lines)/2], "\n")
			} else {
				last.Code = ""
			}
			// A partial excerpt must never acknowledge the original complete content.
			last.ContentRef = ""
			continue
		}
		if last.Snippet != "" {
			last.Snippet = ""
			continue
		}
		result.Hits = result.Hits[:len(result.Hits)-1]
	}
	return result, nil
}

func applyAskSeen(result *graph.AskResult, seen []string) {
	for index := range result.Hits {
		hit := &result.Hits[index]
		if hit.Code == "" {
			continue
		}
		hash := sha256.Sum256([]byte(hit.Pointer + "\x00" + hit.SourceHash + "\x00" + hit.Code))
		hit.ContentRef = fmt.Sprintf("%x", hash[:12])
		hit.Unchanged = slices.Contains(seen, hit.ContentRef)
		if hit.Unchanged {
			hit.Code = ""
		}
	}
}

func setAskSourceHashes(wiring graph.GraphV1, hits []graph.AskHit) {
	hashes := make(map[string]string, len(wiring.Nodes))
	for _, node := range wiring.Nodes {
		pointer := node.Path + ":" + node.Span
		if node.Kind == "file" {
			pointer = node.Path
		}
		hashes[pointer] = node.BodyHash
	}
	for i := range hits {
		hits[i].SourceHash = hashes[hits[i].Pointer]
	}
}

func addWorkspaceEditContext(root string, opts callersOptions, result *graph.AskResult) {
	if len(result.Hits) == 0 {
		return
	}
	child, pointer, ok := strings.Cut(result.Hits[0].Pointer, "/")
	if !ok {
		return
	}
	childRoot := filepath.Join(root, child)
	wiring, _, err := opts.queryCache.load(filepath.Join(childRoot, "graft"))
	if err != nil {
		return
	}
	primary := result.Hits[0]
	primary.Pointer = pointer
	local := graph.AskResult{Hits: []graph.AskHit{primary}}
	addAskEditContext(*wiring, &local)
	setAskSourceHashes(*wiring, local.Hits)
	inlineAskHits(childRoot, askCruxByPointer(*wiring), local.Hits, opts.full, opts.query)
	combined := slices.Clone(result.Hits[:1])
	for _, hit := range local.Hits[1:] {
		hit.Pointer = prefixAskPointer(child, hit.Pointer)
		hit.Scope = new(child)
		combined = append(combined, hit)
	}
	for _, hit := range result.Hits[1:] {
		if !slices.ContainsFunc(combined, func(other graph.AskHit) bool { return other.Pointer == hit.Pointer }) {
			combined = append(combined, hit)
		}
	}
	result.Hits = combined
	result.Note = strings.TrimSpace(result.Note + "\n" + local.Note)
}

// compactAskSource keeps the signature and the best query window, with real line numbers.
func compactAskSource(lines []string, from int, query, pointer string) string {
	if len(lines) <= 8 {
		return strings.Join(lines, "\n")
	}
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	for i, term := range terms {
		terms[i] = graph.AskFold(term)
	}
	best, bestScore := 1, 0
	for i, line := range lines[1:] {
		score := 0
		for _, term := range terms {
			if len(term) >= 3 && strings.Contains(strings.ToLower(line), term) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i+1, score
		}
	}
	start := max(1, min(best-2, len(lines)-7))
	var out strings.Builder
	fmt.Fprintf(&out, "L%d: %s\n", from, lines[0])
	if start > 1 {
		out.WriteString("…\n")
	}
	for i := start; i < min(start+7, len(lines)); i++ {
		fmt.Fprintf(&out, "L%d: %s\n", from+i, lines[i])
	}
	fmt.Fprintf(&out, "… (excerpt; full definition at %s; rerun with --full)", pointer)
	return out.String()
}

// addAskEditContext expands only the strongest hit, using direct indexed relations.
func addAskEditContext(wiring graph.GraphV1, result *graph.AskResult) {
	if len(result.Hits) == 0 {
		return
	}
	nodes := make(map[string]graph.NodeV1, len(wiring.Nodes))
	anchor := ""
	for _, node := range wiring.Nodes {
		nodes[node.ID] = node
		if node.Path+":"+node.Span == result.Hits[0].Pointer {
			anchor = node.ID
		}
	}
	if anchor == "" {
		return
	}
	var related []graph.AskHit
	for _, edge := range wiring.Edges {
		if edge.Relation == "contains" {
			continue
		}
		id, kind := "", "dependency"
		if edge.Source == anchor {
			id = edge.Target
		} else if edge.Target == anchor {
			id, kind = edge.Source, "caller"
		}
		node, ok := nodes[id]
		if !ok || node.Span == "" || node.Kind == "file" {
			continue
		}
		path := "/" + strings.ToLower(node.Path)
		if strings.HasSuffix(path, "_test.go") || strings.Contains(path, ".test.") || strings.Contains(path, ".spec.") || strings.Contains(path, "/tests/") || strings.Contains(path, "/__tests__/") || strings.Contains(path, "/test_") || strings.HasPrefix(node.Name, "Test") {
			kind = "test"
		}
		pointer := node.Path + ":" + node.Span
		if slices.ContainsFunc(result.Hits[:1], func(hit graph.AskHit) bool { return hit.Pointer == pointer }) || slices.ContainsFunc(related, func(hit graph.AskHit) bool { return hit.Pointer == pointer }) {
			continue
		}
		signature := ""
		if node.Signature != nil {
			signature = *node.Signature
		}
		related = append(related, graph.AskHit{Kind: kind, Title: node.Name, Pointer: pointer, Snippet: signature, Relation: edge.Relation, SourceHash: node.BodyHash})
	}
	slices.SortFunc(related, func(a, b graph.AskHit) int {
		return strings.Compare(a.Pointer, b.Pointer)
	})
	// Round-robin prevents a large caller list from hiding tests or dependencies.
	combined := slices.Clone(result.Hits[:1])
	for round := range 2 {
		for _, kind := range []string{"test", "caller", "dependency"} {
			seen := 0
			for _, hit := range related {
				if hit.Kind != kind {
					continue
				}
				if seen == round {
					combined = append(combined, hit)
					break
				}
				seen++
			}
		}
	}
	for _, hit := range result.Hits[1:] {
		if !slices.ContainsFunc(combined, func(other graph.AskHit) bool { return other.Pointer == hit.Pointer }) {
			combined = append(combined, hit)
		}
	}
	result.Hits = combined
	result.Note = strings.TrimSpace(result.Note + "\nEdit context includes bounded direct relations; use callers for complete impact analysis.")
}
