package graph

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
	sitter "github.com/tree-sitter/go-tree-sitter"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

type rawEdge struct {
	source    string
	relation  Relation
	file      string
	targetID  string
	specifier string
	name      string
	viaMember bool
}

type extractResult struct {
	language string
	nodes    []NodeV1
	rawEdges []rawEdge
}

type importBinding struct {
	name      string
	specifier string
}

type extractWalk struct {
	path     string
	source   []byte
	nodes    []NodeV1
	rawEdges []rawEdge
	covered  []bool
	minted   map[string]struct{}
	imported map[string]importBinding
}

func extractFile(rel, source string) (extractResult, error) {
	label, grammarName, ok := sourceGrammar(rel)
	if !ok {
		return extractResult{}, fmt.Errorf("unsupported source extension %q", strings.ToLower(filepath.Ext(rel)))
	}
	var grammar *sitter.Language
	switch grammarName {
	case "typescript":
		grammar = sitter.NewLanguage(typescript.LanguageTypescript())
	case "tsx":
		grammar = sitter.NewLanguage(typescript.LanguageTSX())
	case "javascript":
		grammar = sitter.NewLanguage(javascript.Language())
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(grammar); err != nil {
		return extractResult{}, fmt.Errorf("set %s grammar: %w", label, err)
	}
	sourceBytes := []byte(source)
	tree := parser.Parse(sourceBytes, nil)
	if tree == nil {
		return extractResult{}, fmt.Errorf("parse %q: tree-sitter returned no tree", rel)
	}
	defer tree.Close()
	root := tree.RootNode()
	chars := len(utf16.Encode([]rune(source)))
	file := NodeV1{
		ID: rel, Name: filepath.Base(rel), Kind: "file", Path: rel,
		Span: fmt.Sprintf("L1-L%d", root.EndPosition().Row+1), Exported: true,
		Origin: "ast", BodyHash: sourcefiles.Hash(source), Chars: &chars, SummaryState: "pending",
	}
	walk := extractWalk{
		path: rel, source: sourceBytes, nodes: []NodeV1{file},
		covered:  make([]bool, strings.Count(source, "\n")+1),
		minted:   map[string]struct{}{rel: {}},
		imported: make(map[string]importBinding),
	}
	var collectImports func(*sitter.Node, string)
	collectImports = func(node *sitter.Node, specifier string) {
		if node.Kind() == "import_statement" {
			specifier = importSpecifier(node, walk.source)
		}
		if node.Kind() == "import_specifier" && specifier != "" {
			name := walk.text(node.ChildByFieldName("name"))
			local := walk.text(node.ChildByFieldName("alias"))
			if local == "" {
				local = name
			}
			if name != "" && local != "" {
				walk.imported[local] = importBinding{name: name, specifier: specifier}
			}
		}
		for index := range node.NamedChildCount() {
			collectImports(node.NamedChild(index), specifier)
		}
	}
	collectImports(root, "")
	walk.walk(root, rel, nil, "", walk.imported)
	lines := strings.Split(source, "\n")
	residual := make([]string, 0, len(lines))
	for index, line := range lines {
		if index >= len(walk.covered) || !walk.covered[index] {
			residual = append(residual, line)
		}
	}
	walk.nodes[0].BodyText = new(searchBody(strings.Join(residual, " "), 16000))
	return extractResult{language: label, nodes: walk.nodes, rawEdges: walk.rawEdges}, nil
}

func (w *extractWalk) text(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	return node.Utf8Text(w.source)
}

func (w *extractWalk) walk(node *sitter.Node, parentID string, scope []string, enclosingClass string, imported map[string]importBinding) {
	kind := Kind("")
	name := ""
	headerEnd := node.EndByte()
	switch node.Kind() {
	case "class_declaration", "abstract_class_declaration":
		kind = "class"
	case "function_declaration", "generator_function_declaration":
		kind = "function"
	case "method_definition":
		kind = "method"
	case "interface_declaration":
		kind = "interface"
	case "type_alias_declaration":
		kind = "type"
	case "enum_declaration":
		kind = "enum"
	case "variable_declarator":
		value := node.ChildByFieldName("value")
		if value != nil {
			switch value.Kind() {
			case "arrow_function", "function", "function_expression", "generator_function":
				kind = "function"
				if body := value.ChildByFieldName("body"); body != nil {
					headerEnd = body.StartByte()
				}
			}
		}
	}
	if kind != "" {
		name = w.text(node.ChildByFieldName("name"))
		if name != "" {
			idPart := name
			base := w.path + "#" + strings.Join(append(slices.Clone(scope), idPart), ".")
			id := base
			for suffix := 2; ; suffix++ {
				if _, exists := w.minted[id]; !exists {
					break
				}
				id = base + "~" + strconv.Itoa(suffix)
			}
			w.minted[id] = struct{}{}
			if body := node.ChildByFieldName("body"); body != nil && node.Kind() != "variable_declarator" {
				headerEnd = body.StartByte()
			}
			signature := strings.Join(strings.Fields(string(w.source[node.StartByte():headerEnd])), " ")
			for _, suffix := range []string{"{", ":", "="} {
				if before, ok := strings.CutSuffix(signature, suffix); ok {
					signature = strings.TrimSpace(before)
					break
				}
			}
			var signatureValue *string
			if signature != "" {
				signatureValue = &signature
			}
			exported := false
			for ancestor := node.Parent(); ancestor != nil; ancestor = ancestor.Parent() {
				if ancestor.Kind() == "export_statement" {
					exported = true
					break
				}
			}
			var owner *string
			if kind == "method" && enclosingClass != "" {
				owner = &enclosingClass
			}
			start, end := node.StartPosition().Row, node.EndPosition().Row
			for row := start; row <= end && row < uint(len(w.covered)); row++ {
				w.covered[row] = true
			}
			w.nodes = append(w.nodes, NodeV1{
				ID: id, Name: name, Kind: kind, Path: w.path,
				Span: fmt.Sprintf("L%d-L%d", start+1, end+1), Signature: signatureValue,
				Exported: exported, Origin: "ast", BodyHash: sourcefiles.Hash(string(w.source[node.StartByte():node.EndByte()])),
				BodyText: new(searchBody(w.text(node), 5000)), Owner: owner, SummaryState: "pending",
			})
			w.rawEdges = append(w.rawEdges, rawEdge{source: parentID, relation: "contains", targetID: id, file: w.path})
			if kind == "class" {
				for index := range node.NamedChildCount() {
					heritage := node.NamedChild(index)
					if heritage.Kind() != "class_heritage" {
						continue
					}
					for clauseIndex := range heritage.NamedChildCount() {
						clause := heritage.NamedChild(clauseIndex)
						relation := Relation("")
						switch clause.Kind() {
						case "extends_clause":
							relation = "extends"
						case "implements_clause":
							relation = "implements"
						}
						if relation == "" {
							continue
						}
						for typeIndex := range clause.NamedChildCount() {
							typeNode := clause.NamedChild(typeIndex)
							if typeNode.Kind() == "identifier" || typeNode.Kind() == "type_identifier" {
								w.rawEdges = append(w.rawEdges, rawEdge{source: id, relation: relation, name: w.text(typeNode), file: w.path})
							}
						}
					}
				}
			}
			nextScope := append(slices.Clone(scope), idPart)
			nextClass := enclosingClass
			if kind == "class" {
				nextClass = name
			}
			nextImports := imported
			if kind == "function" || kind == "method" {
				nextImports = w.withoutShadowedImports(imported, node)
			}
			for index := range node.NamedChildCount() {
				w.walk(node.NamedChild(index), id, nextScope, nextClass, nextImports)
			}
			return
		}
	}
	if node.Kind() == "import_statement" {
		if specifier := importSpecifier(node, w.source); specifier != "" {
			w.rawEdges = append(w.rawEdges, rawEdge{source: w.path, relation: "imports", specifier: specifier, file: w.path})
		}
		return
	}
	if node.Kind() == "call_expression" {
		callee := node.ChildByFieldName("function")
		if callee != nil {
			name, viaMember := "", false
			switch callee.Kind() {
			case "identifier":
				name = w.text(callee)
			case "member_expression":
				name = w.text(callee.ChildByFieldName("property"))
				viaMember = name != ""
			}
			if name != "" {
				w.rawEdges = append(w.rawEdges, rawEdge{source: parentID, relation: "calls", name: name, viaMember: viaMember, file: w.path})
			}
		}
	} else if node.Kind() == "identifier" {
		parent := node.Parent()
		var calleeNode, nameNode *sitter.Node
		if parent != nil {
			calleeNode = parent.ChildByFieldName("function")
			nameNode = parent.ChildByFieldName("name")
		}
		callee := parent != nil && parent.Kind() == "call_expression" && calleeNode != nil && calleeNode.Id() == node.Id()
		declared := nameNode != nil && nameNode.Id() == node.Id()
		if !callee && !declared {
			if binding, ok := imported[w.text(node)]; ok {
				w.rawEdges = append(w.rawEdges, rawEdge{source: parentID, relation: "references", name: binding.name, specifier: binding.specifier, file: w.path})
			}
		}
	}
	for index := range node.NamedChildCount() {
		w.walk(node.NamedChild(index), parentID, scope, enclosingClass, imported)
	}
}

// withoutShadowedImports drops imported bindings hidden by locals in this definition.
func (w *extractWalk) withoutShadowedImports(imports map[string]importBinding, definition *sitter.Node) map[string]importBinding {
	if len(imports) == 0 {
		return imports
	}
	shadowed := make(map[string]struct{})
	definitionID := definition.Id()
	definitionValueID := uintptr(0)
	if value := definition.ChildByFieldName("value"); value != nil {
		definitionValueID = value.Id()
	}
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if node.Id() != definitionID && node.Id() != definitionValueID {
			switch node.Kind() {
			case "function_declaration", "generator_function_declaration", "method_definition", "arrow_function", "function_expression", "function":
				if name := node.ChildByFieldName("name"); name != nil && name.Kind() == "identifier" {
					shadowed[w.text(name)] = struct{}{}
				}
				return
			}
		}
		switch node.Kind() {
		case "variable_declarator":
			if name := node.ChildByFieldName("name"); name != nil && name.Kind() == "identifier" {
				shadowed[w.text(name)] = struct{}{}
			}
		case "required_parameter", "optional_parameter":
			if pattern := node.ChildByFieldName("pattern"); pattern != nil && pattern.Kind() == "identifier" {
				shadowed[w.text(pattern)] = struct{}{}
			}
		case "identifier":
			if parent := node.Parent(); parent != nil && parent.Kind() == "formal_parameters" {
				shadowed[w.text(node)] = struct{}{}
			}
		}
		for index := range node.NamedChildCount() {
			visit(node.NamedChild(index))
		}
	}
	visit(definition)
	for name := range shadowed {
		if _, ok := imports[name]; ok {
			filtered := make(map[string]importBinding, len(imports))
			for local, binding := range imports {
				if _, hidden := shadowed[local]; !hidden {
					filtered[local] = binding
				}
			}
			return filtered
		}
	}
	return imports
}

func importSpecifier(node *sitter.Node, source []byte) string {
	value := node.ChildByFieldName("source")
	if value == nil {
		return ""
	}
	for index := range value.NamedChildCount() {
		child := value.NamedChild(index)
		if child.Kind() == "string_fragment" {
			return child.Utf8Text(source)
		}
	}
	return strings.Trim(value.Utf8Text(source), "\"'` ")
}

func searchBody(text string, max int) string {
	normalized := strings.Join(strings.Fields(text), " ")
	units := 0
	for index, r := range normalized {
		if units+utf16.RuneLen(r) > max {
			return normalized[:index]
		}
		units += utf16.RuneLen(r)
	}
	return normalized
}
