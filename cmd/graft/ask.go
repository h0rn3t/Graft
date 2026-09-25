package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

var askPointerPattern = regexp.MustCompile(`^(.*):L(\d+)-L(\d+)$`)

func runAsk(opts callersOptions, stdout, stderr io.Writer) int {
	budget, err := validateAskOptions(opts)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if opts.intent == "edit" || opts.full {
		opts.source = true
	}
	root, contextDir, err := resolvePaths(opts, enginePathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	limit := askLimit(opts.limit)
	// The refresh and the workspace check read --dir only, as in TypeScript.
	queryDir := graphDir(root, opts, false)
	var refresh bytes.Buffer
	refreshBeforeQuery(root, queryDir, opts, &refresh)
	opts.budgetOverhead = refresh.String()
	if _, err := io.WriteString(stderr, opts.budgetOverhead); err != nil {
		return 1
	}
	if children, ok := graph.ReadWorkspaceChildren(queryDir); ok {
		return runWorkspaceAsk(root, queryDir, children, opts, limit, stdout, stderr)
	}
	loaded, index, err := opts.queryCache.load(contextDir)
	if err != nil {
		result := graph.AskResult{
			Query: opts.query,
			Mode:  "empty",
			Hits:  make([]graph.AskHit, 0),
			Note:  "no matching nodes — try different words, or `graft build` if graft/ is empty",
		}
		return writeAskResult(opts, result, stdout, stderr)
	}
	result, err := graph.Ask(*loaded, opts.query, graph.AskOptions{
		Limit:       &limit,
		In:          opts.in,
		NoGraphRank: opts.noGraphRank,
		Index:       index,
	})
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if opts.intent == "edit" {
		addAskEditContext(*loaded, &result)
	}
	if opts.source {
		setAskSourceHashes(*loaded, result.Hits)
		inlineAskHits(root, askCruxByPointer(*loaded), result.Hits, opts.full, opts.query)
		if !opts.full {
			expandNamedAskHit(root, result.Hits, opts.query, budget)
		}
		result.Saved = askSavings(*loaded, result.Hits)
	}
	if result.Saved == nil {
		return writeAskResult(opts, result, stdout, stderr)
	}
	out := &countingWriter{Writer: stdout}
	code := writeAskResult(opts, result, out, stderr)
	recordQuerySavings(contextDir, out.n, result.Saved.BaselineChars)
	return code
}

func runWorkspaceAsk(root, contextDir string, children []string, opts callersOptions, limit float64, stdout, stderr io.Writer) int {
	children = slices.Clone(children)
	onlyChild := ""
	childIn := ""
	if opts.in != "" {
		prefix := strings.TrimRight(opts.in, "/")
		parts := strings.Split(prefix, "/")
		onlyChild = parts[0]
		if !containsString(children, onlyChild) {
			writeDiagnostic(stderr, "no workspace repo %q - repos: %s\n", onlyChild, strings.Join(children, ", "))
			return 1
		}
		if len(parts) > 1 {
			childIn = strings.Join(parts[1:], "/")
		}
	}

	type workspaceGroup struct {
		key          string
		child        string
		hits         []graph.AskHit
		baselineHits []graph.AskHit
	}
	type workspaceCandidate struct {
		child                  string
		result                 graph.AskResult
		ranking                *graph.AskRankingMetadata
		coverage               float64
		coverageStrong         float64
		baselineCoverage       float64
		baselineCoverageStrong float64
		groups                 []workspaceGroup
	}
	candidates := make([]workspaceCandidate, 0, len(children))
	loadedCount := 0
	fileFirst := false
	distinctive := ""
	for _, child := range children {
		childContext := filepath.Join(root, child, "graft")
		loaded, index, err := opts.queryCache.load(childContext)
		if err != nil {
			continue
		}
		loadedCount++
		if onlyChild != "" && child != onlyChild {
			continue
		}
		childResult, err := graph.Ask(*loaded, opts.query, graph.AskOptions{
			Limit:                  new(max(limit*4, 20)),
			In:                     childIn,
			NoGraphRank:            opts.noGraphRank,
			Index:                  index,
			FileFirst:              &fileFirst,
			FileComplement:         true,
			IncludeRankingMetadata: true,
		})
		if distinctive == "" {
			distinctive = childResult.Distinctive
		}
		if err != nil || len(childResult.Hits) == 0 {
			continue
		}
		if opts.source {
			setAskSourceHashes(*loaded, childResult.Hits)
			// Each child inlines its own spans, crux included, before fusion.
			childRoot := filepath.Join(root, child)
			cruxByPointer := askCruxByPointer(*loaded)
			inlineAskHits(childRoot, cruxByPointer, childResult.Hits, opts.full, opts.query)
			inlineAskRanking(childRoot, cruxByPointer, childResult.Ranking, opts.full, opts.query)
		}
		ranking := childResult.Ranking
		groups := make([]workspaceGroup, 0)
		coverage, coverageStrong := askWorkspaceCoverage(childResult, ranking)
		baselineCoverage, baselineCoverageStrong := askWorkspaceBaselineCoverage(childResult, ranking)
		if ranking != nil {
			groups = make([]workspaceGroup, 0, len(ranking.Groups))
			for _, group := range ranking.Groups {
				if len(group.Hits) == 0 {
					continue
				}
				groups = append(groups, workspaceGroup{
					key:          child + "\x00" + group.Key,
					child:        child,
					hits:         group.Hits,
					baselineHits: group.BaselineHits,
				})
			}
		}
		if len(groups) == 0 {
			for _, hit := range childResult.Hits {
				key := child + "\x00" + workspaceHitGroup(hit)
				if len(groups) == 0 || groups[len(groups)-1].key != key {
					groups = append(groups, workspaceGroup{key: key, child: child, hits: []graph.AskHit{hit}, baselineHits: []graph.AskHit{hit}})
				}
			}
		}
		candidates = append(candidates, workspaceCandidate{
			child:                  child,
			result:                 childResult,
			ranking:                ranking,
			coverage:               coverage,
			coverageStrong:         coverageStrong,
			baselineCoverage:       baselineCoverage,
			baselineCoverageStrong: baselineCoverageStrong,
			groups:                 groups,
		})
	}

	gatedOut := make([]graph.AskScopeMatch, 0)
	baselineCandidates := make([]workspaceCandidate, 0, len(candidates))
	survivors := make([]workspaceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		baselineEligible := onlyChild != "" || candidate.baselineCoverageStrong >= 0.1 || candidate.baselineCoverage >= 0.5
		fileEligible := onlyChild != "" || candidate.coverageStrong >= 0.1 || candidate.coverage >= 0.5
		if baselineEligible {
			baselineCandidates = append(baselineCandidates, candidate)
		}
		if baselineEligible || fileEligible {
			survivors = append(survivors, candidate)
			continue
		}
		best := candidate.groups[0].hits[0]
		gatedOut = append(gatedOut, graph.AskScopeMatch{Scope: candidate.child, BestID: prefixAskPointer(candidate.child, best.Pointer)})
	}
	// A child's hit fuses under <child>/<scope>, or <child> for its root scope.
	fusionScope := func(child string, hit graph.AskHit) string {
		if hit.Scope != nil && *hit.Scope != "" {
			return child + "/" + *hit.Scope
		}
		return child
	}

	// Baseline stream: the all-span fusion that decides the locked top hit.
	type baselineBack struct {
		child string
		group string
		hit   graph.AskHit
	}
	baselineDocs := make([]graph.ScopedDoc, 0)
	baselineByID := make(map[string]baselineBack)
	for _, candidate := range baselineCandidates {
		type entry struct {
			group string
			hit   graph.AskHit
		}
		entries := make([]entry, 0)
		if candidate.ranking != nil {
			for _, item := range candidate.ranking.Baseline {
				entries = append(entries, entry{group: item.Group, hit: item.Hit})
			}
		} else {
			for index, hit := range candidate.result.Hits {
				entries = append(entries, entry{group: "singleton:" + strconv.Itoa(index), hit: hit})
			}
		}
		for index, item := range entries {
			id := candidate.child + " " + strconv.Itoa(index)
			baselineDocs = append(baselineDocs, graph.ScopedDoc{ID: id, Scope: fusionScope(candidate.child, item.hit), Score: item.hit.Score})
			baselineByID[id] = baselineBack{child: candidate.child, group: candidate.child + "\x00" + item.group, hit: item.hit}
		}
	}
	baselineFused := graph.FuseScopes(baselineDocs)
	var baselineTop graph.AskHit
	var baselineTopBack baselineBack
	hasBaselineTop := false
	if len(baselineFused.Ranked) > 0 {
		ranked := baselineFused.Ranked[0]
		if back, ok := baselineByID[ranked.ID]; ok {
			baselineTopBack, hasBaselineTop = back, true
			baselineTop = qualifyWorkspaceHit(back.child, back.hit, ranked.Score, ranked.Scope)
		}
	}

	// File stream: one leader per child file takes part in fusion.
	fileDocs := make([]graph.ScopedDoc, 0)
	fileByID := make(map[string]workspaceGroup)
	for _, candidate := range survivors {
		for index, group := range candidate.groups {
			if len(group.hits) == 0 {
				continue
			}
			leader := group.hits[0]
			id := fmt.Sprintf("%s file %08d", candidate.child, index)
			fileDocs = append(fileDocs, graph.ScopedDoc{ID: id, Scope: fusionScope(candidate.child, leader), Score: leader.Score})
			baselineHits := group.baselineHits
			if baselineHits == nil {
				baselineHits = []graph.AskHit{leader}
			}
			fileByID[id] = workspaceGroup{key: group.key, child: candidate.child, hits: group.hits, baselineHits: baselineHits}
		}
	}
	fileFused := graph.FuseScopes(fileDocs)
	rankedGroups := make([]workspaceGroup, 0, len(fileFused.Ranked))
	for _, ranked := range fileFused.Ranked {
		group, ok := fileByID[ranked.ID]
		if !ok {
			continue
		}
		hits := make([]graph.AskHit, 0, len(group.hits))
		for _, hit := range group.hits {
			hits = append(hits, qualifyWorkspaceHit(group.child, hit, ranked.Score, fusionScope(group.child, hit)))
		}
		baselineHits := make([]graph.AskHit, 0, len(group.baselineHits))
		for _, hit := range group.baselineHits {
			baselineHits = append(baselineHits, qualifyWorkspaceHit(group.child, hit, hit.Score, fusionScope(group.child, hit)))
		}
		group.hits, group.baselineHits = hits, baselineHits
		rankedGroups = append(rankedGroups, group)
	}
	projectedGroups := rankedGroups
	if hasBaselineTop {
		key := baselineTopBack.group
		groupIndex := slices.IndexFunc(rankedGroups, func(group workspaceGroup) bool { return group.key == key })
		var locked workspaceGroup
		if groupIndex >= 0 {
			existing := rankedGroups[groupIndex]
			projectedScore := baselineTop.Score
			if len(existing.hits) > 0 {
				projectedScore = existing.hits[0].Score
			}
			locked = existing
			locked.hits = []graph.AskHit{baselineTop}
			for _, hit := range existing.baselineHits {
				if !sameWorkspaceHit(hit, baselineTop) {
					hit.Score = projectedScore
					locked.hits = append(locked.hits, hit)
				}
			}
		} else {
			locked = workspaceGroup{key: key, child: baselineTopBack.child, hits: []graph.AskHit{baselineTop}, baselineHits: []graph.AskHit{baselineTop}}
		}
		projectedGroups = []workspaceGroup{locked}
		for _, group := range rankedGroups {
			if group.key != key {
				projectedGroups = append(projectedGroups, group)
			}
		}
	}
	projectedHits := make([][]graph.AskHit, 0, len(projectedGroups))
	for _, group := range projectedGroups {
		projectedHits = append(projectedHits, group.hits)
	}
	// Name-coverage tiers hold within each child scope: the fused order runs
	// unbounded, each scope's hits are reordered in the positions it holds,
	// and only then is the answer cut to the limit.
	hits := workspaceRoundRobin(projectedHits, math.Inf(1))
	tierWithinScopes(hits)
	if capacity := graph.JSQueueCap(limit); capacity >= 0 {
		hits = hits[:min(capacity, len(hits))]
	}
	result := graph.AskResult{Query: opts.query, Mode: "empty", Hits: hits, Distinctive: distinctive}
	if len(hits) > 0 {
		result.Mode = "lexical"
		federated := make([]string, 0)
		if hasBaselineTop && baselineTop.Scope != nil && *baselineTop.Scope != "" {
			federated = append(federated, *baselineTop.Scope)
		}
		federated = appendUniqueStrings(federated, fileFused.Federated...)
		federatedSet := make(map[string]struct{}, len(federated))
		for _, scope := range federated {
			federatedSet[scope] = struct{}{}
		}
		alsoMatched := make([]graph.AskScopeMatch, 0)
		for _, match := range append(slices.Clone(fileFused.AlsoMatched), gatedOut...) {
			if _, ok := federatedSet[match.Scope]; !ok {
				alsoMatched = append(alsoMatched, match)
			}
		}
		result.Scopes = &graph.AskScopes{Federated: federated, AlsoMatched: alsoMatched}
	}
	if len(result.Hits) == 0 {
		result.Note = fmt.Sprintf("no matching nodes across %d workspace repo(s) — try different words, or `graft build` at a child", loadedCount)
	}
	total := len(children)
	if loadedCount < total {
		coverage := fmt.Sprintf("%d of %d workspace repos have graphs; run graft build to cover %s", loadedCount, total, missingAskChildren(root, children))
		if result.Note != "" {
			result.Note += "\n" + coverage
		} else {
			result.Note = coverage
		}
	}
	if opts.intent == "edit" {
		addWorkspaceEditContext(root, opts, &result)
	}
	return writeAskResult(opts, result, stdout, stderr)
}

func askWorkspaceCoverage(result graph.AskResult, ranking *graph.AskRankingMetadata) (float64, float64) {
	if ranking != nil && len(ranking.Groups) > 0 {
		return ranking.Groups[0].Coverage, ranking.Groups[0].CoverageStrong
	}
	return askOptionalCoverage(result.Coverage), askOptionalCoverage(result.CoverageStrong)
}

func askWorkspaceBaselineCoverage(result graph.AskResult, ranking *graph.AskRankingMetadata) (float64, float64) {
	if ranking != nil {
		return askOptionalCoverage(ranking.BaselineCoverage), askOptionalCoverage(ranking.BaselineCoverageStrong)
	}
	return askOptionalCoverage(result.Coverage), askOptionalCoverage(result.CoverageStrong)
}

func askOptionalCoverage(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// qualifyWorkspaceHit is TypeScript's { ...hit, score, scope, pointer }: the
// pointer gains the child prefix, and a scope the child hit lacked is appended
// after its other keys.
func qualifyWorkspaceHit(child string, hit graph.AskHit, score float64, scope string) graph.AskHit {
	hit.Pointer = prefixAskPointer(child, hit.Pointer)
	if hit.Scope == nil {
		hit.ScopeAfterCode = true
	}
	hit.Scope = new(scope)
	hit.Score = score
	return hit
}

func sameWorkspaceHit(left, right graph.AskHit) bool {
	return left.Kind == right.Kind && left.Title == right.Title && left.Pointer == right.Pointer
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}

func prefixAskPointer(child, pointer string) string {
	parts := strings.Split(pointer, ",")
	for index, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" && !strings.Contains(trimmed, " ") {
			parts[index] = child + "/" + trimmed
		}
	}
	return strings.Join(parts, ", ")
}

func workspaceHitGroup(hit graph.AskHit) string {
	if marker := strings.Index(hit.Pointer, ":L"); marker >= 0 {
		return hit.Pointer[:marker]
	}
	return hit.Pointer
}

func workspaceRoundRobin(groups [][]graph.AskHit, limit float64) []graph.AskHit {
	selected := make([]graph.AskHit, 0)
	capacity := graph.JSQueueCap(limit)
	if capacity == 0 {
		return selected
	}
	for depth := 0; ; depth++ {
		added := false
		for _, group := range groups {
			if depth >= len(group) {
				continue
			}
			selected = append(selected, group[depth])
			added = true
			if capacity > 0 && len(selected) >= capacity {
				return selected
			}
		}
		if !added {
			return selected
		}
	}
}

// tierWithinScopes orders each scope's hits by name coverage, most query terms
// first, while every scope keeps the positions it holds in the fused answer.
func tierWithinScopes(hits []graph.AskHit) {
	positions := make(map[string][]int)
	for i, hit := range hits {
		scope := ""
		if hit.Scope != nil {
			scope = *hit.Scope
		}
		positions[scope] = append(positions[scope], i)
	}
	for _, indexes := range positions {
		scoped := make([]graph.AskHit, len(indexes))
		for j, i := range indexes {
			scoped[j] = hits[i]
		}
		slices.SortStableFunc(scoped, func(left, right graph.AskHit) int { return cmp.Compare(right.NameTerms, left.NameTerms) })
		for j, i := range indexes {
			hits[i] = scoped[j]
		}
	}
}

func missingAskChildren(root string, children []string) string {
	missing := make([]string, 0)
	for _, child := range children {
		if _, err := os.Stat(graph.WiringPath(filepath.Join(root, child, "graft"))); err != nil {
			missing = append(missing, child)
		}
	}
	return strings.Join(missing, ", ")
}

func readAskIndex(path string) *graph.AskIndex {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		Version    int                 `json:"version"`
		AvgBodyLen *float64            `json:"avgBodyLen"`
		DF         [][]json.RawMessage `json:"df"`
		DocCount   int                 `json:"docCount"`
		Docs       []struct {
			ID   string              `json:"id"`
			Name [][]json.RawMessage `json:"name"`
			Path [][]json.RawMessage `json:"path"`
			Body [][]json.RawMessage `json:"body"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.Version != graph.AskIndexVersion || raw.AvgBodyLen == nil || raw.DF == nil || raw.Docs == nil || raw.DocCount != len(raw.Docs) {
		return nil
	}
	df, ok := askIndexPairs(raw.DF)
	if !ok {
		return nil
	}
	docs := make(map[string]graph.AskIndexDoc, len(raw.Docs))
	for _, rawDoc := range raw.Docs {
		if rawDoc.ID == "" {
			return nil
		}
		name, nameOK := askIndexPairs(rawDoc.Name)
		pathCounts, pathOK := askIndexPairs(rawDoc.Path)
		body, bodyOK := askIndexPairs(rawDoc.Body)
		if !nameOK || !pathOK || !bodyOK {
			return nil
		}
		docs[rawDoc.ID] = graph.AskIndexDoc{Name: name, Path: pathCounts, Body: body}
	}
	if len(docs) != raw.DocCount {
		return nil
	}
	return &graph.AskIndex{
		Version:    raw.Version,
		AvgBodyLen: *raw.AvgBodyLen,
		DF:         df,
		DocCount:   raw.DocCount,
		Docs:       docs,
	}
}

func askIndexPairs(pairs [][]json.RawMessage) (map[string]int, bool) {
	counts := make(map[string]int, len(pairs))
	if pairs == nil {
		return nil, false
	}
	for _, pair := range pairs {
		if len(pair) != 2 {
			return nil, false
		}
		var term string
		var count int
		if err := json.Unmarshal(pair[0], &term); err != nil {
			return nil, false
		}
		if err := json.Unmarshal(pair[1], &count); err != nil || term == "" || count < 0 {
			return nil, false
		}
		counts[term] = count
	}
	return counts, true
}

// askLimit reads --limit as the TypeScript CLI does, with Number(): garbage is
// NaN, and NaN, zero, fractions and negatives flow into ranking unchanged.
func askLimit(raw string) float64 {
	if raw == "" {
		return 8
	}
	return jsonjs.ToNumber(raw)
}

func writeAskJSON(stdout, stderr io.Writer, result graph.AskResult) int {
	data, err := jsonjs.Marshal(result, "  ")
	if err != nil {
		writeDiagnostic(stderr, "✗ failed to encode ask result: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
		return 1
	}
	return 0
}

func writeAskHuman(stdout io.Writer, result graph.AskResult, mcp bool) int {
	_, err := io.WriteString(stdout, formatAskText(result, mcp))
	return writeAskError(err)
}

// formatAskText renders a ranked answer for agents and people; mcp selects the
// wording of expansion hints for the MCP surface instead of the CLI.
func formatAskText(result graph.AskResult, mcp bool) string {
	note := askNoteBlock(result.Note)
	if len(result.Hits) == 0 {
		body := note
		if body == "" {
			body = "no matches."
		}
		return body + askEscalationNudge(result, mcp) + "\n"
	}
	var lines []string
	if note != "" {
		lines = append(lines, note, "")
	}
	if result.Mode == "structural" {
		for _, hit := range result.Hits {
			line := fmt.Sprintf("- %s  %s  (%s)", hit.Title, hit.Pointer, hit.Relation)
			if hit.Snippet != "" {
				line += " — " + hit.Snippet
			}
			lines = append(lines, line)
			if hit.ContentRef != "" {
				lines = append(lines, "   ref: "+hit.ContentRef)
			}
			if hit.Unchanged {
				lines = append(lines, "   unchanged; source already supplied")
			}
			if hit.Code != "" {
				lines = append(lines, "", "```", askExcerptText(hit.Code, mcp), "```", "")
			}
		}
	} else {
		for index, hit := range result.Hits {
			label := ""
			if result.Scopes != nil && hit.Scope != nil && *hit.Scope != "" {
				label = "[" + *hit.Scope + "/] "
			}
			bareFile := strings.HasSuffix(hit.Title, " · file") && hit.Code == "" && hit.Snippet == "" && hit.ContentRef == "" && !hit.Unchanged
			switch {
			case bareFile:
				lines = append(lines, fmt.Sprintf("%d. %s%s (file)", index+1, label, hit.Pointer), "")
				continue
			case hit.Kind == "symbol" || strings.HasSuffix(hit.Title, " · "+hit.Kind):
				lines = append(lines, fmt.Sprintf("%d. %s%s", index+1, label, hit.Title))
			default:
				lines = append(lines, fmt.Sprintf("%d. %s%s  [%s]", index+1, label, hit.Title, hit.Kind))
			}
			lines = append(lines, "   "+hit.Pointer)
			if hit.ContentRef != "" {
				lines = append(lines, "   ref: "+hit.ContentRef)
			}
			if hit.Unchanged {
				lines = append(lines, "   unchanged; source already supplied")
			}
			if hit.Snippet != "" && !askExcerptStartsWith(hit.Code, hit.Snippet) {
				lines = append(lines, "   "+hit.Snippet)
			}
			if hit.Doc != "" {
				lines = append(lines, "   "+hit.Doc)
			}
			if hit.Code != "" {
				lines = append(lines, "", "```", askExcerptText(hit.Code, mcp), "```")
			}
			lines = append(lines, "")
		}
		lines = append(lines, askScopeFooterLines(result)...)
	}
	body := jsonjs.TrimEnd(strings.Join(lines, "\n"))
	return body + askEscalationNudge(result, mcp) + "\n"
}

// askExcerptStartsWith reports whether code opens with the signature, compared
// with whitespace collapsed and any line-number prefix removed.
func askExcerptStartsWith(code, signature string) bool {
	first, _, _ := strings.Cut(code, "\n")
	if number, rest, ok := strings.Cut(first, ": "); ok && askExcerptLineNumber(number) > 0 {
		first = rest
	}
	signature = strings.Join(strings.Fields(signature), " ")
	return signature != "" && strings.HasPrefix(strings.Join(strings.Fields(first), " "), signature)
}

// askExcerptLineNumber parses an excerpt line label such as "L42", or returns 0.
func askExcerptLineNumber(label string) int {
	digits, ok := strings.CutPrefix(label, "L")
	if !ok {
		return 0
	}
	number, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return number
}

// askExcerptText rewrites a compact excerpt for text output. Lines holding only
// closing brackets and separators are left out, and the footer states how many
// definition lines were not printed. Whole definitions are returned unchanged,
// and so is the code field of JSON output.
func askExcerptText(code string, mcp bool) string {
	lines := strings.Split(code, "\n")
	pointer, ok := strings.CutPrefix(lines[len(lines)-1], "… (excerpt; full definition at ")
	if ok {
		pointer, ok = strings.CutSuffix(pointer, "; rerun with --full)")
	}
	match := askPointerPattern.FindStringSubmatch(pointer)
	if !ok || match == nil {
		return code
	}
	from, _ := strconv.Atoi(match[2])
	to, _ := strconv.Atoi(match[3])
	kept := make([]string, 0, len(lines))
	printed := 0
	for _, line := range lines[:len(lines)-1] {
		label, text, numbered := strings.Cut(line, ": ")
		if numbered && askExcerptLineNumber(label) > 0 {
			if strings.Trim(text, " \t)]};,") == "" {
				continue
			}
			printed++
		}
		kept = append(kept, line)
	}
	hint := "--full"
	if mcp {
		hint = "full: true"
	}
	return strings.Join(kept, "\n") + fmt.Sprintf("\n… +%d lines (%s)", to-from+1-printed, hint)
}

// askScopeFooterLines is the multi-scope footer: displayed hits per scope,
// biggest first, then every scope gated out of fusion.
func askScopeFooterLines(result graph.AskResult) []string {
	if result.Scopes == nil {
		return nil
	}
	order := make([]string, 0)
	counts := make(map[string]int)
	for _, hit := range result.Hits {
		if hit.Scope == nil {
			continue
		}
		if _, ok := counts[*hit.Scope]; !ok {
			order = append(order, *hit.Scope)
		}
		counts[*hit.Scope]++
	}
	compare := graph.LocaleCompare()
	slices.SortStableFunc(order, func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), compare(a, b))
	})
	out := make([]string, 0, 1+len(result.Scopes.AlsoMatched))
	if len(order) > 0 {
		parts := make([]string, 0, len(order))
		for _, scope := range order {
			parts = append(parts, fmt.Sprintf("%s (%d)", graph.ScopeLabel(scope), counts[scope]))
		}
		out = append(out, "matched in: "+strings.Join(parts, " · "))
	}
	for _, match := range result.Scopes.AlsoMatched {
		label := graph.ScopeLabel(match.Scope)
		out = append(out, "also matched: "+label+" — narrow with --in "+label)
	}
	return out
}

func writeAskError(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

func askNoteBlock(note string) string {
	if note == "" {
		return ""
	}
	lines := strings.Split(note, "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "structural index:") {
			lines[index] = "⚠ " + line
		}
	}
	return strings.Join(lines, "\n")
}

// Match-strength thresholds on the top hit's query-term coverage: by name
// (strong) and over the whole document (broad).
const (
	askWeakStrongCoverage = 0.1 // below this, the top hit's name barely matches
	askWeakBroadCoverage  = 0.5 // below this, the document misses most of the query
	askStrongCoverage     = 0.5 // at or above this, the top hit is inlined in prompt context
)

// askWeakMatch reports whether a ranked answer is too weak to act on: no hits,
// or coverage recorded and low both by name and over the whole document.
func askWeakMatch(result graph.AskResult) bool {
	if len(result.Hits) == 0 {
		return true
	}
	if result.Coverage == nil && result.CoverageStrong == nil {
		return false
	}
	return askOptionalCoverage(result.CoverageStrong) < askWeakStrongCoverage && askOptionalCoverage(result.Coverage) < askWeakBroadCoverage
}

// askEscalationNudge ends a weak ranked answer with the next tool to try, named
// for the surface that serves it.
func askEscalationNudge(result graph.AskResult, mcp bool) string {
	if result.Mode != "lexical" && result.Mode != "empty" || !askWeakMatch(result) {
		return ""
	}
	lead := "weak match"
	if len(result.Hits) == 0 {
		lead = "no hits"
	}
	term := cmp.Or(result.Distinctive, "<literal>")
	if mcp {
		return fmt.Sprintf("\n\n[graft] %s — don't re-ask with new wording; switch tool: graft_find_all {\"pattern\":%q,\"ignore_case\":true} for every occurrence · graft_file_api for a file's full API · graft_trace_calls for who-uses.", lead, term)
	}
	return fmt.Sprintf("\n\n[graft] %s — don't re-ask with new wording; switch tool: `graft grep -i %q` for every occurrence · `graft skeleton <file>` for a file's full API · `graft callers <symbol>` for who-uses.", lead, term)
}

// askCruxByPointer maps each node's path:span pointer to its crux excerpt;
// callers build it once per graph and share it across every inline pass.
func askCruxByPointer(wiring graph.GraphV1) map[string]string {
	cruxByPointer := make(map[string]string)
	for _, node := range wiring.Nodes {
		if node.Crux != nil && node.Crux.Code != "" {
			cruxByPointer[node.Path+":"+node.Span] = node.Crux.Code
		}
	}
	return cruxByPointer
}

// inlineAskRanking inlines source into a child's internal ranking queues too,
// as the TypeScript ask does before a workspace parent fuses them.
func inlineAskRanking(root string, cruxByPointer map[string]string, ranking *graph.AskRankingMetadata, full bool, query ...string) {
	if ranking == nil {
		return
	}
	for index := range ranking.Groups {
		inlineAskHits(root, cruxByPointer, ranking.Groups[index].Hits, full, query...)
		inlineAskHits(root, cruxByPointer, ranking.Groups[index].BaselineHits, full, query...)
	}
	for index := range ranking.Baseline {
		hits := []graph.AskHit{ranking.Baseline[index].Hit}
		inlineAskHits(root, cruxByPointer, hits, full, query...)
		ranking.Baseline[index].Hit = hits[0]
	}
}

func inlineAskHits(root string, cruxByPointer map[string]string, hits []graph.AskHit, full bool, query ...string) {
	for index := range hits {
		hit := &hits[index]
		path, from, to, ok := parseAskPointer(hit.Pointer)
		if !ok {
			continue
		}
		code, hash, exists := sliceAskSpan(filepath.Join(root, filepath.FromSlash(path)), from, to, path, full, query...)
		if !exists {
			continue
		}
		hit.Code, hit.SourceHash = code, hash
		if !full {
			if crux, exists := cruxByPointer[hit.Pointer]; exists {
				hit.Code = crux + "\n… (crux — full definition at " + hit.Pointer + "; rerun with --full)"
			}
		}
	}
}

// expandNamedAskHit inlines the complete top definition when the query names
// it: the agent asked for that symbol, and an excerpt would cost another
// round to expand. The definition may take at most half of the budget, which
// leaves room for the other hits.
func expandNamedAskHit(root string, hits []graph.AskHit, query string, budget int) {
	if len(hits) == 0 || hits[0].Kind == "file" {
		return
	}
	hit := &hits[0]
	name, _, _ := strings.Cut(hit.Title, " · ")
	name = strings.ToLower(name)
	if _, last, ok := strings.CutLast(name, "."); ok {
		name = last
	}
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	if name == "" || !slices.Contains(words, name) {
		return
	}
	path, from, to, ok := parseAskPointer(hit.Pointer)
	if !ok {
		return
	}
	code, hash, exists := sliceAskSpan(filepath.Join(root, filepath.FromSlash(path)), from, to, path, true)
	if !exists || savings.Tokens(savings.Length(code)) > budget/2 {
		return
	}
	hit.Code, hit.SourceHash = code, hash
}

func parseAskPointer(pointer string) (string, int, int, bool) {
	match := askPointerPattern.FindStringSubmatch(pointer)
	if len(match) != 4 {
		return "", 0, 0, false
	}
	from, fromErr := strconv.Atoi(match[2])
	to, toErr := strconv.Atoi(match[3])
	if fromErr != nil || toErr != nil || from < 1 || from > to {
		return "", 0, 0, false
	}
	return match[1], from, to, true
}

func sliceAskSpan(path string, from, to int, relativePath string, full bool, query ...string) (string, string, bool) {
	data, readable, err := sourcefiles.Read(path)
	if err != nil || !readable {
		return "", "", false
	}
	lines := strings.Split(data, "\n")
	end := min(to, len(lines))
	if from > end {
		return "", "", false
	}
	selected := lines[from-1 : end]
	body := strings.Join(selected, "\n")
	hash := sourcefiles.Hash(body)
	if !full {
		return compactAskSource(selected, from, strings.Join(query, " "), fmt.Sprintf("%s:L%d-L%d", relativePath, from, end)), hash, true
	}
	return body, hash, true
}

func askSavings(wiring graph.GraphV1, hits []graph.AskHit) *graph.AskSavings {
	charsByPath := make(map[string]int)
	for _, node := range wiring.Nodes {
		if node.Kind == graph.Kind("file") && node.Chars != nil {
			charsByPath[node.Path] = *node.Chars
		}
	}
	paths := make(map[string]struct{})
	for _, hit := range hits {
		if path, _, _, ok := parseAskPointer(hit.Pointer); ok {
			paths[path] = struct{}{}
			continue
		}
		for path := range strings.SplitSeq(hit.Pointer, ",") {
			path = strings.TrimSpace(path)
			if path != "" && !strings.Contains(path, " ") {
				paths[path] = struct{}{}
			}
		}
	}
	saved := &graph.AskSavings{}
	for path := range paths {
		chars, ok := charsByPath[path]
		if !ok {
			continue
		}
		saved.Files++
		saved.BaselineChars += chars
	}
	if saved.Files == 0 {
		return nil
	}
	return saved
}
