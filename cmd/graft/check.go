package main

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
)

type checkJSONOutput struct {
	Graph *graph.GraphCheckResult `json:"graph"`
}

func runCheck(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts, enginePathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	workspaceDir := graphDir(root, opts, false)
	if _, workspace := graph.ReadWorkspaceChildren(workspaceDir); workspace {
		return runWorkspaceCheck(root, workspaceDir, stdout, stderr)
	}
	graphResult, err := graph.CheckGraph(root, contextDir)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}

	if opts.jsonOutput {
		var graphValue *graph.GraphCheckResult
		if !graphResult.Missing {
			graphValue = &graphResult
		}
		data, err := jsonv2.Marshal(checkJSONOutput{Graph: graphValue}, jsontext.WithIndent("  "))
		if err != nil {
			writeDiagnostic(stderr, "✗ failed to encode check report: %v\n", err)
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
			return 1
		}
	} else if _, err := fmt.Fprintln(stdout, graph.FormatGraphCheckReport(graphResult)); err != nil {
		return 1
	}
	if graphResult.Missing || !graphResult.OK {
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
