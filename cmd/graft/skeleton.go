package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

func runSkeleton(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
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
	data, err := json.MarshalIndent(result, "", "  ")
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
	_, err := io.WriteString(stdout, head+"\n"+strings.Join(lines, "\n")+"\n")
	if err != nil {
		return 1
	}
	return 0
}
