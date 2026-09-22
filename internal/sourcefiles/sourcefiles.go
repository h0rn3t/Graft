// Package sourcefiles enumerates and decodes repository source files.
package sourcefiles

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const defaultMaxFileBytes int64 = 1_000_000

// Options selects the working-tree view used by Walk. An empty Extensions
// slice means every regular file; callers building a graph should pass the
// extensions claimed by their extractors.
type Options struct {
	OutDir            string
	Extensions        []string
	IncludeDirs       []string
	OnlyDirs          []string
	FollowSubmodules  bool
	FollowNestedRepos bool
	MaxFileBytes      int64
}

// File carries the stable repository path and stat data used by graph builds
// and freshness probes.
type File struct {
	Abs     string
	Rel     string
	Size    int64
	MTimeMS float64
}

type walkState struct {
	topRoot    string
	activeRoot map[string]struct{}
}

// Walk lists regular source files using Git's tracked, untracked, and
// non-ignored view when available. Directory skips, nested-repository choices,
// and only-directory filters are shared by builds and freshness probes.
func Walk(root string, opts Options) ([]File, error) {
	requested, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve source root: %w", err)
	}
	requested = filepath.Clean(requested)
	canonical, err := canonicalWalkRoot(requested)
	if err != nil {
		return nil, err
	}
	state := &walkState{topRoot: canonical, activeRoot: make(map[string]struct{})}
	paths, ok, err := gitVisibleFiles(canonical, opts, state)
	if err != nil {
		return nil, err
	}
	if !ok {
		paths, err = walkFilesystem(canonical, opts.IncludeDirs, opts.MaxFileBytes)
		if err != nil {
			return nil, fmt.Errorf("walk source tree: %w", err)
		}
	}
	if requested != canonical {
		remapped := make([]string, 0, len(paths))
		for _, path := range paths {
			rel, err := filepath.Rel(canonical, path)
			if err == nil && rel != "." && filepath.IsLocal(rel) {
				remapped = append(remapped, filepath.Join(requested, rel))
			}
		}
		paths = remapped
	}

	outDir := opts.OutDir
	if outDir != "" {
		if !filepath.IsAbs(outDir) {
			outDir = filepath.Join(requested, outDir)
		}
		outDir = filepath.Clean(outDir)
	}
	includes := stringSet(opts.IncludeDirs)
	var extensions map[string]struct{}
	if len(opts.Extensions) > 0 {
		extensions = make(map[string]struct{}, len(opts.Extensions))
		for _, ext := range opts.Extensions {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext != "" {
				if !strings.HasPrefix(ext, ".") {
					ext = "." + ext
				}
				extensions[ext] = struct{}{}
			}
		}
	}
	onlyDirs := make([]string, len(opts.OnlyDirs))
	for index, dir := range opts.OnlyDirs {
		onlyDirs[index] = strings.ReplaceAll(dir, "\\", "/")
	}
	maxFileBytes := opts.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = defaultMaxFileBytes
	}

	files := make([]File, 0, len(paths))
	for _, path := range paths {
		if outDir != "" {
			outRel, err := filepath.Rel(outDir, path)
			if err == nil && filepath.IsLocal(outRel) {
				continue
			}
		}
		if skippedPath(path, requested, includes) {
			continue
		}
		rel, err := filepath.Rel(requested, path)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if len(onlyDirs) > 0 && !slices.ContainsFunc(onlyDirs, func(dir string) bool {
			return rel == dir || strings.HasPrefix(rel, dir+"/")
		}) {
			continue
		}
		if extensions != nil {
			if _, ok := extensions[strings.ToLower(filepath.Ext(rel))]; !ok {
				continue
			}
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			continue
		}
		modified := info.ModTime()
		files = append(files, File{
			Abs:  path,
			Rel:  rel,
			Size: info.Size(),
			MTimeMS: float64(modified.Unix())*float64(time.Second/time.Millisecond) +
				float64(modified.Nanosecond())/float64(time.Millisecond),
		})
	}
	slices.SortFunc(files, func(a, b File) int {
		return strings.Compare(a.Rel, b.Rel)
	})
	return files, nil
}

// Read decodes UTF-8 and BOM-marked UTF-16LE source. UTF-16BE is unsupported
// and returns ok=false without an error.
func Read(path string) (text string, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff {
		return "", false, nil
	}
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe {
		data = data[2 : len(data)-len(data)%2]
		units := make([]uint16, len(data)/2)
		for index := range units {
			units[index] = binary.LittleEndian.Uint16(data[index*2:])
		}
		return string(utf16.Decode(units)), true, nil
	}
	decoded := make([]byte, 0, len(data))
	for len(data) > 0 {
		r, width := utf8.DecodeRune(data)
		if r == utf8.RuneError && width == 1 {
			width = invalidUTF8Prefix(data)
		}
		decoded = utf8.AppendRune(decoded, r)
		data = data[width:]
	}
	return string(decoded), true, nil
}

// invalidUTF8Prefix consumes one malformed UTF-8 subpart like Node's decoder.
func invalidUTF8Prefix(data []byte) int {
	first := data[0]
	width := 1
	switch {
	case first >= 0xc2 && first <= 0xdf:
		width = 2
	case first >= 0xe0 && first <= 0xef:
		width = 3
	case first >= 0xf0 && first <= 0xf4:
		width = 4
	}
	limit := min(width, len(data))
	for index := 1; index < limit; index++ {
		b := data[index]
		if b < 0x80 || b > 0xbf {
			return index
		}
		if index == 1 && ((first == 0xe0 && b < 0xa0) || (first == 0xed && b > 0x9f) ||
			(first == 0xf0 && b < 0x90) || (first == 0xf4 && b > 0x8f)) {
			return 1
		}
	}
	return limit
}

// Hash returns the SHA-256 hex digest of decoded source text.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func canonicalWalkRoot(dir string) (string, error) {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return dir, nil
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("broken symbolic link: %s", dir)
	}
	info, err = os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("broken symbolic link: %s", dir)
	}
	if !info.IsDir() {
		return dir, nil
	}
	return canonical, nil
}

func gitVisibleFiles(root string, opts Options, state *walkState) ([]string, bool, error) {
	if !opts.FollowSubmodules && !opts.FollowNestedRepos {
		files, ok := gitVisibleFilesShallow(root, opts.IncludeDirs, opts.MaxFileBytes)
		return files, ok, nil
	}
	rootKey := root
	if filepath.VolumeName(rootKey) != "" {
		rootKey = strings.ToLower(rootKey)
	}
	if _, exists := state.activeRoot[rootKey]; exists {
		return nil, true, nil
	}
	state.activeRoot[rootKey] = struct{}{}
	defer delete(state.activeRoot, rootKey)

	command := exec.Command("git", "-C", root, "ls-files", "-t", "--stage", "--cached", "--others", "--exclude-standard", "-z", "--")
	output, err := command.Output()
	if err != nil {
		return nil, false, nil
	}
	type gitEntry struct {
		gitlink bool
		nested  bool
	}
	entries := make(map[string]gitEntry)
	includes := stringSet(opts.IncludeDirs)
	for record := range strings.SplitSeq(string(output), "\x00") {
		if len(record) < 2 || record[1] != ' ' {
			continue
		}
		tag, body := record[0], record[2:]
		rel, entry := "", gitEntry{}
		if tag == '?' {
			rel = body
			if strings.HasSuffix(rel, "/") {
				entry.nested = true
				rel = strings.TrimSuffix(rel, "/")
			}
		} else {
			metadata, path, found := strings.Cut(body, "\t")
			if !found {
				continue
			}
			rel = path
			entry.gitlink = strings.HasPrefix(metadata, "160000 ")
		}
		if rel == "" || !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue
		}
		prior := entries[rel]
		entries[rel] = gitEntry{gitlink: entry.gitlink || prior.gitlink, nested: entry.nested || prior.nested}
	}

	files := make(map[string]struct{}, len(entries))
	for rel, entry := range entries {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if skippedPath(abs, state.topRoot, includes) {
			continue
		}
		if (entry.gitlink && opts.FollowSubmodules) || (entry.nested && opts.FollowNestedRepos) {
			if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
				continue
			}
			childFiles, childOK, err := gitVisibleFiles(abs, opts, state)
			if err != nil {
				return nil, false, err
			}
			if !childOK {
				childFiles, err = walkFilesystem(abs, opts.IncludeDirs, opts.MaxFileBytes)
				if err != nil {
					return nil, false, fmt.Errorf("walk nested repository %q: %w", rel, err)
				}
			}
			for _, child := range childFiles {
				files[child] = struct{}{}
			}
			continue
		}
		if isSourceFile(abs, opts.MaxFileBytes) {
			files[abs] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(files)), true, nil
}

func gitVisibleFilesShallow(root string, includeDirs []string, maxFileBytes int64) ([]string, bool) {
	command := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--")
	output, err := command.Output()
	if err != nil {
		return nil, false
	}
	includes := stringSet(includeDirs)
	files := make([]string, 0)
	for rel := range strings.SplitSeq(string(output), "\x00") {
		if rel == "" || !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue
		}
		if skippedPath(rel, ".", includes) {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if isSourceFile(path, maxFileBytes) {
			files = append(files, path)
		}
	}
	return files, true
}

func walkFilesystem(root string, includeDirs []string, maxFileBytes int64) ([]string, error) {
	includes := stringSet(includeDirs)
	if maxFileBytes <= 0 {
		maxFileBytes = defaultMaxFileBytes
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", root)
	}
	files := make([]string, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root && entry.IsDir() {
			if shouldSkipDir(entry.Name(), includes) {
				return fs.SkipDir
			}
			return nil
		}
		if path != root && !entry.IsDir() && !shouldSkipDir(entry.Name(), includes) && isSourceFile(path, maxFileBytes) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func isSourceFile(path string, maxFileBytes int64) bool {
	if maxFileBytes <= 0 {
		maxFileBytes = defaultMaxFileBytes
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() <= maxFileBytes
}

func skippedPath(path, root string, includes map[string]struct{}) bool {
	rel := path
	if root != "." {
		var err error
		rel, err = filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return true
		}
	}
	for part := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		if shouldSkipDir(part, includes) {
			return true
		}
	}
	return false
}

func shouldSkipDir(name string, includes map[string]struct{}) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	if _, ok := includes[name]; ok {
		return false
	}
	switch name {
	case "node_modules", "dist", "build", "_build", "out", "target", "vendor", "coverage", "__pycache__", "venv":
		return true
	default:
		return false
	}
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}
