package main

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/contextcheck"
	"github.com/NanoNets/context-graph-engine/internal/graph"
)

type checkJSONOutput struct {
	Context contextcheck.Result     `json:"context"`
	Graph   *graph.GraphCheckResult `json:"graph"`
}

func runCheck(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if !opts.rootSet && opts.contextDir == "" {
		defaultContextDir := nearestContextDir(root)
		root = filepath.Dir(defaultContextDir)
		if os.Getenv("GRAFT_DIR") == "" {
			contextDir = defaultContextDir
		}
	}
	workspaceDir := contextDir
	if opts.contextDir == "" {
		workspaceDir = filepath.Join(root, "graft")
	}
	if _, workspace := graph.ReadWorkspaceChildren(workspaceDir); workspace {
		return runWorkspaceCheck(root, workspaceDir, stdout, stderr)
	}
	contextResult, err := contextcheck.Check(root, contextcheck.Options{
		ContextDir: contextDir,
		Extensions: opts.extensions,
	})
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	graphResult, err := graph.CheckGraph(root, contextDir)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	graphFailure := !graphResult.Missing && !graphResult.OK
	contextFailure := !contextResult.Missing && !contextResult.OK
	bothMissing := contextResult.Missing && graphResult.Missing

	if opts.jsonOutput {
		var graphValue *graph.GraphCheckResult
		if !graphResult.Missing {
			graphValue = &graphResult
		}
		data, err := jsonv2.Marshal(checkJSONOutput{Context: contextResult, Graph: graphValue}, jsontext.WithIndent("  "))
		if err != nil {
			writeDiagnostic(stderr, "✗ failed to encode check report: %v\n", err)
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
			return 1
		}
	} else if bothMissing {
		if _, err := fmt.Fprintln(stdout, "graft check: NO GRAPH\n\nNo graft/ graph found. Run `graft build` first."); err != nil {
			return 1
		}
	} else {
		if contextResult.Missing {
			if _, err := fmt.Fprintln(stdout, "deep layer: not built (run `graft build --deep` for concept nodes) — wiring graph is the source of truth"); err != nil {
				return 1
			}
		} else if _, err := fmt.Fprintln(stdout, formatContextCheckReport(contextResult)); err != nil {
			return 1
		}
		if !graphResult.Missing {
			if _, err := fmt.Fprintln(stdout); err != nil {
				return 1
			}
			if _, err := fmt.Fprintln(stdout, graph.FormatGraphCheckReport(graphResult)); err != nil {
				return 1
			}
		}
	}
	if bothMissing || contextFailure || graphFailure {
		return 1
	}
	return 0
}

func runWorkspaceCheck(root, contextDir string, stdout, stderr io.Writer) int {
	workspace := graph.LoadWorkspaceGraphs(root, contextDir)
	lines := []string{fmt.Sprintf("workspace check — %d repo(s)", len(workspace.Loaded)+len(workspace.Missing)), ""}
	ok := true
	for _, child := range workspace.Loaded {
		childRoot := filepath.Join(root, child.Name)
		result, err := graph.CheckGraph(childRoot, filepath.Join(childRoot, "graft"))
		if err != nil {
			writeDiagnostic(stderr, "✗ %s/: %v\n", child.Name, err)
			return 1
		}
		if result.OK {
			lines = append(lines, child.Name+"/: OK")
			continue
		}
		ok = false
		bits := make([]string, 0, 5)
		if len(result.Added) > 0 {
			bits = append(bits, fmt.Sprintf("%d added", len(result.Added)))
		}
		if len(result.Removed) > 0 {
			bits = append(bits, fmt.Sprintf("%d removed", len(result.Removed)))
		}
		if len(result.Changed) > 0 {
			bits = append(bits, fmt.Sprintf("%d changed", len(result.Changed)))
		}
		if len(result.Stale) > 0 {
			bits = append(bits, fmt.Sprintf("%d stale", len(result.Stale)))
		}
		if result.Partial {
			bits = append(bits, "partial extraction")
		}
		status := "STALE"
		if result.Partial {
			status = "PARTIAL"
		}
		lines = append(lines, child.Name+"/: "+status+" ("+strings.Join(bits, ", ")+")")
	}
	for _, child := range workspace.Missing {
		lines = append(lines, child+"/: not built (run graft build)")
	}
	if coverage := workspaceCoverage(workspace); coverage != "" {
		lines = append(lines, "", coverage)
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(lines, "\n")); err != nil {
		return 1
	}
	if !ok {
		return 1
	}
	return 0
}

func workspaceCoverage(workspace graph.WorkspaceGraphs) string {
	if len(workspace.Missing) == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d workspace repos have graphs; run graft build to cover %s",
		len(workspace.Loaded), len(workspace.Loaded)+len(workspace.Missing), strings.Join(workspace.Missing, ", "))
}

func formatContextCheckReport(result contextcheck.Result) string {
	if result.Missing {
		return "graft check: NO GRAPH\n\nNo graft/manifest.json found. Run `graft build --deep` first."
	}
	if result.OK {
		return "graft check: OK — the graph is in sync with the code."
	}
	lines := []string{"graft check: STALE", ""}
	if len(result.ContentDrift) > 0 {
		lines = append(lines, fmt.Sprintf("changed (%d):", len(result.ContentDrift)))
		for _, drift := range result.ContentDrift {
			lines = append(lines, fmt.Sprintf("  ~ %s  (%s → %s)", drift.Path, drift.From, drift.To))
		}
	}
	if len(result.Removed) > 0 {
		lines = append(lines, fmt.Sprintf("removed (%d):", len(result.Removed)))
		for _, path := range result.Removed {
			lines = append(lines, "  - "+path)
		}
	}
	if len(result.Coverage) > 0 {
		lines = append(lines, fmt.Sprintf("not in graph (%d):", len(result.Coverage)))
		for _, path := range result.Coverage {
			lines = append(lines, "  + "+path)
		}
	}
	if len(result.IndexDrift) > 0 {
		lines = append(lines, fmt.Sprintf("index mismatch (%d):", len(result.IndexDrift)))
		for _, problem := range result.IndexDrift {
			lines = append(lines, "  ! "+problem)
		}
	}
	lines = append(lines, "", "Run `graft build --deep` to regenerate, then commit graft/.")
	return strings.Join(lines, "\n")
}
