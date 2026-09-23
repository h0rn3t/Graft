// Package contextcheck checks whether the persisted context manifest matches the source tree.
package contextcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

// Options controls which source files Check considers.
type Options struct {
	// ContextDir overrides the default <root>/graft directory.
	ContextDir string
	// Extensions replaces the default source extension set when non-nil.
	Extensions []string
	// IncludeDirs lifts the built-in directory skips for matching directory names.
	IncludeDirs []string
}

// ContentDrift describes a recorded source file whose decoded content hash changed.
type ContentDrift struct {
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Result contains the deterministic context freshness findings.
type Result struct {
	OK           bool           `json:"ok"`
	Missing      bool           `json:"missing"`
	ContentDrift []ContentDrift `json:"contentDrift"`
	Removed      []string       `json:"removed"`
	Coverage     []string       `json:"coverage"`
	IndexDrift   []string       `json:"indexDrift"`
}

// Check compares the persisted context manifest and node roster with the source tree.
// A missing or invalid manifest sets Result.Missing and does not return an error.
func Check(root string, opts Options) (Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve repository root: %w", err)
	}
	outDir := resolveContextDir(root, opts.ContextDir)
	manifest := readManifest(filepath.Join(outDir, "manifest.json"))
	if manifest == nil {
		return Result{Missing: true}, nil
	}

	current, err := currentFiles(root, outDir, opts)
	if err != nil {
		return Result{}, err
	}
	result := Result{}
	recorded := make(map[string]struct{}, len(manifest.Files))
	for _, ref := range manifest.Files {
		recorded[ref.Path] = struct{}{}
		now, ok := current[ref.Path]
		if !ok {
			result.Removed = append(result.Removed, ref.Path)
			continue
		}
		if now != ref.Hash {
			result.ContentDrift = append(result.ContentDrift, ContentDrift{
				Path: ref.Path,
				From: shortHash(ref.Hash),
				To:   shortHash(now),
			})
		}
	}
	for path := range current {
		if _, ok := recorded[path]; !ok {
			result.Coverage = append(result.Coverage, path)
		}
	}
	slices.SortFunc(result.ContentDrift, func(a, b ContentDrift) int {
		return strings.Compare(a.Path, b.Path)
	})
	slices.Sort(result.Removed)
	slices.Sort(result.Coverage)
	result.IndexDrift, err = indexDrift(outDir, manifest)
	if err != nil {
		return Result{}, err
	}
	result.OK = len(result.ContentDrift) == 0 && len(result.Removed) == 0 &&
		len(result.Coverage) == 0 && len(result.IndexDrift) == 0
	return result, nil
}

func resolveContextDir(root, override string) string {
	if override == "" {
		return filepath.Join(root, "graft")
	}
	if filepath.IsAbs(override) {
		return filepath.Clean(override)
	}
	return filepath.Join(root, override)
}

func readManifest(path string) *graph.Manifest {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var manifest *graph.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil
	}
	return manifest
}

func currentFiles(root, outDir string, opts Options) (map[string]string, error) {
	extensions := opts.Extensions
	if extensions == nil {
		extensions = []string{
			".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".py", ".go", ".rs",
			".java", ".kt", ".scala", ".rb", ".php", ".c", ".h", ".cpp", ".hpp",
			".cc", ".cs", ".swift", ".sql", ".sh", ".proto",
		}
	} else if len(extensions) == 0 {
		return make(map[string]string), nil
	}
	fingerprint := graph.ReadFingerprintScope(outDir)
	var onlyDirs []string
	if fingerprint != nil {
		onlyDirs = fingerprint.OnlyDirs
	}
	files, err := sourcefiles.Walk(root, sourcefiles.Options{
		OutDir: outDir, Extensions: extensions, IncludeDirs: opts.IncludeDirs, OnlyDirs: onlyDirs,
	})
	if err != nil {
		return nil, fmt.Errorf("walk source tree: %w", err)
	}
	current := make(map[string]string, len(files))
	for _, file := range files {
		text, ok, err := sourcefiles.Read(file.Abs)
		if err != nil || !ok {
			continue
		}
		current[file.Rel] = textHash(text)
	}
	return current, nil
}

func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

type parsedNode struct {
	slug          string
	hasSlug       bool
	sourcesDigest string
	content       string
}

func indexDrift(outDir string, manifest *graph.Manifest) ([]string, error) {
	entries, err := os.ReadDir(outDir)
	if errors.Is(err, fs.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return nil, fmt.Errorf("read context nodes: %w", err)
	}
	rootStems := make(map[string]bool)
	for _, file := range manifest.Files {
		if !strings.Contains(file.Path, "/") {
			rootStems[strings.TrimSuffix(file.Path, filepath.Ext(file.Path))] = true
		}
	}
	manifestNodes := make(map[string]graph.ManifestNode, len(manifest.Nodes))
	for _, node := range manifest.Nodes {
		manifestNodes[node.Slug] = node
	}
	seen := make(map[string]bool, len(manifest.Nodes))
	var problems []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "INDEX.md" {
			continue
		}
		path := filepath.Join(outDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read node %q: %w", entry.Name(), err)
		}
		fallbackSlug := strings.TrimSuffix(entry.Name(), ".md")
		node := parseNode(string(data))
		if !node.hasSlug && isRootFileCard(node, fallbackSlug, rootStems) {
			continue
		}
		if node.slug == "" {
			node.slug = fallbackSlug
		}
		seen[node.slug] = true
		manifestNode, ok := manifestNodes[node.slug]
		if !ok {
			problems = append(problems, node.slug+": node file not in manifest")
		} else if manifestNode.SourcesDigest != node.sourcesDigest {
			problems = append(problems, node.slug+": frontmatter digest ≠ manifest")
		}
	}
	for _, node := range manifest.Nodes {
		if !seen[node.Slug] {
			problems = append(problems, node.Slug+": in manifest but node file missing")
		}
	}
	return problems, nil
}

func parseNode(content string) parsedNode {
	node := parsedNode{content: content}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return node
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return node
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch strings.TrimSpace(key) {
		case "slug":
			node.slug, node.hasSlug = value, value != ""
		case "sources_digest":
			node.sourcesDigest = value
		}
	}
	return node
}

func isRootFileCard(node parsedNode, fallbackSlug string, rootStems map[string]bool) bool {
	if node.hasSlug {
		return false
	}
	if rootStems[fallbackSlug] {
		return true
	}
	line := strings.TrimLeft(node.content, " \t\r\n")
	if !strings.HasPrefix(line, "# ") {
		return false
	}
	first := strings.Fields(strings.TrimPrefix(line, "# "))
	if len(first) == 0 {
		return false
	}
	ext := filepath.Ext(first[0])
	if len(ext) < 2 || len(ext) > 13 {
		return false
	}
	for _, r := range ext[1:] {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", r) {
			return false
		}
	}
	return true
}
