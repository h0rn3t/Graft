package blast

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf16"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

// FullDepth walks the whole connected closure.
const FullDepth = math.MaxInt

// Depth is a walk depth that serializes FullDepth as JSON null, the way
// JSON.stringify writes Infinity.
type Depth int

// MarshalJSON writes the depth as a number, or null for the full closure.
func (depth Depth) MarshalJSON() ([]byte, error) {
	if int(depth) == FullDepth {
		return []byte("null"), nil
	}
	return strconv.AppendInt(nil, int64(depth), 10), nil
}

// LabelSource is where a cluster's label came from.
type LabelSource string

// Label sources, best first.
const (
	LabelNamed  LabelSource = "named"
	LabelSymbol LabelSource = "symbol"
)

// TestSignal says whether a diff brought the tests of an area along.
type TestSignal string

// Test signals for a changed area.
const (
	TestsChanged TestSignal = "changed"
	TestsStale   TestSignal = "stale"
	TestsNone    TestSignal = "none"
	TestsNA      TestSignal = "na"
)

// Seed is a changed symbol, or a whole changed file, the walk started from.
type Seed struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      graph.Kind `json:"kind"`
	Path      string     `json:"path"`
	Span      string     `json:"span"`
	WholeFile bool       `json:"wholeFile"`
}

// Impacted is one dependent symbol and how the walk reached it.
type Impacted struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Kind     graph.Kind     `json:"kind"`
	Path     string         `json:"path"`
	Span     string         `json:"span"`
	Relation graph.Relation `json:"relation"`
	Depth    int            `json:"depth"`
}

// ImpactedModule groups dependents into the unit a reviewer thinks in.
type ImpactedModule struct {
	Label       string      `json:"label"`
	LabelSource LabelSource `json:"labelSource"`
	Key         string      `json:"key"`
	Files       []string    `json:"files"`
	Symbols     []Impacted  `json:"symbols"`
	From        []string    `json:"from"`
	Owners      *[]Owner    `json:"owners,omitzero"`
}

// ChangedArea is a group of changed files and its test signal.
type ChangedArea struct {
	Label            string      `json:"label"`
	LabelSource      LabelSource `json:"labelSource"`
	Key              string      `json:"key"`
	Files            []string    `json:"files"`
	Seeds            int         `json:"seeds"`
	Tests            TestSignal  `json:"tests"`
	TestFiles        []string    `json:"testFiles"`
	ChangedTestFiles []string    `json:"changedTestFiles"`
	Reached          int         `json:"reached"`
	Behavioural      int         `json:"behavioural"`
	Unreached        []string    `json:"unreached"`
	SeedNames        []string    `json:"seedNames"`
	Owners           *[]Owner    `json:"owners,omitzero"`
}

// Report is the blast radius of a diff.
type Report struct {
	Basis       string            `json:"basis"`
	Depth       Depth             `json:"depth"`
	Changed     []*ChangedFile    `json:"changed"`
	Unindexed   []string          `json:"unindexed"`
	Deleted     []string          `json:"deleted"`
	Seeds       []Seed            `json:"seeds"`
	Impacted    []Impacted        `json:"impacted"`
	Modules     []*ImpactedModule `json:"modules"`
	TestModules []*ImpactedModule `json:"testModules"`
	Areas       []*ChangedArea    `json:"areas"`
	Reviewers   *[]Reviewer       `json:"reviewers,omitzero"`
}

var (
	spanPattern = regexp.MustCompile(`^L(\d+)-L(\d+)$`)
	testPath    = regexp.MustCompile(`(?i)(^|/)(tests?|specs?|__tests__)/|\.(test|spec)\.[cm]?[jt]sx?$|_test\.(go|py|rb)$`)
)

// maxAreas is how many directory groups the diff is folded down to.
const maxAreas = 5

type spanned struct {
	node       graph.NodeV1
	start, end int
}

func spanBounds(span string) (int, int, bool) {
	match := spanPattern.FindStringSubmatch(span)
	if match == nil {
		return 0, 0, false
	}
	start, _ := strconv.Atoi(match[1])
	end, _ := strconv.Atoi(match[2])
	return start, end, true
}

// seedsForFile returns the innermost symbols in path overlapping any range.
func seedsForFile(wiring graph.GraphV1, path string, ranges []LineRange) []graph.NodeV1 {
	symbols := make([]spanned, 0)
	for _, node := range wiring.Nodes {
		if node.Kind == "file" || node.Path != path {
			continue
		}
		if start, end, ok := spanBounds(node.Span); ok {
			symbols = append(symbols, spanned{node: node, start: start, end: end})
		}
	}
	if len(symbols) == 0 {
		return nil
	}
	hit := make([]graph.NodeV1, 0)
	seen := make(map[string]int)
	for _, changed := range ranges {
		overlapping := make([]int, 0)
		for i, symbol := range symbols {
			if symbol.start <= changed.End && symbol.end >= changed.Start {
				overlapping = append(overlapping, i)
			}
		}
		for _, i := range overlapping {
			symbol := symbols[i]
			containsAnother := false
			for _, j := range overlapping {
				other := symbols[j]
				if j != i && other.start >= symbol.start && other.end <= symbol.end {
					containsAnother = true
					break
				}
			}
			if containsAnother {
				continue
			}
			if at, ok := seen[symbol.node.ID]; ok {
				hit[at] = symbol.node
				continue
			}
			seen[symbol.node.ID] = len(hit)
			hit = append(hit, symbol.node)
		}
	}
	return hit
}

type mergedHit struct {
	hit  Impacted
	from []string
}

// Radius computes the blast radius of changed against wiring.
func Radius(wiring graph.GraphV1, changed []*ChangedFile, basis string, depth int) *Report {
	fileNodes := make(map[string]graph.NodeV1)
	for _, node := range wiring.Nodes {
		if node.Kind == "file" {
			fileNodes[node.Path] = node
		}
	}
	report := &Report{
		Basis: basis, Depth: Depth(depth), Changed: changed,
		Unindexed: []string{}, Deleted: []string{}, Seeds: []Seed{},
	}
	seedPaths := make([]string, 0)
	seedNodes := make(map[string][]graph.NodeV1)
	mergedOrder := make([]string, 0)
	merged := make(map[string]*mergedHit)

	for _, file := range changed {
		if file.Status == StatusDeleted {
			report.Deleted = append(report.Deleted, file.Path)
			continue
		}
		fileNode, ok := fileNodes[file.Path]
		if !ok {
			report.Unindexed = append(report.Unindexed, file.Path)
			continue
		}
		var symbolSeeds []graph.NodeV1
		if len(file.Ranges) > 0 {
			symbolSeeds = seedsForFile(wiring, file.Path, file.Ranges)
		}
		for _, node := range symbolSeeds {
			report.Seeds = append(report.Seeds, Seed{ID: node.ID, Name: node.Name, Kind: node.Kind, Path: node.Path, Span: node.Span})
		}
		walkSeeds := symbolSeeds
		if len(symbolSeeds) == 0 {
			report.Seeds = append(report.Seeds, Seed{ID: fileNode.ID, Name: fileNode.Name, Kind: fileNode.Kind, Path: fileNode.Path, Span: fileNode.Span, WholeFile: true})
			walkSeeds = []graph.NodeV1{fileNode}
		}
		if _, seen := seedNodes[file.Path]; !seen {
			seedPaths = append(seedPaths, file.Path)
		}
		seedNodes[file.Path] = walkSeeds
		for _, edge := range graph.ImpactOfMany(wiring, walkSeeds, depth, graph.DirectionIn) {
			if edge.Node == nil {
				continue
			}
			hit := Impacted{
				ID: edge.Node.ID, Name: edge.Node.Name, Kind: edge.Node.Kind,
				Path: edge.Node.Path, Span: edge.Node.Span, Relation: edge.Relation, Depth: edge.Depth,
			}
			prev, ok := merged[hit.ID]
			if !ok {
				merged[hit.ID] = &mergedHit{hit: hit, from: []string{file.Path}}
				mergedOrder = append(mergedOrder, hit.ID)
				continue
			}
			if !slices.Contains(prev.from, file.Path) {
				prev.from = append(prev.from, file.Path)
			}
			if hit.Depth < prev.hit.Depth {
				prev.hit = hit
			}
		}
	}

	report.Impacted = make([]Impacted, 0, len(mergedOrder))
	origins := make(map[string][]string, len(mergedOrder))
	for _, id := range mergedOrder {
		report.Impacted = append(report.Impacted, merged[id].hit)
		origins[id] = merged[id].from
	}
	report.Modules, report.TestModules = groupByModule(report.Impacted, changed, origins)
	report.Areas = changedAreas(wiring, changed, seedPaths, seedNodes, report.Modules)
	return report
}

type dirGroup struct {
	dir   string
	paths []string
}

type hubCandidate struct {
	name        string
	behavioural bool
	degree      int
}

func changedAreas(wiring graph.GraphV1, changed []*ChangedFile, seedPaths []string, seedNodes map[string][]graph.NodeV1, modules []*ImpactedModule) []*ChangedArea {
	compare := localeCompare()
	changedTests := make(map[string]bool)
	for _, file := range changed {
		if testPath.MatchString(file.Path) {
			changedTests[file.Path] = true
		}
	}
	pathByID := make(map[string]string, len(wiring.Nodes))
	for _, node := range wiring.Nodes {
		pathByID[node.ID] = node.Path
	}
	inDegree := make(map[string]int)
	incoming := make(map[string][]string)
	for _, edge := range wiring.Edges {
		inDegree[edge.Target]++
	}
	for _, edge := range wiring.Edges {
		path, ok := pathByID[edge.Source]
		if !ok || path == "" || !testPath.MatchString(path) {
			continue
		}
		if !slices.Contains(incoming[edge.Target], path) {
			incoming[edge.Target] = append(incoming[edge.Target], path)
		}
	}

	groups := make([]*dirGroup, 0)
	for _, path := range seedPaths {
		if testPath.MatchString(path) {
			continue
		}
		dir := DirLabel(path)
		if group := findGroup(groups, dir); group != nil {
			group.paths = append(group.paths, path)
		} else {
			groups = append(groups, &dirGroup{dir: dir, paths: []string{path}})
		}
	}
	groups = coarsen(groups, maxAreas)

	areas := make([]*ChangedArea, 0, len(groups))
	for _, group := range groups {
		area := &ChangedArea{
			Label: group.dir, LabelSource: LabelSymbol, Key: group.dir,
			Files: []string{}, Tests: TestsNone, TestFiles: []string{}, ChangedTestFiles: []string{},
			Unreached: []string{}, SeedNames: []string{},
		}
		candidates := make([]hubCandidate, 0)
		for _, path := range group.paths {
			nodes := seedNodes[path]
			area.Files = append(area.Files, path)
			area.Seeds += len(nodes)
			for _, node := range nodes {
				behavioural := node.Kind == "function" || node.Kind == "method" || node.Kind == "class"
				from := incoming[node.ID]
				for _, test := range from {
					if !slices.Contains(area.TestFiles, test) {
						area.TestFiles = append(area.TestFiles, test)
					}
					if changedTests[test] && !slices.Contains(area.ChangedTestFiles, test) {
						area.ChangedTestFiles = append(area.ChangedTestFiles, test)
					}
				}
				candidates = append(candidates, hubCandidate{name: node.Name, behavioural: behavioural, degree: inDegree[node.ID]})
				if !behavioural {
					continue
				}
				area.Behavioural++
				if len(from) > 0 {
					area.Reached++
				} else {
					area.Unreached = append(area.Unreached, node.Name)
				}
			}
		}
		slices.SortStableFunc(candidates, func(a, b hubCandidate) int {
			return cmp.Or(boolRank(b.behavioural)-boolRank(a.behavioural), b.degree-a.degree, compare(a.name, b.name))
		})
		for _, candidate := range candidates {
			area.SeedNames = append(area.SeedNames, candidate.name)
		}
		area.Label = HubLabel(area.SeedNames, area.Key)
		sortCodeUnits(area.Files)
		sortCodeUnits(area.TestFiles)
		sortCodeUnits(area.ChangedTestFiles)
		switch {
		case area.Behavioural == 0:
			area.Tests = TestsNA
		case len(area.ChangedTestFiles) > 0:
			area.Tests = TestsChanged
		case len(area.TestFiles) > 0:
			area.Tests = TestsStale
		default:
			area.Tests = TestsNone
		}
		areas = append(areas, area)
	}

	reach := func(area *ChangedArea) int {
		count := 0
		for _, module := range modules {
			if slices.ContainsFunc(module.From, func(file string) bool { return slices.Contains(area.Files, file) }) {
				count++
			}
		}
		return count
	}
	slices.SortStableFunc(areas, func(a, b *ChangedArea) int {
		return cmp.Or(reach(b)-reach(a), len(b.Files)-len(a.Files), compare(a.Label, b.Label))
	})
	return areas
}

func findGroup(groups []*dirGroup, dir string) *dirGroup {
	for _, group := range groups {
		if group.dir == dir {
			return group
		}
	}
	return nil
}

func boolRank(value bool) int {
	if value {
		return 1
	}
	return 0
}

// HubLabel is the deterministic backstop label: the first non-empty name, or fallback.
func HubLabel(names []string, fallback string) string {
	for _, name := range names {
		if name != "" {
			return ShortLabel(name)
		}
	}
	return fallback
}

func groupByModule(impacted []Impacted, changed []*ChangedFile, origins map[string][]string) ([]*ImpactedModule, []*ImpactedModule) {
	compare := localeCompare()
	changedPaths := make(map[string]bool, len(changed))
	for _, file := range changed {
		changedPaths[file.Path] = true
	}
	all := make([]*ImpactedModule, 0)
	byKey := make(map[string]*ImpactedModule)
	for _, hit := range impacted {
		if changedPaths[hit.Path] {
			continue
		}
		key := DirLabel(hit.Path)
		module := byKey[key]
		if module == nil {
			module = &ImpactedModule{Label: key, LabelSource: LabelSymbol, Key: key, Files: []string{}, Symbols: []Impacted{}, From: []string{}}
			byKey[key] = module
			all = append(all, module)
		}
		module.Symbols = append(module.Symbols, hit)
		if !slices.Contains(module.Files, hit.Path) {
			module.Files = append(module.Files, hit.Path)
		}
	}
	for _, module := range all {
		from := make([]string, 0)
		for _, symbol := range module.Symbols {
			for _, path := range origins[symbol.ID] {
				if !slices.Contains(from, path) {
					from = append(from, path)
				}
			}
		}
		sortCodeUnits(from)
		module.From = from
		sortCodeUnits(module.Files)
		slices.SortStableFunc(module.Symbols, func(a, b Impacted) int {
			return cmp.Or(a.Depth-b.Depth, compare(a.Path, b.Path))
		})
		names := make([]string, len(module.Symbols))
		for i, symbol := range module.Symbols {
			names[i] = symbol.Name
		}
		module.Label = HubLabel(names, module.Key)
		module.LabelSource = LabelSymbol
	}
	bySize := func(a, b *ImpactedModule) int {
		return cmp.Or(len(b.Symbols)-len(a.Symbols), compare(a.Label, b.Label))
	}
	modules := make([]*ImpactedModule, 0)
	testModules := make([]*ImpactedModule, 0)
	for _, module := range all {
		if isTestOnly(module) {
			testModules = append(testModules, module)
		} else {
			modules = append(modules, module)
		}
	}
	slices.SortStableFunc(modules, bySize)
	slices.SortStableFunc(testModules, bySize)
	return modules, testModules
}

// coarsen folds directory groups into their parents until at most max remain.
func coarsen(groups []*dirGroup, limit int) []*dirGroup {
	depth := func(dir string) int {
		count := 1
		for _, char := range dir {
			if char == '/' {
				count++
			}
		}
		return count
	}
	for len(groups) > limit {
		ordered := slices.Clone(groups)
		slices.SortStableFunc(ordered, func(a, b *dirGroup) int {
			return cmp.Or(depth(b.dir)-depth(a.dir), len(a.paths)-len(b.paths))
		})
		var pick *dirGroup
		for _, group := range ordered {
			parent := ParentDir(group.dir)
			if parent != "" && findGroup(groups, parent) != nil {
				pick = group
				break
			}
		}
		if pick == nil {
			for _, group := range ordered {
				if ParentDir(group.dir) != "" {
					pick = group
					break
				}
			}
		}
		if pick == nil {
			return groups
		}
		parentDir := ParentDir(pick.dir)
		if parent := findGroup(groups, parentDir); parent != nil {
			parent.paths = append(parent.paths, pick.paths...)
		} else {
			groups = append(groups, &dirGroup{dir: parentDir, paths: slices.Clone(pick.paths)})
		}
		groups = slices.DeleteFunc(groups, func(group *dirGroup) bool { return group == pick })
	}
	return groups
}

func isTestOnly(module *ImpactedModule) bool {
	if len(module.Files) == 0 {
		return false
	}
	for _, file := range module.Files {
		if !testPath.MatchString(file) {
			return false
		}
	}
	return true
}

// localeCompare matches JavaScript's default String.prototype.localeCompare.
func localeCompare() func(a, b string) int {
	return collate.New(language.English).CompareString
}

// sortCodeUnits sorts like Array.prototype.sort with no comparator: by UTF-16 code units.
func sortCodeUnits(values []string) {
	slices.SortStableFunc(values, compareCodeUnits)
}

func compareCodeUnits(a, b string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}
