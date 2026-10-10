package graph

import (
	"cmp"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// DeadConfidence says how sure a dead-code finding is.
type DeadConfidence string

// Dead-code confidence classes, most certain first.
const (
	// DeadHigh is code only this repository can reach — unexported, or Go
	// in an internal package or a command — whose name appears nowhere else.
	DeadHigh DeadConfidence = "high"
	// DeadMedium is exported code nothing in the repository uses; a consumer
	// outside it still might.
	DeadMedium DeadConfidence = "medium"
	// DeadLow has no resolved caller, but something suggests a caller the
	// static graph cannot see; Rule and Reason say what.
	DeadLow DeadConfidence = "low"
)

// DeadRule names why a low-confidence finding may still have a caller.
type DeadRule string

// Reasons a function or method no edge reaches may still be called.
const (
	// DeadUnresolved is a name some use the resolver could not bind
	// carries: a call on a receiver of unknown type, or one that matched
	// several definitions.
	DeadUnresolved DeadRule = "unresolved"
	// DeadTwin is a name a called symbol shares: the resolver may have bound
	// this one's calls to that one.
	DeadTwin DeadRule = "twin"
	// DeadDecorated carries a decorator or annotation: a framework may
	// register it.
	DeadDecorated DeadRule = "decorated"
	// DeadOverriding is a method of a class that extends or implements
	// another, so a call through that type may reach it.
	DeadOverriding DeadRule = "overriding"
	// DeadMentioned has a name the source writes elsewhere.
	DeadMentioned DeadRule = "mentioned"
)

// DeadSymbol is a function or method nothing in the graph calls or references.
type DeadSymbol struct {
	Node       NodeV1
	Confidence DeadConfidence
	// Rule is set for a low-confidence finding.
	Rule   DeadRule
	Reason string
}

// DeadExclusions counts, by reason, the functions and methods no edge
// reaches that FindDeadCode leaves out because something outside the graph
// does reach them, or no finding about them could be checked.
type DeadExclusions struct {
	// Copies are test, fixture, testdata and vendored code, by path.
	Copies int `json:"copies"`
	// Generated is code whose file says a tool wrote it.
	Generated int `json:"generated"`
	// EntryPoints are main, init, constructors and dunder methods.
	EntryPoints int `json:"entryPoints"`
	// RuntimeNames are methods a runtime or standard interface calls by
	// name: String, Error, MarshalJSON, Close.
	RuntimeNames int `json:"runtimeNames"`
	// Declarations are interface methods: a call is dispatched to an
	// implementation.
	Declarations int `json:"declarations"`
	// Unreadable sit in a file that could not be read, so their name could
	// not be checked against the source.
	Unreadable int `json:"unreadable"`
}

// DeadReport is the dead code FindDeadCode found, and what it left out.
type DeadReport struct {
	Symbols  []DeadSymbol
	Excluded DeadExclusions
}

// runtimeMethods are method names a runtime, framework or standard interface
// invokes by name: fmt.Stringer, io.Closer, json.Marshaler, React, Java's
// Object, Rust's Display, and the like.
var runtimeMethods = map[string]bool{
	"String": true, "Error": true, "GoString": true, "Format": true, "Unwrap": true, "Is": true, "As": true,
	"MarshalJSON": true, "UnmarshalJSON": true, "MarshalText": true, "UnmarshalText": true,
	"MarshalBinary": true, "UnmarshalBinary": true, "MarshalYAML": true, "UnmarshalYAML": true,
	"ServeHTTP": true, "Read": true, "Write": true, "Close": true, "Seek": true, "ReadFrom": true, "WriteTo": true,
	"Len": true, "Less": true, "Swap": true, "Scan": true, "Value": true, "Lock": true, "Unlock": true,
	"toString": true, "equals": true, "hashCode": true, "compareTo": true, "run": true, "call": true,
	"close": true, "iterator": true, "apply": true, "accept": true, "toJSON": true, "render": true,
	"componentDidMount": true, "componentDidUpdate": true, "componentWillUnmount": true, "ngOnInit": true, "ngOnDestroy": true,
	"setUp": true, "tearDown": true, "setUpClass": true, "tearDownClass": true,
	"fmt": true, "drop": true, "from": true, "deref": true, "eq": true, "cmp": true, "partial_cmp": true,
	"hash": true, "clone": true, "default": true, "next": true, "poll": true,
}

// FindDeadCode lists the production functions and methods under the in
// prefix that no edge calls or references, most certain first, and counts
// those it leaves out. Code the runtime reaches by itself — entry points,
// methods a standard interface calls by name, interface declarations,
// methods that implement an interface — is never listed. A finding is low
// when something suggests a caller the graph cannot see: a use of its name
// the resolver could not bind, a same-named symbol that is called, a
// decorator, a supertype, or its name written elsewhere in the source, which
// repoRoot is read for. Exported code is medium, unless Go confines it to
// this module, in an internal package or a command.
func FindDeadCode(graph GraphV1, repoRoot, in string) (DeadReport, error) {
	prefix := normalizePathPrefix(in)
	if in != "" {
		if err := assertPrefixIndexed(graph, prefix); err != nil {
			return DeadReport{}, err
		}
	}
	byID := nodeIndex(graph)
	used := make(map[string]bool)
	usedNames := make(map[string]bool)
	extended := make(map[string]bool)
	owner := make(map[string]string)
	for _, edge := range graph.Edges {
		switch edge.Relation {
		case "calls", "references":
			if edge.Source != edge.Target {
				used[edge.Target] = true
				if target := byID[edge.Target]; target != nil && (target.Kind == "function" || target.Kind == "method") {
					usedNames[target.Name] = true
				}
			}
		case "implements":
			used[edge.Source] = true
			extended[edge.Source] = true
		case "extends":
			extended[edge.Source] = true
		case "contains":
			owner[edge.Target] = edge.Source
		}
	}
	commands := make(map[string]bool)
	for _, node := range graph.Nodes {
		if node.Kind == "function" && node.Name == "main" && strings.HasSuffix(node.Path, ".go") {
			commands[path.Dir(node.Path)] = true
		}
	}

	var report DeadReport
	var candidates []NodeV1
	names := make(map[string]bool)
	for _, node := range graph.Nodes {
		if (node.Kind != "function" && node.Kind != "method") || used[node.ID] || (in != "" && !pathUnderPrefix(node.Path, prefix)) {
			continue
		}
		switch excluded := &report.Excluded; {
		case node.Generated:
			excluded.Generated++
		case IsCopyPath(node.Path):
			excluded.Copies++
		case deadEntryPoint(node):
			excluded.EntryPoints++
		case node.Kind == "method" && runtimeMethods[node.Name]:
			excluded.RuntimeNames++
		case node.Kind == "method" && byID[owner[node.ID]] != nil && byID[owner[node.ID]].Kind == "interface":
			excluded.Declarations++
		default:
			candidates = append(candidates, node)
			names[node.Name] = true
		}
	}
	mentions, lines := countMentions(graph, repoRoot, names)

	for _, node := range candidates {
		fileLines, readable := lines[node.Path]
		if !readable {
			report.Excluded.Unreadable++
			continue
		}
		first, last, _ := parseLineSpan(node.Span)
		own := 0
		for _, line := range mentions.lines[node.Path+"\x00"+node.Name] {
			if line >= first && line <= last {
				own++
			}
		}
		goFile := strings.HasSuffix(node.Path, ".go")
		unresolved := graph.Meta.UnresolvedNames[node.Name]
		// A Go function is named bare or through its package, never through
		// a receiver.
		if node.Kind == "function" && goFile {
			unresolved.Untyped = 0
		}
		symbol := DeadSymbol{Node: node, Confidence: DeadLow}
		switch elsewhere := mentions.total[node.Name] - own; {
		case unresolved.Untyped > 0:
			symbol.Rule, symbol.Reason = DeadUnresolved, fmt.Sprintf("%d use%s of .%s on a receiver of unknown type may be its calls", unresolved.Untyped, pluralS(unresolved.Untyped), node.Name)
		case unresolved.Ambiguous > 0:
			symbol.Rule, symbol.Reason = DeadUnresolved, fmt.Sprintf("%d use%s of %s matched several definitions, this one among them", unresolved.Ambiguous, pluralS(unresolved.Ambiguous), node.Name)
		case usedNames[node.Name]:
			symbol.Rule, symbol.Reason = DeadTwin, "a symbol of the same name is called, and the resolver may have bound this one's calls to it"
		case deadDecorated(node, fileLines, first):
			symbol.Rule, symbol.Reason = DeadDecorated, "decorated or annotated: a framework may call it"
		case node.Kind == "method" && extended[owner[node.ID]] && !goFile:
			symbol.Rule, symbol.Reason = DeadOverriding, "its class extends or implements another, so it may be called through that type"
		case elsewhere > 0:
			symbol.Rule, symbol.Reason = DeadMentioned, fmt.Sprintf("its name appears %d× elsewhere, in a use the graph could not follow", elsewhere)
		case deadReachableOutside(node, commands):
			symbol.Confidence, symbol.Reason = DeadMedium, "exported, but nothing in this repository uses it"
		default:
			symbol.Confidence, symbol.Reason = DeadHigh, "nothing calls or references it, and its name appears nowhere else"
		}
		report.Symbols = append(report.Symbols, symbol)
	}
	rank := map[DeadConfidence]int{DeadHigh: 0, DeadMedium: 1, DeadLow: 2}
	slices.SortStableFunc(report.Symbols, func(a, b DeadSymbol) int {
		aLine, _, _ := parseLineSpan(a.Node.Span)
		bLine, _, _ := parseLineSpan(b.Node.Span)
		return cmp.Or(cmp.Compare(rank[a.Confidence], rank[b.Confidence]), cmp.Compare(a.Node.Path, b.Node.Path), cmp.Compare(aLine, bLine))
	})
	return report, nil
}

// deadReachableOutside reports whether code outside the repository may call
// node: an exported symbol, unless it is Go in an internal package or in a
// command — a directory of package main — which no other module imports.
func deadReachableOutside(node NodeV1, commands map[string]bool) bool {
	if !node.Exported {
		return false
	}
	if !strings.HasSuffix(node.Path, ".go") {
		return true
	}
	dir := path.Dir(node.Path)
	return !commands[dir] && !slices.Contains(strings.Split(dir, "/"), "internal")
}

// pluralS is "s" for any count but one.
func pluralS(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// deadEntryPoint reports a definition the runtime calls by itself.
func deadEntryPoint(node NodeV1) bool {
	name := node.Name
	switch {
	case name == "main", name == "constructor":
		return true
	case name == "init" && strings.HasSuffix(node.Path, ".go"):
		return true
	case strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__"):
		return true
	case node.Owner != nil && *node.Owner == name: // a Java or C++ constructor
		return true
	}
	return false
}

// deadDecorated reports a definition carrying an annotation in its signature
// (Java) or a decorator or attribute on the lines above it: `@route(...)`,
// `#[test]`. A multi-line decorator call is followed upwards through lines
// that end inside its argument list.
func deadDecorated(node NodeV1, lines []string, first int) bool {
	if node.Signature != nil && strings.Contains(*node.Signature, "@") {
		return true
	}
	for row := first - 2; row >= 0 && row >= first-10 && row < len(lines); row-- {
		line := strings.TrimSpace(lines[row])
		switch {
		case line == "":
			return false
		case strings.HasPrefix(line, "@"), strings.HasPrefix(line, "#["):
			return true
		case !strings.ContainsAny(line[len(line)-1:], ",()[]"):
			return false
		}
	}
	return false
}

// mentionIndex counts identifier occurrences of the names asked for: total
// over every indexed file, and the lines of each in each file.
type mentionIndex struct {
	total map[string]int
	lines map[string][]int // path + "\x00" + name → 1-based lines, in order
}

// countMentions scans every indexed file for the given names, and returns the
// lines of each file read, for decorator checks. A file that can no longer be
// read mentions nothing.
func countMentions(graph GraphV1, repoRoot string, names map[string]bool) (mentionIndex, map[string][]string) {
	index := mentionIndex{total: make(map[string]int), lines: make(map[string][]int)}
	files := make(map[string][]string)
	if len(names) == 0 {
		return index, files
	}
	for _, node := range graph.Nodes {
		if node.Kind != "file" {
			continue
		}
		text, readable, err := sourcefiles.Read(filepath.Join(repoRoot, filepath.FromSlash(node.Path)))
		if err != nil || !readable {
			continue
		}
		fileLines := strings.Split(text, "\n")
		files[node.Path] = fileLines
		for row, line := range fileLines {
			for _, word := range identifiers(line) {
				if names[word] {
					index.total[word]++
					key := node.Path + "\x00" + word
					index.lines[key] = append(index.lines[key], row+1)
				}
			}
		}
	}
	return index, files
}

// identifiers splits a line into identifier-like words: letters, digits,
// `_` and `$`, any non-ASCII byte counting as a letter.
func identifiers(line string) []string {
	var words []string
	start := -1
	for index := 0; index <= len(line); index++ {
		inWord := index < len(line) && (line[index] == '_' || line[index] == '$' || line[index] >= 0x80 ||
			('a' <= line[index] && line[index] <= 'z') || ('A' <= line[index] && line[index] <= 'Z') ||
			('0' <= line[index] && line[index] <= '9'))
		switch {
		case inWord && start < 0:
			start = index
		case !inWord && start >= 0:
			words = append(words, line[start:index])
			start = -1
		}
	}
	return words
}
