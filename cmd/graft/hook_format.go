package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
)

const (
	hookSep                  = "\x1b[38;5;244m · \x1b[0m"
	hookOrientationDirective = "[graft] This repo is indexed by graft. To find, understand, or change code, reach for graft first; it answers from a prebuilt graph with exact file:line, faster than grep/read. Pick the ONE tool that fits and act on its answer. Most tasks need a single call. If one isn't enough, switch to the tool that fits the next need; don't call the same tool again and again or re-ask a question reworded:\n" +
		"  • graft ask \"<task>\" --source: locate + understand. Ranked nodes with the code inlined at each file:line (the ≤8-line crux; add --full for the whole span). The default for \"how does X work\" / \"where is Y\".\n" +
		"  • graft grep \"<literal>\": exhaustive find. Every occurrence, grouped by enclosing symbol; use when you need them ALL (ask is ranked top-N and misses instances).\n" +
		"  • graft skeleton <file>: a file's whole API in ~200 tokens, every signature + span, ~10x cheaper than reading the file.\n" +
		"  • graft callers <sym> [--direction out] [--depth N|all]: exact edges. Who calls it (default), what it calls (--direction out), or the full blast radius (--depth 2, or --depth all for every connected source). Run before you change a symbol.\n" +
		"  • graft map: orientation for an unfamiliar repo, directory clusters, hubs, hotspots. map alone is the answer; don't then skeleton every subsystem it names.\n" +
		"  In a monorepo, add --in <path>/ to ask/grep/callers to scope to one sub-project; hits are labeled [scope/].\n" +
		"  Already know the file or symbol to change? Go straight to it: graft grep \"<symbol>\", read the span, edit. Save ask for when you don't yet know where the code lives.\n" +
		"  Refactor, rename, or multi-file change? Run graft callers <sym> --depth all FIRST to map every connected file; editing the primary file and stopping is the classic miss (platform siblings, a new file to extract).\n"
)

type hookFreshness struct {
	Missing int
	Total   int
}

func hookANSI(color, value string) string {
	return "\x1b[" + color + "m" + value + "\x1b[0m"
}

func hookIndexFreshness(root string) *hookFreshness {
	fingerprint := graph.ReadFingerprintScope(hookContextDir(root))
	if fingerprint == nil {
		return nil
	}
	freshness := &hookFreshness{Total: len(fingerprint.Files)}
	for path := range fingerprint.Files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			freshness.Missing++
		}
	}
	return freshness
}

func hookStaleBanner(freshness *hookFreshness) string {
	if freshness == nil || freshness.Missing == 0 {
		return ""
	}
	return fmt.Sprintf("⚠ graft's index may be ahead of your working tree: %d of %d indexed files are not on disk (branch switch or uncommitted move?). If graft names a path that isn't there, don't chase it — `graft grep` the symbol to find where it lives now; run `graft build` to refresh.", freshness.Missing, freshness.Total)
}

func formatHookOrientation(index string, budget int, staleNote string) string {
	banner := ""
	if staleNote != "" {
		banner = staleNote + "\n\n"
	}
	return banner + hookOrientationDirective + "\nrepo map (graft/INDEX.md):\n" + hookTruncateUTF16(index, budget)
}

func hookTruncateUTF16(value string, limit int) string {
	if savings.Length(value) <= limit {
		return value
	}
	length := 0
	for index, r := range value {
		width := utf16.RuneLen(r)
		if length+width > limit {
			return value[:index]
		}
		length += width
	}
	return value
}

func formatHookBlastRadius(wiring graph.GraphV1, filePath string, cap int) string {
	ids := make(map[string]struct{})
	normalizedPath := filepath.ToSlash(filePath)
	for _, node := range wiring.Nodes {
		nodePath := filepath.ToSlash(node.Path)
		if node.Path != "" && (normalizedPath == nodePath || strings.HasSuffix(normalizedPath, "/"+nodePath)) {
			ids[node.ID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return ""
	}

	var edges []graph.EdgeV1
	for _, edge := range wiring.Edges {
		_, targetInFile := ids[edge.Target]
		_, sourceInFile := ids[edge.Source]
		if targetInFile && !sourceInFile {
			edges = append(edges, edge)
		}
	}
	if len(edges) == 0 {
		return ""
	}
	if cap < 0 {
		cap = 0
	}
	if cap > len(edges) {
		cap = len(edges)
	}

	nodes := make(map[string]graph.NodeV1, len(wiring.Nodes))
	for _, node := range wiring.Nodes {
		nodes[node.ID] = node
	}
	items := make([]string, 0, cap+1)
	for _, edge := range edges[:cap] {
		label := edge.Source
		if node, ok := nodes[edge.Source]; ok {
			label = node.Name + " (" + filepath.Base(node.Path) + ")"
		}
		items = append(items, fmt.Sprintf(" • %s ← %s", edge.Relation, label))
	}
	if len(edges) > cap {
		items = append(items, fmt.Sprintf(" • +%d more", len(edges)-cap))
	}
	return fmt.Sprintf("[graft] blast radius for %s, who depends on it:\n%s", filepath.Base(filePath), strings.Join(items, "\n"))
}

func hookRetrievalBody(hits []graph.AskHit) string {
	blocks := make([]string, 0, len(hits))
	withCode := false
	for index, hit := range hits {
		pointer, _, _ := strings.Cut(hit.Pointer, ",")
		pointer = strings.TrimSpace(pointer)
		snippet := hookTruncateUTF16(strings.Join(strings.Fields(hit.Snippet), " "), 140)
		block := fmt.Sprintf(" %d. %s: %s", index+1, hit.Title, pointer)
		if snippet != "" {
			block += " — " + snippet
		}
		if hit.Code != "" {
			withCode = true
			block += "\n```\n" + askExcerptText(hit.Code, false) + "\n```"
		}
		blocks = append(blocks, block)
	}
	header := "[graft] starting points for this task: pull the code inline with `graft ask \"<what you need>\" --source`, trace impact with `graft callers <symbol>`, or search with `graft grep \"<literal>\"`:"
	if withCode {
		header = "[graft] retrieved context: cite these spans instead of re-opening the files; if an excerpt is cut, rerun with --full or open just that range:"
	}
	return header + "\n" + strings.Join(blocks, "\n")
}

func hookRetrievalTokensSaved(result graph.AskResult, cap int) int {
	if len(result.Hits) == 0 || result.Saved == nil || result.Saved.BaselineChars <= 0 {
		return 0
	}
	if cap > len(result.Hits) {
		cap = len(result.Hits)
	}
	if cap <= 0 {
		return 0
	}
	pack := savings.Tokens(savings.Length(hookRetrievalBody(result.Hits[:cap])))
	base := savings.Tokens(result.Saved.BaselineChars)
	if base <= pack {
		return 0
	}
	return base - pack
}

func formatHookRetrieval(result *graph.AskResult, cap int) string {
	if result == nil || len(result.Hits) == 0 {
		return ""
	}
	if cap > len(result.Hits) {
		cap = len(result.Hits)
	}
	if cap <= 0 {
		return ""
	}
	return hookRetrievalBody(result.Hits[:cap])
}

func hookWeakMatchNudge(session *sessionState, strong float64) string {
	if session.Nudges >= 2 {
		return ""
	}
	session.Nudges++
	return fmt.Sprintf("[graft] no strong match for this prompt (name-field match %.2f) — the graph has more than this probe found. Run `graft ask \"<your task>\" --source` before grepping.", strong)
}

func relevantHookRetrieval(result *graph.AskResult, session *sessionState, cap int, agent string) string {
	if result == nil || len(result.Hits) == 0 {
		return ""
	}
	if askWeakMatch(*result) {
		return hookWeakMatchNudge(session, askOptionalCoverage(result.CoverageStrong))
	}
	// Only a strong top hit is inlined; every other hit is a pointer. A
	// revision covers the hit's code whether or not it is shown, plus whether
	// it was held back, so a changed body and a pointer later sent inlined are
	// both delivered again.
	hits := slices.Clone(result.Hits)
	for i := range hits {
		if i > 0 || askOptionalCoverage(result.CoverageStrong) < askStrongCoverage {
			hits[i].Code = ""
		}
	}

	seen := make(map[string]struct{}, len(session.InjectedRevisions))
	for _, revision := range session.InjectedRevisions {
		seen[revision] = struct{}{}
	}
	fresh := make([]graph.AskHit, 0, len(hits))
	var revisions []string
	for i, hit := range hits {
		fields := []string{agent, hit.Pointer, hit.Title, hit.SourceHash, result.Hits[i].Code, hit.Snippet}
		if hit.Code != result.Hits[i].Code {
			fields = append(fields, "pointer")
		}
		content := fmt.Sprintf("%q", fields)
		revision := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		if _, ok := seen[revision]; ok {
			continue
		}
		seen[revision] = struct{}{}
		fresh = append(fresh, hit)
		revisions = append(revisions, revision)
	}
	filtered := *result
	filtered.Hits = fresh
	text := formatHookRetrieval(&filtered, cap)
	if text == "" {
		return ""
	}
	if cap > len(fresh) {
		cap = len(fresh)
	}
	for _, hit := range fresh[:cap] {
		session.InjectedPointers = append(session.InjectedPointers, hit.Pointer)
	}
	session.InjectedRevisions = append(session.InjectedRevisions, revisions[:cap]...)
	if len(session.InjectedPointers) > 40 {
		session.InjectedPointers = session.InjectedPointers[len(session.InjectedPointers)-40:]
	}
	if len(session.InjectedRevisions) > 40 {
		session.InjectedRevisions = session.InjectedRevisions[len(session.InjectedRevisions)-40:]
	}
	session.SavedTokens += hookRetrievalTokensSaved(filtered, cap)
	return text
}

func hookFreshnessSegment(stats hookStats) string {
	switch {
	case stats.Syncing:
		return hookANSI("38;2;224;165;68", "syncing…")
	case stats.Dirty && stats.StaleCount > 0:
		return hookANSI("38;2;224;165;68", fmt.Sprintf("⚠ %d stale", stats.StaleCount))
	case stats.Dirty:
		return hookANSI("38;2;224;165;68", "⚠ stale")
	default:
		return hookANSI("38;2;84;111;255", "✓ synced")
	}
}

func renderHookStatusline(stats *hookStats, session *sessionState, contextPercent *int) []string {
	if stats == nil {
		return []string{hookANSI("38;5;244", "◤ graft · not built · run ") + hookANSI("38;5;251", "graft build")}
	}
	top := []string{
		hookANSI("38;5;244", "◤ ") + hookANSI("38;2;84;111;255", "graft"),
		hookANSI("38;5;251", fmt.Sprintf("%d nodes / %d edges", stats.NodeCount, stats.EdgeCount)),
		hookFreshnessSegment(*stats),
	}
	if session != nil && session.SavedTokens > 0 {
		money := ""
		if session.InputCostMicros != nil && session.InputTokensBilled != nil {
			if usd, ok := savings.DollarsSaved(float64(session.SavedTokens), float64(*session.InputCostMicros), float64(*session.InputTokensBilled)); ok {
				money = " · ~" + savings.FormatDollars(usd)
			}
		}
		top = append(top, hookANSI("38;2;84;111;255", fmt.Sprintf("~%s tok saved%s", savings.Group(session.SavedTokens), money)))
	}

	var bottom []string
	if contextPercent != nil {
		bottom = append(bottom, hookANSI("38;5;251", fmt.Sprintf("ctx %d%%", *contextPercent)))
	}
	if stats.LastFile != nil {
		bottom = append(bottom, hookANSI("38;5;244", "last: ")+hookANSI("38;5;251", filepath.Base(*stats.LastFile)))
	}
	lines := []string{strings.Join(top, hookSep)}
	if len(bottom) > 0 {
		lines = append(lines, hookANSI("38;5;244", "▸ ")+strings.Join(bottom, hookSep))
	}
	return lines
}

func resolveHookStats(root string) *hookStats {
	if cached := readHookStats(root); cached != nil && cached.NodeCount > 0 {
		return cached
	}
	wiring, err := graph.Read(graph.WiringPath(hookContextDir(root)))
	if err != nil {
		return nil
	}
	stats := emptyHookStats()
	if wiring.Meta.Version != 0 {
		stats.NodeCount = wiring.Meta.NodeCount
		stats.EdgeCount = wiring.Meta.EdgeCount
	} else {
		stats.NodeCount = len(wiring.Nodes)
		stats.EdgeCount = len(wiring.Edges)
	}
	if wiring.Meta.Languages != nil {
		stats.Languages = wiring.Meta.Languages
	}
	stats.TotalCount = len(wiring.Nodes)
	for _, node := range wiring.Nodes {
		if node.SummaryState == "ready" {
			stats.ReadyCount++
		}
	}
	return &stats
}

func renderHookSubagent(agent string, session *sessionState) string {
	line := hookANSI("38;5;244", "◤ ") + hookANSI("38;2;84;111;255", agent)
	if session != nil {
		if query := session.PerAgentQuery[agent]; query != "" {
			line += hookSep + hookANSI("38;5;244", "graft: ") + hookANSI("38;5;251", query)
		}
	}
	return line
}
