package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func runGrep(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts, queryPathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	refreshBeforeQuery(root, contextDir, opts, stderr)
	if _, workspace := graph.ReadWorkspaceChildren(contextDir); workspace {
		return runWorkspaceGrep(root, contextDir, opts, stdout, stderr)
	}
	loaded, err := opts.queryCache.loadGraph(contextDir)
	if err != nil {
		writeDiagnostic(stderr, "✗ no graph — run graft build first\n")
		return 1
	}
	result, err := graph.Grep(*loaded, root, opts.query, graph.GrepOptions{
		IgnoreCase: opts.ignoreCase,
		Fixed:      opts.fixed,
		In:         opts.in,
	})
	if err != nil {
		if message, ok := grepSyntaxMessage(err, opts.query, opts.ignoreCase); ok {
			writeDiagnostic(stderr, "✗ invalid pattern \"%s\": %s\n", opts.query, message)
		} else {
			writeDiagnostic(stderr, "✗ %v\n", err)
		}
		return 1
	}
	if opts.jsonOutput {
		return writeGrepJSON(stdout, stderr, result)
	}
	if result.TotalHits == 0 {
		writeDiagnostic(stderr, "%s\n", grepZeroHitNote(result))
		return 0
	}
	text := formatGrepResult(result)
	if _, err := io.WriteString(stdout, text); err != nil {
		return 1
	}
	if result.Saved != nil {
		recordQuerySavings(contextDir, len(text), result.Saved.BaselineChars)
	}
	return 0
}

// grepSyntaxMessage is JavaScript's SyntaxError message for a pattern Go's
// regexp rejects, or false for any other failure.
func grepSyntaxMessage(err error, pattern string, ignoreCase bool) (string, bool) {
	patternErr, ok := errors.AsType[*graph.GrepPatternError](err)
	if !ok {
		return "", false
	}
	message := patternErr.Error()
	switch {
	case strings.Contains(message, "missing closing ]"):
		message = "Unterminated character class"
	case strings.Contains(message, "missing closing )"):
		message = "Unterminated group"
	case strings.Contains(message, "missing argument to repetition operator"):
		message = "Nothing to repeat"
	}
	flag := ""
	if ignoreCase {
		flag = "i"
	}
	return fmt.Sprintf("Invalid regular expression: /%s/%s: %s", pattern, flag, message), true
}

// runWorkspaceGrep greps every child of a workspace, as the TypeScript
// runWorkspaceGrep does; --in does not apply there.
func runWorkspaceGrep(root, contextDir string, opts callersOptions, stdout, stderr io.Writer) int {
	result, coverage, err := federateGrep(root, contextDir, opts.query, opts.ignoreCase, opts.fixed, opts.in)
	if err != nil {
		// Thrown in TypeScript, so the top-level handler prints the bare message.
		if message, ok := grepSyntaxMessage(err, opts.query, opts.ignoreCase); ok {
			writeDiagnostic(stderr, "%s\n", message)
		} else {
			writeDiagnostic(stderr, "%v\n", err)
		}
		return 1
	}
	if opts.jsonOutput {
		return writeGrepJSON(stdout, stderr, result)
	}
	if result.TotalHits == 0 {
		note := grepZeroHitNote(result)
		if coverage != "" {
			note += "\n" + coverage
		}
		writeDiagnostic(stderr, "%s\n", note)
		return 0
	}
	text := formatGrepResult(result)
	if coverage != "" {
		text += coverage + "\n"
	}
	if _, err := io.WriteString(stdout, text); err != nil {
		return 1
	}
	return 0
}

func writeGrepJSON(stdout, stderr io.Writer, result graph.GrepResult) int {
	data, err := jsonjs.Marshal(result, "  ")
	if err != nil {
		writeDiagnostic(stderr, "✗ failed to encode grep result: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
		return 1
	}
	return 0
}

func formatGrepResult(result graph.GrepResult) string {
	head := grepHeader(result)
	if note := grepTruncationNote(result); note != "" {
		head += "\n" + note
	}
	var output strings.Builder
	output.WriteString(head)
	output.WriteString("\n\n")
	for index, group := range result.Groups {
		if index > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(grepGroupHeader(group))
		for _, hit := range group.Hits {
			fmt.Fprintf(&output, "\n  L%d: %s", hit.Line, hit.Text)
		}
		output.WriteByte('\n')
	}
	return strings.TrimRight(output.String(), "\n") + "\n"
}

// inlineGrepSource renders a narrow search with the source around its hits,
// or returns "" when that adds nothing within budget bytes. Hits that all lie
// in one small file show that file once, whole, after one line per group;
// otherwise, when at most three definitions hold the hits, each comes whole,
// shortest first, while the answer fits: a long one taken first would crowd
// out the rest, which the agent then reads in another round. Matching lines
// are marked ▸.
func inlineGrepSource(root string, result graph.GrepResult, budget int) string {
	const maxDefinitions = 3
	if len(result.Groups) == 0 {
		return ""
	}
	head := grepHeader(result)
	if note := grepTruncationNote(result); note != "" {
		head += "\n" + note
	}
	read := func(path string) []string {
		data, readable, err := sourcefiles.Read(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || !readable {
			return nil
		}
		return strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	}
	marked := func(lines []string, from, to int, groups ...graph.GrepGroup) string {
		matches := make(map[int]bool)
		for _, group := range groups {
			for _, hit := range group.Hits {
				matches[hit.Line] = true
			}
		}
		var out strings.Builder
		for number := from; number <= min(to, len(lines)); number++ {
			mark := "  "
			if matches[number] {
				mark = "▸ "
			}
			fmt.Fprintf(&out, "\n%sL%d: %s", mark, number, lines[number-1])
		}
		return out.String()
	}

	path := result.Groups[0].Path
	if len(result.Groups) > 1 && !slices.ContainsFunc(result.Groups, func(group graph.GrepGroup) bool { return group.Path != path }) {
		if lines := read(path); lines != nil && len(lines) <= askWholeFileLines {
			var text strings.Builder
			text.WriteString(head + "\n\n")
			for _, group := range result.Groups {
				at := make([]string, len(group.Hits))
				for i, hit := range group.Hits {
					at[i] = fmt.Sprintf("L%d", hit.Line)
				}
				fmt.Fprintf(&text, "%s · %s\n", grepGroupHeader(group), strings.Join(at, ", "))
			}
			fmt.Fprintf(&text, "\nwhole file %s · %d lines · matches marked ▸%s\n", path, len(lines), marked(lines, 1, len(lines), result.Groups...))
			if text.Len() <= budget {
				return text.String()
			}
		}
	}
	type definition struct{ group, from, to int }
	var definitions []definition
	for i, group := range result.Groups {
		if group.Symbol == nil {
			continue
		}
		if _, from, to, ok := parseAskPointer(group.Symbol.Path + ":" + group.Symbol.Span); ok {
			definitions = append(definitions, definition{group: i, from: from, to: to})
		}
	}
	if len(definitions) == 0 || len(definitions) > maxDefinitions {
		return ""
	}
	slices.SortStableFunc(definitions, func(a, b definition) int { return cmp.Compare(a.to-a.from, b.to-b.from) })
	sections := make([]string, len(result.Groups))
	for i, group := range result.Groups {
		var section strings.Builder
		section.WriteString(grepGroupHeader(group))
		for _, hit := range group.Hits {
			fmt.Fprintf(&section, "\n  L%d: %s", hit.Line, hit.Text)
		}
		sections[i] = section.String()
	}
	render := func() string { return head + "\n\n" + strings.Join(sections, "\n\n") + "\n" }
	inlined := false
	for _, definition := range definitions {
		group := result.Groups[definition.group]
		lines := read(group.Symbol.Path)
		if lines == nil {
			continue
		}
		hitLines := sections[definition.group]
		sections[definition.group] = grepGroupHeader(group) + marked(lines, definition.from, definition.to, group)
		if len(render()) > budget {
			sections[definition.group] = hitLines
			continue
		}
		inlined = true
	}
	if !inlined {
		return ""
	}
	return render()
}

// grepCopyPattern marks a pattern that itself asks for tests or copies.
var grepCopyPattern = regexp.MustCompile(`(?i)test|spec|fixture|mock|vendor|generated`)

// fitGrepResult keeps the top-ranked hits whose rendered groups fit in budget
// bytes and counts the rest as truncated, leaving result itself untouched.
// While production code matches, tests and copies (testdata, fixtures,
// generated and vendored code) are left for grepRemainderNote to name, unless
// the pattern asks for them; they are not past the cap, and a truncation note
// would send the agent to narrow the search for them.
func fitGrepResult(result graph.GrepResult, budget int) graph.GrepResult {
	isCopy := func(group graph.GrepGroup) bool { return group.Generated || graph.IsCopyPath(group.Path) }
	production := slices.DeleteFunc(slices.Clone(result.Groups), isCopy)
	if len(production) > 0 && len(production) < len(result.Groups) && !grepCopyPattern.MatchString(result.Pattern) {
		for _, group := range result.Groups {
			if isCopy(group) {
				result.TotalHits -= len(group.Hits)
			}
		}
		result.Groups = production
	}
	used := 0
	for index, group := range result.Groups {
		used += len(grepGroupHeader(group)) + 2
		for kept, hit := range group.Hits {
			used += len(fmt.Sprintf("\n  L%d: %s", hit.Line, hit.Text))
			if used <= budget {
				continue
			}
			dropped := len(group.Hits) - kept
			for _, rest := range result.Groups[index+1:] {
				dropped += len(rest.Hits)
			}
			groups := slices.Clone(result.Groups[:index+1])
			groups[index].Hits = group.Hits[:kept]
			if kept == 0 {
				groups = groups[:index]
			}
			result.Groups = groups
			result.TotalHits -= dropped
			result.Truncated.Hits += dropped
			return result
		}
	}
	return result
}

func grepHeader(result graph.GrepResult) string {
	files := make(map[string]struct{}, len(result.Groups))
	for _, group := range result.Groups {
		files[group.Path] = struct{}{}
	}
	return fmt.Sprintf("\"%s\" — %d hits in %d symbols across %d files (searched %d indexed files)", result.Pattern, result.TotalHits, len(result.Groups), len(files), result.FilesSearched)
}

func grepGroupHeader(group graph.GrepGroup) string {
	if group.Symbol != nil {
		return fmt.Sprintf("%s · %s · %s:%s · %d in-edges", group.Symbol.Name, group.Symbol.Kind, group.Symbol.Path, group.Symbol.Span, group.InDegree)
	}
	return fmt.Sprintf("%s (module level) · %d in-edges", group.Path, group.InDegree)
}

func grepTruncationNote(result graph.GrepResult) string {
	if result.Truncated.Files == 0 && result.Truncated.Hits == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if result.Truncated.Hits > 0 {
		parts = append(parts, fmt.Sprintf("%d more hit%s beyond the cap", result.Truncated.Hits, pluralSuffix(result.Truncated.Hits)))
	}
	if result.Truncated.Files > 0 {
		parts = append(parts, fmt.Sprintf("%d indexed file%s unreadable", result.Truncated.Files, pluralSuffix(result.Truncated.Files)))
	}
	return fmt.Sprintf("(truncated: %s — narrow with --in or refine the pattern)", strings.Join(parts, ", "))
}

// grepRemainderNote names the files holding the hits fitGrepResult dropped,
// most hits first, so a capped answer still says where to narrow. A file whose
// dropped hits lie in one or two symbols names them too, a definition to read
// by name; a file spread over more is only counted, since a few of many names
// would be noise.
func grepRemainderNote(full, fitted graph.GrepResult) string {
	key := func(group graph.GrepGroup) string {
		if group.Symbol == nil {
			return group.Path
		}
		return group.Path + "\x00" + group.Symbol.Name + "\x00" + group.Symbol.Span
	}
	kept := make(map[string]int)
	for _, group := range fitted.Groups {
		kept[key(group)] += len(group.Hits)
	}
	dropped := make(map[string]int)
	names := make(map[string][]string)
	generated := make(map[string]bool)
	for _, group := range full.Groups {
		rest := len(group.Hits) - kept[key(group)]
		if rest <= 0 {
			continue
		}
		dropped[group.Path] += rest
		generated[group.Path] = group.Generated
		if group.Symbol != nil {
			names[group.Path] = append(names[group.Path], group.Symbol.Name)
		}
	}
	if len(dropped) == 0 {
		return ""
	}
	copyRank := func(path string) int {
		if generated[path] || graph.IsCopyPath(path) {
			return 1
		}
		return 0
	}
	// Production files first, so a test tree's tally cannot hide them.
	paths := slices.SortedFunc(maps.Keys(dropped), func(a, b string) int {
		return cmp.Or(cmp.Compare(copyRank(a), copyRank(b)), cmp.Compare(dropped[b], dropped[a]), strings.Compare(a, b))
	})
	const shown, named = 8, 2
	parts := make([]string, 0, shown+1)
	for _, path := range paths[:min(shown, len(paths))] {
		part := fmt.Sprintf("%s (%d", path, dropped[path])
		if in := names[path]; len(in) > 0 && len(in) <= named {
			part += " in " + strings.Join(in, ", ")
		}
		parts = append(parts, part+")")
	}
	if len(paths) > shown {
		parts = append(parts, fmt.Sprintf("%d more files", len(paths)-shown))
	}
	return "more hits in: " + strings.Join(parts, ", ") + " — narrow with in: one of these files or a tighter pattern"
}

func grepZeroHitNote(result graph.GrepResult) string {
	note := fmt.Sprintf("no hits for \"%s\" in %d indexed files. The pattern may be too specific — retry graft grep with a bare symbol name or short substring (drop the receiver, full signature, and regex anchors). All indexed code was searched; use raw grep -rn only for genuinely unindexed files (docs, configs, brand-new files)", result.Pattern, result.FilesSearched)
	if result.Truncated.Files == 0 {
		return note
	}
	return fmt.Sprintf("%s — note: %d indexed file%s could not be read (stale graph? run graft build)", note, result.Truncated.Files, pluralSuffix(result.Truncated.Files))
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
