package blast

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maxModuleBoxes   = 5
	maxTableRows     = 6
	maxSymbolsListed = 60
	maxOwnerRows     = 8
)

var testGlyph = map[TestSignal]string{TestsChanged: "✓", TestsStale: "⚠", TestsNone: "✗", TestsNA: "–"}

func plural(count int, one string) string {
	return pluralOf(count, one, one+"s")
}

func pluralOf(count int, one, many string) string {
	if count == 1 {
		return strconv.Itoa(count) + " " + one
	}
	return strconv.Itoa(count) + " " + many
}

func depthLabel(depth Depth) string {
	if int(depth) == FullDepth {
		return "full closure"
	}
	return "depth " + strconv.Itoa(int(depth))
}

func mermaidLabel(lines ...string) string {
	escaped := make([]string, len(lines))
	for i, line := range lines {
		escaped[i] = strings.NewReplacer(`"`, "#quot;", "<", "&lt;", ">", "&gt;").Replace(line)
	}
	return `"` + strings.Join(escaped, "<br/>") + `"`
}

func symbolCount(modules []*ImpactedModule) int {
	total := 0
	for _, module := range modules {
		total += len(module.Symbols)
	}
	return total
}

// MermaidDiagram draws one circle per affected area, or returns false when
// nothing depends on the diff.
func MermaidDiagram(report *Report) (string, bool) {
	shown := report.Modules[:min(len(report.Modules), maxModuleBoxes)]
	if len(shown) == 0 {
		return "", false
	}
	hidden := report.Modules[len(shown):]
	lines := []string{"flowchart TB"}
	ids := make([]string, len(shown))
	for i, module := range shown {
		ids[i] = "A" + strconv.Itoa(i)
		lines = append(lines, fmt.Sprintf("  %s((%s))", ids[i], mermaidLabel(module.Label, plural(len(module.Symbols), "symbol"))))
	}
	if len(hidden) > 0 {
		lines = append(lines, fmt.Sprintf("  AX((%s))", mermaidLabel(plural(len(hidden), "smaller area"), plural(symbolCount(hidden), "symbol"))))
	}
	lines = append(lines,
		"  classDef reached fill:#D9EDF3,stroke:#3AA7C9,stroke-width:1.5px,color:#0E313C;",
		"  class "+strings.Join(ids, ",")+" reached;")
	if len(hidden) > 0 {
		lines = append(lines,
			"  classDef tail fill:#EEF2F3,stroke:#9AA4A9,stroke-width:1px,color:#3A4247;",
			"  class AX tail;")
	}
	return strings.Join(lines, "\n"), true
}

// MarkdownReport renders the PR-comment body. With a non-empty root the symbol
// list quotes the line that reaches the diff.
func MarkdownReport(report *Report, root string) string {
	out := make([]string, 0)
	symbols := symbolCount(report.Modules)
	out = append(out, "### 🌱 graft blast radius", "", headline(report, symbols))
	if line, ok := testHeadline(report); ok {
		out = append(out, line)
	}
	if line, ok := tagLine(report); ok {
		out = append(out, line)
	}
	if symbols > 0 {
		if diagram, ok := MermaidDiagram(report); ok {
			out = append(out, "", "```mermaid", diagram, "```")
		}
		out = append(out, "")
		out = append(out, impactTable(report)...)
	}
	if owners := ownerSection(report); len(owners) > 0 {
		out = append(out, "")
		out = append(out, owners...)
	}
	out = append(out, "")
	terms := newReachTerms(report.Seeds, report.Changed)
	read := fileReader(root)
	out = append(out, detailSections(report, symbols, func(symbol Impacted) (evidenceLine, bool) {
		return impactedLine(symbol, terms, read)
	})...)
	if caveats := caveatLines(report); len(caveats) > 0 {
		out = append(out, "")
		out = append(out, caveats...)
	}
	out = append(out, "", fmt.Sprintf("<sub>`graft blast` · %s · %s · %s</sub>", report.Basis, depthLabel(report.Depth), plural(len(report.Changed), "changed file")))
	return strings.Join(out, "\n") + "\n"
}

func headline(report *Report, symbols int) string {
	areas := plural(len(report.Areas), "area")
	if symbols == 0 {
		return fmt.Sprintf("**Nothing outside this diff depends on it.** %s changed; no indexed dependents at %s.", areas, depthLabel(report.Depth))
	}
	return fmt.Sprintf("**%s changed → %s can be affected.** %s, %s.", areas, plural(len(report.Modules), "area"), plural(symbols, "dependent symbol"), depthLabel(report.Depth))
}

func areaLabels(areas []*ChangedArea, signal TestSignal) []string {
	labels := make([]string, 0)
	for _, area := range areas {
		if area.Tests == signal {
			labels = append(labels, area.Label)
		}
	}
	return labels
}

func testHeadline(report *Report) (string, bool) {
	if len(report.Areas) == 0 {
		return "", false
	}
	none, stale, changed := areaLabels(report.Areas, TestsNone), areaLabels(report.Areas, TestsStale), areaLabels(report.Areas, TestsChanged)
	parts := make([]string, 0, 3)
	if len(none) > 0 {
		parts = append(parts, "**no test reaches "+strings.Join(none, ", ")+"**")
	}
	if len(stale) > 0 {
		verb := "have tests"
		if len(stale) == 1 {
			verb = "has tests"
		}
		parts = append(parts, strings.Join(stale, ", ")+" "+verb+" the diff did not touch")
	}
	if len(changed) > 0 {
		their := "their"
		if len(changed) == 1 {
			their = "its"
		}
		parts = append(parts, plural(len(changed), "area")+" updated "+their+" tests")
	}
	if len(parts) == 0 {
		return "", false
	}
	return "Tests: " + strings.Join(parts, "; ") + ".", true
}

func impactTable(report *Report) []string {
	shown := report.Modules[:min(len(report.Modules), maxTableRows)]
	hidden := report.Modules[len(shown):]
	areaOf := make(map[string]string)
	for _, area := range report.Areas {
		for _, file := range area.Files {
			areaOf[file] = area.Label
		}
	}
	rows := []string{"| Can be affected | Symbols | Nearest hop | Reached from |", "| --- | --: | --- | --- |"}
	for _, module := range shown {
		hop := "—"
		if len(module.Symbols) > 0 {
			nearest := module.Symbols[0]
			hop = fmt.Sprintf("`%s:%s` %s — %s, depth %d", nearest.Path, nearest.Span, nearest.Name, nearest.Relation, nearest.Depth)
		}
		names := make([]string, 0)
		for _, file := range module.From {
			name, ok := areaOf[file]
			if !ok {
				name = file
			}
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
		from := "—"
		switch {
		case len(names) > 2:
			from = fmt.Sprintf("%s +%d", strings.Join(names[:2], ", "), len(names)-2)
		case len(names) > 0:
			from = strings.Join(names, ", ")
		}
		rows = append(rows, fmt.Sprintf("| %s | %d | %s | %s |", module.Label, len(module.Symbols), hop, from))
	}
	if len(hidden) > 0 {
		labels := make([]string, 0, 3)
		for _, module := range hidden[:min(len(hidden), 3)] {
			labels = append(labels, module.Label)
		}
		more := ""
		if len(hidden) > 3 {
			more = ", …"
		}
		rows = append(rows, fmt.Sprintf("| _%s_ | %d | %s%s | see below |", plural(len(hidden), "smaller area"), symbolCount(hidden), strings.Join(labels, ", "), more))
	}
	return rows
}

func tagLine(report *Report) (string, bool) {
	if report.Reviewers == nil || len(*report.Reviewers) == 0 {
		return "", false
	}
	total := len(report.Areas) + len(report.Modules)
	bits := make([]string, 0, len(*report.Reviewers))
	for _, person := range *report.Reviewers {
		who := "**" + person.Name + "**"
		if person.Handle != "" {
			who = "@" + person.Handle
		}
		why := strings.Join(person.Areas, ", ")
		if len(person.Areas) >= 3 {
			why = fmt.Sprintf("%d of %d areas", len(person.Areas), total)
		}
		bits = append(bits, who+" — "+why)
	}
	return "Tag: " + strings.Join(bits, " · "), true
}

func ownerCell(owners *[]Owner, now int64) string {
	if owners == nil || len(*owners) == 0 {
		return "_only you — nobody else has touched these files_"
	}
	cells := make([]string, 0, len(*owners))
	for _, owner := range *owners {
		cells = append(cells, fmt.Sprintf("%s — %s, last %s", Mention(owner), plural(owner.Commits, "commit"), SinceLabel(owner.Last, now)))
	}
	return strings.Join(cells, " · ")
}

type ownerRow struct {
	label, side string
	owners      *[]Owner
}

func ownerSection(report *Report) []string {
	if report.Reviewers == nil {
		return nil
	}
	rows := make([]ownerRow, 0, len(report.Areas)+len(report.Modules))
	for _, area := range report.Areas {
		rows = append(rows, ownerRow{label: area.Label, side: "changed", owners: area.Owners})
	}
	for _, module := range report.Modules {
		rows = append(rows, ownerRow{label: module.Label, side: "affected", owners: module.Owners})
	}
	named := make([]ownerRow, 0)
	unnamed := make([]ownerRow, 0)
	people := make([]string, 0)
	for _, row := range rows {
		if row.owners != nil && len(*row.owners) > 0 {
			named = append(named, row)
		} else {
			unnamed = append(unnamed, row)
		}
		if row.owners == nil {
			continue
		}
		for _, owner := range *row.owners {
			who := owner.Name
			if owner.Handle != "" {
				who = owner.Handle
			}
			if !slices.Contains(people, who) {
				people = append(people, who)
			}
		}
	}
	if len(named) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	out := []string{
		"<details>",
		fmt.Sprintf("<summary><strong>Who knows this code</strong> — %s across %s</summary>", pluralOf(len(people), "person", "people"), plural(len(rows), "area")),
		"",
		"| Area | Who knows it |",
		"| --- | --- |",
	}
	ordered := append(named, unnamed...)
	for _, row := range ordered[:min(len(ordered), maxOwnerRows)] {
		out = append(out, fmt.Sprintf("| **%s** · %s | %s |", row.label, row.side, ownerCell(row.owners, now)))
	}
	if len(ordered) > maxOwnerRows {
		out = append(out, fmt.Sprintf("| _…%s_ | |", plural(len(ordered)-maxOwnerRows, "further area")))
	}
	return append(out, "",
		"_Ownership is git history over each area's own files, weighted towards recent work "+
			"(120-day half-life). Merge commits and bots are dropped, and you are dropped from your own PR. "+
			"A name with no `@` has no GitHub handle in its commit email — tag them by hand, or add a "+
			"`.mailmap` entry. A suggestion from history, not a CODEOWNERS rule._",
		"", "</details>")
}

func detailSections(report *Report, symbols int, evidence func(Impacted) (evidenceLine, bool)) []string {
	out := make([]string, 0)
	if symbols > 0 {
		out = append(out, "<details>", fmt.Sprintf("<summary><strong>All %s</strong>, grouped by area</summary>", plural(symbols, "dependent symbol")), "")
		out = append(out, symbolList(report, symbols, evidence)...)
		out = append(out, "</details>")
	}
	if len(report.Areas) > 0 {
		out = append(out, testSignalSection(report.Areas)...)
	}
	if len(report.TestModules) > 0 {
		files := testModuleFiles(report.TestModules)
		verb := "reference"
		if len(files) == 1 {
			verb = "references"
		}
		out = append(out, "<details>",
			fmt.Sprintf("<summary>%s also %s this code</summary>", plural(len(files), "test suite"), verb), "",
			plural(symbolCount(report.TestModules), "symbol")+", kept out of the diagram and the table so they cannot crowd out the areas a reviewer has to look at.", "")
		for _, file := range files[:min(len(files), 20)] {
			out = append(out, "- `"+file+"`")
		}
		if len(files) > 20 {
			out = append(out, fmt.Sprintf("- …%d more", len(files)-20))
		}
		out = append(out, "", "</details>")
	}
	return out
}

func symbolList(report *Report, symbols int, evidence func(Impacted) (evidenceLine, bool)) []string {
	out := make([]string, 0)
	listed := 0
	for _, module := range report.Modules {
		out = append(out, fmt.Sprintf("**%s** — %s in %s", module.Label, plural(len(module.Symbols), "symbol"), plural(len(module.Files), "file")), "")
		for _, symbol := range module.Symbols {
			if listed >= maxSymbolsListed {
				listed++
				break
			}
			listed++
			out = append(out, fmt.Sprintf("- `%s:%s` — %s (%s, depth %d)", symbol.Path, symbol.Span, symbol.Name, symbol.Relation, symbol.Depth))
			if line, ok := evidence(symbol); ok {
				out = append(out, fmt.Sprintf("  ```%d: %s```", line.N, trimJS(line.Text)))
			}
		}
		out = append(out, "")
		if listed >= maxSymbolsListed {
			out = append(out, fmt.Sprintf("…%s not listed.", plural(symbols-listed, "further symbol")), "")
			break
		}
	}
	return out
}

func testSignalSection(areas []*ChangedArea) []string {
	count := func(signal TestSignal) int { return len(areaLabels(areas, signal)) }
	states := make([]string, 0, 4)
	for _, state := range []struct {
		signal TestSignal
		glyph  string
	}{{TestsChanged, "✓"}, {TestsStale, "⚠"}, {TestsNone, "✗"}, {TestsNA, "–"}} {
		if n := count(state.signal); n > 0 {
			states = append(states, fmt.Sprintf("%d %s", n, state.glyph))
		}
	}
	out := []string{
		"<details>",
		"<summary><strong>Test signal</strong> per changed area — " + strings.Join(states, " · ") + "</summary>",
		"",
		"_Reached = a node under a test path has a resolved edge into the changed symbol. It undercounts anything called indirectly — through a CLI, a spawned process or a dynamic import — so read a low ratio as “look here”, never as a coverage gate._",
		"",
	}
	for _, area := range areas {
		bits := make([]string, 0, 2)
		if area.Behavioural > 0 {
			bits = append(bits, fmt.Sprintf("%d of %d reached", area.Reached, area.Behavioural))
		}
		switch {
		case len(area.ChangedTestFiles) > 0:
			quoted := make([]string, len(area.ChangedTestFiles))
			for i, file := range area.ChangedTestFiles {
				quoted[i] = "`" + file + "`"
			}
			bits = append(bits, plural(len(area.ChangedTestFiles), "test file")+" changed here: "+strings.Join(quoted, ", "))
		case len(area.TestFiles) > 0:
			verb := "reach"
			if len(area.TestFiles) == 1 {
				verb = "reaches"
			}
			bits = append(bits, plural(len(area.TestFiles), "test file")+" "+verb+" it, none changed here")
		case area.Behavioural > 0:
			bits = append(bits, "no test file reaches it")
		default:
			bits = append(bits, "no function, method or class changed here")
		}
		out = append(out, fmt.Sprintf("- %s **%s** — %s", testGlyph[area.Tests], area.Label, strings.Join(bits, " · ")))
		if len(area.Unreached) > 0 {
			names := make([]string, 0, 8)
			for _, name := range area.Unreached[:min(len(area.Unreached), 8)] {
				names = append(names, "`"+name+"`")
			}
			more := ""
			if len(area.Unreached) > 8 {
				more = fmt.Sprintf(", …%d more", len(area.Unreached)-8)
			}
			out = append(out, "  - not reached: "+strings.Join(names, ", ")+more)
		}
	}
	return append(out, "", "</details>")
}

func testModuleFiles(modules []*ImpactedModule) []string {
	files := make([]string, 0)
	for _, module := range modules {
		for _, file := range module.Files {
			if !slices.Contains(files, file) {
				files = append(files, file)
			}
		}
	}
	sortCodeUnits(files)
	return files
}

func caveatLines(report *Report) []string {
	out := make([]string, 0, 2)
	if len(report.Deleted) > 0 {
		out = append(out, fmt.Sprintf("⚠️ %s (%s) — their dependents cannot be computed from a graph built at this commit, since the files are gone from it.",
			plural(len(report.Deleted), "deleted file"), strings.Join(report.Deleted[:min(len(report.Deleted), 5)], ", ")))
	}
	if len(report.Unindexed) > 0 {
		out = append(out, fmt.Sprintf("⚠️ %s not in the graph (%s) — no parser claims the extension, or the index predates the file.",
			plural(len(report.Unindexed), "changed file"), strings.Join(report.Unindexed[:min(len(report.Unindexed), 5)], ", ")))
	}
	return out
}

var trailingNewlines = regexp.MustCompile(`\n+$`)

// TextReport renders the terminal report.
func TextReport(report *Report) string {
	symbols := symbolCount(report.Modules)
	lines := []string{
		fmt.Sprintf("blast radius — %s (%s)", report.Basis, depthLabel(report.Depth)),
		fmt.Sprintf("  changed: %s in %s, %s", plural(len(report.Changed), "file"), plural(len(report.Areas), "area"), plural(len(report.Seeds), "seed symbol")),
		fmt.Sprintf("  impacted: %s in %s", plural(symbols, "symbol"), plural(len(report.Modules), "area")),
		"",
	}
	for _, area := range report.Areas {
		lines = append(lines, fmt.Sprintf("%s %s — %s, %d/%d reached by a test", testGlyph[area.Tests], area.Label, plural(len(area.Files), "changed file"), area.Reached, area.Behavioural))
	}
	if len(report.Areas) > 0 {
		lines = append(lines, "")
	}
	for _, module := range report.Modules {
		lines = append(lines, fmt.Sprintf("%s — %s in %s", module.Label, plural(len(module.Symbols), "symbol"), plural(len(module.Files), "file")))
		for _, symbol := range module.Symbols[:min(len(module.Symbols), 25)] {
			lines = append(lines, fmt.Sprintf("  %s ← %s (%s:%s) [depth %d]", symbol.Relation, symbol.Name, symbol.Path, symbol.Span, symbol.Depth))
		}
		if hidden := len(module.Symbols) - 25; hidden > 0 {
			lines = append(lines, "  …"+plural(hidden, "more symbol"))
		}
		lines = append(lines, "")
	}
	if symbols == 0 {
		lines = append(lines, "no indexed dependents outside the changed files themselves", "")
	}
	if len(report.TestModules) > 0 {
		files := testModuleFiles(report.TestModules)
		verb := "reference"
		if len(files) == 1 {
			verb = "references"
		}
		lines = append(lines, fmt.Sprintf("%s also %s this code (not listed)", plural(len(files), "test suite"), verb), "")
	}
	if report.Reviewers != nil && len(*report.Reviewers) > 0 {
		now := time.Now().UnixMilli()
		lines = append(lines, "who to tag")
		for _, person := range (*report.Reviewers)[:min(len(*report.Reviewers), MaxReviewers)] {
			why := strings.Join(person.Areas, ", ")
			if len(person.Areas) >= 3 {
				why = fmt.Sprintf("%d areas", len(person.Areas))
			}
			lines = append(lines, fmt.Sprintf("  %s — %s · %s, last %s", Mention(person.Owner), why, plural(person.Commits, "commit"), SinceLabel(person.Last, now)))
		}
		lines = append(lines, "")
	}
	for _, line := range caveatLines(report) {
		lines = append(lines, strings.Replace(line, "⚠️ ", "⚠ ", 1), "")
	}
	return trailingNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n")
}

// trimJS trims like String.prototype.trim, which also strips the byte order mark.
func trimJS(text string) string {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}
