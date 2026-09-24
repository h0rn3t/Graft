package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
)

func runWorkspaceBuild(opts callersOptions, root, contextDir string, children []string, stdout, stderr io.Writer) int {
	if _, err := os.Stat(graph.WiringPath(contextDir)); err == nil {
		dirs := make([]string, 0, len(children))
		for _, child := range children {
			dirs = append(dirs, child+"/graft/")
		}
		writeDiagnostic(stderr,
			"⚠ this folder contains %d separate git repos — splitting: each repo now gets its own committable graft/ (%s); the combined graph here is replaced by a workspace index. Queries from here now search all repos, fairly.\n",
			len(children), strings.Join(dirs, ", "),
		)
	}
	writeDiagnostic(stderr, "building %d workspace repos: %s\n", len(children), strings.Join(children, ", "))
	for _, child := range children {
		childOptions := opts
		childOptions.root = filepath.Join(root, child)
		childOptions.rootSet = true
		childOptions.contextDir = ""
		childOptions.workspaceChildName = child
		if status := runBuild(childOptions, stdout, stderr); status != 0 {
			return status
		}
	}
	if err := graph.WriteWorkspace(contextDir, children); err != nil {
		writeDiagnostic(stderr, "✗ workspace index write failed: %v\n", err)
		return 1
	}
	if err := graph.ClearWorkspaceParent(contextDir); err != nil {
		writeDiagnostic(stderr, "✗ workspace parent cleanup failed: %v\n", err)
		return 1
	}
	ensureBuildIgnoreFiles(root, contextDir, opts.noGitignore, true)
	if _, err := fmt.Fprintf(stdout, "✓ workspace: %d repos federated → graft/workspace.json\n", len(children)); err != nil {
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "  graft/ is git-ignored — each teammate runs `graft build` to regenerate it locally."); err != nil {
		return 1
	}
	return 0
}
