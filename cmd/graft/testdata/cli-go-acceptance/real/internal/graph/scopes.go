package graph

import (
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/collate"
	textlanguage "golang.org/x/text/language"
)

// scopeMarkers are the project-marker files, in the order `markers` lists them.
var scopeMarkers = []string{"package.json", "go.mod", "pyproject.toml", "setup.py", "Cargo.toml", "composer.json", "pom.xml", "build.gradle", "build.gradle.kts"}

// minScopeNodes is the smallest non-file node count a sub-scope keeps.
const minScopeNodes = 5

// localeCompare returns a comparator matching JavaScript's default
// String.prototype.localeCompare, which the TypeScript graph writer sorts by.
// A collator is not safe for concurrent use, so each sort takes its own.
// LocaleCompare orders strings like JavaScript's String.prototype.localeCompare
// in the English locale.
func LocaleCompare() func(a, b string) int {
	return localeCompare()
}

func localeCompare() func(a, b string) int {
	return collate.New(textlanguage.English).CompareString
}

func utf16Len(text string) int {
	return len(utf16.Encode([]rune(text)))
}

var (
	pnpmPackagesKey = regexp.MustCompile(`^packages\s*:`)
	pnpmListItem    = regexp.MustCompile(`^\s*-\s*`)
)

type scopeCandidate struct {
	markers     []string
	isWorkspace bool
}

// discoverScopes finds sub-project boundaries among the visible repository
// files (scopes.ts discoverScopes); relFiles are posix repo-relative paths.
// The files come from the repository walk, so every probed path stays under root.
func discoverScopes(root string, relFiles []string) []ScopeV1 {
	dirs := []string{""}
	seen := map[string]struct{}{"": {}}
	for _, rel := range relFiles {
		parts := strings.Split(rel, "/")
		for depth := 1; depth < len(parts); depth++ {
			dir := strings.Join(parts[:depth], "/")
			if _, ok := seen[dir]; !ok {
				seen[dir] = struct{}{}
				dirs = append(dirs, dir)
			}
		}
	}

	markerMap := make(map[string][]string)
	var markerDirs []string
	for _, dir := range dirs {
		var found []string
		for _, marker := range scopeMarkers {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), marker)); err == nil {
				found = append(found, marker)
			}
		}
		if len(found) > 0 {
			markerMap[dir] = found
			markerDirs = append(markerDirs, dir)
		}
	}

	globs, hasWorkspace := readWorkspaceGlobs(root)
	workspaceMatches := make(map[string]struct{})
	var workspaceOrder []string
	for _, glob := range globs {
		for _, dir := range resolveWorkspaceGlob(dirs, glob) {
			if _, ok := workspaceMatches[dir]; !ok {
				workspaceMatches[dir] = struct{}{}
				workspaceOrder = append(workspaceOrder, dir)
			}
		}
	}

	candidates := make(map[string]scopeCandidate)
	var order []string
	for _, dir := range markerDirs {
		markers := markerMap[dir]
		_, isWorkspace := workspaceMatches[dir]
		if hasWorkspace && !isWorkspace && slices.Contains(markers, "package.json") {
			markers = slices.DeleteFunc(slices.Clone(markers), func(m string) bool { return m == "package.json" })
			if len(markers) == 0 {
				continue
			}
		}
		if dir != "" && strings.Count(dir, "/")+1 > 2 && !isWorkspace {
			continue
		}
		candidates[dir] = scopeCandidate{markers: markers, isWorkspace: isWorkspace}
		order = append(order, dir)
	}
	for _, dir := range workspaceOrder {
		if existing, ok := candidates[dir]; ok {
			existing.isWorkspace = true
			candidates[dir] = existing
			continue
		}
		candidates[dir] = scopeCandidate{markers: markerMap[dir], isWorkspace: true}
		order = append(order, dir)
	}

	nearestAncestor := func(prefix string, set map[string]scopeCandidate) (string, bool) {
		segments := strings.Split(prefix, "/")
		for count := len(segments) - 1; count >= 1; count-- {
			ancestor := strings.Join(segments[:count], "/")
			if _, ok := set[ancestor]; ok {
				return ancestor, true
			}
		}
		return "", false
	}
	frozen := maps.Clone(candidates)
	for _, prefix := range order {
		if !frozen[prefix].isWorkspace {
			continue
		}
		if ancestor, ok := nearestAncestor(prefix, frozen); ok && !frozen[ancestor].isWorkspace {
			delete(candidates, ancestor)
		}
	}
	postA := maps.Clone(candidates)
	for _, prefix := range order {
		entry, ok := postA[prefix]
		if !ok || prefix == "" {
			continue
		}
		if ancestor, ok := nearestAncestor(prefix, postA); ok && (!entry.isWorkspace || postA[ancestor].isWorkspace) {
			delete(candidates, prefix)
		}
	}

	var scopes []ScopeV1
	for _, prefix := range order {
		if entry, ok := candidates[prefix]; ok {
			scopes = append(scopes, ScopeV1{Prefix: prefix, Label: prefix, Markers: nonNil(entry.markers)})
		}
	}
	if len(scopes) == 0 {
		return []ScopeV1{{Prefix: "", Label: "", Markers: []string{}}}
	}
	if len(scopes) == 1 && scopes[0].Prefix == "" {
		return scopes
	}
	compare := localeCompare()
	slices.SortStableFunc(scopes, func(a, b ScopeV1) int {
		return cmp.Or(utf16Len(b.Prefix)-utf16Len(a.Prefix), compare(a.Prefix, b.Prefix))
	})
	return scopes
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// readWorkspaceGlobs reads pnpm-workspace.yaml `packages:`, else package.json
// `workspaces`; ok is false when the repository declares neither.
func readWorkspaceGlobs(root string) (globs []string, ok bool) {
	if data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
		lines := regexp.MustCompile(`\r?\n`).Split(string(data), -1)
		if start := slices.IndexFunc(lines, func(line string) bool { return pnpmPackagesKey.MatchString(strings.TrimSpace(line)) }); start >= 0 {
			for _, line := range lines[start+1:] {
				if strings.TrimSpace(line) == "" {
					continue
				}
				if !pnpmListItem.MatchString(line) {
					break
				}
				if value := trimOneQuote(strings.TrimSpace(pnpmListItem.ReplaceAllString(line, "")), "'\""); value != "" {
					globs = append(globs, value)
				}
			}
			if len(globs) > 0 {
				return globs, true
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil, false
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(data, &pkg) != nil || len(pkg.Workspaces) == 0 {
		return nil, false
	}
	var list []any
	if json.Unmarshal(pkg.Workspaces, &list) != nil {
		var object struct {
			Packages []any `json:"packages"`
		}
		if json.Unmarshal(pkg.Workspaces, &object) != nil || object.Packages == nil {
			return nil, false
		}
		list = object.Packages
	}
	globs = make([]string, 0, len(list))
	for _, value := range list {
		if glob, isString := value.(string); isString {
			globs = append(globs, glob)
		}
	}
	return globs, true
}

// resolveWorkspaceGlob supports `dir/*`, `dir/**`, and literal directories.
func resolveWorkspaceGlob(dirs []string, pattern string) []string {
	normalized := strings.TrimSuffix(strings.ReplaceAll(pattern, "\\", "/"), "/")
	base, recursive := "", false
	switch {
	case normalized == "**":
		recursive = true
	case normalized == "*":
	case strings.HasSuffix(normalized, "/**"):
		base, recursive = strings.TrimSuffix(normalized, "/**"), true
	case strings.HasSuffix(normalized, "/*"):
		base = strings.TrimSuffix(normalized, "/*")
	case !strings.Contains(normalized, "*"):
		if slices.Contains(dirs, normalized) {
			return []string{normalized}
		}
		return nil
	default:
		return nil
	}
	var matches []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if recursive {
			if base == "" || strings.HasPrefix(dir, base+"/") {
				matches = append(matches, dir)
			}
			continue
		}
		parent := ""
		if slash := strings.LastIndex(dir, "/"); slash >= 0 {
			parent = dir[:slash]
		}
		if parent == base {
			matches = append(matches, dir)
		}
	}
	return matches
}

// applyMinSubstanceGuard folds scopes with fewer than minScopeNodes non-file
// nodes into the root scope.
func applyMinSubstanceGuard(scopes []ScopeV1, nodes []NodeV1) []ScopeV1 {
	if len(scopes) <= 1 {
		return scopes
	}
	counts := make(map[string]int, len(scopes))
	for _, node := range nodes {
		if node.Kind != "file" {
			counts[mapScopeOf(node.Path, scopes).Prefix]++
		}
	}
	var kept []ScopeV1
	for _, scope := range scopes {
		if scope.Prefix == "" || counts[scope.Prefix] >= minScopeNodes {
			kept = append(kept, scope)
		}
	}
	if len(kept) == 0 {
		return []ScopeV1{{Prefix: "", Label: "", Markers: []string{}}}
	}
	if len(kept) == 1 && kept[0].Prefix == "" {
		return []ScopeV1{{Prefix: "", Label: "", Markers: kept[0].Markers}}
	}
	return kept
}
