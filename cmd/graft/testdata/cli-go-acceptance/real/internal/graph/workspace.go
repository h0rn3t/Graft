package graph

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// WorkspaceChild pairs a workspace repository's directory name with its graph.
type WorkspaceChild struct {
	Name  string  // Child repository directory name relative to the workspace root.
	Graph GraphV1 // The child's loaded wiring graph.
}

// WorkspaceGraphs contains loaded child graphs and children without a readable graph.
type WorkspaceGraphs struct {
	// Loaded contains readable graphs in sorted child order.
	Loaded []WorkspaceChild
	// Missing contains listed or discovered children without a readable graph.
	Missing []string
}

// ReadWorkspaceChildren reads a version 1 index from contextDir.
//
// It returns sorted child names and false when the index is missing, oversized, or
// invalid. An empty but valid child list is returned with true.
func ReadWorkspaceChildren(contextDir string) ([]string, bool) {
	indexFS, err := os.OpenRoot(contextDir)
	if err != nil {
		return nil, false
	}
	defer func() { _ = indexFS.Close() }() // The read-only index root has no buffered state to flush.
	data, err := readWorkspaceFile(indexFS, "workspace.json")
	if err != nil {
		return nil, false
	}
	var index struct {
		Version  float64  `json:"version"`
		Children []string `json:"children"`
	}
	// encoding/json v1 matches JSON.parse's last-value behavior for duplicate keys.
	if err := json.Unmarshal(data, &index); err != nil || index.Version != 1 || index.Children == nil {
		return nil, false
	}
	children := slices.Clone(index.Children)
	slices.Sort(children)
	return children, true
}

// LoadWorkspaceGraphs loads sorted child graphs from a workspace index.
//
// An empty contextDir uses <root>/graft for workspace.json. Missing or invalid
// indexes fall back to discovering immediate Git repositories under root. Child
// graphs always use each child's own graft directory; missing or invalid graphs
// are listed in Missing. Child paths stay within root. Workspace indexes and local
// build settings are limited to 1 MiB before decoding.
func LoadWorkspaceGraphs(root, contextDir string) WorkspaceGraphs {
	result := WorkspaceGraphs{
		Loaded:  make([]WorkspaceChild, 0),
		Missing: make([]string, 0),
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return result
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return result
	}
	defer func() { _ = rootFS.Close() }() // The read-only root has no buffered state to flush.

	indexDir := contextDir
	if indexDir == "" {
		indexDir = filepath.Join(root, "graft")
	}
	children, indexed := ReadWorkspaceChildren(indexDir)
	if !indexed {
		children = nil
	}
	if children == nil {
		children = discoverWorkspaceChildren(root, rootFS)
	}
	slices.Sort(children)
	for _, child := range children {
		if child == "" || child == "." || child == ".." || filepath.Base(child) != child || filepath.VolumeName(child) != "" {
			result.Missing = append(result.Missing, child)
			continue
		}
		data, err := rootFS.ReadFile(filepath.Join(child, "graft", GraphDir, GraphFile))
		if err != nil {
			result.Missing = append(result.Missing, child)
			continue
		}
		var graph *GraphV1
		if err := json.Unmarshal(data, &graph); err != nil || graph == nil {
			result.Missing = append(result.Missing, child)
			continue
		}
		result.Loaded = append(result.Loaded, WorkspaceChild{Name: child, Graph: *graph})
	}
	return result
}

// DiscoverWorkspaceChildren lists immediate git repositories under root in
// deterministic order, applying the persisted includeDirs overrides.
func DiscoverWorkspaceChildren(root string) []string {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil
	}
	defer func() { _ = rootFS.Close() }() // The read-only root has no buffered state to flush.
	return discoverWorkspaceChildren(root, rootFS)
}

func discoverWorkspaceChildren(root string, rootFS *os.Root) []string {
	var config struct {
		IncludeDirs []string `json:"includeDirs"`
	}
	if data, err := readWorkspaceFile(rootFS, filepath.Join(".graft", "config.json")); err == nil {
		if json.Unmarshal(data, &config) != nil {
			config.IncludeDirs = nil
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	skipDirs := [...]string{
		"node_modules", "dist", "build", "_build", "out", "target", "vendor",
		"coverage", "__pycache__", "venv",
	}
	children := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if slices.Contains(skipDirs[:], name) && !slices.Contains(config.IncludeDirs, name) {
			continue
		}
		if _, err := rootFS.Stat(filepath.Join(name, ".git")); err == nil {
			children = append(children, name)
		}
	}
	slices.Sort(children)
	return children
}

// WorkspaceBuildChildren reports the child repos to split and whether root is
// a workspace build target. A valid existing workspace index keeps an empty
// workspace recognizable; rebuilding it discovers the currently present repos.
func WorkspaceBuildChildren(root, contextDir string) ([]string, bool) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, false
	}
	if contextDir == "" {
		contextDir = filepath.Join(root, "graft")
	} else if !filepath.IsAbs(contextDir) {
		contextDir = filepath.Join(root, contextDir)
	}
	if _, indexed := ReadWorkspaceChildren(contextDir); indexed {
		return DiscoverWorkspaceChildren(root), true
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		return nil, false
	}
	children := DiscoverWorkspaceChildren(root)
	return children, len(children) >= 2
}

// WriteWorkspace atomically writes a sorted version-one workspace index.
func WriteWorkspace(outDir string, children []string) error {
	type workspaceIndex struct {
		Version  int      `json:"version"`
		Children []string `json:"children"`
	}
	sorted := slices.Clone(children)
	if sorted == nil {
		sorted = make([]string, 0)
	}
	slices.Sort(sorted)
	data, err := jsonv2.Marshal(workspaceIndex{Version: 1, Children: sorted}, jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encode workspace index: %w", err)
	}
	if err := writeAtomicSidecar(filepath.Join(outDir, "workspace.json"), append(data, '\n')); err != nil {
		return fmt.Errorf("write workspace index: %w", err)
	}
	return nil
}

// ClearWorkspaceParent removes parent graph artifacts while preserving the
// workspace index just written to contextDir.
func ClearWorkspaceParent(contextDir string) error {
	root, err := os.OpenRoot(contextDir)
	if err != nil {
		return fmt.Errorf("open workspace context directory: %w", err)
	}
	defer func() { _ = root.Close() }() // The directory root has no buffered state to flush.
	entries, err := os.ReadDir(contextDir)
	if err != nil {
		return fmt.Errorf("read workspace context directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == "workspace.json" {
			continue
		}
		if err := root.RemoveAll(entry.Name()); err != nil {
			return fmt.Errorf("remove parent graph artifact %q: %w", entry.Name(), err)
		}
	}
	return nil
}

// FederateMap formats a repository map for each loaded child and reports missing graphs.
// A non-zero MaxDirs budget is divided across the loaded children, with at least one
// directory retained per child.
func FederateMap(root, contextDir string, options RepoMapOptions) string {
	workspace := LoadWorkspaceGraphs(root, contextDir)
	childOptions := options
	if childOptions.MaxDirs != 0 && len(workspace.Loaded) > 0 {
		childOptions.MaxDirs = max(1, childOptions.MaxDirs/len(workspace.Loaded))
	}
	sections := make([]string, 0, len(workspace.Loaded))
	for _, child := range workspace.Loaded {
		body := strings.TrimSpace(FormatRepoMap(BuildRepoMap(child.Graph, childOptions)))
		sections = append(sections, "## "+child.Name+"/\n"+body)
	}
	parts := []string{
		fmt.Sprintf("workspace map — %d repo(s)", len(workspace.Loaded)),
		"",
		strings.Join(sections, "\n\n"),
	}
	if len(workspace.Missing) > 0 {
		parts = append(parts, "", fmt.Sprintf(
			"%d of %d workspace repos have graphs; run graft build to cover %s",
			len(workspace.Loaded), len(workspace.Loaded)+len(workspace.Missing), strings.Join(workspace.Missing, ", "),
		))
	}
	return strings.Join(parts, "\n") + "\n"
}

// readWorkspaceFile reads one bounded metadata file through root.
func readWorkspaceFile(root *os.Root, name string) ([]byte, error) {
	const maxBytes = 1 << 20
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("workspace metadata exceeds %d bytes", maxBytes)
	}
	return data, nil
}
