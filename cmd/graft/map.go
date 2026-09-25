package main

import (
	"fmt"
	"io"
	"strconv"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
)

func runMap(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts, queryPathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	maxDirs, err := mapMaxDirs(opts.maxDirs)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	refreshBeforeQuery(root, contextDir, opts, stderr)
	if _, ok := graph.ReadWorkspaceChildren(contextDir); ok {
		// The TypeScript workspace path renders text even when --json is set.
		if _, err := io.WriteString(stdout, graph.FederateMap(root, contextDir, graph.RepoMapOptions{MaxDirs: maxDirs})); err != nil {
			return 1
		}
		return 0
	}
	loaded, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		writeDiagnostic(stderr, "✗ no graph — run graft build first\n")
		return 1
	}
	result := graph.BuildRepoMap(*loaded, graph.RepoMapOptions{MaxDirs: maxDirs})
	if opts.jsonOutput {
		data, err := jsonjs.Marshal(result, "  ")
		if err != nil {
			writeDiagnostic(stderr, "✗ failed to encode map result: %v\n", err)
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
			return 1
		}
		return 0
	}
	text := graph.FormatRepoMap(result)
	if _, err := io.WriteString(stdout, text); err != nil {
		return 1
	}
	if result.Saved != nil {
		recordQuerySavings(contextDir, len(text), result.Saved.BaselineChars)
	}
	return 0
}

func mapMaxDirs(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("--max-dirs must be a positive integer, got %q", raw)
	}
	return value, nil
}
