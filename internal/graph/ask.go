package graph

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
)

const (
	defaultAskLimit       = 8
	askGraphWeight        = 0.5
	askRescueFloor        = 0.15
	askTestPenalty        = 0.35
	askParticipationRatio = 0.25
)

var (
	askStructuralIncoming = regexp.MustCompile(`(?i)\b(caller|callers|calls?\s+into|who\s+calls|what\s+calls|called\s+by|used\s+by|uses)\b`)
	askStructuralOutgoing = regexp.MustCompile(`(?i)\b(callee|callees|what\s+does\s+\w+\s+call|calls\s+what|imports?|depends\s+on)\b`)
	askSubjectSeparator   = regexp.MustCompile(`[^A-Za-z0-9_.]+`)
	askCamelBoundary      = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	askTokenSeparator     = regexp.MustCompile(`[^a-z0-9]+`)
)

var askStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "can": {}, "do": {}, "does": {},
	"for": {}, "get": {}, "how": {}, "i": {}, "in": {}, "is": {}, "it": {},
	"of": {}, "on": {}, "or": {}, "set": {}, "that": {}, "the": {}, "this": {},
	"to": {}, "use": {}, "used": {}, "using": {}, "we": {}, "what": {}, "when": {},
	"where": {}, "which": {}, "why": {}, "with": {},
}

// AskOptions controls deterministic graph and lexical retrieval.
type AskOptions struct {
	// Limit caps the returned hits. Zero uses the CLI default.
	Limit int
	// In narrows matching nodes to this segment-aware path prefix.
	In string
	// NoGraphRank disables the connectivity re-rank for lexical results.
	NoGraphRank bool
	// Index supplies the build-time token sidecar for a slim persisted graph.
	Index *AskIndex
	// Concepts supplies prose nodes loaded from root-level context markdown.
	Concepts []AskConcept
	// FileFirst controls whether lexical results emit one leader per file before
	// sibling spans. Nil preserves the CLI default of true.
	FileFirst *bool
	// FileComplement enables bounded union evidence from lexical symbols in one
	// file. The default is enabled whenever the file-first default is active.
	FileComplement bool
	// FileTopLock controls whether the exact baseline top hit leads the result.
	// Nil follows FileFirst, matching the TypeScript retrieval contract.
	FileTopLock *bool
	// IncludeRankingMetadata exposes internal file queues for workspace fusion.
	IncludeRankingMetadata bool
}

// AskConcept is a prose node participating in lexical Ask ranking.
type AskConcept struct {
	Slug    string
	Name    string
	Sources []string
	Related []string
	Snippet string
	Text    string
}

// AskIndexDoc contains token-count fields for one graph node.
type AskIndexDoc struct {
	Name map[string]int
	Path map[string]int
	Body map[string]int
}

// AskIndex is the validated, in-memory form of the ask token sidecar.
type AskIndex struct {
	Version    int
	AvgBodyLen float64
	DF         map[string]int
	DocCount   int
	Docs       map[string]AskIndexDoc
}

// AskHit is one structural or lexical result returned by Ask.
type AskHit struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Pointer  string   `json:"pointer"`
	Snippet  string   `json:"snippet"`
	Relation Relation `json:"relation,omitempty"`
	Related  []string `json:"related,omitempty"`
	Score    float64  `json:"score"`
	Code     string   `json:"code,omitempty"`
	Scope    string   `json:"scope,omitempty"`
}

// AskSavings records the whole-file baseline for returned hits.
type AskSavings struct {
	Files         int `json:"files"`
	BaselineChars int `json:"baselineChars"`
}

// AskResult is the JSON-compatible result of a graph query.
type AskResult struct {
	Query          string              `json:"query"`
	Mode           string              `json:"mode"`
	Subject        string              `json:"subject,omitempty"`
	Hits           []AskHit            `json:"hits"`
	Note           string              `json:"note,omitempty"`
	Saved          *AskSavings         `json:"saved,omitempty"`
	Rules          []AppliedRule       `json:"rules,omitempty"`
	Coverage       *float64            `json:"coverage,omitempty"`
	CoverageStrong *float64            `json:"coverageStrong,omitempty"`
	Scopes         *AskScopes          `json:"scopes,omitempty"`
	Ranking        *AskRankingMetadata `json:"-"`
}

// AskRankingGroup is one file or concept queue used by file-aware ranking.
type AskRankingGroup struct {
	Key            string
	Hits           []AskHit
	BaselineHits   []AskHit
	Coverage       float64
	CoverageStrong float64
}

// AskRankingEntry ties a baseline hit to its file or concept queue.
type AskRankingEntry struct {
	Group string
	Hit   AskHit
}

// AskRankingMetadata carries internal queues across a workspace boundary.
type AskRankingMetadata struct {
	Groups                 []AskRankingGroup
	Baseline               []AskRankingEntry
	BaselineCoverage       *float64
	BaselineCoverageStrong *float64
}

// AskScopeMatch identifies a workspace scope that matched but was not fused.
type AskScopeMatch struct {
	Scope  string `json:"scope"`
	BestID string `json:"bestId"`
}

// AskScopes records the workspace scopes represented in a fused result.
type AskScopes struct {
	Federated   []string        `json:"federated"`
	AlsoMatched []AskScopeMatch `json:"alsoMatched"`
}

// AskWorkspaceRun is one child repository's ranked Ask result.
type AskWorkspaceRun struct {
	Scope string
	Hits  []AskHit
}

// FuseAsk combines independently scored workspace Ask results by reciprocal rank.
func FuseAsk(query string, runs []AskWorkspaceRun, limit int) AskResult {
	byScope := make(map[string][]askFusionDocument)
	for _, run := range runs {
		for index, hit := range run.Hits {
			if hit.Score <= 0 {
				continue
			}
			id := fmt.Sprintf("%s %d", run.Scope, index)
			byScope[run.Scope] = append(byScope[run.Scope], askFusionDocument{
				id:    id,
				scope: run.Scope,
				hit:   hit,
			})
		}
	}
	if len(byScope) == 0 {
		return AskResult{Query: query, Mode: "empty", Hits: make([]AskHit, 0)}
	}
	for scope := range byScope {
		slices.SortFunc(byScope[scope], func(left, right askFusionDocument) int {
			return cmp.Or(cmp.Compare(right.hit.Score, left.hit.Score), cmp.Compare(left.id, right.id))
		})
	}
	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	slices.Sort(scopes)
	participating := scopes
	alsoMatched := make([]AskScopeMatch, 0)
	if len(scopes) > 1 {
		ordered := slices.Clone(scopes)
		slices.SortFunc(ordered, func(left, right string) int {
			return cmp.Or(
				cmp.Compare(byScope[right][0].hit.Score, byScope[left][0].hit.Score),
				cmp.Compare(left, right),
			)
		})
		gate := askParticipationRatio * byScope[ordered[0]][0].hit.Score
		participating = make([]string, 0, len(ordered))
		for _, scope := range ordered {
			if byScope[scope][0].hit.Score >= gate {
				participating = append(participating, scope)
				continue
			}
			alsoMatched = append(alsoMatched, AskScopeMatch{Scope: scope, BestID: byScope[scope][0].id})
		}
	}
	result := AskResult{
		Query: query,
		Mode:  "lexical",
		Hits:  make([]AskHit, 0),
		Scopes: &AskScopes{
			Federated:   participating,
			AlsoMatched: alsoMatched,
		},
	}
	if len(participating) == 1 {
		for _, document := range byScope[participating[0]] {
			hit := document.hit
			hit.Scope = document.scope
			result.Hits = append(result.Hits, hit)
		}
	} else {
		ranked := make([]askFusionDocument, 0)
		maximum := 0.0
		for _, scope := range participating {
			for index, document := range byScope[scope] {
				document.hit.Score = 1 / float64(61+index)
				maximum = max(maximum, document.hit.Score)
				ranked = append(ranked, document)
			}
		}
		for index := range ranked {
			ranked[index].hit.Score /= maximum
			ranked[index].hit.Scope = ranked[index].scope
		}
		slices.SortFunc(ranked, func(left, right askFusionDocument) int {
			return cmp.Or(
				cmp.Compare(right.hit.Score, left.hit.Score),
				cmp.Compare(left.scope, right.scope),
				cmp.Compare(left.id, right.id),
			)
		})
		for _, document := range ranked {
			result.Hits = append(result.Hits, document.hit)
		}
	}
	if limit > 0 && len(result.Hits) > limit {
		result.Hits = result.Hits[:limit]
	}
	return result
}

type askFusionDocument struct {
	id    string
	scope string
	hit   AskHit
}

type askDocument struct {
	node NodeV1
	name map[string]int
	path map[string]int
	body map[string]int
}

type askConceptDocument struct {
	concept AskConcept
	name    map[string]int
	body    map[string]int
}

type askCandidate struct {
	node               NodeV1
	rawLexical         float64
	lexical            float64
	graph              float64
	rankFactor         float64
	baseline           float64
	matchedTerms       map[string]struct{}
	matchedStrongTerms map[string]struct{}
	eligible           bool
}

// Ask routes a plain-language query to structural edges or lexical graph ranking.
func Ask(wiring GraphV1, query string, opts AskOptions) (AskResult, error) {
	limit := opts.Limit
	if limit == 0 {
		limit = defaultAskLimit
	}
	if limit < 0 {
		return AskResult{}, fmt.Errorf("--limit must be a positive integer, got %q", fmt.Sprint(limit))
	}
	prefix := normalizePathPrefix(opts.In)
	if prefix != "" {
		indexed := slices.ContainsFunc(wiring.Nodes, func(node NodeV1) bool {
			return pathUnderPrefix(node.Path, prefix)
		})
		if !indexed {
			return AskResult{}, fmt.Errorf("nothing indexed under %q (or any path prefix)", prefix+"/")
		}
	}

	if incoming := askStructuralIncoming.MatchString(query); incoming || askStructuralOutgoing.MatchString(query) {
		structuralResult, fallthroughNote := askStructural(wiring, query, limit, prefix, incoming)
		if structuralResult != nil {
			return *structuralResult, nil
		}
		result := askLexical(wiring, query, limit, prefix, opts)
		result.Note = fallthroughNote
		if result.Note == "" {
			result.Note = fmt.Sprintf("structural index: no entries for %q — showing lexical matches", askSubjectWords(query)[0])
		}
		return result, nil
	}

	return askLexical(wiring, query, limit, prefix, opts), nil
}

func askFileOptions(opts AskOptions) (fileFirst, fileComplement, fileTopLock bool) {
	fileFirst = true
	if opts.FileFirst != nil {
		fileFirst = *opts.FileFirst
	}
	fileTopLock = fileFirst
	if opts.FileTopLock != nil {
		fileTopLock = *opts.FileTopLock
	}
	fileComplement = opts.FileComplement || fileTopLock
	return fileFirst, fileComplement, fileTopLock
}

func askStructural(wiring GraphV1, query string, limit int, prefix string, incoming bool) (*AskResult, string) {
	words := askSubjectWords(query)
	var subjects []NodeV1
	tried := query
	for _, word := range words {
		tried = word
		matches, err := ResolveSymbol(wiring, word, ResolveSymbolOptions{In: prefix})
		if err != nil {
			return nil, ""
		}
		if len(matches) > 0 {
			subjects = matches
			break
		}
	}
	if len(subjects) == 0 {
		return nil, askFallthroughNote(tried)
	}

	outgoing := askStructuralOutgoing.MatchString(query) && !incoming
	relations := map[Relation]struct{}{"calls": {}, "references": {}, "implements": {}, "extends": {}}
	if outgoing {
		relations = map[Relation]struct{}{"calls": {}, "references": {}, "imports": {}, "implements": {}, "extends": {}}
	}
	ids := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		ids[subject.ID] = struct{}{}
	}
	nodes := nodeIndex(wiring)
	seen := make(map[string]struct{})
	hits := make([]AskHit, 0)
	for _, edge := range wiring.Edges {
		if _, ok := relations[edge.Relation]; !ok {
			continue
		}
		anchor, other := edge.Target, edge.Source
		kind := "caller"
		if outgoing {
			anchor, other = edge.Source, edge.Target
			kind = "callee"
		}
		if _, ok := ids[anchor]; !ok {
			continue
		}
		key := other + string(edge.Relation)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		node := nodes[other]
		hit := AskHit{Kind: kind, Pointer: other, Title: other, Relation: edge.Relation, Score: 1}
		if node != nil {
			hit.Title = node.Name
			hit.Pointer = node.Path + ":" + node.Span
			hit.Snippet = askNodeSnippet(*node)
		}
		hits = append(hits, hit)
	}
	if len(hits) == 0 {
		return nil, askFallthroughNote(subjects[0].Name)
	}
	slices.SortFunc(hits, func(left, right AskHit) int {
		return cmp.Compare(left.Pointer, right.Pointer)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	note := fmt.Sprintf("callers / references of %s", subjects[0].Name)
	if outgoing {
		note = fmt.Sprintf("outgoing edges from %s", subjects[0].Name)
	}
	return &AskResult{
		Query:   query,
		Mode:    "structural",
		Subject: subjects[0].Name,
		Hits:    hits,
		Note:    note,
	}, ""
}

func askLexical(wiring GraphV1, query string, limit int, prefix string, opts AskOptions) AskResult {
	fileFirst, fileComplement, fileTopLock := askFileOptions(opts)
	result := AskResult{Query: query, Mode: "empty", Hits: make([]AskHit, 0)}
	queryTerms := askTermCounts(query)
	for term := range queryTerms {
		queryTerms[term] = 1
	}
	if len(queryTerms) == 0 {
		result.Note = "no matching nodes — try different words, or `graft build` if graft/ is empty"
		return result
	}

	usableIndex := askUsableIndex(opts.Index, wiring)
	conceptDocuments := make([]askConceptDocument, 0, len(opts.Concepts))
	for _, concept := range opts.Concepts {
		if prefix != "" && !slices.ContainsFunc(concept.Sources, func(path string) bool {
			return pathUnderPrefix(path, prefix)
		}) {
			continue
		}
		conceptDocuments = append(conceptDocuments, askConceptDocument{
			concept: concept,
			name:    askCounts(concept.Name),
			body:    askCounts(concept.Text),
		})
	}
	documents := make([]askDocument, 0, len(wiring.Nodes))
	for _, node := range wiring.Nodes {
		if prefix != "" && !pathUnderPrefix(node.Path, prefix) {
			continue
		}
		name := askCounts(node.Name)
		path := askCounts(node.Path)
		body := askCounts(askNodeBody(node))
		if usableIndex != nil {
			if indexed, ok := usableIndex.Docs[node.ID]; ok {
				name = maps.Clone(indexed.Name)
				path = maps.Clone(indexed.Path)
				body = maps.Clone(indexed.Body)
			}
		}
		documents = append(documents, askDocument{
			node: node,
			name: name,
			path: path,
			body: body,
		})
	}
	if len(documents) == 0 {
		result.Note = "no matching nodes — try different words, or `graft build` if graft/ is empty"
		return result
	}
	documentsByID := make(map[string]askDocument, len(documents))
	for _, document := range documents {
		documentsByID[document.node.ID] = document
	}

	df := make(map[string]int)
	documentCount := len(documents) + len(conceptDocuments)
	averageBodyLength := 0.0
	if usableIndex != nil && prefix == "" {
		df = maps.Clone(usableIndex.DF)
		documentCount = usableIndex.DocCount + len(conceptDocuments)
		averageBodyLength = usableIndex.AvgBodyLen
	} else {
		for _, document := range conceptDocuments {
			seen := make(map[string]struct{})
			for term := range document.name {
				seen[term] = struct{}{}
			}
			for term := range document.body {
				seen[term] = struct{}{}
			}
			for term := range seen {
				df[term]++
			}
		}
		for _, doc := range documents {
			seen := make(map[string]struct{})
			for term := range doc.name {
				seen[term] = struct{}{}
			}
			for term := range doc.path {
				seen[term] = struct{}{}
			}
			for term := range doc.body {
				seen[term] = struct{}{}
			}
			for term := range seen {
				df[term]++
			}
		}
		if len(documents) > 0 {
			for _, doc := range documents {
				averageBodyLength += float64(askMapLength(doc.body))
			}
			averageBodyLength /= float64(len(documents))
		}
	}
	if usableIndex != nil && prefix == "" {
		for _, document := range conceptDocuments {
			seen := make(map[string]struct{})
			for term := range document.name {
				seen[term] = struct{}{}
			}
			for term := range document.body {
				seen[term] = struct{}{}
			}
			for term := range seen {
				df[term]++
			}
		}
	}
	idf := make(map[string]float64, len(df))
	for term, frequency := range df {
		idf[term] = math.Log(1 + float64(documentCount)/float64(1+frequency))
	}
	defaultIDF := math.Log(1 + float64(documentCount))

	lexical := make(map[string]float64)
	maxLexical := 0.0
	for _, doc := range documents {
		total := askScore(queryTerms, doc.name, idf, defaultIDF)*3 +
			askScore(queryTerms, doc.path, idf, defaultIDF)*2 +
			askBM25(queryTerms, doc.body, idf, defaultIDF, askMapLength(doc.body), averageBodyLength)
		if total == 0 {
			continue
		}
		factor := 1.0
		if !askWantsTests(query) && askIsTestPath(doc.node.Path) {
			factor = askTestPenalty
		}
		lexical[doc.node.ID] = total * factor
		maxLexical = max(maxLexical, total*factor)
	}

	graphRank := make(map[string]float64)
	if !opts.NoGraphRank && len(lexical) > 0 {
		graphRank = askPageRank(wiring, lexical, prefix)
	}
	ids := make(map[string]NodeV1, len(documents))
	for _, doc := range documents {
		ids[doc.node.ID] = doc.node
	}
	for id, score := range graphRank {
		if score >= askRescueFloor {
			if _, ok := ids[id]; ok {
				if _, exists := lexical[id]; !exists {
					lexical[id] = 0
				}
			}
		}
	}

	candidates := make([]askCandidate, 0, len(lexical))
	for id, rawScore := range lexical {
		node, ok := ids[id]
		if !ok {
			continue
		}
		lexicalScore := 0.0
		if maxLexical > 0 {
			lexicalScore = rawScore / maxLexical
		}
		factor := 1.0
		if !askWantsTests(query) && askIsTestPath(node.Path) {
			factor = askTestPenalty
		}
		baseline := (lexicalScore + askGraphWeight*graphRank[id]) * factor
		if baseline <= 0 {
			continue
		}
		matchedTerms := make(map[string]struct{})
		matchedStrongTerms := make(map[string]struct{})
		if document, exists := documentsByID[id]; exists {
			for term := range queryTerms {
				if hasAskTerm(document.name, term) || hasAskTerm(document.path, term) || hasAskTerm(document.body, term) {
					matchedTerms[term] = struct{}{}
				}
				if node.Kind != Kind("file") && hasAskTerm(document.name, term) {
					matchedStrongTerms[term] = struct{}{}
				}
			}
		}
		candidates = append(candidates, askCandidate{
			node:               node,
			rawLexical:         rawScore,
			lexical:            lexicalScore,
			graph:              graphRank[id],
			rankFactor:         factor,
			baseline:           baseline,
			matchedTerms:       matchedTerms,
			matchedStrongTerms: matchedStrongTerms,
			eligible:           node.Kind != Kind("file") && rawScore > 0,
		})
	}
	conceptHits := make([]AskHit, 0, len(conceptDocuments))
	maxConcept := 0.0
	for _, document := range conceptDocuments {
		total := askScore(queryTerms, document.name, idf, defaultIDF)*3 +
			askScore(queryTerms, document.body, idf, defaultIDF)
		if total <= 0 {
			continue
		}
		maxConcept = max(maxConcept, total)
		conceptHits = append(conceptHits, AskHit{
			Kind:    "concept",
			Title:   firstNonEmpty(document.concept.Name, document.concept.Slug),
			Pointer: askConceptPointer(document.concept),
			Snippet: document.concept.Snippet,
			Related: document.concept.Related,
			Score:   total,
		})
	}
	if maxConcept > 0 {
		for index := range conceptHits {
			conceptHits[index].Score /= maxConcept
		}
	}
	rankedFiles := askRankedFiles(candidates, queryTerms, idf, defaultIDF)
	needsFileQueues := fileComplement && (fileTopLock || opts.IncludeRankingMetadata)
	baselineCandidates := slices.Clone(candidates)
	slices.SortFunc(baselineCandidates, askCandidateBaselineOrder)
	baselineSymbolHits := make([]AskHit, 0, len(baselineCandidates))
	if needsFileQueues {
		candidatesToBuild := baselineCandidates
		if !opts.IncludeRankingMetadata && len(candidatesToBuild) > 1 {
			candidatesToBuild = candidatesToBuild[:1]
		}
		for _, candidate := range candidatesToBuild {
			hit := askCandidateHit(candidate, candidate.baseline)
			baselineSymbolHits = append(baselineSymbolHits, hit)
		}
	}

	fileGroups := make([]AskRankingGroup, 0, len(rankedFiles))
	fileQueueByGroup := make(map[string][]AskHit, len(rankedFiles))
	baselineQueueByGroup := make(map[string][]AskHit, len(rankedFiles))
	symbolHits := make([]AskHit, 0, len(rankedFiles))
	for _, file := range rankedFiles {
		key := "file:" + file.file
		fileQueue := make([]AskHit, 0, len(file.queue))
		baselineQueue := make([]AskHit, 0, len(file.queue))
		for index, candidate := range file.queue {
			score := candidate.baseline
			if index == 0 {
				score = file.score
			}
			fileQueue = append(fileQueue, askCandidateHit(candidate, score))
			baselineQueue = append(baselineQueue, askCandidateHit(candidate, candidate.baseline))
		}
		if len(fileQueue) == 0 {
			continue
		}
		fileQueueByGroup[key] = fileQueue
		baselineQueueByGroup[key] = baselineQueue
		groupHits := fileQueue[:1]
		if opts.IncludeRankingMetadata {
			groupHits = fileQueue
		}
		fileGroups = append(fileGroups, AskRankingGroup{
			Key:            key,
			Hits:           groupHits,
			BaselineHits:   baselineQueue,
			Coverage:       file.unionCoverage,
			CoverageStrong: file.unionStrongCoverage,
		})
		symbolHits = append(symbolHits, groupHits[0])
	}
	if !fileComplement {
		fileGroups = nil
		symbolHits = symbolHits[:0]
		for _, candidate := range baselineCandidates {
			symbolHits = append(symbolHits, askCandidateHit(candidate, candidate.baseline))
		}
	}

	hits := append(slices.Clone(conceptHits), symbolHits...)
	slices.SortFunc(hits, askHitScoreOrder)
	baselineScored := hits
	if needsFileQueues {
		baselineScored = append(slices.Clone(conceptHits), baselineSymbolHits...)
		slices.SortFunc(baselineScored, askHitScoreOrder)
	}

	conceptGroups := make([]AskRankingGroup, 0, len(conceptHits))
	for _, hit := range conceptHits {
		coverage := askCoverage(queryTerms, hit, documents, conceptDocuments, idf, defaultIDF, false)
		coverageStrong := askCoverage(queryTerms, hit, documents, conceptDocuments, idf, defaultIDF, true)
		conceptGroups = append(conceptGroups, AskRankingGroup{
			Key:            askHitGroupKey(hit),
			Hits:           []AskHit{hit},
			BaselineHits:   []AskHit{hit},
			Coverage:       coverage,
			CoverageStrong: coverageStrong,
		})
	}
	unlockedGroups := append(conceptGroups, fileGroups...)
	slices.SortFunc(unlockedGroups, func(left, right AskRankingGroup) int {
		return cmp.Or(
			cmp.Compare(right.Hits[0].Score, left.Hits[0].Score),
			cmp.Compare(left.Hits[0].Title, right.Hits[0].Title),
		)
	})
	projectedGroups := slices.Clone(unlockedGroups)
	lockedGroupKey := ""
	if fileTopLock && fileFirst && len(baselineScored) > 0 {
		baselineTop := baselineScored[0]
		lockedGroupKey = askHitGroupKey(baselineTop)
		groupIndex := slices.IndexFunc(projectedGroups, func(group AskRankingGroup) bool {
			return group.Key == lockedGroupKey
		})
		baselineQueue := baselineQueueByGroup[lockedGroupKey]
		if baselineTop.Kind == "concept" {
			baselineQueue = []AskHit{baselineTop}
		} else if !opts.IncludeRankingMetadata && limit <= len(projectedGroups) {
			baselineQueue = nil
		}
		lockedHits := []AskHit{baselineTop}
		for _, hit := range baselineQueue {
			if !sameAskHit(hit, baselineTop) {
				lockedHits = append(lockedHits, hit)
			}
		}
		locked := AskRankingGroup{
			Key:            lockedGroupKey,
			Hits:           lockedHits,
			BaselineHits:   baselineQueue,
			Coverage:       askCoverage(queryTerms, baselineTop, documents, conceptDocuments, idf, defaultIDF, false),
			CoverageStrong: askCoverage(queryTerms, baselineTop, documents, conceptDocuments, idf, defaultIDF, true),
		}
		if groupIndex >= 0 {
			locked.Coverage = projectedGroups[groupIndex].Coverage
			locked.CoverageStrong = projectedGroups[groupIndex].CoverageStrong
			projectedGroups = append([]AskRankingGroup{locked}, append(projectedGroups[:groupIndex], projectedGroups[groupIndex+1:]...)...)
		} else {
			projectedGroups = append([]AskRankingGroup{locked}, projectedGroups...)
		}
	}
	if fileTopLock && fileFirst && !opts.IncludeRankingMetadata && limit > len(projectedGroups) {
		for index := range projectedGroups {
			if projectedGroups[index].Key == lockedGroupKey {
				continue
			}
			if queue := fileQueueByGroup[projectedGroups[index].Key]; len(queue) > 0 {
				projectedGroups[index].Hits = queue
			}
		}
	}

	switch {
	case fileTopLock && fileFirst:
		hits = askRoundRobinGroups(projectedGroups, limit)
	case fileFirst:
		hits = askFileFirst(hits, limit)
	default:
		if limit > 0 && len(hits) > limit {
			hits = hits[:limit]
		}
	}
	if opts.IncludeRankingMetadata && needsFileQueues {
		groups := slices.Clone(unlockedGroups)
		if limit > 0 && len(groups) > limit {
			groups = groups[:limit]
		}
		for index := range groups {
			if len(groups[index].Hits) > limit {
				groups[index].Hits = groups[index].Hits[:limit]
			}
			if len(groups[index].BaselineHits) > limit {
				groups[index].BaselineHits = groups[index].BaselineHits[:limit]
			}
		}
		entries := make([]AskRankingEntry, 0, min(len(baselineScored), limit))
		for index, hit := range baselineScored {
			if limit > 0 && index >= limit {
				break
			}
			entries = append(entries, AskRankingEntry{Group: askHitGroupKey(hit), Hit: hit})
		}
		metadata := &AskRankingMetadata{Groups: groups, Baseline: entries}
		if len(baselineScored) > 0 {
			coverage := askCoverage(queryTerms, baselineScored[0], documents, conceptDocuments, idf, defaultIDF, false)
			coverageStrong := askCoverage(queryTerms, baselineScored[0], documents, conceptDocuments, idf, defaultIDF, true)
			metadata.BaselineCoverage = &coverage
			metadata.BaselineCoverageStrong = &coverageStrong
		}
		result.Ranking = metadata
	}
	result.Hits = hits
	if len(hits) > 0 {
		result.Mode = "lexical"
		coverage := askCoverage(queryTerms, hits[0], documents, conceptDocuments, idf, defaultIDF, false)
		result.Coverage = &coverage
		coverageStrong := askCoverage(queryTerms, hits[0], documents, conceptDocuments, idf, defaultIDF, true)
		result.CoverageStrong = &coverageStrong
	} else {
		result.Note = "no matching nodes — try different words, or `graft build` if graft/ is empty"
	}
	return result
}

func askSubjectWords(query string) []string {
	seen := make(map[string]struct{})
	words := make([]string, 0)
	for _, word := range askSubjectSeparator.Split(query, -1) {
		if word == "" {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		words = append(words, word)
	}
	slices.SortFunc(words, func(left, right string) int {
		return cmp.Or(cmp.Compare(len(right), len(left)), cmp.Compare(left, right))
	})
	return words
}

func askTermCounts(text string) map[string]int {
	text = askCamelBoundary.ReplaceAllString(text, "$1 $2")
	counts := make(map[string]int)
	for _, term := range askTokenSeparator.Split(strings.ToLower(text), -1) {
		if term == "" || len(term) <= 1 {
			continue
		}
		if _, stop := askStopWords[term]; stop {
			continue
		}
		counts[term]++
	}
	return counts
}

func askCounts(text string) map[string]int {
	return askTermCounts(text)
}

func askScore(query, document map[string]int, idf map[string]float64, defaultIDF float64) float64 {
	total := 0.0
	for term, queryCount := range query {
		if documentCount := document[term]; documentCount > 0 {
			weight := idf[term]
			if weight == 0 {
				weight = defaultIDF
			}
			total += float64(queryCount*documentCount) * weight
		}
	}
	return total
}

func askBM25(query, document map[string]int, idf map[string]float64, defaultIDF float64, documentLength int, averageLength float64) float64 {
	const (
		k1 = 1.2
		b  = 0.75
	)
	normalization := k1 * (1 - b + b*float64(documentLength)/max(averageLength, 1))
	total := 0.0
	for term := range query {
		frequency := document[term]
		if frequency == 0 {
			continue
		}
		weight := idf[term]
		if weight == 0 {
			weight = defaultIDF
		}
		total += weight * (float64(frequency) * (k1 + 1) / (float64(frequency) + normalization))
	}
	return total
}

func askMapLength(values map[string]int) int {
	length := 0
	for _, count := range values {
		length += count
	}
	return length
}

type askRankedFile struct {
	file                string
	representative      askCandidate
	queue               []askCandidate
	score               float64
	unionCoverage       float64
	unionStrongCoverage float64
}

func askRankedFiles(candidates []askCandidate, query map[string]int, idf map[string]float64, defaultIDF float64) []askRankedFile {
	queryWeights := make(map[string]float64, len(query))
	totalWeight := 0.0
	for term := range query {
		weight := idf[term]
		if weight == 0 {
			weight = defaultIDF
		}
		queryWeights[term] = weight
		totalWeight += weight
	}
	if totalWeight > 0 {
		for term, weight := range queryWeights {
			queryWeights[term] = weight / totalWeight
		}
	}

	groups := make(map[string][]askCandidate)
	for _, candidate := range candidates {
		if candidate.baseline > 0 {
			groups[candidate.node.Path] = append(groups[candidate.node.Path], candidate)
		}
	}
	ranked := make([]askRankedFile, 0, len(groups))
	for file, members := range groups {
		donors := make([]askCandidate, 0, len(members))
		for _, candidate := range members {
			if candidate.eligible && candidate.rawLexical > 0 && candidate.lexical > 0 {
				donors = append(donors, candidate)
			}
		}
		slices.SortFunc(donors, func(left, right askCandidate) int {
			return cmp.Or(
				cmp.Compare(right.rawLexical, left.rawLexical),
				cmp.Compare(askCandidateCoverage(right, queryWeights), askCandidateCoverage(left, queryWeights)),
				cmp.Compare(askCandidateTitle(left), askCandidateTitle(right)),
				cmp.Compare(left.node.ID, right.node.ID),
			)
		})

		anchorCoverage := 0.0
		unionCoverage := 0.0
		unionStrongCoverage := 0.0
		pooledLexical := 0.0
		if len(donors) > 0 {
			anchorCoverage = askCandidateCoverage(donors[0], queryWeights)
			unionTerms := make(map[string]struct{})
			for _, donor := range donors {
				for term := range donor.matchedTerms {
					unionTerms[term] = struct{}{}
				}
			}
			for term := range unionTerms {
				unionCoverage += queryWeights[term]
			}
			unionCoverage = min(max(unionCoverage, 0), 1)
			unionStrongTerms := make(map[string]struct{})
			for _, donor := range donors {
				for term := range donor.matchedStrongTerms {
					unionStrongTerms[term] = struct{}{}
				}
			}
			for term := range unionStrongTerms {
				unionStrongCoverage += queryWeights[term]
			}
			unionStrongCoverage = min(max(unionStrongCoverage, 0), 1)
			residual := 0.0
			if unionCoverage > 0 {
				residual = anchorCoverage * max(unionCoverage-anchorCoverage, 0) / unionCoverage
			}
			pooledLexical = min(max(donors[0].lexical+(1-donors[0].lexical)*residual, 0), 1)
		}

		ordered := slices.Clone(members)
		slices.SortFunc(ordered, askCandidateBaselineOrder)
		if len(ordered) == 0 {
			continue
		}
		representative := ordered[0]
		score := representative.baseline
		if len(donors) > 0 {
			pooledScore := donors[0].rankFactor * (pooledLexical + askGraphWeight*donors[0].graph)
			if pooledScore > score {
				representative = donors[0]
				score = pooledScore
			}
		}
		queue := make([]askCandidate, 0, len(ordered))
		queue = append(queue, representative)
		for _, candidate := range ordered {
			if candidate.node.ID != representative.node.ID {
				queue = append(queue, candidate)
			}
		}
		ranked = append(ranked, askRankedFile{
			file:                file,
			representative:      representative,
			queue:               queue,
			score:               score,
			unionCoverage:       unionCoverage,
			unionStrongCoverage: unionStrongCoverage,
		})
	}
	slices.SortFunc(ranked, func(left, right askRankedFile) int {
		return cmp.Or(
			cmp.Compare(right.score, left.score),
			cmp.Compare(askCandidateTitle(left.representative), askCandidateTitle(right.representative)),
			cmp.Compare(left.file, right.file),
		)
	})

	return ranked
}

func askCandidateCoverage(candidate askCandidate, queryWeights map[string]float64) float64 {
	coverage := 0.0
	for term := range candidate.matchedTerms {
		coverage += queryWeights[term]
	}
	return min(max(coverage, 0), 1)
}

func askCandidateBaselineOrder(left, right askCandidate) int {
	return cmp.Or(
		cmp.Compare(right.baseline, left.baseline),
		cmp.Compare(askCandidateTitle(left), askCandidateTitle(right)),
		cmp.Compare(left.node.ID, right.node.ID),
	)
}

func askCandidateTitle(candidate askCandidate) string {
	return candidate.node.Name + " · " + string(candidate.node.Kind)
}

func askCandidateHit(candidate askCandidate, score float64) AskHit {
	return AskHit{
		Kind:    "symbol",
		Title:   askCandidateTitle(candidate),
		Pointer: askNodePointer(candidate.node),
		Snippet: askNodeSnippet(candidate.node),
		Score:   score,
	}
}

func askHitScoreOrder(left, right AskHit) int {
	return cmp.Or(cmp.Compare(right.Score, left.Score), cmp.Compare(left.Title, right.Title))
}

func askHitGroupKey(hit AskHit) string {
	if hit.Kind != "symbol" {
		return "concept:" + hit.Title + ":" + hit.Pointer
	}
	if marker := strings.Index(hit.Pointer, ":L"); marker >= 0 {
		return "file:" + hit.Pointer[:marker]
	}
	return "file:" + hit.Pointer
}

func sameAskHit(left, right AskHit) bool {
	return left.Kind == right.Kind && left.Pointer == right.Pointer && left.Title == right.Title
}

func askRoundRobinGroups(groups []AskRankingGroup, limit int) []AskHit {
	selected := make([]AskHit, 0)
	for depth := 0; ; depth++ {
		added := false
		for _, group := range groups {
			if depth >= len(group.Hits) {
				continue
			}
			selected = append(selected, group.Hits[depth])
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

func askNodeBody(node NodeV1) string {
	body := ""
	if node.Signature != nil {
		body += *node.Signature
	}
	if node.Summary != nil {
		body += " " + *node.Summary
	}
	if node.BodyText != nil {
		body += " " + *node.BodyText
	}
	return body
}

func askNodePointer(node NodeV1) string {
	if node.Kind == Kind("file") {
		return node.Path
	}
	return node.Path + ":" + node.Span
}

func askConceptPointer(concept AskConcept) string {
	if len(concept.Sources) > 0 {
		return strings.Join(concept.Sources, ", ")
	}
	return concept.Slug
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func askNodeSnippet(node NodeV1) string {
	if node.Summary != nil {
		return strings.TrimSpace(strings.SplitN(*node.Summary, "\n", 2)[0])
	}
	if node.Signature != nil {
		return *node.Signature
	}
	return ""
}

func askFallthroughNote(subject string) string {
	return fmt.Sprintf("structural index: no entries for '%s' — showing lexical matches; for precise edges try graft callers '%s', or graft grep '%s' for every reference (loosen the pattern if it returns nothing)", subject, subject, subject)
}

func askWantsTests(query string) bool {
	lower := strings.ToLower(query)
	for _, term := range []string{"test", "tests", "spec", "specs", "coverage", "assert", "asserts", "fixture", "fixtures", "mock", "mocks"} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func askIsTestPath(path string) bool {
	lower := strings.ToLower(path)
	segments := strings.Split(lower, "/")
	for _, segment := range segments[:max(len(segments)-1, 0)] {
		if segment == "test" || segment == "tests" || segment == "spec" || segment == "__tests__" {
			return true
		}
	}
	base := segments[len(segments)-1]
	return strings.Contains(base, "_test.") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_") || base == "conftest.py"
}

func askPageRank(wiring GraphV1, seeds map[string]float64, prefix string) map[string]float64 {
	ids := make(map[string]struct{})
	for _, node := range wiring.Nodes {
		if prefix == "" || pathUnderPrefix(node.Path, prefix) {
			ids[node.ID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return map[string]float64{}
	}
	adjacency := make(map[string][]string)
	for _, edge := range wiring.Edges {
		if !isWalkRelation(edge.Relation) {
			continue
		}
		if _, ok := ids[edge.Source]; !ok {
			continue
		}
		if _, ok := ids[edge.Target]; !ok {
			continue
		}
		adjacency[edge.Source] = append(adjacency[edge.Source], edge.Target)
		adjacency[edge.Target] = append(adjacency[edge.Target], edge.Source)
	}
	seedTotal := 0.0
	for id, weight := range seeds {
		if _, ok := ids[id]; ok && weight > 0 {
			seedTotal += weight
		}
	}
	if seedTotal == 0 {
		return map[string]float64{}
	}
	restart := make(map[string]float64)
	for id, weight := range seeds {
		if _, ok := ids[id]; ok && weight > 0 {
			restart[id] = weight / seedTotal
		}
	}
	rank := maps.Clone(restart)
	for range 25 {
		next := make(map[string]float64, len(restart))
		for id, weight := range restart {
			next[id] = 0.25 * weight
		}
		dangling := 0.0
		for id, mass := range rank {
			neighbours := adjacency[id]
			if len(neighbours) == 0 {
				dangling += mass
				continue
			}
			share := 0.75 * mass / float64(len(neighbours))
			for _, neighbour := range neighbours {
				next[neighbour] += share
			}
		}
		for id, weight := range restart {
			next[id] += 0.75 * dangling * weight
		}
		rank = next
	}
	maximum := 0.0
	for _, score := range rank {
		maximum = max(maximum, score)
	}
	if maximum == 0 {
		return map[string]float64{}
	}
	for id, score := range rank {
		rank[id] = score / maximum
	}
	return rank
}

func askCoverage(query map[string]int, hit AskHit, documents []askDocument, conceptDocuments []askConceptDocument, idf map[string]float64, defaultIDF float64, strong bool) float64 {
	if hit.Kind == "concept" {
		for _, document := range conceptDocuments {
			if askConceptPointer(document.concept) != hit.Pointer {
				continue
			}
			matched := 0.0
			total := 0.0
			for term := range query {
				weight := idf[term]
				if weight == 0 {
					weight = defaultIDF
				}
				total += weight
				if hasAskTerm(document.name, term) || (!strong && hasAskTerm(document.body, term)) {
					matched += weight
				}
			}
			if total > 0 {
				return matched / total
			}
		}
		return 0
	}
	for _, document := range documents {
		if askNodePointer(document.node) != hit.Pointer {
			continue
		}
		if strong && document.node.Kind == Kind("file") {
			return 0
		}
		matched := 0.0
		total := 0.0
		for term := range query {
			weight := idf[term]
			if weight == 0 {
				weight = defaultIDF
			}
			total += weight
			matchedIn := hasAskTerm(document.name, term)
			if !strong {
				matchedIn = matchedIn || hasAskTerm(document.path, term) || hasAskTerm(document.body, term)
			}
			if matchedIn {
				matched += weight
			}
		}
		if total > 0 {
			return matched / total
		}
	}
	return 0
}

func askUsableIndex(index *AskIndex, wiring GraphV1) *AskIndex {
	if index == nil || index.Version != 1 || index.DocCount != len(wiring.Nodes) || index.DocCount != len(index.Docs) {
		return nil
	}
	for _, node := range wiring.Nodes {
		if _, ok := index.Docs[node.ID]; !ok {
			return nil
		}
	}
	return index
}

func askFileFirst(hits []AskHit, limit int) []AskHit {
	queues := make(map[string][]AskHit)
	order := make([]string, 0)
	for _, hit := range hits {
		file := askHitGroup(hit)
		if _, ok := queues[file]; !ok {
			order = append(order, file)
		}
		queues[file] = append(queues[file], hit)
	}
	selected := make([]AskHit, 0, min(len(hits), limit))
	for depth := 0; ; depth++ {
		added := false
		for _, file := range order {
			queue := queues[file]
			if depth >= len(queue) {
				continue
			}
			selected = append(selected, queue[depth])
			added = true
			if len(selected) >= limit {
				return selected
			}
		}
		if !added {
			return selected
		}
	}
}

func askHitGroup(hit AskHit) string {
	if hit.Kind != "symbol" {
		return "concept:" + hit.Title + ":" + hit.Pointer
	}
	if marker := strings.Index(hit.Pointer, ":L"); marker >= 0 {
		return hit.Pointer[:marker]
	}
	return hit.Pointer
}

func hasAskTerm(document map[string]int, term string) bool {
	if document[term] > 0 || document[term+"s"] > 0 {
		return true
	}
	return strings.HasSuffix(term, "s") && document[strings.TrimSuffix(term, "s")] > 0
}
