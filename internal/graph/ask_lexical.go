package graph

import (
	"cmp"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsmath"
)

// The lexical ranking combines bounded file ranking, comparable-scope fusion,
// personalized PageRank, and name-coverage tiers over the final selection. It
// started as a bit-for-bit port of the TypeScript ranking and has since moved
// on (inflection folding, tiers); the Go goldens and ranking tests now pin it.
// Floating-point sums still run in insertion order and sorts stay stable, and
// each product that feeds a sum is wrapped in float64() so the compiler cannot
// fuse it into an FMA, which keeps scores reproducible across platforms.

const (
	askRescueFloorTS = 0.15
	askGraphWeightTS = 0.5
)

var (
	askWantsTestsPattern = regexp.MustCompile(`(?i)\b(tests?|testdata|specs?|coverage|assert(?:ion)?s?|fixtures?|mocks?|generated|vendor(?:ed)?)\b`)
	askTestPathPattern   = regexp.MustCompile(`(?i)(^|/)(tests?|__tests__|spec|testdata|fixtures?|__fixtures__|generated|__generated__|vendor)(/|$)` +
		`|(_test|\.(?:test|spec|gen|generated|pb))\.[a-z]+$|(^|/)(test_[^/]+|conftest)\.py$|(^|/)zz_generated[._]`)
	askSpanStartPattern = regexp.MustCompile(`^L(\d+)-L\d+$`)
)

// askScores is a string→number Map that iterates in insertion order.
type askScores struct {
	ids    []string
	values map[string]float64
}

func newAskScores() *askScores {
	return &askScores{values: make(map[string]float64)}
}

func (scores *askScores) set(id string, value float64) {
	if _, ok := scores.values[id]; !ok {
		scores.ids = append(scores.ids, id)
	}
	scores.values[id] = value
}

func (scores *askScores) size() int {
	return len(scores.ids)
}

// askTermSet is a Set of terms that iterates in insertion order.
type askTermSet struct {
	terms []string
	has   map[string]struct{}
}

func newAskTermSet() *askTermSet {
	return &askTermSet{has: make(map[string]struct{})}
}

func (set *askTermSet) add(term string) {
	if _, ok := set.has[term]; !ok {
		set.terms = append(set.terms, term)
		set.has[term] = struct{}{}
	}
}

type askLexDoc struct {
	node NodeV1
	name map[string]int
	path map[string]int
	body map[string]int
}

// askFileCandidate is file-rank.ts's FileRankCandidate.
type askFileCandidate struct {
	id                 string
	file               string
	kind               string
	rawLexical         float64
	lexical            float64
	graph              float64
	rankFactor         float64
	baselineScore      float64
	baselineTieKey     *string
	matchedTerms       *askTermSet
	matchedStrongTerms *askTermSet
	eligible           bool
	spanStart          float64
}

type askRankedFileTS struct {
	file                string
	representative      askFileCandidate
	queue               []askFileCandidate
	unionCoverage       float64
	unionStrongCoverage float64
	score               float64
}

// askScopedDoc is fuse.ts's ScopedDoc.
type askScopedDoc struct {
	id    string
	scope string
	score float64
}

// askScopeCandidate is fuse.ts's ScopeRankCandidate.
type askScopeCandidate struct {
	askScopedDoc
	lexical    float64
	graph      float64
	rankFactor float64
}

type askFusion struct {
	ranked      []askScopedDoc
	federated   []string
	alsoMatched []AskScopeMatch
}

type askGroupTS struct {
	key            string
	hits           []*AskHit
	coverage       float64
	coverageStrong float64
}

// jsDiff is the sign of a - b as a JavaScript comparator sees it: NaN, like
// zero, falls through to the next tie-break.
func jsDiff(a, b float64) int {
	switch difference := a - b; {
	case difference > 0:
		return 1
	case difference < 0:
		return -1
	default:
		return 0
	}
}

// compareCodeUnitsTS orders strings like JavaScript's < operator and default
// sort: by UTF-16 code unit.
func compareCodeUnitsTS(a, b string) int {
	left, right := []rune(a), []rune(b)
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == right[i] {
			continue
		}
		return cmpUnit(left[i], right[i])
	}
	return len(left) - len(right)
}

func cmpUnit(a, b rune) int {
	unit := func(r rune) rune {
		if r >= 0x10000 {
			return 0xD800 + (r-0x10000)>>10
		}
		return r
	}
	if unit(a) != unit(b) {
		return int(unit(a) - unit(b))
	}
	return int(a - b)
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return min(1, value)
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

// jsSliceEnd is the end index JavaScript's array.slice(0, end) uses.
func jsSliceEnd(length int, end float64) int {
	switch {
	case math.IsNaN(end):
		return 0
	case math.IsInf(end, 1):
		return length
	case math.IsInf(end, -1):
		return 0
	}
	integer := math.Trunc(end)
	if integer < 0 {
		return max(length+int(max(integer, -float64(length))), 0)
	}
	return int(min(integer, float64(length)))
}

// JSQueueCap is file-selection.ts's round-robin cap for a JavaScript number
// limit: floor(limit), at least 0, or -1 for no cap when limit is not finite.
func JSQueueCap(limit float64) int {
	if math.IsNaN(limit) || math.IsInf(limit, 0) {
		return -1
	}
	return int(max(0, math.Floor(limit)))
}

func askUniqueTerms(text string) []string {
	set := newAskTermSet()
	for _, term := range askTerms(text) {
		set.add(term)
	}
	return set.terms
}

func askHasTermTS(field map[string]int, term string) bool {
	if _, ok := field[term]; ok {
		return true
	}
	if _, ok := field[term+"s"]; ok {
		return true
	}
	if before, ok := strings.CutSuffix(term, "s"); ok {
		_, ok := field[before]
		return ok
	}
	return false
}

func askIDF(idf map[string]float64, term string, fallback float64) float64 {
	if weight, ok := idf[term]; ok {
		return weight
	}
	return fallback
}

func askScoreTS(query []string, doc map[string]int, idf map[string]float64) float64 {
	total := 0.0
	for _, term := range query {
		if count := doc[term]; count != 0 {
			total += float64(float64(count) * askIDF(idf, term, 1))
		}
	}
	return total
}

func askBM25TS(query []string, doc map[string]int, idf map[string]float64, length int, average float64) float64 {
	const (
		k1 = 1.2
		b  = 0.75
	)
	if average == 0 || math.IsNaN(average) {
		average = 1
	}
	norm := float64(k1 * (1 - b + float64(b*float64(length))/average))
	total := 0.0
	for _, term := range query {
		if frequency := float64(doc[term]); frequency != 0 {
			total += float64(askIDF(idf, term, 1) * ((frequency * (k1 + 1)) / (frequency + norm)))
		}
	}
	return total
}

func askBagLength(bag map[string]int) int {
	total := 0
	for _, count := range bag {
		total += count
	}
	return total
}

func askMatchedIDFShare(query []string, fields []map[string]int, idf map[string]float64, fallback float64) float64 {
	matched, total := 0.0, 0.0
	for _, term := range query {
		weight := askIDF(idf, term, fallback)
		total += weight
		if slices.ContainsFunc(fields, func(field map[string]int) bool { return askHasTermTS(field, term) }) {
			matched += weight
		}
	}
	if total > 0 {
		return matched / total
	}
	return 0
}

func askNormalizedQueryWeights(query []string, idf map[string]float64, fallback float64) map[string]float64 {
	total := 0.0
	for _, term := range query {
		total += askIDF(idf, term, fallback)
	}
	weights := make(map[string]float64, len(query))
	if total <= 0 {
		return weights
	}
	for _, term := range query {
		weights[term] = askIDF(idf, term, fallback) / total
	}
	return weights
}

func askMatchedLexicalTerms(query []string, name, path, body map[string]int) *askTermSet {
	terms := newAskTermSet()
	for _, term := range query {
		_, inName := name[term]
		_, inPath := path[term]
		_, inBody := body[term]
		if inName || inPath || inBody {
			terms.add(term)
		}
	}
	return terms
}

func askMatchedStrongTerms(query []string, name map[string]int) *askTermSet {
	terms := newAskTermSet()
	for _, term := range query {
		if askHasTermTS(name, term) {
			terms.add(term)
		}
	}
	return terms
}

func askSpanStartTS(span string) float64 {
	if match := askSpanStartPattern.FindStringSubmatch(span); match != nil {
		value, err := strconv.ParseFloat(match[1], 64)
		if err == nil {
			return value
		}
	}
	return math.Inf(1)
}

// ── graphrank.ts ─────────────────────────────────────────────────────────────

type askTopology struct {
	ids       map[string]struct{}
	adjacency map[string][]string
}

func askLink(adjacency map[string][]string, source, target string) {
	adjacency[source] = append(adjacency[source], target)
}

func askPrepareTopology(wiring GraphV1, keep func(string) bool) askTopology {
	topology := askTopology{ids: make(map[string]struct{}), adjacency: make(map[string][]string)}
	for _, node := range wiring.Nodes {
		if keep == nil || keep(node.ID) {
			topology.ids[node.ID] = struct{}{}
		}
	}
	if len(topology.ids) == 0 {
		return topology
	}
	for _, edge := range wiring.Edges {
		if !isWalkRelation(edge.Relation) {
			continue
		}
		_, source := topology.ids[edge.Source]
		_, target := topology.ids[edge.Target]
		if !source || !target {
			continue
		}
		askLink(topology.adjacency, edge.Source, edge.Target)
		askLink(topology.adjacency, edge.Target, edge.Source)
	}
	return topology
}

func askPreparePartitions(wiring GraphV1, partitionOf func(string) (string, bool)) map[string]*askTopology {
	partitionByID := make(map[string]string)
	partitions := make(map[string]*askTopology)
	for _, node := range wiring.Nodes {
		partition, ok := partitionOf(node.ID)
		if !ok {
			continue
		}
		partitionByID[node.ID] = partition
		topology := partitions[partition]
		if topology == nil {
			topology = &askTopology{ids: make(map[string]struct{}), adjacency: make(map[string][]string)}
			partitions[partition] = topology
		}
		topology.ids[node.ID] = struct{}{}
	}
	for _, edge := range wiring.Edges {
		if !isWalkRelation(edge.Relation) {
			continue
		}
		partition, ok := partitionByID[edge.Source]
		if !ok {
			continue
		}
		if target, ok := partitionByID[edge.Target]; !ok || target != partition {
			continue
		}
		topology := partitions[partition]
		askLink(topology.adjacency, edge.Source, edge.Target)
		askLink(topology.adjacency, edge.Target, edge.Source)
	}
	return partitions
}

// askPageRankTS is a personalized PageRank from seeds over topology. Ranks
// live in slices indexed by first-visit order, which is the insertion order
// the TypeScript Maps iterate in, so every sum runs in the same order and the
// scores stay bit-identical to a map-per-iteration walk.
func askPageRankTS(topology askTopology, seeds *askScores) *askScores {
	const (
		alpha = 0.25
		iters = 25
	)
	seedTotal := 0.0
	for _, id := range seeds.ids {
		if _, ok := topology.ids[id]; ok && seeds.values[id] > 0 {
			seedTotal += seeds.values[id]
		}
	}
	if seedTotal <= 0 {
		return newAskScores()
	}
	var ids []string
	index := make(map[string]int)
	visit := func(id string) int {
		at, ok := index[id]
		if !ok {
			at = len(ids)
			index[id] = at
			ids = append(ids, id)
		}
		return at
	}
	var restart []float64
	for _, id := range seeds.ids {
		if _, ok := topology.ids[id]; ok && seeds.values[id] > 0 {
			visit(id)
			restart = append(restart, seeds.values[id]/seedTotal)
		}
	}
	// neighbours[i] lists node i's neighbours by index; it is filled the
	// first time node i spreads its rank, which is when a Map-based walk
	// would first insert them.
	var neighbours [][]int
	rank := slices.Clone(restart)
	next := make([]float64, 0, len(rank))
	for range iters {
		next = next[:0]
		for _, value := range restart {
			next = append(next, alpha*value)
		}
		dangling := 0.0
		for at, mass := range rank {
			if at == len(neighbours) {
				linked := topology.adjacency[ids[at]]
				indexes := make([]int, len(linked))
				for position, id := range linked {
					indexes[position] = visit(id)
				}
				neighbours = append(neighbours, indexes)
			}
			if len(neighbours[at]) == 0 {
				dangling += mass
				continue
			}
			share := ((1 - alpha) * mass) / float64(len(neighbours[at]))
			for _, neighbour := range neighbours[at] {
				for len(next) <= neighbour {
					next = append(next, 0)
				}
				next[neighbour] += share
			}
		}
		if dangling > 0 {
			spread := (1 - alpha) * dangling
			for at, value := range restart {
				next[at] += float64(spread * value)
			}
		}
		rank, next = next, rank
	}
	maximum := 0.0
	for _, value := range rank {
		if value > maximum {
			maximum = value
		}
	}
	out := newAskScores()
	if maximum <= 0 {
		return out
	}
	for at, value := range rank {
		out.set(ids[at], value/maximum)
	}
	return out
}

// ── file-rank.ts ─────────────────────────────────────────────────────────────

func askStartOf(candidate askFileCandidate) float64 {
	if math.IsNaN(candidate.spanStart) || math.IsInf(candidate.spanStart, 0) || candidate.spanStart < 0 {
		return math.Inf(1)
	}
	return candidate.spanStart
}

func askCandidateCoverageTS(candidate askFileCandidate, weights map[string]float64) float64 {
	share := 0.0
	if candidate.matchedTerms != nil {
		for _, term := range candidate.matchedTerms.terms {
			if weight := weights[term]; finitePositive(weight) {
				share += weight
			}
		}
	}
	return clamp01(share)
}

func askBaselineOrder(compare func(a, b string) int) func(a, b askFileCandidate) int {
	return func(a, b askFileCandidate) int {
		if order := jsDiff(b.baselineScore, a.baselineScore); order != 0 {
			return order
		}
		if a.baselineTieKey != nil && b.baselineTieKey != nil {
			return compare(*a.baselineTieKey, *b.baselineTieKey)
		}
		if order := jsDiff(b.rawLexical, a.rawLexical); order != 0 {
			return order
		}
		// tokenCost is never known here: Infinity - Infinity falls through.
		if order := jsDiff(askStartOf(a), askStartOf(b)); order != 0 {
			return order
		}
		return compare(a.id, b.id)
	}
}

func askRankFilesBounded(candidates []askFileCandidate, weights map[string]float64, compare func(a, b string) int) []askRankedFileTS {
	baselineOrder := askBaselineOrder(compare)
	groupOrder := make([]string, 0)
	groups := make(map[string][]askFileCandidate)
	for _, candidate := range candidates {
		if candidate.file == "" || !finitePositive(candidate.baselineScore) {
			continue
		}
		if _, ok := groups[candidate.file]; !ok {
			groupOrder = append(groupOrder, candidate.file)
		}
		groups[candidate.file] = append(groups[candidate.file], candidate)
	}
	ranked := make([]askRankedFileTS, 0, len(groupOrder))
	for _, file := range groupOrder {
		members := groups[file]
		donors := make([]askFileCandidate, 0, len(members))
		for _, candidate := range members {
			if candidate.kind == "symbol" && candidate.eligible && finitePositive(candidate.rawLexical) && finitePositive(candidate.lexical) {
				donors = append(donors, candidate)
			}
		}
		slices.SortStableFunc(donors, func(a, b askFileCandidate) int {
			if order := jsDiff(b.rawLexical, a.rawLexical); order != 0 {
				return order
			}
			if order := jsDiff(askCandidateCoverageTS(b, weights), askCandidateCoverageTS(a, weights)); order != 0 {
				return order
			}
			if order := jsDiff(askStartOf(a), askStartOf(b)); order != 0 {
				return order
			}
			return compare(a.id, b.id)
		})
		unionCoverage, unionStrongCoverage, pooledLexical := 0.0, 0.0, 0.0
		var anchor *askFileCandidate
		if len(donors) > 0 {
			anchor = &donors[0]
			unionTerms := newAskTermSet()
			for _, donor := range donors {
				for _, term := range donor.matchedTerms.terms {
					unionTerms.add(term)
				}
			}
			for _, term := range unionTerms.terms {
				if weight := weights[term]; finitePositive(weight) {
					unionCoverage += weight
				}
			}
			unionCoverage = clamp01(unionCoverage)
			unionStrongTerms := newAskTermSet()
			for _, donor := range donors {
				if donor.matchedStrongTerms != nil {
					for _, term := range donor.matchedStrongTerms.terms {
						unionStrongTerms.add(term)
					}
				}
			}
			for _, term := range unionStrongTerms.terms {
				if weight := weights[term]; finitePositive(weight) {
					unionStrongCoverage += weight
				}
			}
			unionStrongCoverage = clamp01(unionStrongCoverage)
			anchorCoverage := askCandidateCoverageTS(*anchor, weights)
			residual := 0.0
			if unionCoverage > 0 {
				residual = clamp01(anchorCoverage * math.Max(0, unionCoverage-anchorCoverage) / unionCoverage)
			}
			pooledLexical = clamp01(anchor.lexical + float64((1-clamp01(anchor.lexical))*residual))
		}
		ordered := slices.Clone(members)
		slices.SortStableFunc(ordered, baselineOrder)
		baselineRepresentative := ordered[0]
		pooledScore := 0.0
		if anchor != nil {
			pooledScore = clamp01(anchor.rankFactor) * (pooledLexical + float64(0.5*clamp01(anchor.graph)))
		}
		representative, score := baselineRepresentative, baselineRepresentative.baselineScore
		if anchor != nil && pooledScore > baselineRepresentative.baselineScore {
			representative, score = *anchor, pooledScore
		}
		queue := []askFileCandidate{representative}
		for _, candidate := range ordered {
			if candidate.id != representative.id {
				queue = append(queue, candidate)
			}
		}
		ranked = append(ranked, askRankedFileTS{
			file:                file,
			representative:      representative,
			queue:               queue,
			unionCoverage:       unionCoverage,
			unionStrongCoverage: unionStrongCoverage,
			score:               score,
		})
	}
	slices.SortStableFunc(ranked, func(a, b askRankedFileTS) int {
		if order := jsDiff(b.score, a.score); order != 0 {
			return order
		}
		if order := baselineOrder(a.representative, b.representative); order != 0 {
			return order
		}
		return compare(a.file, b.file)
	})
	return ranked
}

// ── fuse.ts ──────────────────────────────────────────────────────────────────

func askCombineComparableScopes(docs []askScopedDoc, compare func(a, b string) int) askFusion {
	scopeOrder := make([]string, 0)
	byScope := make(map[string][]askScopedDoc)
	for _, doc := range docs {
		if doc.score <= 0 {
			continue
		}
		if _, ok := byScope[doc.scope]; !ok {
			scopeOrder = append(scopeOrder, doc.scope)
		}
		byScope[doc.scope] = append(byScope[doc.scope], doc)
	}
	byScore := func(a, b askScopedDoc) int {
		if order := jsDiff(b.score, a.score); order != 0 {
			return order
		}
		return compare(a.id, b.id)
	}
	for _, scope := range scopeOrder {
		slices.SortStableFunc(byScope[scope], byScore)
	}
	if len(scopeOrder) == 0 {
		return askFusion{ranked: []askScopedDoc{}, federated: []string{}, alsoMatched: []AskScopeMatch{}}
	}
	if len(scopeOrder) == 1 {
		scope := scopeOrder[0]
		ranked := make([]askScopedDoc, 0, len(byScope[scope]))
		for _, doc := range byScope[scope] {
			ranked = append(ranked, askScopedDoc{id: doc.id, scope: scope, score: doc.score})
		}
		return askFusion{ranked: ranked, federated: []string{scope}, alsoMatched: []AskScopeMatch{}}
	}
	scopesByBest := slices.Clone(scopeOrder)
	slices.SortStableFunc(scopesByBest, func(a, b string) int {
		if order := jsDiff(byScope[b][0].score, byScope[a][0].score); order != 0 {
			return order
		}
		return compare(a, b)
	})
	gate := askParticipationRatio * byScope[scopesByBest[0]][0].score
	federated := make([]string, 0)
	alsoMatched := make([]AskScopeMatch, 0)
	for _, scope := range scopesByBest {
		if byScope[scope][0].score >= gate {
			federated = append(federated, scope)
		} else {
			alsoMatched = append(alsoMatched, AskScopeMatch{Scope: scope, BestID: byScope[scope][0].id})
		}
	}
	order := make([]string, 0)
	best := make(map[string]askScopedDoc)
	for _, scope := range federated {
		for _, doc := range byScope[scope] {
			previous, ok := best[doc.id]
			if !ok {
				order = append(order, doc.id)
			}
			if !ok || doc.score > previous.score {
				best[doc.id] = askScopedDoc{id: doc.id, scope: scope, score: doc.score}
			}
		}
	}
	maximum := 0.0
	for _, id := range order {
		if best[id].score > maximum {
			maximum = best[id].score
		}
	}
	ranked := make([]askScopedDoc, 0, len(order))
	for _, id := range order {
		entry := best[id]
		score := 0.0
		if maximum > 0 {
			score = entry.score / maximum
		}
		ranked = append(ranked, askScopedDoc{id: id, scope: entry.scope, score: score})
	}
	slices.SortStableFunc(ranked, func(a, b askScopedDoc) int {
		if order := jsDiff(b.score, a.score); order != 0 {
			return order
		}
		if order := compare(a.scope, b.scope); order != 0 {
			return order
		}
		return compare(a.id, b.id)
	})
	return askFusion{ranked: ranked, federated: federated, alsoMatched: alsoMatched}
}

type askScopeOps struct {
	lex                func(scope string) *askScores
	hasIdentifierMatch func(scope string) bool
	walk               func(scope string, seeds *askScores) *askScores
	rankFactor         func(scope, id string) float64
	collapse           func(candidates []askScopeCandidate) []askScopedDoc
}

func askRankScopesAndFuse(scopes []string, ops askScopeOps, compare func(a, b string) int) askFusion {
	type scopeMeta struct {
		lex    *askScores
		maxLex float64
		bestID string
	}
	metaOrder := make([]string, 0, len(scopes))
	meta := make(map[string]scopeMeta)
	for _, scope := range scopes {
		lex := ops.lex(scope)
		if lex.size() == 0 {
			continue
		}
		maxLex, bestID := 0.0, ""
		for _, id := range lex.ids {
			value := lex.values[id]
			if value > maxLex || (value == maxLex && compareCodeUnitsTS(id, bestID) < 0) {
				maxLex, bestID = value, id
			}
		}
		if maxLex <= 0 {
			continue
		}
		metaOrder = append(metaOrder, scope)
		meta[scope] = scopeMeta{lex: lex, maxLex: maxLex, bestID: bestID}
	}
	alsoMatched := make([]AskScopeMatch, 0)
	named := make(map[string]bool, len(metaOrder))
	anyNamed := false
	for _, scope := range metaOrder {
		named[scope] = ops.hasIdentifierMatch(scope)
		anyNamed = anyNamed || named[scope]
	}
	if anyNamed {
		kept := make([]string, 0, len(metaOrder))
		for _, scope := range metaOrder {
			if named[scope] {
				kept = append(kept, scope)
				continue
			}
			alsoMatched = append(alsoMatched, AskScopeMatch{Scope: scope, BestID: meta[scope].bestID})
		}
		metaOrder = kept
	}
	globalMaxLex := 0.0
	for _, scope := range metaOrder {
		if meta[scope].maxLex > globalMaxLex {
			globalMaxLex = meta[scope].maxLex
		}
	}
	candidates := make([]askScopeCandidate, 0)
	for _, scope := range metaOrder {
		lex := meta[scope].lex
		pr := ops.walk(scope, lex)
		ids := newAskTermSet()
		for _, id := range lex.ids {
			ids.add(id)
		}
		for _, id := range pr.ids {
			if pr.values[id] >= askRescueFloorTS {
				ids.add(id)
			}
		}
		for _, id := range ids.terms {
			lexical := 0.0
			if globalMaxLex > 0 {
				lexical = lex.values[id] / globalMaxLex
			}
			graphScore := pr.values[id]
			factor := ops.rankFactor(scope, id)
			blended := (lexical + float64(askGraphWeightTS*graphScore)) * factor
			if blended > 0 {
				candidates = append(candidates, askScopeCandidate{
					id: id, scope: scope, score: blended,
					lexical:    lexical,
					graph:      graphScore,
					rankFactor: factor,
				})
			}
		}
	}
	var scoped []askScopedDoc
	if ops.collapse != nil {
		scoped = ops.collapse(candidates)
	} else {
		scoped = make([]askScopedDoc, 0, len(candidates))
		for _, candidate := range candidates {
			scoped = append(scoped, candidate.askScopedDoc)
		}
	}
	combined := askCombineComparableScopes(scoped, compare)
	return askFusion{
		ranked:      combined.ranked,
		federated:   combined.federated,
		alsoMatched: append(alsoMatched, combined.alsoMatched...),
	}
}

// ── file-selection.ts ────────────────────────────────────────────────────────

func askRoundRobinQueues(queues [][]*AskHit, limit float64) []*AskHit {
	capacity := JSQueueCap(limit)
	out := make([]*AskHit, 0)
	if capacity == 0 {
		return out
	}
	for depth := 0; capacity < 0 || len(out) < capacity; depth++ {
		added := false
		for _, queue := range queues {
			if depth >= len(queue) {
				continue
			}
			out = append(out, queue[depth])
			added = true
			if capacity >= 0 && len(out) >= capacity {
				break
			}
		}
		if !added {
			break
		}
	}
	return out
}

func askFileFirstRoundRobin(groups []string, values []*AskHit, limit float64) []*AskHit {
	capacity := JSQueueCap(limit)
	out := make([]*AskHit, 0)
	if capacity == 0 {
		return out
	}
	order := make([]string, 0)
	queues := make(map[string][]*AskHit)
	for index, value := range values {
		if _, ok := queues[groups[index]]; !ok {
			order = append(order, groups[index])
		}
		queues[groups[index]] = append(queues[groups[index]], value)
	}
	for depth := 0; ; depth++ {
		added := false
		for _, group := range order {
			queue := queues[group]
			if depth >= len(queue) {
				continue
			}
			out = append(out, queue[depth])
			added = true
			if capacity >= 0 && len(out) >= capacity {
				return out
			}
		}
		if !added {
			return out
		}
	}
}

// ── lexical() ────────────────────────────────────────────────────────────────

// askLexical ranks the graph's nodes against query exactly as the TypeScript
// lexical() does.
func askLexical(wiring GraphV1, query string, limit float64, prefix string, opts AskOptions) AskResult {
	fileFirst, fileComplement, fileTopLock := askFileOptions(opts)
	includeRankingMetadata := opts.IncludeRankingMetadata
	graphRank := !opts.NoGraphRank
	compare := localeCompare()
	q := askUniqueTerms(query)
	wantsTests := askWantsTestsPattern.MatchString(query) || askTestPathPattern.MatchString(prefix)
	testFactor := func(path string) float64 {
		if !wantsTests && askTestPathPattern.MatchString(path) {
			return askTestPenalty
		}
		return 1
	}

	index := askUsableIndex(opts.Index, wiring)
	graphNodes := wiring.Nodes
	if prefix != "" {
		graphNodes = make([]NodeV1, 0, len(wiring.Nodes))
		for _, node := range wiring.Nodes {
			if pathUnderPrefix(node.Path, prefix) {
				graphNodes = append(graphNodes, node)
			}
		}
	}
	symbolDocs := make([]askLexDoc, 0, len(graphNodes))
	for _, node := range graphNodes {
		if index != nil {
			if doc, ok := index.Docs[node.ID]; ok {
				symbolDocs = append(symbolDocs, askLexDoc{node: node, name: doc.Name, path: doc.Path, body: doc.Body})
				continue
			}
		}
		symbolDocs = append(symbolDocs, askLexDoc{node: node, name: askTermCounts(node.Name), path: askTermCounts(node.Path), body: askTermCounts(askNodeBody(node))})
	}

	useIndexStats := index != nil && prefix == ""
	df := make(map[string]int)
	documentCount := len(symbolDocs)
	if useIndexStats {
		maps.Copy(df, index.DF)
		documentCount = index.DocCount
	}
	countBag := func(fields ...map[string]int) {
		seen := make(map[string]struct{})
		for _, field := range fields {
			for term := range field {
				if _, dup := seen[term]; !dup {
					seen[term] = struct{}{}
					df[term]++
				}
			}
		}
	}
	if !useIndexStats {
		for _, doc := range symbolDocs {
			countBag(doc.name, doc.path, doc.body)
		}
	}
	idf := make(map[string]float64, len(df))
	for term, frequency := range df {
		idf[term] = jsmath.Log(1 + float64(documentCount)/float64(1+frequency))
	}
	defaultIDF := jsmath.Log(1 + float64(documentCount))

	matchedOf := make(map[*AskHit]float64)
	matchedStrongOf := make(map[*AskHit]float64)
	selectionGroupOf := make(map[*AskHit]string)

	byID := make(map[string]NodeV1, len(wiring.Nodes))
	for _, node := range wiring.Nodes {
		byID[node.ID] = node
	}
	docsByID := make(map[string]askLexDoc, len(symbolDocs))
	for _, doc := range symbolDocs {
		docsByID[doc.node.ID] = doc
	}
	symbolHits := make([]*AskHit, 0)
	baselineSymbolHits := make([]*AskHit, 0)
	baselineHitByID := make(map[string]*AskHit)
	fileQueueByGroup := make(map[string]func() []*AskHit)
	baselineQueueByGroup := make(map[string]func() []*AskHit)
	fileGroups := make([]askGroupTS, 0)
	needsFileQueues := fileComplement && (fileTopLock || includeRankingMetadata)
	makeSymbolHit := func(id string, score float64, scope *string) *AskHit {
		node, ok := byID[id]
		if !ok {
			return nil
		}
		hit := &AskHit{
			Kind:    "symbol",
			Title:   node.Name + " · " + string(node.Kind),
			Pointer: askNodePointer(node),
			Snippet: askNodeSnippetTS(node),
			Score:   score,
		}
		if doc := firstSummaryLine(node.Summary); doc != nil {
			hit.Doc = *doc
		}
		hit.Scope = scope
		if doc, ok := docsByID[id]; ok {
			matchedOf[hit] = askMatchedIDFShare(q, []map[string]int{doc.name, doc.path, doc.body}, idf, defaultIDF)
			if node.Kind == Kind("file") {
				matchedStrongOf[hit] = 0
			} else {
				matchedStrongOf[hit] = askMatchedIDFShare(q, []map[string]int{doc.name}, idf, defaultIDF)
				if testFactor(node.Path) == 1 {
					hit.NameTerms = len(askMatchedStrongTerms(q, doc.name).terms)
				}
			}
		} else {
			matchedOf[hit], matchedStrongOf[hit] = 0, 0
		}
		selectionGroupOf[hit] = "file:" + node.Path
		return hit
	}
	symbolTitle := func(id string) string {
		if node, ok := byID[id]; ok {
			return node.Name + " · " + string(node.Kind)
		}
		return id
	}
	scopes := graphScopes(wiring)
	var scopeMeta *AskScopes
	averageBodyLength := 0.0
	if useIndexStats {
		averageBodyLength = index.AvgBodyLen
	} else if len(symbolDocs) > 0 {
		total := 0
		for _, doc := range symbolDocs {
			total += askBagLength(doc.body)
		}
		averageBodyLength = float64(total) / float64(len(symbolDocs))
	}
	rawLexicalByID := make(map[string]float64)
	matchedTermsByID := make(map[string]*askTermSet)
	matchedStrongTermsByID := make(map[string]*askTermSet)
	fileQueryWeights := map[string]float64{}
	if fileComplement {
		fileQueryWeights = askNormalizedQueryWeights(q, idf, defaultIDF)
	}
	lexicalTotal := func(doc askLexDoc) float64 {
		return (float64(askScoreTS(q, doc.name, idf)*3) +
			float64(askScoreTS(q, doc.path, idf)*2) +
			askBM25TS(q, doc.body, idf, askBagLength(doc.body), averageBodyLength)) * testFactor(doc.node.Path)
	}
	recordMatches := func(doc askLexDoc, total float64) {
		if !fileComplement {
			return
		}
		rawLexicalByID[doc.node.ID] = total
		matchedTermsByID[doc.node.ID] = askMatchedLexicalTerms(q, doc.name, doc.path, doc.body)
		if doc.node.Kind != Kind("file") {
			matchedStrongTermsByID[doc.node.ID] = askMatchedStrongTerms(q, doc.name)
		}
	}
	emptyTerms := newAskTermSet()
	termsFor := func(terms map[string]*askTermSet, id string) *askTermSet {
		if set, ok := terms[id]; ok {
			return set
		}
		return emptyTerms
	}
	materialize := func(file askRankedFileTS, leaderScore float64, scoreOf func(askFileCandidate) float64, scopeOf func(askFileCandidate) *string) (func() []*AskHit, func() []*AskHit) {
		fileQueue := func() []*AskHit {
			hits := make([]*AskHit, 0, len(file.queue))
			for index, member := range file.queue {
				if index > 0 {
					if hit, ok := baselineHitByID[member.id]; ok {
						hits = append(hits, hit)
						continue
					}
				}
				score := scoreOf(member)
				if index == 0 {
					score = leaderScore
				}
				if hit := makeSymbolHit(member.id, score, scopeOf(member)); hit != nil {
					hits = append(hits, hit)
				}
			}
			return hits
		}
		baselineQueue := func() []*AskHit {
			hits := make([]*AskHit, 0, len(file.queue))
			for _, member := range file.queue {
				if hit := makeSymbolHit(member.id, scoreOf(member), scopeOf(member)); hit != nil {
					hits = append(hits, hit)
				}
			}
			return hits
		}
		return fileQueue, baselineQueue
	}
	addFileGroup := func(file askRankedFileTS, leaderScore float64, leaderScope *string, fileQueue, baselineQueue func() []*AskHit) {
		var hits []*AskHit
		if includeRankingMetadata {
			hits = fileQueue()
		} else if len(file.queue) > 0 {
			if hit := makeSymbolHit(file.queue[0].id, leaderScore, leaderScope); hit != nil {
				hits = []*AskHit{hit}
			}
		}
		if len(hits) == 0 {
			return
		}
		group := askGroupTS{key: "file:" + file.file, hits: hits, coverage: file.unionCoverage, coverageStrong: file.unionStrongCoverage}
		fileQueueByGroup[group.key] = fileQueue
		baselineQueueByGroup[group.key] = baselineQueue
		fileGroups = append(fileGroups, group)
		symbolHits = append(symbolHits, hits[0])
	}

	if len(scopes) > 1 {
		scopeOrder := make([]string, 0)
		byScope := make(map[string][]askLexDoc)
		scopeByID := make(map[string]string)
		for _, doc := range symbolDocs {
			scope := scopeOf(doc.node.Path, scopes).Prefix
			scopeByID[doc.node.ID] = scope
			if _, ok := byScope[scope]; !ok {
				scopeOrder = append(scopeOrder, scope)
			}
			byScope[scope] = append(byScope[scope], doc)
		}
		sortedScopes := slices.Clone(scopeOrder)
		slices.SortStableFunc(sortedScopes, compareCodeUnitsTS)
		var partitions map[string]*askTopology
		var collapsedByID map[string]askScopeCandidate
		var collapsedComponents []askScopeCandidate
		var collapsedFileByRepresentative map[string]askRankedFileTS
		ops := askScopeOps{
			lex: func(scope string) *askScores {
				out := newAskScores()
				for _, doc := range byScope[scope] {
					if total := lexicalTotal(doc); total > 0 {
						out.set(doc.node.ID, total)
						recordMatches(doc, total)
					}
				}
				return out
			},
			hasIdentifierMatch: func(scope string) bool {
				return slices.ContainsFunc(byScope[scope], func(doc askLexDoc) bool {
					return slices.ContainsFunc(q, func(term string) bool {
						return askHasTermTS(doc.name, term) || askHasTermTS(doc.path, term)
					})
				})
			},
			walk: func(scope string, seeds *askScores) *askScores {
				if !graphRank {
					return newAskScores()
				}
				if partitions == nil {
					partitions = askPreparePartitions(wiring, func(id string) (string, bool) {
						scope, ok := scopeByID[id]
						return scope, ok
					})
				}
				topology, ok := partitions[scope]
				if !ok {
					return newAskScores()
				}
				return askPageRankTS(*topology, seeds)
			},
			rankFactor: func(_, id string) float64 {
				return testFactor(byID[id].Path)
			},
		}
		if fileComplement {
			ops.collapse = func(candidates []askScopeCandidate) []askScopedDoc {
				collapsedComponents = slices.Clone(candidates)
				collapsedByID = make(map[string]askScopeCandidate, len(candidates))
				for _, candidate := range candidates {
					collapsedByID[candidate.id] = candidate
				}
				fileCandidates := make([]askFileCandidate, 0, len(candidates))
				for _, candidate := range candidates {
					node, ok := byID[candidate.id]
					rawLexical := rawLexicalByID[candidate.id]
					tieKey := symbolTitle(candidate.id)
					fileCandidate := askFileCandidate{
						id:                 candidate.id,
						kind:               "symbol",
						rawLexical:         rawLexical,
						lexical:            candidate.lexical,
						graph:              candidate.graph,
						rankFactor:         candidate.rankFactor,
						baselineScore:      candidate.score,
						baselineTieKey:     &tieKey,
						matchedTerms:       termsFor(matchedTermsByID, candidate.id),
						matchedStrongTerms: termsFor(matchedStrongTermsByID, candidate.id),
						eligible:           ok && node.Kind != Kind("file") && rawLexical > 0,
						spanStart:          math.NaN(),
					}
					if ok {
						fileCandidate.file = node.Path
						fileCandidate.spanStart = askSpanStartTS(node.Span)
						if node.Kind == Kind("file") {
							fileCandidate.kind = "file"
						}
					}
					fileCandidates = append(fileCandidates, fileCandidate)
				}
				rankedFiles := askRankFilesBounded(fileCandidates, fileQueryWeights, compare)
				collapsedFileByRepresentative = make(map[string]askRankedFileTS, len(rankedFiles))
				out := make([]askScopedDoc, 0, len(rankedFiles))
				for _, file := range rankedFiles {
					collapsedFileByRepresentative[file.representative.id] = file
					out = append(out, askScopedDoc{id: file.representative.id, scope: collapsedByID[file.representative.id].scope, score: file.score})
				}
				return out
			}
		}
		fusion := askRankScopesAndFuse(sortedScopes, ops, compare)
		if needsFileQueues {
			baselineDocs := make([]askScopedDoc, 0, len(collapsedComponents))
			for _, candidate := range collapsedComponents {
				baselineDocs = append(baselineDocs, candidate.askScopedDoc)
			}
			baselineFusion := askCombineComparableScopes(baselineDocs, compare)
			baselineScoreByID := make(map[string]float64, len(baselineFusion.ranked))
			for _, ranked := range baselineFusion.ranked {
				baselineScoreByID[ranked.id] = ranked.score
			}
			displayOrder := func(a, b askScopedDoc) int {
				if order := jsDiff(b.score, a.score); order != 0 {
					return order
				}
				return compare(symbolTitle(a.id), symbolTitle(b.id))
			}
			var toBuild []askScopedDoc
			if includeRankingMetadata {
				toBuild = slices.Clone(baselineFusion.ranked)
				slices.SortStableFunc(toBuild, displayOrder)
			} else if len(baselineFusion.ranked) > 0 {
				top := baselineFusion.ranked[0]
				for _, ranked := range baselineFusion.ranked[1:] {
					if displayOrder(ranked, top) < 0 {
						top = ranked
					}
				}
				toBuild = []askScopedDoc{top}
			}
			for _, ranked := range toBuild {
				scope := ranked.scope
				if hit := makeSymbolHit(ranked.id, ranked.score, &scope); hit != nil {
					baselineSymbolHits = append(baselineSymbolHits, hit)
					baselineHitByID[ranked.id] = hit
				}
			}
			for _, ranked := range fusion.ranked {
				file, ok := collapsedFileByRepresentative[ranked.id]
				if !ok {
					continue
				}
				scopeFor := func(member askFileCandidate) *string {
					scope := ranked.scope
					if component, ok := collapsedByID[member.id]; ok {
						scope = component.scope
					}
					return &scope
				}
				scoreOf := func(member askFileCandidate) float64 {
					if score, ok := baselineScoreByID[member.id]; ok {
						return score
					}
					return member.baselineScore
				}
				fileQueue, baselineQueue := materialize(file, ranked.score, scoreOf, scopeFor)
				var leaderScope *string
				if len(file.queue) > 0 {
					leaderScope = scopeFor(file.queue[0])
				}
				addFileGroup(file, ranked.score, leaderScope, fileQueue, baselineQueue)
			}
		} else {
			for _, ranked := range fusion.ranked {
				scope := ranked.scope
				if hit := makeSymbolHit(ranked.id, ranked.score, &scope); hit != nil {
					symbolHits = append(symbolHits, hit)
				}
			}
		}
		if len(fusion.federated) > 1 || len(fusion.alsoMatched) > 0 {
			scopeMeta = &AskScopes{Federated: fusion.federated, AlsoMatched: fusion.alsoMatched}
		}
	} else {
		lex := newAskScores()
		maxLex := 0.0
		for _, doc := range symbolDocs {
			if total := lexicalTotal(doc); total > 0 {
				lex.set(doc.node.ID, total)
				maxLex = math.Max(maxLex, total)
				recordMatches(doc, total)
			}
		}
		pr := newAskScores()
		if graphRank && lex.size() > 0 {
			var keep func(string) bool
			if prefix != "" {
				keep = func(id string) bool { return pathUnderPrefix(byID[id].Path, prefix) }
			}
			pr = askPageRankTS(askPrepareTopology(wiring, keep), lex)
		}
		ids := newAskTermSet()
		for _, id := range lex.ids {
			ids.add(id)
		}
		for _, id := range pr.ids {
			if pr.values[id] >= askRescueFloorTS {
				ids.add(id)
			}
		}
		type singleCandidate struct {
			id                                   string
			node                                 NodeV1
			lexical, graph, rankFactor, baseline float64
		}
		candidates := make([]singleCandidate, 0, len(ids.terms))
		for _, id := range ids.terms {
			node, ok := byID[id]
			if !ok {
				continue
			}
			lexical := 0.0
			if maxLex > 0 {
				lexical = lex.values[id] / maxLex
			}
			graphScore := pr.values[id]
			factor := testFactor(node.Path)
			baseline := (lexical + float64(askGraphWeightTS*graphScore)) * factor
			if baseline <= 0 {
				continue
			}
			candidates = append(candidates, singleCandidate{id: id, node: node, lexical: lexical, graph: graphScore, rankFactor: factor, baseline: baseline})
		}
		var rankedFiles []askRankedFileTS
		if fileComplement {
			fileCandidates := make([]askFileCandidate, 0, len(candidates))
			for _, candidate := range candidates {
				rawLexical := rawLexicalByID[candidate.id]
				tieKey := symbolTitle(candidate.id)
				kind := "symbol"
				if candidate.node.Kind == Kind("file") {
					kind = "file"
				}
				fileCandidates = append(fileCandidates, askFileCandidate{
					id:                 candidate.id,
					file:               candidate.node.Path,
					kind:               kind,
					rawLexical:         rawLexical,
					lexical:            candidate.lexical,
					graph:              candidate.graph,
					rankFactor:         candidate.rankFactor,
					baselineScore:      candidate.baseline,
					baselineTieKey:     &tieKey,
					matchedTerms:       termsFor(matchedTermsByID, candidate.id),
					matchedStrongTerms: termsFor(matchedStrongTermsByID, candidate.id),
					eligible:           candidate.node.Kind != Kind("file") && rawLexical > 0,
					spanStart:          askSpanStartTS(candidate.node.Span),
				})
			}
			rankedFiles = askRankFilesBounded(fileCandidates, fileQueryWeights, compare)
		}
		switch {
		case needsFileQueues && rankedFiles != nil:
			order := func(a, b singleCandidate) int {
				if order := jsDiff(b.baseline, a.baseline); order != 0 {
					return order
				}
				return compare(a.node.Name+" · "+string(a.node.Kind), b.node.Name+" · "+string(b.node.Kind))
			}
			var toBuild []singleCandidate
			if includeRankingMetadata {
				toBuild = slices.Clone(candidates)
				slices.SortStableFunc(toBuild, order)
			} else if len(candidates) > 0 {
				top := candidates[0]
				for _, candidate := range candidates[1:] {
					if order(candidate, top) < 0 {
						top = candidate
					}
				}
				toBuild = []singleCandidate{top}
			}
			for _, candidate := range toBuild {
				if hit := makeSymbolHit(candidate.id, candidate.baseline, nil); hit != nil {
					baselineSymbolHits = append(baselineSymbolHits, hit)
					baselineHitByID[candidate.id] = hit
				}
			}
			noScope := func(askFileCandidate) *string { return nil }
			baselineOf := func(member askFileCandidate) float64 { return member.baselineScore }
			for _, file := range rankedFiles {
				fileQueue, baselineQueue := materialize(file, file.score, baselineOf, noScope)
				addFileGroup(file, file.score, nil, fileQueue, baselineQueue)
			}
		case rankedFiles != nil:
			for _, file := range rankedFiles {
				if hit := makeSymbolHit(file.representative.id, file.score, nil); hit != nil {
					symbolHits = append(symbolHits, hit)
				}
			}
		default:
			for _, candidate := range candidates {
				if hit := makeSymbolHit(candidate.id, candidate.baseline, nil); hit != nil {
					symbolHits = append(symbolHits, hit)
				}
			}
		}
	}

	scoreOrder := func(a, b *AskHit) int {
		if order := jsDiff(b.Score, a.Score); order != 0 {
			return order
		}
		return compare(a.Title, b.Title)
	}
	scored := slices.Clone(symbolHits)
	slices.SortStableFunc(scored, scoreOrder)
	baselineScored := scored
	if needsFileQueues {
		baselineScored = slices.Clone(baselineSymbolHits)
		slices.SortStableFunc(baselineScored, scoreOrder)
	}
	groupOf := func(hit *AskHit, fallback string) string {
		if key, ok := selectionGroupOf[hit]; ok {
			return key
		}
		return fallback
	}
	unlockedGroups := slices.Clone(fileGroups)
	slices.SortStableFunc(unlockedGroups, func(a, b askGroupTS) int {
		return scoreOrder(a.hits[0], b.hits[0])
	})
	projectedGroups := unlockedGroups
	lockedGroupKey := ""
	locked := false
	if fileTopLock && fileFirst && len(baselineScored) > 0 {
		baselineTop := baselineScored[0]
		key := groupOf(baselineTop, "locked:top")
		lockedGroupKey, locked = key, true
		var existing *askGroupTS
		for index := range unlockedGroups {
			if unlockedGroups[index].key == key {
				existing = &unlockedGroups[index]
				break
			}
		}
		var baselineQueue []*AskHit
		if includeRankingMetadata || limit > float64(len(unlockedGroups)) {
			if queue, ok := baselineQueueByGroup[key]; ok {
				baselineQueue = queue()
			} else {
				for _, hit := range baselineScored {
					if groupOf(hit, "") == key {
						baselineQueue = append(baselineQueue, hit)
					}
				}
			}
		}
		var lockedGroup askGroupTS
		if existing != nil {
			lockedGroup = *existing
			lockedGroup.hits = []*AskHit{baselineTop}
			for _, hit := range baselineQueue {
				if !sameAskHit(*hit, *baselineTop) {
					lockedGroup.hits = append(lockedGroup.hits, hit)
				}
			}
		} else {
			lockedGroup = askGroupTS{key: key, hits: []*AskHit{baselineTop}, coverage: matchedOf[baselineTop], coverageStrong: matchedStrongOf[baselineTop]}
			if len(baselineQueue) > 0 {
				lockedGroup.hits = baselineQueue
			}
		}
		projectedGroups = []askGroupTS{lockedGroup}
		for _, group := range unlockedGroups {
			if group.key != key {
				projectedGroups = append(projectedGroups, group)
			}
		}
	}
	if fileTopLock && fileFirst && !includeRankingMetadata && limit > float64(len(projectedGroups)) {
		expanded := make([]askGroupTS, 0, len(projectedGroups))
		for _, group := range projectedGroups {
			if !locked || group.key != lockedGroupKey {
				if queue, ok := fileQueueByGroup[group.key]; ok {
					group.hits = queue()
				}
			}
			expanded = append(expanded, group)
		}
		projectedGroups = expanded
	}
	var selected []*AskHit
	switch {
	case fileTopLock && fileFirst:
		queues := make([][]*AskHit, 0, len(projectedGroups))
		for _, group := range projectedGroups {
			queues = append(queues, group.hits)
		}
		selected = askRoundRobinQueues(queues, math.Inf(1))
	case fileFirst:
		groups := make([]string, 0, len(scored))
		for index, hit := range scored {
			groups = append(groups, groupOf(hit, "ungrouped:"+strconv.Itoa(index)))
		}
		selected = askFileFirstRoundRobin(groups, scored, math.Inf(1))
	default:
		selected = slices.Clone(scored)
	}
	// Name-coverage tiers: the round-robin runs unbounded, a stable sort puts
	// hits whose names match more query terms first, and only then is the
	// selection cut, so file diversity and score order hold within each tier.
	slices.SortStableFunc(selected, func(left, right *AskHit) int {
		return cmp.Compare(right.NameTerms, left.NameTerms)
	})
	if capacity := JSQueueCap(limit); fileFirst && capacity >= 0 {
		selected = selected[:min(capacity, len(selected))]
	}
	var top *AskHit
	if len(selected) > 0 {
		top = selected[0]
	} else if len(scored) > 0 {
		top = scored[0]
	}
	if fileTopLock && top != nil && top.Scope != nil && *top.Scope != "" && scopeMeta != nil {
		federated := newAskTermSet()
		federated.add(*top.Scope)
		for _, scope := range scopeMeta.Federated {
			federated.add(scope)
		}
		alsoMatched := make([]AskScopeMatch, 0, len(scopeMeta.AlsoMatched))
		for _, match := range scopeMeta.AlsoMatched {
			if match.Scope != *top.Scope {
				alsoMatched = append(alsoMatched, match)
			}
		}
		scopeMeta = &AskScopes{Federated: federated.terms, AlsoMatched: alsoMatched}
	}

	result := AskResult{Query: query, Mode: "empty", Hits: make([]AskHit, 0), Scopes: scopeMeta}
	rarest := -1.0
	for _, term := range q {
		weight, ok := idf[term]
		if !ok {
			weight = defaultIDF
		}
		if weight > rarest {
			result.Distinctive, rarest = term, weight
		}
	}
	// Suggest the word as the query spelled it, not its folded stem.
	for _, word := range askTokenSeparator.Split(strings.ToLower(askCamelBoundary.ReplaceAllString(query, "$1 $2")), -1) {
		if word != "" && AskFold(word) == result.Distinctive {
			result.Distinctive = word
			break
		}
	}
	if len(scored) > 0 {
		result.Mode = "lexical"
	} else {
		result.Note = "no matching nodes — try different words, or `graft build` if graft/ is empty" + scopesHereClause(scopes)
	}
	for _, hit := range selected[:jsSliceEnd(len(selected), limit)] {
		result.Hits = append(result.Hits, *hit)
	}
	if top != nil && len(q) > 0 {
		coverage, coverageStrong := matchedOf[top], matchedStrongOf[top]
		result.Coverage, result.CoverageStrong = &coverage, &coverageStrong
	}
	if includeRankingMetadata && needsFileQueues {
		result.Ranking = askRankingMetadataTS(unlockedGroups, baselineScored, limit, selectionGroupOf, matchedOf, matchedStrongOf)
	}
	return result
}

func askRankingMetadataTS(groups []askGroupTS, baselineScored []*AskHit, limit float64, groupOf map[*AskHit]string, matchedOf, matchedStrongOf map[*AskHit]float64) *AskRankingMetadata {
	values := func(hits []*AskHit) []AskHit {
		out := make([]AskHit, 0, len(hits))
		for _, hit := range hits {
			out = append(out, *hit)
		}
		return out
	}
	metadata := &AskRankingMetadata{}
	for _, group := range groups[:jsSliceEnd(len(groups), limit)] {
		baseline := make([]*AskHit, 0)
		for _, hit := range baselineScored {
			if key, ok := groupOf[hit]; ok && key == group.key {
				baseline = append(baseline, hit)
			}
		}
		metadata.Groups = append(metadata.Groups, AskRankingGroup{
			Key:            group.key,
			Hits:           values(group.hits[:jsSliceEnd(len(group.hits), limit)]),
			BaselineHits:   values(baseline[:jsSliceEnd(len(baseline), limit)]),
			Coverage:       group.coverage,
			CoverageStrong: group.coverageStrong,
		})
	}
	for index, hit := range baselineScored[:jsSliceEnd(len(baselineScored), limit)] {
		key, ok := groupOf[hit]
		if !ok {
			key = "ungrouped:" + strconv.Itoa(index)
		}
		metadata.Baseline = append(metadata.Baseline, AskRankingEntry{Group: key, Hit: *hit})
	}
	if len(baselineScored) > 0 {
		coverage, coverageStrong := matchedOf[baselineScored[0]], matchedStrongOf[baselineScored[0]]
		metadata.BaselineCoverage, metadata.BaselineCoverageStrong = &coverage, &coverageStrong
	}
	return metadata
}

// askNodeSnippetTS is the node's signature. A documented node's summary goes
// to AskHit.Doc instead, so hook pointers keep carrying the signature.
func askNodeSnippetTS(node NodeV1) string {
	if node.Signature != nil {
		return *node.Signature
	}
	return ""
}

// ScopedDoc is one scored document attributed to a fusion scope.
type ScopedDoc struct {
	ID    string
	Scope string
	Score float64
}

// ScopeFusion is fuseScopes' result: the fused order, the federated scopes,
// and the scopes gated out with their best document.
type ScopeFusion struct {
	Ranked      []ScopedDoc
	Federated   []string
	AlsoMatched []AskScopeMatch
}

// FuseScopes ports fuse.ts's fuseScopes: partition by scope, rank within each,
// gate weak scopes by PARTICIPATION_RATIO, and fuse the rest by reciprocal
// rank, normalized so the top fused document scores 1.
func FuseScopes(docs []ScopedDoc) ScopeFusion {
	compare := localeCompare()
	scopeOrder := make([]string, 0)
	byScope := make(map[string][]ScopedDoc)
	for _, doc := range docs {
		if doc.Score <= 0 {
			continue
		}
		if _, ok := byScope[doc.Scope]; !ok {
			scopeOrder = append(scopeOrder, doc.Scope)
		}
		byScope[doc.Scope] = append(byScope[doc.Scope], doc)
	}
	for _, scope := range scopeOrder {
		slices.SortStableFunc(byScope[scope], func(a, b ScopedDoc) int {
			if order := jsDiff(b.Score, a.Score); order != 0 {
				return order
			}
			return compare(a.ID, b.ID)
		})
	}
	if len(scopeOrder) == 0 {
		return ScopeFusion{Ranked: []ScopedDoc{}, Federated: []string{}, AlsoMatched: []AskScopeMatch{}}
	}
	if len(scopeOrder) == 1 {
		scope := scopeOrder[0]
		return ScopeFusion{Ranked: slices.Clone(byScope[scope]), Federated: []string{scope}, AlsoMatched: []AskScopeMatch{}}
	}
	scopesByBest := slices.Clone(scopeOrder)
	slices.SortStableFunc(scopesByBest, func(a, b string) int {
		if order := jsDiff(byScope[b][0].Score, byScope[a][0].Score); order != 0 {
			return order
		}
		return compare(a, b)
	})
	gate := askParticipationRatio * byScope[scopesByBest[0]][0].Score
	federated := make([]string, 0)
	alsoMatched := make([]AskScopeMatch, 0)
	for _, scope := range scopesByBest {
		if byScope[scope][0].Score >= gate {
			federated = append(federated, scope)
		} else {
			alsoMatched = append(alsoMatched, AskScopeMatch{Scope: scope, BestID: byScope[scope][0].ID})
		}
	}
	type accumulated struct {
		scope    string
		score    float64
		bestRank int
	}
	order := make([]string, 0)
	acc := make(map[string]*accumulated)
	for _, scope := range federated {
		for rank, doc := range byScope[scope] {
			contribution := 1 / float64(61+rank)
			previous, ok := acc[doc.ID]
			if !ok {
				order = append(order, doc.ID)
				acc[doc.ID] = &accumulated{scope: scope, score: contribution, bestRank: rank}
				continue
			}
			previous.score += contribution
			if rank < previous.bestRank {
				previous.scope, previous.bestRank = scope, rank
			}
		}
	}
	maximum := 0.0
	for _, id := range order {
		if acc[id].score > maximum {
			maximum = acc[id].score
		}
	}
	ranked := make([]ScopedDoc, 0, len(order))
	for _, id := range order {
		ranked = append(ranked, ScopedDoc{ID: id, Scope: acc[id].scope, Score: acc[id].score / maximum})
	}
	slices.SortStableFunc(ranked, func(a, b ScopedDoc) int {
		if order := jsDiff(b.Score, a.Score); order != 0 {
			return order
		}
		if order := compare(a.Scope, b.Scope); order != 0 {
			return order
		}
		return compare(a.ID, b.ID)
	})
	return ScopeFusion{Ranked: ranked, Federated: federated, AlsoMatched: alsoMatched}
}
