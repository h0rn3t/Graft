package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

func runMap(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
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
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			writeDiagnostic(stderr, "✗ failed to encode map result: %v\n", err)
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
			return 1
		}
		return 0
	}
	if _, err := io.WriteString(stdout, graph.FormatRepoMap(result)); err != nil {
		return 1
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
