package graph

import (
	"cmp"
	"slices"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

var javaKinds = map[string]Kind{
	"class_declaration":                   "class",
	"interface_declaration":               "interface",
	"enum_declaration":                    "enum",
	"record_declaration":                  "struct",
	"annotation_type_declaration":         "interface",
	"annotation_type_element_declaration": "method",
	"method_declaration":                  "method",
	"constructor_declaration":             "method",
}

// javaTypeKinds are the declarations that own nested members and heritage.
var javaTypeKinds = []Kind{"class", "interface", "enum", "struct"}

var javaTypeDeclarations = []string{"class_declaration", "interface_declaration", "enum_declaration", "record_declaration", "annotation_type_declaration"}

// children lists every child, anonymous tokens included.
func children(node *sitter.Node) []*sitter.Node {
	all := make([]*sitter.Node, 0, node.ChildCount())
	for index := range node.ChildCount() {
		all = append(all, node.Child(index))
	}
	return all
}

// namedChildOfKind is the first named child whose kind is one of kinds.
func namedChildOfKind(node *sitter.Node, kinds ...string) *sitter.Node {
	if node == nil {
		return nil
	}
	for _, child := range namedChildren(node) {
		if slices.Contains(kinds, child.Kind()) {
			return child
		}
	}
	return nil
}

func namedChildrenOfKind(node *sitter.Node, kinds ...string) []*sitter.Node {
	if node == nil {
		return nil
	}
	var matches []*sitter.Node
	for _, child := range namedChildren(node) {
		if slices.Contains(kinds, child.Kind()) {
			matches = append(matches, child)
		}
	}
	return matches
}

func (x *extractor) describeJava(node *sitter.Node) *defDescriptor {
	kind, ok := javaKinds[node.Kind()]
	if !ok {
		return nil
	}
	name := x.text(node.ChildByFieldName("name"))
	if name == "" {
		return nil
	}
	desc := &defDescriptor{name: name, kind: kind, headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node}
	if node.Kind() == "method_declaration" || node.Kind() == "constructor_declaration" {
		if parameters := node.ChildByFieldName("parameters"); parameters != nil {
			declared := namedChildrenOfKind(parameters, "formal_parameter", "spread_parameter")
			desc.arity = new(len(declared))
			desc.variadic = slices.ContainsFunc(declared, func(p *sitter.Node) bool { return p.Kind() == "spread_parameter" })
		}
	}
	return desc
}

func javaExported(node *sitter.Node) bool {
	modifiers := namedChildOfKind(node, "modifiers")
	return modifiers != nil && slices.ContainsFunc(children(modifiers), func(c *sitter.Node) bool {
		return c.Kind() == "public" || c.Kind() == "protected"
	})
}

func (x *extractor) javaHeritage(node *sitter.Node, classID string, ctx walkCtx) []rawEdge {
	typeParameters := make(map[string]struct{})
	if parameters := node.ChildByFieldName("type_parameters"); parameters != nil {
		var visit func(*sitter.Node)
		visit = func(n *sitter.Node) {
			if n.Kind() == "type_identifier" {
				typeParameters[x.text(n)] = struct{}{}
			}
			for _, child := range namedChildren(n) {
				visit(child)
			}
		}
		visit(parameters)
	}
	var edges []rawEdge
	for _, clause := range namedChildren(node) {
		relation := Relation("")
		switch clause.Kind() {
		case "superclass":
			relation = "extends"
		case "super_interfaces", "extends_interfaces":
			relation = "implements"
		default:
			continue
		}
		entries := namedChildren(clause)
		if list := namedChildOfKind(clause, "type_list"); list != nil {
			entries = namedChildren(list)
		}
		for _, entry := range entries {
			name := x.javaSupertypeName(entry)
			if _, isParameter := typeParameters[name]; name == "" || isParameter {
				continue
			}
			edges = append(edges, rawEdge{source: classID, relation: relation, name: name, file: ctx.rel})
		}
	}
	return edges
}

// javaSupertypeName erases type arguments and keeps a qualified name whole.
func (x *extractor) javaSupertypeName(node *sitter.Node) string {
	switch {
	case node == nil:
	case node.Kind() == "generic_type" && node.NamedChildCount() > 0:
		return x.javaSupertypeName(node.NamedChild(0))
	case node.Kind() == "scoped_type_identifier", node.Kind() == "type_identifier":
		return x.text(node)
	}
	return ""
}

// javaConstructedTypeName names what a `new` constructs; a qualified name is dropped.
func (x *extractor) javaConstructedTypeName(node *sitter.Node) string {
	switch {
	case node == nil:
	case node.Kind() == "generic_type" && node.NamedChildCount() > 0:
		return x.javaConstructedTypeName(node.NamedChild(0))
	case node.Kind() == "type_identifier":
		return x.text(node)
	}
	return ""
}

func (x *extractor) javaAnnotationReferences(node *sitter.Node, sourceID string, ctx walkCtx) []rawEdge {
	var edges []rawEdge
	for _, annotation := range namedChildrenOfKind(namedChildOfKind(node, "modifiers"), "marker_annotation", "annotation") {
		name := annotation.ChildByFieldName("name")
		if name != nil && (name.Kind() == "identifier" || name.Kind() == "scoped_identifier") {
			edges = append(edges, rawEdge{source: sourceID, relation: "references", name: x.text(name), file: ctx.rel})
		}
	}
	return edges
}

func (x *extractor) javaCallee(node *sitter.Node) (callee, bool) {
	if node.Kind() == "object_creation_expression" {
		name := x.javaConstructedTypeName(node.ChildByFieldName("type"))
		return callee{name: name}, name != ""
	}
	name := node.ChildByFieldName("name")
	if name == nil {
		return callee{}, false
	}
	object := node.ChildByFieldName("object")
	if object == nil {
		return callee{name: x.text(name), viaMember: true, receiver: "this"}, true
	}
	return callee{name: x.text(name), viaMember: true, receiver: x.javaReceiver(object)}, true
}

func (x *extractor) javaReceiver(object *sitter.Node) string {
	switch object.Kind() {
	case "identifier":
		return x.text(object)
	case "this":
		return "this"
	case "field_access":
		inner, field := object.ChildByFieldName("object"), object.ChildByFieldName("field")
		if inner != nil && inner.Kind() == "this" && field != nil {
			return "this." + x.text(field)
		}
	}
	return ""
}

func javaArgCount(node *sitter.Node) *int {
	if arguments := node.ChildByFieldName("arguments"); arguments != nil {
		return new(int(arguments.NamedChildCount()))
	}
	return nil
}

// javaAnonymousClass mints `{anonymous}` for `new Type() { … }` so its methods
// take that owner instead of the enclosing type's.
func (x *extractor) javaAnonymousClass(node *sitter.Node, ctx walkCtx) bool {
	body := namedChildOfKind(node, "class_body")
	if body == nil {
		return false
	}
	const idPart = "{anonymous}"
	id := x.mintID(ctx.rel + "#" + strings.Join(append(slices.Clone(ctx.scope), idPart), "."))
	text := x.text(node)
	x.nodes = append(x.nodes, NodeV1{
		ID: id, Name: idPart, Kind: "class", Path: ctx.rel,
		Span:      spanOf(node),
		Signature: cleanSignature(string(x.source[node.StartByte():body.StartByte()])),
		Origin:    "ast", BodyHash: sourcefiles.Hash(text), BodyText: new(searchBody(text, maxBodyChars)), SummaryState: "pending",
	})
	x.edges = append(x.edges, rawEdge{source: ctx.parentID, relation: "contains", targetID: id, file: ctx.rel})
	if super := x.javaConstructedTypeName(node.ChildByFieldName("type")); super != "" {
		x.edges = append(x.edges, rawEdge{source: id, relation: "implements", name: super, file: ctx.rel})
	}
	anonymous := ctx
	anonymous.scope = append(slices.Clone(ctx.scope), idPart)
	anonymous.enclosingKind = "class"
	anonymous.parentID = id
	anonymous.enclosingClass = idPart
	for _, child := range namedChildren(node) {
		if child.Kind() == "class_body" {
			x.walk(child, anonymous)
		} else {
			x.walk(child, ctx)
		}
	}
	return true
}

func (w *bindingWalk) javaTypeName(node *sitter.Node) string {
	switch {
	case node == nil:
	case node.Kind() == "type_identifier":
		if text := w.text(node); text != "var" {
			return text
		}
	case node.Kind() == "generic_type" && node.NamedChildCount() > 0:
		return w.javaTypeName(node.NamedChild(0))
	case node.Kind() == "scoped_type_identifier":
		return w.text(lastNamedChild(node))
	case node.Kind() == "array_type":
		element := node.ChildByFieldName("element")
		if element == nil {
			for _, child := range namedChildren(node) {
				if child.Kind() != "dimensions" {
					element = child
					break
				}
			}
		}
		return w.javaTypeName(element)
	}
	return ""
}

func (w *bindingWalk) javaNewTypeName(value *sitter.Node) string {
	if value == nil || value.Kind() != "object_creation_expression" {
		return ""
	}
	return w.javaTypeName(value.ChildByFieldName("type"))
}

func (w *bindingWalk) handleJava(node *sitter.Node, scope []string, classScope string) {
	scopePath := strings.Join(scope, ".")
	bind := func(name *sitter.Node, typeName string) {
		if typeName != "" && name != nil && name.Kind() == "identifier" {
			w.bindings.set(scopePath, w.text(name), typeName)
		}
	}
	switch node.Kind() {
	case "formal_parameter", "enhanced_for_statement":
		bind(node.ChildByFieldName("name"), w.javaTypeName(node.ChildByFieldName("type")))
	case "spread_parameter":
		var typeNode, name *sitter.Node
		for _, child := range namedChildren(node) {
			if child.Kind() == "variable_declarator" {
				if name == nil {
					name = child.ChildByFieldName("name")
				}
			} else if typeNode == nil {
				typeNode = child
			}
		}
		bind(name, w.javaTypeName(typeNode))
	case "resource":
		bind(node.ChildByFieldName("name"), cmp.Or(w.javaTypeName(node.ChildByFieldName("type")), w.javaNewTypeName(node.ChildByFieldName("value"))))
	case "catch_formal_parameter":
		typeNode := namedChildOfKind(namedChildOfKind(node, "catch_type"), "type_identifier", "scoped_type_identifier", "identifier")
		bind(node.ChildByFieldName("name"), w.javaTypeName(typeNode))
	case "local_variable_declaration", "field_declaration":
		isField := node.Kind() == "field_declaration"
		target := scopePath
		if isField {
			target = cmp.Or(classScope, scopePath)
		}
		for _, declarator := range namedChildrenOfKind(node, "variable_declarator") {
			name := declarator.ChildByFieldName("name")
			if name == nil || name.Kind() != "identifier" {
				continue
			}
			typeName := cmp.Or(w.javaTypeName(node.ChildByFieldName("type")), w.javaNewTypeName(declarator.ChildByFieldName("value")))
			if typeName == "" {
				continue
			}
			w.bindings.set(target, w.text(name), typeName)
			if isField {
				w.bindings.set(target, "self."+w.text(name), typeName)
			}
		}
	}
}
