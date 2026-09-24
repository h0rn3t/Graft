package graph

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/savings"
)

const (
	defaultMapMaxDirs    = 16
	defaultMapHubsPerDir = 3
	defaultMapHotspots   = 12
	mapSplitThreshold    = 0.6
	mapDirColumnWidth    = 20
)

// Hub is a highly coupled symbol shown in a directory or global hotspot list.
type Hub struct {
	Name     string `json:"name"`
	Kind     Kind   `json:"kind"`
	Path     string `json:"path"`
	Span     string `json:"span"`
	InDegree int    `json:"inDegree"`
}

// DirEntry is one directory cluster in a repository map.
type DirEntry struct {
	Path      string   `json:"path"`
	Files     int      `json:"files"`
	Symbols   int      `json:"symbols"`
	Languages []string `json:"languages"`
	Hubs      []Hub    `json:"hubs"`
	IsFile    bool     `json:"isFile"`
}

// ScopeGroup is one scope's directory breakdown in a multi-scope map.
type ScopeGroup struct {
	Scope   string     `json:"scope"`
	Dirs    []DirEntry `json:"dirs"`
	Dropped int        `json:"dropped"`
}

// MapTotals contains repository-wide map counts.
type MapTotals struct {
	Files     int      `json:"files"`
	Symbols   int      `json:"symbols"`
	Edges     int      `json:"edges"`
	Languages []string `json:"languages"`
}

// MapSavings records the known whole-file baseline for a repository map.
type MapSavings struct {
	Files         int `json:"files"`
	BaselineChars int `json:"baselineChars"`
}

// RepoMap is the deterministic, JSON-compatible repository orientation report.
type RepoMap struct {
	Totals   MapTotals    `json:"totals"`
	Dirs     []DirEntry   `json:"dirs"`
	Scopes   []ScopeGroup `json:"scopes,omitempty"`
	Hotspots []Hub        `json:"hotspots"`
	Dropped  int          `json:"dropped"`
	Saved    *MapSavings  `json:"saved,omitempty"`
}

// RepoMapOptions controls directory and hotspot caps. Zero values use the CLI defaults.
type RepoMapOptions struct {
	MaxDirs    int
	HubsPerDir int
	Hotspots   int
}

// BuildRepoMap builds a deterministic orientation report from a wiring graph.
func BuildRepoMap(wiring GraphV1, opts RepoMapOptions) RepoMap {
	maxDirs := opts.MaxDirs
	if maxDirs == 0 {
		maxDirs = defaultMapMaxDirs
	}
	hubsPerDir := opts.HubsPerDir
	if hubsPerDir == 0 {
		hubsPerDir = defaultMapHubsPerDir
	}
	hotspots := opts.Hotspots
	if hotspots == 0 {
		hotspots = defaultMapHotspots
	}
	maxDirs = max(maxDirs, 0)
	hubsPerDir = max(hubsPerDir, 0)
	hotspots = max(hotspots, 0)

	fileNodes := make([]NodeV1, 0)
	allFilePaths := make([]string, 0)
	for _, node := range wiring.Nodes {
		if node.Kind == Kind("file") {
			fileNodes = append(fileNodes, node)
			allFilePaths = append(allFilePaths, node.Path)
		}
	}
	inDegree := mapInDegree(wiring.Edges)
	scopes := mapScopes(wiring)

	var dirs []DirEntry
	dropped := 0
	var scopeGroups []ScopeGroup
	if len(scopes) <= 1 {
		computed := computeMapDirs(wiring.Nodes, inDegree, maxDirs, hubsPerDir, "")
		dirs = computed.dirs
		dropped = computed.dropped
	} else {
		scopeGroups = make([]ScopeGroup, 0, len(scopes))
		for _, scope := range scopes {
			nodesInScope := make([]NodeV1, 0)
			for _, node := range wiring.Nodes {
				if mapScopeOf(node.Path, scopes).Prefix == scope.Prefix {
					nodesInScope = append(nodesInScope, node)
				}
			}
			computed := computeMapDirs(nodesInScope, inDegree, maxDirs, hubsPerDir, scope.Prefix)
			scopeGroups = append(scopeGroups, ScopeGroup{
				Scope:   mapScopeLabel(scope.Prefix),
				Dirs:    computed.dirs,
				Dropped: computed.dropped,
			})
		}
		dirs = make([]DirEntry, 0)
	}

	allSymbols := make([]NodeV1, 0, len(wiring.Nodes)-len(fileNodes))
	for _, node := range wiring.Nodes {
		if node.Kind != Kind("file") {
			allSymbols = append(allSymbols, node)
		}
	}
	return RepoMap{
		Totals: MapTotals{
			Files:     len(fileNodes),
			Symbols:   len(allSymbols),
			Edges:     len(wiring.Edges),
			Languages: mapSortedLanguages(allFilePaths),
		},
		Dirs:     dirs,
		Scopes:   scopeGroups,
		Hotspots: mapTopHubs(allSymbols, inDegree, hotspots),
		Dropped:  dropped,
		Saved:    mapSavings(wiring.Nodes, allFilePaths),
	}
}

type mapDirComputation struct {
	dirs    []DirEntry
	dropped int
}

func computeMapDirs(nodes []NodeV1, inDegree map[string]int, maxDirs, hubsPerDir int, stripPrefix string) mapDirComputation {
	fileNodes := make([]NodeV1, 0)
	for _, node := range nodes {
		if node.Kind == Kind("file") {
			fileNodes = append(fileNodes, node)
		}
	}
	totalFiles := len(fileNodes)
	depthOneCounts := make(map[string]int)
	for _, node := range fileNodes {
		depthOneCounts[mapDirKey(mapRelativePath(node.Path, stripPrefix), 1)]++
	}

	splitSegment := ""
	if totalFiles > 0 {
		for segment, count := range depthOneCounts {
			if float64(count)/float64(totalFiles) > mapSplitThreshold {
				splitSegment = segment
				break
			}
		}
	}
	groups := make(map[string]*mapDirGroup)
	fileRelativePaths := make(map[string]struct{}, len(fileNodes))
	for _, node := range fileNodes {
		fileRelativePaths[mapRelativePath(node.Path, stripPrefix)] = struct{}{}
	}
	for _, node := range nodes {
		relativePath := mapRelativePath(node.Path, stripPrefix)
		depth := 1
		if splitSegment != "" && mapDirKey(relativePath, 1) == splitSegment {
			depth = 2
		}
		key := mapDirKey(relativePath, depth)
		group := groups[key]
		if group == nil {
			group = &mapDirGroup{}
			groups[key] = group
		}
		if node.Kind == Kind("file") {
			group.files = append(group.files, node)
		} else {
			group.symbols = append(group.symbols, node)
		}
	}

	dirs := make([]DirEntry, 0, len(groups))
	for key, group := range groups {
		paths := make([]string, 0, len(group.files))
		for _, file := range group.files {
			paths = append(paths, file.Path)
		}
		dirs = append(dirs, DirEntry{
			Path:      mapFullPath(key, stripPrefix),
			Files:     len(group.files),
			Symbols:   len(group.symbols),
			Languages: mapSortedLanguages(paths),
			Hubs:      mapTopHubs(group.symbols, inDegree, hubsPerDir),
			IsFile:    hasMapPath(fileRelativePaths, key),
		})
	}
	compare := localeCompare()
	slices.SortFunc(dirs, func(left, right DirEntry) int {
		return cmp.Or(cmp.Compare(right.Symbols, left.Symbols), compare(left.Path, right.Path))
	})
	dropped := max(len(dirs)-maxDirs, 0)
	return mapDirComputation{dirs: dirs[:min(len(dirs), maxDirs)], dropped: dropped}
}

type mapDirGroup struct {
	files   []NodeV1
	symbols []NodeV1
}

func mapDirKey(path string, depth int) string {
	segments := strings.Split(path, "/")
	return strings.Join(segments[:min(depth, len(segments))], "/")
}

func mapRelativePath(path, prefix string) string {
	if prefix == "" {
		return path
	}
	if path == prefix {
		return ""
	}
	return strings.TrimPrefix(path, prefix+"/")
}

func mapFullPath(path, prefix string) string {
	if prefix == "" || path == "" {
		if prefix != "" && path == "" {
			return prefix
		}
		return path
	}
	return prefix + "/" + path
}

func hasMapPath(paths map[string]struct{}, path string) bool {
	_, ok := paths[path]
	return ok
}

func mapTopHubs(nodes []NodeV1, inDegree map[string]int, cap int) []Hub {
	hubs := make([]Hub, 0)
	for _, node := range nodes {
		degree := inDegree[node.ID]
		if degree == 0 {
			continue
		}
		hubs = append(hubs, Hub{Name: node.Name, Kind: node.Kind, Path: node.Path, Span: node.Span, InDegree: degree})
	}
	compare := localeCompare()
	slices.SortFunc(hubs, func(left, right Hub) int {
		return cmp.Or(
			cmp.Compare(right.InDegree, left.InDegree),
			compare(left.Name, right.Name),
			compare(left.Path, right.Path),
		)
	})
	return hubs[:min(len(hubs), cap)]
}

func mapInDegree(edges []EdgeV1) map[string]int {
	degrees := make(map[string]int)
	for _, edge := range edges {
		if isWalkRelation(edge.Relation) {
			degrees[edge.Target]++
		}
	}
	return degrees
}

func mapSortedLanguages(paths []string) []string {
	labels := make(map[string]struct{})
	for _, path := range paths {
		if label := mapLanguageLabel(path); label != "" {
			labels[label] = struct{}{}
		}
	}
	languages := make([]string, 0, len(labels))
	for label := range labels {
		languages = append(languages, label)
	}
	slices.Sort(languages)
	return languages
}

func mapLanguageLabel(path string) string {
	path = strings.ToLower(filepath.ToSlash(path))
	labels := []struct {
		ext   string
		label string
	}{
		{".tsx", "tsx"},
		{".jsx", "jsx"},
		{".mts", "typescript"},
		{".cts", "typescript"},
		{".ts", "typescript"},
		{".mjs", "javascript"},
		{".cjs", "javascript"},
		{".js", "javascript"},
		{".pyi", "python"},
		{".py", "python"},
		{".go", "go"},
		{".java", "java"},
		{".kt", "kotlin"},
		{".kts", "kotlin"},
		{".swift", "swift"},
		{".php", "php"},
		{".r", "r"},
	}
	for _, entry := range labels {
		if strings.HasSuffix(path, entry.ext) {
			return entry.label
		}
	}
	return ""
}

func mapScopes(wiring GraphV1) []ScopeV1 {
	if wiring.Meta.Scopes == nil {
		return []ScopeV1{{Prefix: "", Label: "", Markers: []string{}}}
	}
	return *wiring.Meta.Scopes
}

func mapScopeOf(path string, scopes []ScopeV1) ScopeV1 {
	for _, scope := range scopes {
		if scope.Prefix != "" && (path == scope.Prefix || strings.HasPrefix(path, scope.Prefix+"/")) {
			return scope
		}
	}
	for _, scope := range scopes {
		if scope.Prefix == "" {
			return scope
		}
	}
	return ScopeV1{}
}

func mapScopeLabel(prefix string) string {
	if prefix == "" {
		return "(root)"
	}
	return prefix + "/"
}

// FormatRepoMap renders a repository map for human-readable CLI output.
func FormatRepoMap(repoMap RepoMap) string {
	header := fmt.Sprintf("repo map — %d files · %d symbols · %d edges · %s", repoMap.Totals.Files, repoMap.Totals.Symbols, repoMap.Totals.Edges, strings.Join(repoMap.Totals.Languages, ", "))
	lines := []string{header, ""}
	if len(repoMap.Scopes) > 0 {
		for _, scope := range repoMap.Scopes {
			lines = append(lines, "## "+scope.Scope)
			for _, dir := range scope.Dirs {
				lines = append(lines, formatMapDirLine(dir))
			}
			if note := mapDroppedNote(scope.Dropped); note != "" {
				lines = append(lines, note)
			}
			lines = append(lines, "")
		}
	} else {
		for _, dir := range repoMap.Dirs {
			lines = append(lines, formatMapDirLine(dir))
		}
		if note := mapDroppedNote(repoMap.Dropped); note != "" {
			lines = append(lines, note)
		}
		lines = append(lines, "")
	}
	hotspots := make([]string, 0, len(repoMap.Hotspots))
	for _, hub := range repoMap.Hotspots {
		hotspots = append(hotspots, fmt.Sprintf("%s · %s · %s:%s · %d←", hub.Name, hub.Kind, hub.Path, hub.Span, hub.InDegree))
	}
	lines = append(lines, "hotspots: "+strings.Join(hotspots, "  "))
	body := strings.Join(lines, "\n")
	return mapWithSavings(body, repoMap.Saved) + "\n"
}

func formatMapDirLine(dir DirEntry) string {
	label := dir.Path
	if !dir.IsFile {
		label += "/"
	}
	if len(label) < mapDirColumnWidth {
		label += strings.Repeat(" ", mapDirColumnWidth-len(label))
	}
	hubs := make([]string, 0, len(dir.Hubs))
	for _, hub := range dir.Hubs {
		hubs = append(hubs, fmt.Sprintf("%s (%s, %d←)", hub.Name, mapPathBase(hub.Path), hub.InDegree))
	}
	line := fmt.Sprintf("%s%d files · %d symbols", label, dir.Files, dir.Symbols)
	if len(hubs) > 0 {
		line += "   hubs: " + strings.Join(hubs, ", ")
	}
	return line
}

func mapPathBase(path string) string {
	path = filepath.ToSlash(path)
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[index+1:]
	}
	return path
}

func mapDroppedNote(dropped int) string {
	if dropped <= 0 {
		return ""
	}
	word := "directories"
	if dropped == 1 {
		word = "directory"
	}
	return fmt.Sprintf("… +%d more %s not shown (raise max-dirs to see more)", dropped, word)
}

func mapSavings(nodes []NodeV1, paths []string) *MapSavings {
	fileChars := make(map[string]int)
	for _, node := range nodes {
		if node.Kind == Kind("file") && node.Chars != nil {
			fileChars[node.Path] = *node.Chars
		}
	}
	saved := &MapSavings{}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		chars, ok := fileChars[path]
		if ok {
			saved.Files++
			saved.BaselineChars += chars
		}
	}
	if saved.Files == 0 {
		return nil
	}
	return saved
}

func mapWithSavings(body string, saved *MapSavings) string {
	if saved == nil {
		return body
	}
	return savings.With(body, saved.Files, saved.BaselineChars)
}
