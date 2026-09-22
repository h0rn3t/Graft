package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

func runGrep(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	loaded, err := graph.Read(graph.WiringPath(contextDir))
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
		if patternErr, ok := errors.AsType[*graph.GrepPatternError](err); ok {
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
			if opts.ignoreCase {
				flag = "i"
			}
			writeDiagnostic(stderr, "✗ invalid pattern %q: Invalid regular expression: /%s/%s: %s\n", opts.query, opts.query, flag, message)
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
	if _, err := io.WriteString(stdout, formatGrepResult(result)); err != nil {
		return 1
	}
	return 0
}

func writeGrepJSON(stdout, stderr io.Writer, result graph.GrepResult) int {
	data, err := json.MarshalIndent(result, "", "  ")
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
