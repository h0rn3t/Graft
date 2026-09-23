package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
	"github.com/NanoNets/context-graph-engine/internal/savings"
)

const maxAskSpanLines = 80

var askPointerPattern = regexp.MustCompile(`^(.*):L(\d+)-L(\d+)$`)

func runAsk(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts, enginePathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	limit := askLimit(opts.limit)
	// The refresh and the workspace check read --dir only, as in TypeScript.
	queryDir := graphDir(root, opts, false)
	refreshBeforeQuery(root, queryDir, opts, stderr)
	if children, ok := graph.ReadWorkspaceChildren(queryDir); ok {
		return runWorkspaceAsk(root, queryDir, children, opts, limit, stdout, stderr)
	}
	loaded, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		result := graph.AskResult{
			Query: opts.query,
			Mode:  "empty",
			Hits:  make([]graph.AskHit, 0),
			Note:  "no matching nodes — try different words, or `graft build` if graft/ is empty",
		}
		if opts.jsonOutput {
			return writeAskJSON(stdout, stderr, result)
		}
		return writeAskHuman(stdout, result)
	}
	result, err := graph.Ask(*loaded, opts.query, graph.AskOptions{
		Limit:       &limit,
		In:          opts.in,
		NoGraphRank: opts.noGraphRank,
		Index:       readAskIndex(filepath.Join(contextDir, ".cache", "ask-index.json")),
		Concepts:    readAskConcepts(contextDir),
	})
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if opts.source {
		inlineAskSource(root, *loaded, &result, opts.full)
		result.Saved = askSavings(*loaded, result.Hits)
		pointers := make([]string, 0, len(result.Hits))
		for _, hit := range result.Hits {
			pointers = append(pointers, hit.Pointer)
		}
		result.Rules = graph.ApplyBrainRules(pointers, readAskBrainRules(root), loaded)
	}
	if opts.jsonOutput {
		return writeAskJSON(stdout, stderr, result)
	}
	return writeAskHuman(stdout, result)
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
	for _, child := range children {
		childContext := filepath.Join(root, child, "graft")
		loaded, err := graph.Read(graph.WiringPath(childContext))
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
			Index:                  readAskIndex(filepath.Join(childContext, ".cache", "ask-index.json")),
			Concepts:               readAskConcepts(childContext),
			FileFirst:              &fileFirst,
			FileComplement:         true,
			IncludeRankingMetadata: true,
		})
		if err != nil || len(childResult.Hits) == 0 {
			continue
		}
		if opts.source {
			// Each child inlines its own spans, crux included, before fusion.
			childRoot := filepath.Join(root, child)
			inlineAskSource(childRoot, *loaded, &childResult, opts.full)
			inlineAskRanking(childRoot, *loaded, childResult.Ranking, opts.full)
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

	// File stream: one leader per child file (or concept) takes part in fusion.
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
	hits := workspaceRoundRobin(projectedHits, limit)
	result := graph.AskResult{Query: opts.query, Mode: "empty", Hits: hits}
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
	if opts.jsonOutput {
		return writeAskJSON(stdout, stderr, result)
	}
	return writeAskHuman(stdout, result)
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
	if hit.Kind != "symbol" {
		return "concept:" + hit.Title + ":" + hit.Pointer
	}
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

func missingAskChildren(root string, children []string) string {
	missing := make([]string, 0)
	for _, child := range children {
		if _, err := os.Stat(graph.WiringPath(filepath.Join(root, child, "graft"))); err != nil {
			missing = append(missing, child)
		}
	}
	return strings.Join(missing, ", ")
}

func readAskConcepts(dir string) []graph.AskConcept {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	concepts := make([]graph.AskConcept, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "INDEX.md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		concepts = append(concepts, parseAskConcept(entry.Name(), string(data)))
	}
	return concepts
}

func readAskBrainRules(root string) []graph.BrainRule {
	linked := os.Getenv("GRAFT_BRAIN_ID") != "" && os.Getenv("GRAFT_BRAIN_TOKEN") != ""
	if !linked {
		data, err := os.ReadFile(filepath.Join(root, ".graft", "config.json"))
		if err != nil {
			return nil
		}
		var config struct {
			Brain *struct {
				BrainID string `json:"brainId"`
				Token   string `json:"token"`
			} `json:"brain"`
		}
		if err := json.Unmarshal(data, &config); err != nil || config.Brain == nil {
			return nil
		}
		linked = config.Brain.BrainID != "" && config.Brain.Token != ""
	}
	if !linked {
		return nil
	}

	contextDir := os.Getenv("GRAFT_DIR")
	if contextDir == "" {
		contextDir = filepath.Join(root, "graft")
	} else if !filepath.IsAbs(contextDir) {
		contextDir = filepath.Join(root, contextDir)
	}
	data, err := os.ReadFile(filepath.Join(contextDir, ".cache", "brain-rules.json"))
	if err != nil {
		return nil
	}
	var cache struct {
		Rules []graph.BrainRule `json:"rules"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return cache.Rules
}

func parseAskConcept(filename, content string) graph.AskConcept {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	concept := graph.AskConcept{Slug: strings.TrimSuffix(filename, ".md")}
	bodyStart := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for index := 1; index < len(lines); index++ {
			if strings.TrimSpace(lines[index]) == "---" {
				bodyStart = index + 1
				break
			}
		}
	}
	section := ""
	var frontmatter []string
	if bodyStart > 0 {
		frontmatter = lines[1 : bodyStart-1]
	}
	for _, raw := range frontmatter {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "slug:"):
			concept.Slug = askFrontmatterValue(strings.TrimPrefix(line, "slug:"))
		case strings.HasPrefix(line, "name:"):
			concept.Name = askFrontmatterValue(strings.TrimPrefix(line, "name:"))
		case line == "sources:":
			section = "sources"
		case line == "links:":
			section = "links"
		case strings.HasPrefix(line, "- path:") && section == "sources":
			concept.Sources = append(concept.Sources, askFrontmatterValue(strings.TrimPrefix(line, "- path:")))
		case strings.HasPrefix(line, "- to:") && section == "links":
			concept.Related = append(concept.Related, askFrontmatterValue(strings.TrimPrefix(line, "- to:")))
		}
	}
	body := strings.Join(lines[bodyStart:], "\n")
	concept.Snippet = askFirstProse(body)
	concept.Text = concept.Name + " " + concept.Snippet + " " + strings.Join(concept.Sources, " ")
	return concept
}

func askFrontmatterValue(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "\"'")
}

func askFirstProse(body string) string {
	for raw := range strings.SplitSeq(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<!--") || strings.HasPrefix(line, "-") {
			continue
		}
		return line
	}
	return ""
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
	if err := json.Unmarshal(data, &raw); err != nil || raw.Version != 1 || raw.AvgBodyLen == nil || raw.DF == nil || raw.Docs == nil || raw.DocCount != len(raw.Docs) {
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
	noteHit(len(result.Hits) > 0)
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

func writeAskHuman(stdout io.Writer, result graph.AskResult) int {
	noteHit(len(result.Hits) > 0)
	_, err := io.WriteString(stdout, formatAskText(result))
	return writeAskError(err)
}

// formatAskText renders an ask result exactly as the TypeScript formatAsk does.
func formatAskText(result graph.AskResult) string {
	head := `graft ask — "` + result.Query + `"  (` + result.Mode + ")"
	note := askNoteBlock(result.Note)
	if len(result.Hits) == 0 {
		body := note
		if body == "" {
			body = "no matches."
		}
		return head + "\n\n" + body + askEscalationNudge(result) + "\n"
	}
	lines := []string{head, ""}
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
			if hit.Code != "" {
				lines = append(lines, "", "```", hit.Code, "```", "")
			}
		}
	} else {
		for index, hit := range result.Hits {
			label := ""
			if result.Scopes != nil && hit.Scope != nil && *hit.Scope != "" {
				label = "[" + *hit.Scope + "/] "
			}
			lines = append(lines, fmt.Sprintf("%d. %s%s  [%s]", index+1, label, hit.Title, hit.Kind))
			lines = append(lines, "   "+hit.Pointer)
			if hit.Snippet != "" {
				lines = append(lines, "   "+hit.Snippet)
			}
			if len(hit.Related) > 0 {
				lines = append(lines, "   related: "+strings.Join(hit.Related, ", "))
			}
			if hit.Code != "" {
				lines = append(lines, "", "```", hit.Code, "```")
			}
			lines = append(lines, "")
		}
		lines = append(lines, askScopeFooterLines(result)...)
	}
	if len(result.Rules) > 0 {
		lines = append(lines, "", "rules that govern these symbols")
		for _, rule := range result.Rules {
			stale := ""
			if rule.Stale {
				stale = " (STALE — the code changed since this was decided)"
			}
			lines = append(lines, "- "+rule.Rule+stale)
			pointer := rule.Pointer
			if rule.SourceURL != "" {
				pointer += " · " + rule.SourceURL
			}
			lines = append(lines, "  "+pointer)
		}
	}
	body := jsonjs.TrimEnd(strings.Join(lines, "\n"))
	if savings := askSavingsLine(result, body); savings != "" {
		body = savings + "\n\n" + body
	}
	return body + askEscalationNudge(result) + "\n"
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

func askEscalationNudge(result graph.AskResult) string {
	if result.Mode != "lexical" && result.Mode != "empty" || len(result.Hits) > 3 {
		return ""
	}
	if len(result.Hits) == 0 {
		return "\n\n[graft] no hits — don't re-ask with new wording; switch tool: `graft grep \"<literal>\"` for every occurrence · `graft skeleton <file>` for a file's full API · `graft callers <symbol>` for who-uses."
	}
	noun := "hit"
	if len(result.Hits) != 1 {
		noun = "hits"
	}
	return fmt.Sprintf("\n\n[graft] only %d %s — don't re-ask with new wording; switch tool: `graft grep \"<literal>\"` for every occurrence · `graft skeleton <file>` for a file's full API · `graft callers <symbol>` for who-uses.", len(result.Hits), noun)
}

func inlineAskSource(root string, wiring graph.GraphV1, result *graph.AskResult, full bool) {
	inlineAskHits(root, wiring, result.Hits, full)
}

// inlineAskRanking inlines source into a child's internal ranking queues too,
// as the TypeScript ask does before a workspace parent fuses them.
func inlineAskRanking(root string, wiring graph.GraphV1, ranking *graph.AskRankingMetadata, full bool) {
	if ranking == nil {
		return
	}
	for index := range ranking.Groups {
		inlineAskHits(root, wiring, ranking.Groups[index].Hits, full)
		inlineAskHits(root, wiring, ranking.Groups[index].BaselineHits, full)
	}
	for index := range ranking.Baseline {
		hits := []graph.AskHit{ranking.Baseline[index].Hit}
		inlineAskHits(root, wiring, hits, full)
		ranking.Baseline[index].Hit = hits[0]
	}
}

func inlineAskHits(root string, wiring graph.GraphV1, hits []graph.AskHit, full bool) {
	cruxByPointer := make(map[string]string)
	for _, node := range wiring.Nodes {
		if node.Crux != nil && node.Crux.Code != "" {
			cruxByPointer[node.Path+":"+node.Span] = node.Crux.Code
		}
	}
	for index := range hits {
		hit := &hits[index]
		path, from, to, ok := parseAskPointer(hit.Pointer)
		if !ok {
			continue
		}
		if !full {
			if crux, exists := cruxByPointer[hit.Pointer]; exists {
				hit.Code = crux + "\n… (crux — full definition at " + hit.Pointer + "; rerun with --full)"
				continue
			}
		}
		code, exists := sliceAskSpan(filepath.Join(root, filepath.FromSlash(path)), from, to, path, full)
		if exists {
			hit.Code = code
		}
	}
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

func sliceAskSpan(path string, from, to int, relativePath string, full bool) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ToValidUTF8(string(data), "�"), "\n")
	end := min(to, len(lines))
	if from > end {
		return "", false
	}
	selected := lines[from-1 : end]
	if len(selected) > maxAskSpanLines {
		selected = append(selected[:maxAskSpanLines], fmt.Sprintf("… (+%d more lines; open %s:L%d-L%d)", len(selected)-maxAskSpanLines, relativePath, from, end))
	}
	return strings.Join(selected, "\n"), true
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

func askSavingsLine(result graph.AskResult, body string) string {
	if result.Saved == nil {
		return ""
	}
	return savings.AskLine(body, result.Saved.Files, result.Saved.BaselineChars)
}
