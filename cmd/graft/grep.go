package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
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

// fitGrepResult keeps the top-ranked hits whose rendered groups fit in budget
// bytes and counts the rest as truncated, leaving result itself untouched.
func fitGrepResult(result graph.GrepResult, budget int) graph.GrepResult {
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
