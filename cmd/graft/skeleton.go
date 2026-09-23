package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
	"github.com/NanoNets/context-graph-engine/internal/savings"
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
	return writeSkeletonHuman(stdout, result)
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
		line := fmt.Sprintf("- %s  %s %s", entry.Span, entry.Kind, entry.Name)
		if entry.Signature != nil {
			line += "  " + *entry.Signature
		}
		if entry.Summary != nil {
			line += " — " + *entry.Summary
		}
		lines = append(lines, line)
	}
	body := head + "\n" + strings.Join(lines, "\n")
	if result.Saved != nil {
		body = savings.With(body, result.Saved.Files, result.Saved.BaselineChars)
	}
	_, err := io.WriteString(stdout, body+"\n")
	if err != nil {
		return 1
	}
	return 0
}
