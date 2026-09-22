package main

import (
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
)

const maxAskSpanLines = 80

var askPointerPattern = regexp.MustCompile(`^(.*):L(\d+)-L(\d+)$`)

func runAsk(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	limit, err := askLimit(opts.limit)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if children, ok := graph.ReadWorkspaceChildren(contextDir); ok {
		return runWorkspaceAsk(root, contextDir, children, opts, limit, stdout, stderr)
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
		Limit:       limit,
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

func runWorkspaceAsk(root, contextDir string, children []string, opts callersOptions, limit int, stdout, stderr io.Writer) int {
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
			Limit:                  max(limit*4, 20),
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

	baselineRuns := make([]graph.AskWorkspaceRun, 0, len(baselineCandidates))
	type baselineBack struct {
		child string
		group string
	}
	baselineByHit := make(map[string]baselineBack)
	for _, candidate := range baselineCandidates {
		entries := make([]graph.AskHit, 0)
		if candidate.ranking != nil {
			entries = make([]graph.AskHit, 0, len(candidate.ranking.Baseline))
			for _, entry := range candidate.ranking.Baseline {
				entries = append(entries, entry.Hit)
				baselineByHit[workspaceHitIdentity(candidate.child, entry.Hit)] = baselineBack{child: candidate.child, group: candidate.child + "\x00" + entry.Group}
			}
		}
		if len(entries) == 0 {
			entries = candidate.result.Hits
			for _, hit := range entries {
				baselineByHit[workspaceHitIdentity(candidate.child, hit)] = baselineBack{child: candidate.child, group: candidate.child + "\x00" + workspaceHitGroup(hit)}
			}
		}
		baselineRuns = append(baselineRuns, graph.AskWorkspaceRun{Scope: candidate.child, Hits: entries})
	}
	baselineResult := graph.FuseAsk(opts.query, baselineRuns, 0)
	var baselineTop graph.AskHit
	var baselineTopBack baselineBack
	hasBaselineTop := len(baselineResult.Hits) > 0
	if hasBaselineTop {
		baselineTop = baselineResult.Hits[0]
		baselineTopBack, hasBaselineTop = baselineByHit[workspaceHitIdentity(baselineTop.Scope, baselineTop)]
	}

	fileRuns := make([]graph.AskWorkspaceRun, 0, len(survivors))
	fileBack := make(map[string]workspaceGroup)
	for _, candidate := range survivors {
		leaders := make([]graph.AskHit, 0, len(candidate.groups))
		for _, group := range candidate.groups {
			leader := group.hits[0]
			leaders = append(leaders, leader)
			fileBack[workspaceHitIdentity(candidate.child, leader)] = group
		}
		if len(leaders) > 0 {
			fileRuns = append(fileRuns, graph.AskWorkspaceRun{Scope: candidate.child, Hits: leaders})
		}
	}
	fileResult := graph.FuseAsk(opts.query, fileRuns, 0)
	rankedGroups := make([]workspaceGroup, 0, len(fileResult.Hits))
	for _, ranked := range fileResult.Hits {
		group, ok := fileBack[workspaceHitIdentity(ranked.Scope, ranked)]
		if !ok {
			continue
		}
		group.hits = qualifyWorkspaceHits(group.child, group.hits, ranked.Score)
		group.baselineHits = qualifyWorkspaceHits(group.child, group.baselineHits, 0)
		rankedGroups = append(rankedGroups, group)
	}
	if hasBaselineTop {
		baselineTop = qualifyWorkspaceHit(baselineTopBack.child, baselineTop, baselineTop.Score)
	}
	projectedGroups := slices.Clone(rankedGroups)
	if hasBaselineTop {
		groupIndex := slices.IndexFunc(projectedGroups, func(group workspaceGroup) bool {
			return group.key == baselineTopBack.group
		})
		baselineQueue := []graph.AskHit{baselineTop}
		projectedScore := baselineTop.Score
		if groupIndex >= 0 {
			baselineQueue = append([]graph.AskHit(nil), projectedGroups[groupIndex].baselineHits...)
			projectedScore = projectedGroups[groupIndex].hits[0].Score
		}
		lockedHits := []graph.AskHit{baselineTop}
		for _, hit := range baselineQueue {
			if !sameWorkspaceHit(hit, baselineTop) {
				hit.Score = projectedScore
				lockedHits = append(lockedHits, hit)
			}
		}
		locked := workspaceGroup{key: baselineTopBack.group, child: baselineTopBack.child, hits: lockedHits, baselineHits: baselineQueue}
		if groupIndex >= 0 {
			projectedGroups = append([]workspaceGroup{locked}, append(projectedGroups[:groupIndex], projectedGroups[groupIndex+1:]...)...)
		} else {
			projectedGroups = append([]workspaceGroup{locked}, projectedGroups...)
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
		if hasBaselineTop {
			federated = append(federated, baselineTop.Scope)
		}
		if fileResult.Scopes != nil {
			federated = appendUniqueStrings(federated, fileResult.Scopes.Federated...)
		}
		federatedSet := make(map[string]struct{}, len(federated))
		for _, scope := range federated {
			federatedSet[scope] = struct{}{}
		}
		alsoMatched := make([]graph.AskScopeMatch, 0)
		if fileResult.Scopes != nil {
			for _, match := range fileResult.Scopes.AlsoMatched {
				if _, ok := federatedSet[match.Scope]; !ok {
					alsoMatched = append(alsoMatched, match)
				}
			}
		}
		for _, match := range gatedOut {
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
	if opts.source {
		inlineAskSource(root, graph.GraphV1{}, &result, opts.full)
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

func workspaceHitIdentity(scope string, hit graph.AskHit) string {
	return scope + "\x00" + hit.Kind + "\x00" + hit.Title + "\x00" + hit.Pointer
}

func qualifyWorkspaceHit(child string, hit graph.AskHit, score float64) graph.AskHit {
	hit.Pointer = prefixAskPointer(child, hit.Pointer)
	hit.Scope = child
	if score != 0 {
		hit.Score = score
	}
	return hit
}

func qualifyWorkspaceHits(child string, hits []graph.AskHit, score float64) []graph.AskHit {
	qualified := make([]graph.AskHit, 0, len(hits))
	for _, hit := range hits {
		qualified = append(qualified, qualifyWorkspaceHit(child, hit, score))
	}
	return qualified
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

func workspaceRoundRobin(groups [][]graph.AskHit, limit int) []graph.AskHit {
	selected := make([]graph.AskHit, 0)
	for depth := 0; ; depth++ {
		added := false
		for _, group := range groups {
			if depth >= len(group) {
				continue
			}
			selected = append(selected, group[depth])
			added = true
			if limit > 0 && len(selected) >= limit {
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

func askLimit(raw string) (int, error) {
	if raw == "" {
		return 8, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("--limit must be a positive integer, got %q", raw)
	}
	return value, nil
}

func writeAskJSON(stdout, stderr io.Writer, result graph.AskResult) int {
	data, err := json.MarshalIndent(result, "", "  ")
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
	head := fmt.Sprintf("graft ask — %q  (%s)", result.Query, result.Mode)
	note := askNoteBlock(result.Note)
	if len(result.Hits) == 0 {
		body := note
		if body == "" {
			body = "no matches."
		}
		body += askEscalationNudge(result)
		_, err := fmt.Fprintf(stdout, "%s\n\n%s\n", head, body)
		return writeAskError(err)
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
			if hit.Scope != "" {
				label = "[" + hit.Scope + "/] "
			}
			lines = append(lines, fmt.Sprintf("%d. %s%s  [%s]", index+1, label, hit.Title, hit.Kind))
			lines = append(lines, "   "+hit.Pointer)
			if hit.Snippet != "" {
				lines = append(lines, "   "+hit.Snippet)
			}
			if hit.Code != "" {
				lines = append(lines, "", "```", hit.Code, "```")
			}
			lines = append(lines, "")
		}
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
	body := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if savings := askSavingsLine(result, body); savings != "" {
		body = savings + "\n\n" + body
	}
	body += askEscalationNudge(result)
	_, err := io.WriteString(stdout, body+"\n")
	return writeAskError(err)
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
	cruxByPointer := make(map[string]string)
	for _, node := range wiring.Nodes {
		if node.Crux != nil && node.Crux.Code != "" {
			cruxByPointer[node.Path+":"+node.Span] = node.Crux.Code
		}
	}
	for index := range result.Hits {
		hit := &result.Hits[index]
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
	savings := &graph.AskSavings{}
	for path := range paths {
		chars, ok := charsByPath[path]
		if !ok {
			continue
		}
		savings.Files++
		savings.BaselineChars += chars
	}
	if savings.Files == 0 {
		return nil
	}
	return savings
}

func askSavingsLine(result graph.AskResult, body string) string {
	if result.Saved == nil || result.Saved.BaselineChars <= 0 {
		return ""
	}
	pack := (len(body) + 2) / 4
	base := (result.Saved.BaselineChars + 2) / 4
	if base <= pack {
		return ""
	}
	saved := base - pack
	pct := saved * 100 / base
	return fmt.Sprintf("[graft] tokens saved ≈ %s (%d%%) — this pack ≈ %s tok vs reading the %d source file(s) whole ≈ %s tok. Estimate (baseline = those files read in full).", formatAskNumber(saved), pct, formatAskNumber(pack), result.Saved.Files, formatAskNumber(base))
}

func formatAskNumber(value int) string {
	if value < 1000 {
		return strconv.Itoa(value)
	}
	return fmt.Sprintf("%d,%03d", value/1000, value%1000)
}
