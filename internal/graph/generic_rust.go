package graph

import (
	_ "embed"
	"fmt"
	"path"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
	sitter "github.com/tree-sitter/go-tree-sitter"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

//go:embed queries/rust.scm
var rustTags string

// extractRust implements the generic tags-query tier for Rust.
func extractRust(rel, source string) (extractResult, error) {
	lang := sitter.NewLanguage(rust.Language())
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		return extractResult{}, fmt.Errorf("set rust grammar: %w", err)
	}
	data := []byte(source)
	tree := parser.Parse(data, nil)
	if tree == nil {
		return extractResult{}, fmt.Errorf("parse %q: tree-sitter returned no tree", rel)
	}
	defer tree.Close()
	query, queryErr := sitter.NewQuery(lang, rustTags)
	if queryErr != nil {
		return extractResult{}, fmt.Errorf("compile rust tags query: %s", queryErr.Message)
	}
	defer query.Close()
	cursor := sitter.NewQueryCursor()
	defer cursor.Close()

	chars := len(data)
	nodes := []NodeV1{{
		ID: rel, Name: path.Base(rel), Kind: "file", Path: rel,
		Span:     fmt.Sprintf("L1-L%d", strings.Count(source, "\n")+1),
		Exported: true, Origin: "generic", BodyHash: sourcefiles.Hash(source),
		Chars: &chars, SummaryState: "pending",
	}}
	edges := make([]rawEdge, 0)
	defNameAt := make(map[uint]struct{})
	seen := make(map[uint]struct{})
	minted := map[string]struct{}{rel: {}}
	type definition struct {
		id         string
		start, end uint
	}
	defs := make([]definition, 0)
	type reference struct {
		name string
		at   uint
	}
	calls := make([]reference, 0)
	lines := strings.Split(source, "\n")
	names := query.CaptureNames()
	matches := cursor.Matches(query, tree.RootNode(), data)
	for match := matches.Next(); match != nil; match = matches.Next() {
		var defNode, nameNode, callNode *sitter.Node
		kind := Kind("")
		for _, capture := range match.Captures {
			node := capture.Node
			switch name := names[capture.Index]; {
			case name == "name":
				nameNode = &node
			case strings.HasPrefix(name, "definition."):
				defNode = &node
				kind = Kind(strings.TrimPrefix(name, "definition."))
			case name == "reference.call":
				callNode = &node
			}
		}
		if defNode != nil && nameNode != nil {
			defNameAt[nameNode.StartByte()] = struct{}{}
			if _, duplicate := seen[defNode.StartByte()]; !duplicate {
				seen[defNode.StartByte()] = struct{}{}
				name := nameNode.Utf8Text(data)
				base := rel + "#" + name
				id := base
				for suffix := 2; ; suffix++ {
					if _, taken := minted[id]; !taken {
						break
					}
					id = fmt.Sprintf("%s~%d", base, suffix)
				}
				minted[id] = struct{}{}
				start, end := defNode.StartPosition().Row, defNode.EndPosition().Row
				sig := strings.TrimSpace(lines[start])
				sig = strings.TrimSpace(strings.TrimSuffix(sig, "{"))
				body := string(data[defNode.StartByte():defNode.EndByte()])
				node := NodeV1{
					ID: id, Name: name, Kind: kind, Path: rel,
					Span:     fmt.Sprintf("L%d-L%d", start+1, end+1),
					Exported: true, Origin: "generic", BodyHash: sourcefiles.Hash(body),
					BodyText: new(searchBody(body, maxBodyChars)), SummaryState: "pending",
				}
				if sig != "" {
					node.Signature = &sig
				}
				nodes = append(nodes, node)
				defs = append(defs, definition{id: id, start: defNode.StartByte(), end: defNode.EndByte()})
			}
		}
		if callNode != nil && nameNode != nil {
			calls = append(calls, reference{name: nameNode.Utf8Text(data), at: nameNode.StartByte()})
		}
	}
	for _, call := range calls {
		if _, isDef := defNameAt[call.at]; isDef {
			continue
		}
		sourceID, width := rel, uint(^uint(0))
		for _, def := range defs {
			if def.start <= call.at && call.at < def.end && def.end-def.start < width {
				sourceID, width = def.id, def.end-def.start
			}
		}
		edges = append(edges, rawEdge{source: sourceID, relation: "calls", name: call.name, file: rel})
	}
	var visitUses func(*sitter.Node)
	visitUses = func(node *sitter.Node) {
		if node.Kind() == "use_declaration" {
			if spec, ok := rustUseModule(node.Utf8Text(data)); ok {
				edges = append(edges, rawEdge{source: rel, relation: "imports", specifier: spec, file: rel})
			}
		}
		for _, child := range namedChildren(node) {
			visitUses(child)
		}
	}
	visitUses(tree.RootNode())
	return extractResult{language: "rust", nodes: nodes, rawEdges: edges}, nil
}

func rustUseModule(raw string) (string, bool) {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "use")), ";"))
	if before, _, found := strings.Cut(s, "{"); found {
		s = strings.TrimSuffix(strings.TrimSpace(before), "::")
	} else if before, _, found := strings.Cut(s, " as "); found {
		s = before
	}
	if s != "crate" && !strings.HasPrefix(s, "crate::") || strings.Contains(s, "*") {
		return "", false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(s, "crate"), "::")
	if rest == "" {
		return "crate", true
	}
	return "crate/" + strings.ReplaceAll(strings.Join(strings.Fields(rest), ""), "::", "/"), true
}
