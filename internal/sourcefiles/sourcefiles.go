// Package sourcefiles enumerates and decodes repository source files.
package sourcefiles

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/h0rn3t/Graft/internal/gitx"
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
	NoReuse           bool
	NoSeed            bool
	NoCacheWrite      bool // Suppress extraction cache writes for read-only graph checks.
	OnProgress        func(index, total int, file string)
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
	var persisted struct {
		IncludeDirs       []string `json:"includeDirs"`
		FollowSubmodules  bool     `json:"followSubmodules"`
		FollowNestedRepos bool     `json:"followNestedRepos"`
	}
	if data, err := os.ReadFile(filepath.Join(requested, ".graft", "config.json")); err == nil {
		_ = json.Unmarshal(data, &persisted)
	}
	if len(opts.IncludeDirs) == 0 {
		opts.IncludeDirs = persisted.IncludeDirs
	}
	opts.FollowSubmodules = opts.FollowSubmodules || persisted.FollowSubmodules
	opts.FollowNestedRepos = opts.FollowNestedRepos || persisted.FollowNestedRepos
	canonical, err := canonicalWalkRoot(requested)
	if err != nil {
		return nil, err
	}
	state := &walkState{topRoot: canonical, activeRoot: make(map[string]struct{})}
	found, ok, err := gitVisibleFiles(canonical, opts, state)
	if err != nil {
		return nil, err
	}
	if !ok {
		found, err = walkFilesystem(canonical, opts.IncludeDirs, opts.MaxFileBytes)
		if err != nil {
			return nil, fmt.Errorf("walk source tree: %w", err)
		}
	}
	if requested != canonical {
		remapped := make([]sourceFile, 0, len(found))
		for _, file := range found {
			rel, err := filepath.Rel(canonical, file.path)
			if err == nil && rel != "." && filepath.IsLocal(rel) {
				remapped = append(remapped, sourceFile{path: filepath.Join(requested, rel), info: file.info})
			}
		}
		found = remapped
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
	files := make([]File, 0, len(found))
	for _, file := range found {
		path := file.path
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
		modified := file.info.ModTime()
		files = append(files, File{
			Abs:  path,
			Rel:  rel,
			Size: file.info.Size(),
			// sec*1e3 + nsec/1e6 rounded per operation, as Node computes mtimeMs;
			// the explicit conversion keeps the product from fusing into an FMA.
			MTimeMS: float64(float64(modified.Unix())*float64(time.Second/time.Millisecond)) +
				float64(modified.Nanosecond())/float64(time.Millisecond),
		})
	}
	// Git-visible files keep git's order and filesystem walks their sorted
	// order, exactly the order the TypeScript walk yields.
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
	if utf8.Valid(data) {
		return string(data), true, nil
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

// sourceFile is a listed file with the Lstat taken when it was listed.
type sourceFile struct {
	path string
	info fs.FileInfo
}

// gitTimeout bounds one git listing, so a wedged git cannot hang a build.
const gitTimeout = 2 * time.Minute

// gitListFiles runs git ls-files in root. ok is false when git is not
// installed or root is in no git work tree, and the caller walks the
// filesystem instead; any other git failure (dubious ownership, a corrupt
// index, a git too old for the flags) is returned, since a walk would
// silently ignore .gitignore.
func gitListFiles(root string, args ...string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	output, err := gitx.Run(ctx, root, append([]string{"ls-files"}, args...)...)
	if err == nil {
		return output, true, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", false, nil
	}
	// safe.directory given on the command line is trusted, so a work tree
	// that only ownership keeps git out of still answers true here.
	inside, probeErr := gitx.Run(ctx, root, "-c", "safe.directory=*", "rev-parse", "--is-inside-work-tree")
	if probeErr != nil || strings.TrimSpace(inside) != "true" {
		return "", false, nil
	}
	return "", false, fmt.Errorf("list files in %s: %w", root, err)
}

func gitVisibleFiles(root string, opts Options, state *walkState) ([]sourceFile, bool, error) {
	if !opts.FollowSubmodules && !opts.FollowNestedRepos {
		return gitVisibleFilesShallow(root, opts.IncludeDirs, opts.MaxFileBytes)
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

	output, ok, err := gitListFiles(root, "-t", "--stage", "--cached", "--others", "--exclude-standard", "-z", "--")
	if !ok {
		return nil, false, err
	}
	type gitEntry struct {
		gitlink bool
		nested  bool
	}
	// Git's own output order, merged per path, as the TypeScript walk keeps it.
	order := make([]string, 0)
	entries := make(map[string]gitEntry)
	includes := stringSet(opts.IncludeDirs)
	for record := range strings.SplitSeq(output, "\x00") {
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
		prior, seen := entries[rel]
		if !seen {
			order = append(order, rel)
		}
		entries[rel] = gitEntry{gitlink: entry.gitlink || prior.gitlink, nested: entry.nested || prior.nested}
	}

	files := make([]sourceFile, 0, len(entries))
	added := make(map[string]struct{}, len(entries))
	add := func(file sourceFile) {
		if _, dup := added[file.path]; !dup {
			added[file.path] = struct{}{}
			files = append(files, file)
		}
	}
	for _, rel := range order {
		entry := entries[rel]
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
				add(child)
			}
			continue
		}
		if info, ok := sourceInfo(abs, opts.MaxFileBytes); ok {
			add(sourceFile{path: abs, info: info})
		}
	}
	return files, true, nil
}

func gitVisibleFilesShallow(root string, includeDirs []string, maxFileBytes int64) ([]sourceFile, bool, error) {
	output, ok, err := gitListFiles(root, "--cached", "--others", "--exclude-standard", "-z", "--")
	if !ok {
		return nil, false, err
	}
	includes := stringSet(includeDirs)
	files := make([]sourceFile, 0)
	for rel := range strings.SplitSeq(output, "\x00") {
		if rel == "" || !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue
		}
		if skippedPath(rel, ".", includes) {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if info, ok := sourceInfo(path, maxFileBytes); ok {
			files = append(files, sourceFile{path: path, info: info})
		}
	}
	return files, true, nil
}

// walkFilesystem lists source files under root in sorted order. A directory
// below root that cannot be read is skipped rather than failing the walk.
func walkFilesystem(root string, includeDirs []string, maxFileBytes int64) ([]sourceFile, error) {
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
	files := make([]sourceFile, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != root && entry.IsDir() {
			if shouldSkipDir(entry.Name(), includes) {
				return fs.SkipDir
			}
			return nil
		}
		if path == root || entry.IsDir() || shouldSkipDir(entry.Name(), includes) {
			return nil
		}
		if info, ok := sourceInfo(path, maxFileBytes); ok {
			files = append(files, sourceFile{path: path, info: info})
		}
		return nil
	})
	return files, err
}

// sourceInfo is the one Lstat a listed file gets: ok when it is a regular
// file within the size limit.
func sourceInfo(path string, maxFileBytes int64) (fs.FileInfo, bool) {
	if maxFileBytes <= 0 {
		maxFileBytes = defaultMaxFileBytes
	}
	info, err := os.Lstat(path)
	return info, err == nil && info.Mode().IsRegular() && info.Size() <= maxFileBytes
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
