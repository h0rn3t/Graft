package graph

import (
	_ "embed"
	"fmt"
	"path"
	"strings"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
	sitter "github.com/tree-sitter/go-tree-sitter"
	c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

//go:embed queries/rust.scm
var rustTags string

//go:embed queries/c.scm
var cTags string

//go:embed queries/cpp.scm
var cppTags string

//go:embed queries/ruby.scm
var rubyTags string

var genericNativeGrammars = map[string]struct {
	language func() *sitter.Language
	tags     *string
}{
	"rust": {func() *sitter.Language { return sitter.NewLanguage(rust.Language()) }, &rustTags},
	"c":    {func() *sitter.Language { return sitter.NewLanguage(c.Language()) }, &cTags},
	"cpp":  {func() *sitter.Language { return sitter.NewLanguage(cpp.Language()) }, &cppTags},
	"ruby": {func() *sitter.Language { return sitter.NewLanguage(ruby.Language()) }, &rubyTags},
}

// extractGenericTags implements the shared tags-query tier for native grammars.
func extractGenericTags(rel, source, name string, lang *sitter.Language, tags string) (extractResult, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		return extractResult{}, fmt.Errorf("set %s grammar: %w", name, err)
	}
	data := []byte(source)
	tree := parser.Parse(data, nil)
	if tree == nil {
		return extractResult{}, fmt.Errorf("parse %q: tree-sitter returned no tree", rel)
	}
	defer tree.Close()
	query, queryErr := sitter.NewQuery(lang, tags)
	if queryErr != nil {
		return extractResult{}, fmt.Errorf("compile %s tags query: %s", name, queryErr.Message)
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
			whole := defNode
			for parent := whole.Parent(); parent != nil && genericDefContainer(parent.Kind()); parent = whole.Parent() {
				whole = parent
			}
			if _, duplicate := seen[whole.StartByte()]; !duplicate {
				seen[whole.StartByte()] = struct{}{}
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
				start, end := whole.StartPosition().Row, whole.EndPosition().Row
				sig := strings.TrimSpace(lines[start])
				sig = strings.TrimSpace(strings.TrimSuffix(sig, "{"))
				body := string(data[whole.StartByte():whole.EndByte()])
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
				defs = append(defs, definition{id: id, start: whole.StartByte(), end: whole.EndByte()})
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
	var visitImports func(*sitter.Node)
	visitImports = func(node *sitter.Node) {
		if name == "rust" && node.Kind() == "use_declaration" {
			if spec, ok := rustUseModule(node.Utf8Text(data)); ok {
				edges = append(edges, rawEdge{source: rel, relation: "imports", specifier: spec, file: rel})
			}
		}
		if (name == "c" || name == "cpp") && node.Kind() == "preproc_include" {
			if pathNode := node.ChildByFieldName("path"); pathNode != nil {
				raw := pathNode.Utf8Text(data)
				if strings.HasPrefix(raw, "\"") {
					if spec := strings.TrimSpace(strings.Trim(raw, "\"")); spec != "" {
						edges = append(edges, rawEdge{source: rel, relation: "imports", specifier: spec, file: rel})
					}
				}
			}
		}
		for _, child := range namedChildren(node) {
			visitImports(child)
		}
	}
	visitImports(tree.RootNode())
	return extractResult{language: name, nodes: nodes, rawEdges: edges}, nil
}

func genericDefContainer(kind string) bool {
	return strings.HasSuffix(kind, "definition") || strings.HasSuffix(kind, "declaration") ||
		strings.HasSuffix(kind, "specifier") || strings.HasSuffix(kind, "_item")
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
