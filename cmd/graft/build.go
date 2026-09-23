package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

func runBuild(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}

	var onlyDirs []string
	for _, dir := range opts.onlyDirs {
		dir = strings.ReplaceAll(dir, "\\", "/")
		dir = strings.Trim(filepath.ToSlash(filepath.Clean(dir)), "/")
		if dir == "" || dir == "." || !filepath.IsLocal(filepath.FromSlash(dir)) {
			writeDiagnostic(stderr, "✗ --only-dir: expected a non-empty repo-relative path\n")
			return 1
		}
		onlyDirs = append(onlyDirs, dir)
	}

	built, err := graph.BuildGraph(root, sourcefiles.Options{OutDir: contextDir, OnlyDirs: onlyDirs})
	if err != nil {
		writeDiagnostic(stderr, "✗ graph build failed: %v\n", err)
		return 1
	}
	if len(built.Unsupported) > 0 || len(built.Errors) > 0 {
		if len(built.Unsupported) > 0 {
			writeDiagnostic(stderr, "✗ Go build skipped: %d source file(s) use unsupported language adapters; the existing graph was not changed\n", len(built.Unsupported))
			writeDiagnostic(stderr, "  example: %s\n", built.Unsupported[0])
		}
		if len(built.Errors) > 0 {
			writeDiagnostic(stderr, "✗ Go build skipped: %d source file(s) could not be parsed or read; the existing graph was not changed\n", len(built.Errors))
			writeDiagnostic(stderr, "  example: %s\n", built.Errors[0])
		}
		return 1
	}
	if _, err := graph.Write(built.Graph, contextDir); err != nil {
		writeDiagnostic(stderr, "✗ graph write failed: %v\n", err)
		return 1
	}
	if err := graph.WriteAskIndex(contextDir, built.Graph); err != nil {
		writeDiagnostic(stderr, "✗ ask index write failed: %v\n", err)
		return 1
	}
	if err := graph.WriteFingerprint(contextDir, graph.ExtractorID, built.Fingerprints, built.OnlyDirs); err != nil {
		writeDiagnostic(stderr, "✗ fingerprint write failed: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(
		stdout,
		"✓ wiring: %d nodes, %d edges\n  parsed: %d of %d files (%d reused from cache)\n  → %s\n",
		len(built.Graph.Nodes), len(built.Graph.Edges), built.Parsed,
		len(built.Fingerprints), built.Reused, contextDir,
	); err != nil {
		return 1
	}
	for _, limitation := range built.Limitations {
		writeDiagnostic(stderr, "⚠ native extractor limitation: %s\n", limitation)
	}
	writeDiagnostic(stderr, "⚠ structural Go build only; Markdown cards, Git ignore updates, and LLM summaries are not migrated yet\n")
	return 0
}
