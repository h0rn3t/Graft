package graph

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// DeadConfidence says how sure a dead-code finding is.
type DeadConfidence string

// Dead-code confidence classes, most certain first.
const (
	// DeadHigh is unexported code whose name appears nowhere else.
	DeadHigh DeadConfidence = "high"
	// DeadMedium is exported code nothing in the repository uses; a consumer
	// outside it still might.
	DeadMedium DeadConfidence = "medium"
	// DeadLow has no resolved caller, but something suggests a caller the
	// static graph cannot see; Reason says what.
	DeadLow DeadConfidence = "low"
)

// DeadSymbol is a function or method nothing in the graph calls or references.
type DeadSymbol struct {
	Node       NodeV1
	Confidence DeadConfidence
	Reason     string
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
// prefix that no edge calls or references, most certain first. Entry points
// the runtime calls (main, init, constructors, dunder methods) and methods
// that implement an interface are never listed. repoRoot is read to tell
// whether a name is mentioned anywhere outside its own definition, which a
// call through an untyped receiver would do.
func FindDeadCode(graph GraphV1, repoRoot, in string) ([]DeadSymbol, error) {
	prefix := normalizePathPrefix(in)
	if in != "" {
		if err := assertPrefixIndexed(graph, prefix); err != nil {
			return nil, err
		}
	}
	used := make(map[string]bool)
	extended := make(map[string]bool)
	for _, edge := range graph.Edges {
		switch edge.Relation {
		case "calls", "references":
			if edge.Source != edge.Target {
				used[edge.Target] = true
			}
		case "implements":
			used[edge.Source] = true
			extended[edge.Source] = true
		case "extends":
			extended[edge.Source] = true
		}
	}
	var candidates []NodeV1
	names := make(map[string]bool)
	for _, node := range graph.Nodes {
		if (node.Kind != "function" && node.Kind != "method") || used[node.ID] || IsCopyPath(node.Path) ||
			(in != "" && !pathUnderPrefix(node.Path, prefix)) || deadEntryPoint(node) {
			continue
		}
		candidates = append(candidates, node)
		names[node.Name] = true
	}
	mentions, lines := countMentions(graph, repoRoot, names)

	dead := make([]DeadSymbol, 0, len(candidates))
	for _, node := range candidates {
		first, last, _ := parseLineSpan(node.Span)
		own := 0
		for _, line := range mentions.lines[node.Path+"\x00"+node.Name] {
			if line >= first && line <= last {
				own++
			}
		}
		owner := ""
		if dot := strings.LastIndexByte(node.ID, '.'); node.Kind == "method" && dot > strings.IndexByte(node.ID, '#') {
			owner = node.ID[:dot]
		}
		symbol := DeadSymbol{Node: node}
		switch elsewhere := mentions.total[node.Name] - own; {
		case elsewhere > 0:
			symbol.Confidence, symbol.Reason = DeadLow, fmt.Sprintf("its name appears %d× elsewhere, maybe through a receiver the graph could not type", elsewhere)
		case deadDecorated(node, lines[node.Path], first):
			symbol.Confidence, symbol.Reason = DeadLow, "decorated or annotated: a framework may call it"
		case runtimeMethods[node.Name] && node.Kind == "method":
			symbol.Confidence, symbol.Reason = DeadLow, "a runtime or standard interface may call it by name"
		case owner != "" && extended[owner] && !strings.HasSuffix(node.Path, ".go"):
			symbol.Confidence, symbol.Reason = DeadLow, "its class extends or implements another, so it may be called through that type"
		case node.Exported:
			symbol.Confidence, symbol.Reason = DeadMedium, "exported, but nothing in this repository uses it"
		default:
			symbol.Confidence, symbol.Reason = DeadHigh, "nothing calls or references it, and its name appears nowhere else"
		}
		dead = append(dead, symbol)
	}
	rank := map[DeadConfidence]int{DeadHigh: 0, DeadMedium: 1, DeadLow: 2}
	slices.SortStableFunc(dead, func(a, b DeadSymbol) int {
		aLine, _, _ := parseLineSpan(a.Node.Span)
		bLine, _, _ := parseLineSpan(b.Node.Span)
		return cmp.Or(cmp.Compare(rank[a.Confidence], rank[b.Confidence]), cmp.Compare(a.Node.Path, b.Node.Path), cmp.Compare(aLine, bLine))
	})
	return dead, nil
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
