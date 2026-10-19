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

	"github.com/h0rn3t/Graft/internal/fsutil"
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

// Graphs returns the loaded children's graphs in child order.
func (workspace WorkspaceGraphs) Graphs() []GraphV1 {
	graphs := make([]GraphV1, 0, len(workspace.Loaded))
	for _, child := range workspace.Loaded {
		graphs = append(graphs, child.Graph)
	}
	return graphs
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
// build settings are limited to 1 MiB before decoding. A non-nil read loads each
// child's graph from its graft directory, so a caller can share graphs it keeps;
// nil decodes every graph afresh.
func LoadWorkspaceGraphs(root, contextDir string, read func(contextDir string) (*GraphV1, error)) WorkspaceGraphs {
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
		if !validWorkspaceChild(child) {
			result.Missing = append(result.Missing, child)
			continue
		}
		graphPath := filepath.Join(child, "graft", GraphDir, GraphFile)
		var graph *GraphV1
		if read == nil {
			data, err := rootFS.ReadFile(graphPath)
			if err != nil {
				result.Missing = append(result.Missing, child)
				continue
			}
			if err := json.Unmarshal(data, &graph); err != nil || graph == nil {
				result.Missing = append(result.Missing, child)
				continue
			}
		} else {
			// The root resolves the path first, so read never follows a link out of it.
			if _, err := rootFS.Stat(graphPath); err != nil {
				result.Missing = append(result.Missing, child)
				continue
			}
			if graph, err = read(filepath.Join(root, child, "graft")); err != nil {
				result.Missing = append(result.Missing, child)
				continue
			}
		}
		result.Loaded = append(result.Loaded, WorkspaceChild{Name: child, Graph: *graph})
	}
	return result
}

// WorkspaceChildSize is the size of a workspace repository's graph.
type WorkspaceChildSize struct {
	Name      string // Child repository directory name relative to the workspace root.
	Built     bool   // Whether the child has a readable graph with a meta object.
	NodeCount int
	EdgeCount int
}

// MeasureWorkspace returns the graph size of each child listed in the
// workspace index in contextDir, in sorted child order. It returns false when
// contextDir holds no valid index or root cannot be opened.
//
// Each graph is decoded only up to its meta object, which Write puts first, so
// measuring dozens of repositories stays cheap enough for a statusline. Child
// paths stay within root.
func MeasureWorkspace(root, contextDir string) ([]WorkspaceChildSize, bool) {
	children, indexed := ReadWorkspaceChildren(contextDir)
	if !indexed {
		return nil, false
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, false
	}
	defer func() { _ = rootFS.Close() }() // The read-only root has no buffered state to flush.
	sizes := make([]WorkspaceChildSize, 0, len(children))
	for _, child := range children {
		size := WorkspaceChildSize{Name: child}
		if validWorkspaceChild(child) {
			nodeCount, edgeCount, err := readGraphMetaSize(rootFS, filepath.Join(child, "graft", GraphDir, GraphFile))
			size.Built, size.NodeCount, size.EdgeCount = err == nil, nodeCount, edgeCount
		}
		sizes = append(sizes, size)
	}
	return sizes, true
}

// readGraphMetaSize decodes the graph at path under root only as far as its
// meta object, and returns the node and edge counts the meta records.
func readGraphMetaSize(root *os.Root, path string) (nodeCount, edgeCount int, err error) {
	file, err := root.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = file.Close() }() // Read-only; closing cannot lose data.
	decoder := jsontext.NewDecoder(file)
	if token, err := decoder.ReadToken(); err != nil || token.Kind() != jsontext.KindBeginObject {
		return 0, 0, errors.Join(errors.New("graph is not a JSON object"), err)
	}
	for decoder.PeekKind() == jsontext.KindString {
		name, err := decoder.ReadToken()
		if err != nil {
			return 0, 0, err
		}
		if name.String() != "meta" {
			if err := decoder.SkipValue(); err != nil {
				return 0, 0, err
			}
			continue
		}
		var meta struct {
			NodeCount int `json:"nodeCount"`
			EdgeCount int `json:"edgeCount"`
		}
		if err := jsonv2.UnmarshalDecode(decoder, &meta); err != nil {
			return 0, 0, err
		}
		return meta.NodeCount, meta.EdgeCount, nil
	}
	return 0, 0, errors.New("graph has no meta object")
}

// validWorkspaceChild reports whether a listed child names a directory
// directly under the workspace root.
func validWorkspaceChild(child string) bool {
	return child != "" && child != "." && child != ".." && filepath.Base(child) == child && filepath.VolumeName(child) == ""
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
	if err := fsutil.WriteFileAtomic(filepath.Join(outDir, "workspace.json"), append(data, '\n'), 0o644); err != nil {
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
// read loads child graphs as in [LoadWorkspaceGraphs].
// read loads child graphs as in [LoadWorkspaceGraphs].
// A non-zero MaxDirs budget is divided across the loaded children, with at least one
// directory retained per child.
func FederateMap(root, contextDir string, options RepoMapOptions, read func(contextDir string) (*GraphV1, error)) string {
	workspace := LoadWorkspaceGraphs(root, contextDir, read)
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
