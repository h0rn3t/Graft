package main

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/NanoNets/context-graph-engine/internal/blast"
	"github.com/NanoNets/context-graph-engine/internal/graph"
)

const blastDefaultDepth = 2

func runBlast(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, shownDir, err := blastPaths(opts, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQuery(root)
	refreshBeforeQuery(root, contextDir, opts, stderr)

	format := opts.format
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "markdown" && format != "mermaid" && format != "json" {
		writeDiagnostic(stderr, "✗ --format must be text, markdown, mermaid or json, got \"%s\"\n", format)
		return 1
	}
	depth := blastDefaultDepth
	if opts.depth != "" {
		if depth, err = resolveDepth(opts.depth); err != nil {
			writeDiagnostic(stderr, "✗ %v\n", err)
			return 1
		}
	}

	loaded, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		writeDiagnostic(stderr, "✗ no graph found at %s — run `graft build` first\n", shownDir)
		return 1
	}
	if opts.base != nil && !blast.RefExists(root, *opts.base) {
		writeDiagnostic(stderr, "✗ base ref \"%s\" is not in this checkout.\n  In CI, fetch enough history for the merge base: actions/checkout with `fetch-depth: 0`.\n", *opts.base)
		return 1
	}
	diff, ok := blast.ChangedFiles(root, opts.base)
	if !ok {
		writeDiagnostic(stderr, "✗ could not read a diff in %s — is this a git repository?\n", root)
		return 1
	}

	report := blast.Radius(*loaded, diff.Files, diff.Basis, depth)
	if opts.name {
		note, err := blast.NameReport(context.Background(), *loaded, report, contextDir)
		if err != nil {
			writeDiagnostic(stderr, "✗ %v\n", err)
			return 1
		}
		if note != "" {
			writeDiagnostic(stderr, "• --name: %s\n", note)
		}
	}
	if !opts.noOwners {
		// With no --base there is no commit range, so the local identity stands in
		// for the author the suggestions must leave out.
		authors := blast.LocalIdentity(root)
		if opts.base != nil {
			authors = blast.DiffAuthors(root, *opts.base)
		}
		blast.AttachOwners(root, report, blast.OwnerOptions{Exclude: append(authors, opts.prAuthors...)})
	}

	var output string
	switch format {
	case "json":
		data, err := jsonv2.Marshal(report, jsontext.WithIndent("  "))
		if err != nil {
			writeDiagnostic(stderr, "✗ failed to encode blast report: %v\n", err)
			return 1
		}
		output = string(data) + "\n"
	case "mermaid":
		diagram, ok := blast.MermaidDiagram(report)
		if !ok {
			diagram = "%% no dependents to draw"
		}
		output = diagram + "\n"
	case "markdown":
		output = blast.MarkdownReport(report, root)
	default:
		output = blast.TextReport(report)
	}
	if _, err := io.WriteString(stdout, output); err != nil {
		return 1
	}
	return 0
}

// blastPaths resolves the root and context directory the way the TypeScript
// queryRoot and contextDirFor do: an explicit dir is taken as given, otherwise
// the nearest indexed ancestor answers, and --dir replaces <root>/graft
// verbatim. shownDir is the context directory as the diagnostics print it.
func blastPaths(opts callersOptions, stderr io.Writer) (string, string, string, error) {
	root := opts.root
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", "", fmt.Errorf("failed to resolve working directory: %w", err)
		}
		root = nearestGraftRoot(cwd, opts.contextDir)
		if root != cwd {
			writeDiagnostic(stderr, "[graft] no graft/ here — answering from %s/graft\n", root)
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve repository root %q: %w", opts.root, err)
	}
	if opts.contextDir == "" {
		contextDir := filepath.Join(root, "graft")
		return root, contextDir, contextDir, nil
	}
	contextDir, err := filepath.Abs(opts.contextDir)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve context directory %q: %w", opts.contextDir, err)
	}
	return root, contextDir, opts.contextDir, nil
}

// nearestGraftRoot returns the nearest ancestor of start holding a wiring graph
// or a workspace index, or start itself. A --dir override skips the walk.
func nearestGraftRoot(start, override string) string {
	if override != "" {
		return start
	}
	for dir := start; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(graph.WiringPath(filepath.Join(dir, "graft"))); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "graft", "workspace.json")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return start
		}
	}
}
