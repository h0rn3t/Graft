package graph

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

const (
	defaultAskLimit       = 8
	askGraphWeight        = 0.5
	askRescueFloor        = 0.15
	askTestPenalty        = 0.35
	askParticipationRatio = 0.25
)

var (
	// Case-sensitive, like the TypeScript patterns; jsSpace is JavaScript's \s.
	askStructuralIncoming = regexp.MustCompile(strings.ReplaceAll(`\b(caller|callers|calls?\s+into|who\s+calls|what\s+calls|called\s+by|used\s+by|uses)\b`, `\s`, jsSpaceClass))
	askStructuralOutgoing = regexp.MustCompile(strings.ReplaceAll(`\b(callee|callees|what\s+does\s+\w+\s+call|calls\s+what|imports?|depends\s+on)\b`, `\s`, jsSpaceClass))
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
	// Limit caps the returned hits with JavaScript number semantics: NaN,
	// zero, fractions and negatives behave as Number(--limit) does in the
	// TypeScript CLI. Nil uses the default of 8.
	Limit *float64
	// In narrows matching nodes to this segment-aware path prefix.
	In string
	// NoGraphRank disables the connectivity re-rank for lexical results.
	NoGraphRank bool
	// Index supplies the build-time token sidecar for a slim persisted graph.
	Index *AskIndex
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
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Pointer string `json:"pointer"`
	Snippet string `json:"snippet"`
	// Doc is the first line of a ranked symbol's documentation comment.
	Doc      string   `json:"doc,omitempty"`
	Relation Relation `json:"relation,omitempty"`
	Score    float64  `json:"score"`
	// Scope names the ranking scope of a multi-scope hit; the root is "".
	Scope *string `json:"scope,omitempty"`
	Code  string  `json:"code,omitempty"`
	// SourceHash tracks revisions without exposing internal graph metadata.
	SourceHash string `json:"-"`
	// ContentRef identifies source delivered to callers using opt-in deduplication.
	ContentRef string `json:"contentRef,omitempty"`
	Unchanged  bool   `json:"unchanged,omitempty"`
	// ScopeAfterCode serializes scope after code: a workspace hit gains its
	// scope by object spread, which appends a key the child hit lacked.
	ScopeAfterCode bool `json:"-"`
	// NameTerms counts the distinct query terms the hit's name matches, the
	// name-coverage tier; it stays zero on downranked paths (tests, copies).
	NameTerms int `json:"-"`
}

// MarshalJSON keeps the TypeScript key order, including a workspace hit's
// appended scope.
func (hit AskHit) MarshalJSON() ([]byte, error) {
	type plain AskHit
	if !hit.ScopeAfterCode {
		return jsonjs.Marshal(plain(hit), "")
	}
	return jsonjs.Marshal(struct {
		Kind       string   `json:"kind"`
		Title      string   `json:"title"`
		Pointer    string   `json:"pointer"`
		Snippet    string   `json:"snippet"`
		Doc        string   `json:"doc,omitempty"`
		Relation   Relation `json:"relation,omitempty"`
		Score      float64  `json:"score"`
		Code       string   `json:"code,omitempty"`
		Scope      *string  `json:"scope,omitempty"`
		ContentRef string   `json:"contentRef,omitempty"`
		Unchanged  bool     `json:"unchanged,omitempty"`
	}{hit.Kind, hit.Title, hit.Pointer, hit.Snippet, hit.Doc, hit.Relation, hit.Score, hit.Code, hit.Scope, hit.ContentRef, hit.Unchanged}, "")
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
	Scopes         *AskScopes          `json:"scopes,omitempty"`
	Coverage       *float64            `json:"coverage,omitempty"`
	CoverageStrong *float64            `json:"coverageStrong,omitempty"`
	Note           string              `json:"note,omitempty"`
	Saved          *AskSavings         `json:"saved,omitempty"`
	Ranking        *AskRankingMetadata `json:"-"`
	// Distinctive is the query term rarest in the graph, suggested as a next
	// search when the answer is weak; it is not part of the JSON contract.
	Distinctive string `json:"-"`
}

// AskRankingGroup is one file queue used by file-aware ranking.
type AskRankingGroup struct {
	Key            string
	Hits           []AskHit
	BaselineHits   []AskHit
	Coverage       float64
	CoverageStrong float64
}

// AskRankingEntry ties a baseline hit to its file queue.
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
			hit.ScopeAfterCode = hit.Scope == nil
			hit.Scope = new(document.scope)
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
			ranked[index].hit.ScopeAfterCode = ranked[index].hit.Scope == nil
			ranked[index].hit.Scope = new(ranked[index].scope)
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

// Ask routes a plain-language query to structural edges or lexical graph ranking.
func Ask(wiring GraphV1, query string, opts AskOptions) (AskResult, error) {
	limit := float64(defaultAskLimit)
	if opts.Limit != nil {
		limit = *opts.Limit
	}
	prefix := normalizePathPrefix(opts.In)
	if err := assertPrefixIndexed(wiring, prefix); err != nil {
		return AskResult{}, err
	}

	if incoming := askStructuralIncoming.MatchString(query); incoming || askStructuralOutgoing.MatchString(query) {
		structuralResult, fallthroughNote := askStructural(wiring, query, limit, prefix, incoming)
		if structuralResult != nil {
			return *structuralResult, nil
		}
		result := askLexical(wiring, query, limit, prefix, opts)
		if result.Note != "" {
			result.Note = fallthroughNote + "\n" + result.Note
		} else {
			result.Note = fallthroughNote
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

func askStructural(wiring GraphV1, query string, limit float64, prefix string, incoming bool) (*AskResult, string) {
	words := askSubjectWords(query)
	var subjects []NodeV1
	for _, word := range words {
		matches, err := ResolveSymbol(wiring, word, ResolveSymbolOptions{In: prefix})
		if err == nil && len(matches) > 0 {
			subjects = matches
			break
		}
	}
	if len(subjects) == 0 {
		tried := query
		if len(words) > 0 {
			tried = words[0]
		}
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
			hit.Snippet = askNodeSnippetTS(*node)
		}
		hits = append(hits, hit)
	}
	if len(hits) == 0 {
		return nil, askFallthroughNote(subjects[0].Name)
	}
	compare := localeCompare()
	slices.SortStableFunc(hits, func(left, right AskHit) int {
		return compare(left.Pointer, right.Pointer)
	})
	hits = hits[:jsSliceEnd(len(hits), limit)]
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

// jsSpaceClass is the character class JavaScript's \s matches.
const jsSpaceClass = `[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

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
	slices.SortStableFunc(words, func(left, right string) int {
		return len(right) - len(left)
	})
	return words
}

func askTermCounts(text string) map[string]int {
	counts := make(map[string]int)
	for _, term := range askTerms(text) {
		counts[term]++
	}
	return counts
}

// askTerms tokenizes like the TypeScript ask: camelCase split, lower-cased, cut
// on non-alphanumerics, one-letter tokens and stop words dropped, order kept.
// Each term is then folded with AskFold, so the index and the query share one
// vocabulary.
func askTerms(text string) []string {
	text = askCamelBoundary.ReplaceAllString(text, "$1 $2")
	// JavaScript lower-cases U+0130 to "i" plus a combining dot, which then
	// splits the token; Go maps it to a bare "i".
	text = strings.ReplaceAll(text, "\u0130", "i\u0307")
	terms := make([]string, 0)
	for _, term := range askTokenSeparator.Split(strings.ToLower(text), -1) {
		if len(term) <= 1 {
			continue
		}
		if _, stop := askStopWords[term]; stop {
			continue
		}
		terms = append(terms, AskFold(term))
	}
	return terms
}

// AskFold folds regular English inflections of a lower-case term so that its
// forms match: a plural -s, -es or -ies, then -ing or -ed (undoubling a final
// consonant), then a trailing -e. Every step keeps a stem of three or more
// letters, so short words stay as they are.
func AskFold(term string) string {
	endsWith := func(word string, suffixes ...string) bool {
		return slices.ContainsFunc(suffixes, func(suffix string) bool { return strings.HasSuffix(word, suffix) })
	}
	switch {
	case len(term) >= 5 && strings.HasSuffix(term, "ies"):
		term = term[:len(term)-3] + "y"
	case len(term) >= 5 && strings.HasSuffix(term, "es") && endsWith(term[:len(term)-2], "s", "x", "z", "ch", "sh"):
		term = term[:len(term)-2]
	case len(term) >= 4 && strings.HasSuffix(term, "s") && !endsWith(term, "ss", "us", "is"):
		term = term[:len(term)-1]
	}
	for _, suffix := range []string{"ing", "ed"} {
		stem, ok := strings.CutSuffix(term, suffix)
		if !ok || len(stem) < 3 {
			continue
		}
		if n := len(stem); n >= 4 && stem[n-1] == stem[n-2] && strings.IndexByte("bdgmnprt", stem[n-1]) >= 0 {
			stem = stem[:n-1]
		}
		term = stem
		break
	}
	if len(term) >= 4 && strings.HasSuffix(term, "e") {
		term = term[:len(term)-1]
	}
	return term
}

func sameAskHit(left, right AskHit) bool {
	return left.Kind == right.Kind && left.Pointer == right.Pointer && left.Title == right.Title
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

func askFallthroughNote(subject string) string {
	return fmt.Sprintf("structural index: no entries for '%s' — showing lexical matches; for precise edges try graft callers '%s', or graft grep '%s' for every reference (loosen the pattern if it returns nothing)", subject, subject, subject)
}

func askUsableIndex(index *AskIndex, wiring GraphV1) *AskIndex {
	if index == nil || index.Version != AskIndexVersion || index.DocCount != len(wiring.Nodes) || index.DocCount != len(index.Docs) {
		return nil
	}
	for _, node := range wiring.Nodes {
		if _, ok := index.Docs[node.ID]; !ok {
			return nil
		}
	}
	return index
}
