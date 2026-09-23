package graph

import (
	"fmt"
	"slices"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

var phpKinds = map[string]Kind{
	"function_definition":   "function",
	"method_declaration":    "method",
	"class_declaration":     "class",
	"interface_declaration": "interface",
	"trait_declaration":     "trait",
	"enum_declaration":      "enum",
}

// lastBackslashSegment is the TypeScript `/^.*\\/` replacement: the text after
// the final backslash.
func lastBackslashSegment(text string) string {
	if index := strings.LastIndex(text, "\\"); index >= 0 {
		return text[index+1:]
	}
	return text
}

func (x *extractor) describePHP(node *sitter.Node, ctx walkCtx) *defDescriptor {
	switch node.Kind() {
	case "anonymous_function", "arrow_function":
		return &defDescriptor{name: phpClosureName(node, x.source), kind: "function", headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node}
	case "anonymous_class":
		return &defDescriptor{name: "{anonymous}", kind: "class", headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node}
	}
	kind, ok := phpKinds[node.Kind()]
	if !ok {
		return nil
	}
	name := x.text(node.ChildByFieldName("name"))
	if name == "" {
		return nil
	}
	if kind == "function" && ctx.enclosingKind == "enum" {
		kind = "method"
	}
	return &defDescriptor{name: name, kind: kind, headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node}
}

// phpClosureName is the variable a closure is assigned to, else `{closure}`.
func phpClosureName(node *sitter.Node, source []byte) string {
	if parent := node.Parent(); parent != nil && parent.Kind() == "assignment_expression" && sameNode(parent.ChildByFieldName("right"), node) {
		if left := parent.ChildByFieldName("left"); left != nil && left.Kind() == "variable_name" {
			return strings.TrimPrefix(nodeText(left, source), "$")
		}
	}
	return "{closure}"
}

func (x *extractor) phpExported(node *sitter.Node) bool {
	if visibility := namedChildOfKind(node, "visibility_modifier"); visibility != nil {
		return x.text(visibility) == "public"
	}
	return true
}

func (x *extractor) phpHeritage(node *sitter.Node, classID string, ctx walkCtx) []rawEdge {
	var edges []rawEdge
	for _, clause := range namedChildren(node) {
		relation := map[string]Relation{"base_clause": "extends", "class_interface_clause": "implements"}[clause.Kind()]
		if relation == "" {
			continue
		}
		for _, target := range namedChildrenOfKind(clause, "name", "qualified_name") {
			edges = append(edges, rawEdge{source: classID, relation: relation, name: lastBackslashSegment(x.text(target)), file: ctx.rel})
		}
	}
	return edges
}

func (x *extractor) phpAttributeReferences(node *sitter.Node, sourceID string, ctx walkCtx) []rawEdge {
	var edges []rawEdge
	for _, list := range namedChildrenOfKind(node, "attribute_list") {
		for _, group := range namedChildrenOfKind(list, "attribute_group") {
			for _, attribute := range namedChildrenOfKind(group, "attribute") {
				name := attribute.ChildByFieldName("name")
				if name == nil {
					name = namedChildOfKind(attribute, "name", "qualified_name")
				}
				if name == nil {
					continue
				}
				edge := rawEdge{source: sourceID, relation: "references", name: x.text(name), file: ctx.rel}
				switch imported, ok := ctx.imported[x.text(name)]; {
				case name.Kind() == "qualified_name":
					fqn := strings.TrimPrefix(x.text(name), "\\")
					edge.name, edge.specifier = lastBackslashSegment(fqn), fqn
				case ok:
					edge.name, edge.specifier = imported.name, imported.specifier
				}
				edges = append(edges, edge)
			}
		}
	}
	return edges
}

func (x *extractor) phpCallee(node *sitter.Node) (callee, bool) {
	if node.Kind() == "function_call_expression" {
		function := node.ChildByFieldName("function")
		name := ""
		switch {
		case function == nil:
		case function.Kind() == "name":
			name = x.text(function)
		case function.Kind() == "qualified_name":
			name = lastBackslashSegment(x.text(function))
		}
		return callee{name: name}, name != ""
	}
	name := node.ChildByFieldName("name")
	if name == nil {
		return callee{}, false
	}
	if node.Kind() == "scoped_call_expression" {
		return callee{name: x.text(name), viaMember: true, receiver: x.phpScopeReceiver(node.ChildByFieldName("scope"))}, true
	}
	receiver := ""
	if object := node.ChildByFieldName("object"); object != nil && object.Kind() == "variable_name" {
		receiver = x.text(object)
		if receiver == "$this" {
			receiver = "this"
		}
	}
	return callee{name: x.text(name), viaMember: true, receiver: receiver}, true
}

func (x *extractor) phpScopeReceiver(scope *sitter.Node) string {
	if scope == nil {
		return ""
	}
	text := x.text(scope)
	switch {
	case scope.Kind() == "relative_scope" || text == "self" || text == "static" || text == "parent":
		return "self"
	case scope.Kind() == "name":
		return text
	case scope.Kind() == "qualified_name":
		return lastBackslashSegment(text)
	}
	return ""
}

func (x *extractor) phpImportSpecifier(node *sitter.Node) string {
	if name := namedChildOfKind(node, "qualified_name", "name"); name != nil {
		return strings.TrimPrefix(x.text(name), "\\")
	}
	return ""
}

func (x *extractor) collectPHPImportedSymbols(root *sitter.Node, imported map[string]importBinding) {
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if node.Kind() != "namespace_use_declaration" {
			for _, child := range namedChildren(node) {
				visit(child)
			}
			return
		}
		prefix := strings.TrimSuffix(x.text(namedChildOfKind(node, "namespace_name")), "\\")
		var clauses []*sitter.Node
		for _, child := range namedChildren(node) {
			switch child.Kind() {
			case "namespace_use_clause":
				clauses = append(clauses, child)
			case "namespace_use_group":
				clauses = append(clauses, namedChildrenOfKind(child, "namespace_use_clause")...)
			}
		}
		for _, clause := range clauses {
			names := namedChildrenOfKind(clause, "name")
			qualified := namedChildOfKind(clause, "qualified_name")
			var fqn, name string
			switch {
			case qualified != nil:
				fqn = strings.TrimPrefix(x.text(qualified), "\\")
				name = lastBackslashSegment(fqn)
			case len(names) > 0:
				name = x.text(names[0])
				fqn = name
				if prefix != "" {
					fqn = prefix + "\\" + name
				}
			default:
				continue
			}
			local := name
			switch {
			case qualified != nil && len(names) >= 1:
				local = x.text(names[len(names)-1])
			case qualified == nil && len(names) >= 2:
				local = x.text(names[1])
			}
			imported[local] = importBinding{name: name, specifier: fqn}
		}
	}
	visit(root)
}

// phpCollapsedEnumName recognizes the ERROR node tree-sitter-php 0.23 produces
// for an enum holding a `const`, returning the enum's name.
func (x *extractor) phpCollapsedEnumName(node *sitter.Node) string {
	if node.Kind() != "ERROR" || namedChildOfKind(node, "enum_case") == nil {
		return ""
	}
	return x.text(namedChildOfKind(node, "name"))
}

func phpCollapsedEnumHold(node *sitter.Node) bool {
	return slices.Contains([]string{"const_declaration", "function_definition", "method_declaration", "ERROR"}, node.Kind())
}

func (x *extractor) phpCollapsedEnumClose(node *sitter.Node) bool {
	return node.Kind() == "ERROR" && strings.TrimSpace(x.text(node)) == "}"
}

func (x *extractor) walkPHPChildren(children []*sitter.Node, ctx walkCtx) {
	for index := 0; index < len(children); {
		node := children[index]
		name := x.phpCollapsedEnumName(node)
		if name == "" {
			x.walk(node, ctx)
			index++
			continue
		}
		group := []*sitter.Node{node}
		next := index + 1
		for next < len(children) && phpCollapsedEnumHold(children[next]) {
			group = append(group, children[next])
			next++
			if x.phpCollapsedEnumClose(children[next-1]) {
				break
			}
		}
		x.emitPHPCollapsedEnum(name, node, group, ctx)
		index = next
	}
}

func (x *extractor) emitPHPCollapsedEnum(name string, errorNode *sitter.Node, group []*sitter.Node, ctx walkCtx) {
	last := group[len(group)-1]
	id := x.mintID(ctx.rel + "#" + strings.Join(append(slices.Clone(ctx.scope), name), "."))
	body := string(x.source[errorNode.StartByte():last.EndByte()])
	x.nodes = append(x.nodes, NodeV1{
		ID: id, Name: name, Kind: "enum", Path: ctx.rel,
		Span:      fmt.Sprintf("L%d-L%d", errorNode.StartPosition().Row+1, last.EndPosition().Row+1),
		Signature: new("enum " + name), Exported: true, Origin: "ast",
		BodyHash: sourcefiles.Hash(body), BodyText: new(searchBody(body, maxBodyChars)), SummaryState: "pending",
	})
	x.edges = append(x.edges, rawEdge{source: ctx.parentID, relation: "contains", targetID: id, file: ctx.rel})
	child := ctx
	child.scope = append(slices.Clone(ctx.scope), name)
	child.enclosingKind = "enum"
	child.parentID = id
	for _, member := range group {
		switch {
		case x.phpCollapsedEnumClose(member):
		case x.phpCollapsedEnumName(member) != "":
			x.walkPHPChildren(namedChildren(member), child)
		default:
			x.walk(member, child)
		}
	}
}

func (w *bindingWalk) phpDefName(node *sitter.Node) (string, bool) {
	switch node.Kind() {
	case "class_declaration", "interface_declaration", "trait_declaration", "enum_declaration", "method_declaration", "function_definition":
		name := node.ChildByFieldName("name")
		return w.text(name), name != nil
	case "anonymous_function", "arrow_function":
		return phpClosureName(node, w.source), true
	case "anonymous_class":
		return "{anonymous}", true
	}
	return "", false
}

func (w *bindingWalk) phpTypeName(node *sitter.Node) string {
	switch {
	case node == nil:
	case node.Kind() == "named_type" || node.Kind() == "optional_type":
		if node.NamedChildCount() > 0 {
			return w.phpTypeName(node.NamedChild(0))
		}
	case node.Kind() == "name":
		return w.text(node)
	case node.Kind() == "qualified_name":
		return lastBackslashSegment(w.text(node))
	}
	return ""
}

func (w *bindingWalk) handlePHP(node *sitter.Node, scope []string) {
	scopePath := strings.Join(scope, ".")
	switch node.Kind() {
	case "simple_parameter":
		typeName, name := w.phpTypeName(node.ChildByFieldName("type")), node.ChildByFieldName("name")
		if typeName != "" && name != nil && name.Kind() == "variable_name" {
			w.bindings.set(scopePath, w.text(name), typeName)
		}
	case "assignment_expression":
		left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
		if left == nil || left.Kind() != "variable_name" || right == nil || right.Kind() != "object_creation_expression" {
			return
		}
		if class := namedChildOfKind(right, "name", "qualified_name"); class != nil {
			w.bindings.set(scopePath, w.text(left), lastBackslashSegment(w.text(class)))
		}
	}
}
