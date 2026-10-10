package main

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/savings"
)

func runSkeleton(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts, queryPathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	refreshBeforeQuery(root, contextDir, opts, stderr)
	loaded, err := graph.Read(graph.WiringPath(contextDir))
	result := graph.SkeletonResult{
		File:    opts.query,
		Entries: make([]graph.SkeletonEntry, 0),
		Note:    "no wiring graph — run `graft build` first",
	}
	if err == nil {
		result = graph.Skeleton(*loaded, opts.query)
	}
	if opts.jsonOutput {
		return writeSkeletonJSON(stdout, stderr, result)
	}
	out := &countingWriter{Writer: stdout}
	code := writeSkeletonHuman(out, result)
	if result.Saved != nil {
		recordQuerySavings(contextDir, out.n, result.Saved.BaselineChars)
	}
	return code
}

func writeSkeletonJSON(stdout, stderr io.Writer, result graph.SkeletonResult) int {
	data, err := jsonjs.Marshal(result, "  ")
	if err != nil {
		writeDiagnostic(stderr, "✗ failed to encode skeleton result: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
		return 1
	}
	return 0
}

func writeSkeletonHuman(stdout io.Writer, result graph.SkeletonResult) int {
	head := "graft skeleton — " + result.File
	if len(result.Entries) == 0 {
		_, err := fmt.Fprintf(stdout, "%s\n\n%s\n", head, result.Note)
		if err != nil {
			return 1
		}
		return 0
	}
	lines := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		// A field follows its struct, one level in.
		bullet := "- "
		if entry.Kind == "field" {
			bullet = "  - "
		}
		line := fmt.Sprintf("%s%s  %s %s", bullet, entry.Span, entry.Kind, entry.Name)
		if entry.Signature != nil {
			line = bullet + entry.Span + " " + *entry.Signature
		}
		if entry.Summary != nil {
			line += " — " + *entry.Summary
		}
		lines = append(lines, line)
	}
	body := head + "\n" + strings.Join(lines, "\n")
	_, err := io.WriteString(stdout, body+"\n")
	if err != nil {
		return 1
	}
	return 0
}

// fitSkeletonText renders a skeleton in at most ceiling characters. Past it
// the docs go first, then the signatures, then the definitions that still do
// not fit, the first of them named so the next call can read one by name.
func fitSkeletonText(result graph.SkeletonResult, ceiling int) string {
	render := func(result graph.SkeletonResult) string {
		var text strings.Builder
		writeSkeletonHuman(&text, result)
		return text.String()
	}
	text := render(result)
	if savings.Length(text) <= ceiling {
		return text
	}
	coarse := result
	coarse.Entries = slices.Clone(result.Entries)
	for _, left := range []string{"docs", "signatures and docs"} {
		for i := range coarse.Entries {
			coarse.Entries[i].Summary = nil
			if left != "docs" {
				coarse.Entries[i].Signature = nil
			}
		}
		// The head line says what this coarser view leaves out.
		coarse.File = fmt.Sprintf("%s (%s left out to fit one answer)", result.File, left)
		if text = render(coarse); savings.Length(text) <= ceiling {
			return text
		}
	}
	entries := coarse.Entries
	cut := func(kept int) string {
		coarse.Entries = entries[:kept]
		rest := entries[kept:]
		names := make([]string, 0, 7)
		for _, entry := range rest[:min(len(rest), 6)] {
			names = append(names, entry.Name)
		}
		if len(rest) > 6 {
			names = append(names, fmt.Sprintf("+%d more", len(rest)-6))
		}
		from, _, _ := spanLines(rest[0].Span)
		return render(coarse) + fmt.Sprintf("⋮ +%d more definitions from L%d: %s — graft_read_symbol reads one by name\n", len(rest), from, strings.Join(names, ", "))
	}
	// Even the names alone run past the ceiling, so at least one is cut.
	kept := sort.Search(len(entries)-1, func(i int) bool { return savings.Length(cut(i+1)) > ceiling })
	return cut(kept)
}
