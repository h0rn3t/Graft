package graph

import (
	"cmp"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

var (
	kotlinTypeKinds = []Kind{"class", "interface", "enum"}
	// swiftTypeKinds includes "module": an extension body owns members of the
	// type it extends without becoming a type node itself.
	swiftTypeKinds = []Kind{"class", "struct", "enum", "interface", "module"}
)

// childBodyStart is where the first named child of one of kinds starts, else the node's end.
func childBodyStart(node *sitter.Node, kinds ...string) uint {
	if body := namedChildOfKind(node, kinds...); body != nil {
		return body.StartByte()
	}
	return node.EndByte()
}

func (x *extractor) describeKotlin(node *sitter.Node, ctx walkCtx) *defDescriptor {
	typeName := x.text(namedChildOfKind(node, "type_identifier"))
	switch node.Kind() {
	case "class_declaration":
		if typeName == "" {
			return nil
		}
		annotation := slices.ContainsFunc(namedChildrenOfKind(namedChildOfKind(node, "modifiers"), "class_modifier"), func(c *sitter.Node) bool {
			return x.text(c) == "annotation"
		})
		kind := Kind("class")
		switch {
		case namedChildOfKind(node, "enum_class_body") != nil:
			kind = "enum"
		case slices.ContainsFunc(children(node), func(c *sitter.Node) bool { return c.Kind() == "interface" }), annotation:
			kind = "interface"
		}
		return &defDescriptor{name: typeName, kind: kind, headerEnd: childBodyStart(node, "class_body", "enum_class_body"), hashNode: node}
	case "object_declaration":
		if typeName == "" {
			return nil
		}
		return &defDescriptor{name: typeName, kind: "class", headerEnd: childBodyStart(node, "class_body"), hashNode: node}
	case "function_declaration":
		name := x.text(namedChildOfKind(node, "simple_identifier"))
		if name == "" {
			return nil
		}
		kind := Kind("function")
		if slices.Contains(kotlinTypeKinds, ctx.enclosingKind) {
			kind = "method"
		}
		return &defDescriptor{name: name, kind: kind, headerEnd: childBodyStart(node, "function_body"), hashNode: node}
	case "secondary_constructor":
		if ctx.enclosingClass == "" {
			return nil
		}
		return &defDescriptor{name: ctx.enclosingClass, kind: "method", headerEnd: childBodyStart(node, "statements"), hashNode: node}
	case "type_alias":
		if typeName == "" {
			return nil
		}
		return &defDescriptor{name: typeName, kind: "type", headerEnd: node.EndByte(), hashNode: node}
	case "property_declaration":
		if ctx.enclosingKind != "" {
			return nil
		}
		name := x.text(namedChildOfKind(namedChildOfKind(node, "variable_declaration"), "simple_identifier"))
		if name == "" {
			return nil
		}
		return &defDescriptor{name: name, kind: "variable", headerEnd: node.EndByte(), hashNode: node}
	}
	return nil
}

func (x *extractor) kotlinExported(node *sitter.Node) bool {
	modifiers := namedChildOfKind(node, "modifiers")
	if modifiers == nil {
		return true
	}
	visibility := namedChildOfKind(modifiers, "visibility_modifier")
	return visibility == nil || x.text(visibility) == "public"
}

func (x *extractor) kotlinHeritage(node *sitter.Node, classID string, ctx walkCtx) []rawEdge {
	var edges []rawEdge
	for _, specifier := range namedChildrenOfKind(node, "delegation_specifier") {
		if target := namedChildOfKind(namedChildOfKind(specifier, "user_type"), "type_identifier"); target != nil {
			edges = append(edges, rawEdge{source: classID, relation: "extends", name: x.text(target), file: ctx.rel})
		}
	}
	return edges
}

// kotlinSwiftCallee reads `foo()` and `obj.foo()` from the shared
// call_expression → navigation_expression shape of both grammars.
func (x *extractor) kotlinSwiftCallee(node *sitter.Node, lang language) (callee, bool) {
	if node.NamedChildCount() == 0 {
		return callee{}, false
	}
	target := node.NamedChild(0)
	switch target.Kind() {
	case "simple_identifier":
		return callee{name: x.text(target)}, true
	case "navigation_expression":
	default:
		return callee{}, false
	}
	name := namedChildOfKind(namedChildOfKind(target, "navigation_suffix"), "simple_identifier")
	if name == nil {
		return callee{}, false
	}
	var receiver *sitter.Node
	if target.NamedChildCount() > 0 {
		receiver = target.NamedChild(0)
	}
	self, member := "this_expression", callee{name: x.text(name), viaMember: true}
	if lang == langSwift {
		self = "self_expression"
	}
	switch {
	case receiver == nil:
	case receiver.Kind() == "simple_identifier":
		member.receiver = x.text(receiver)
	case receiver.Kind() == self:
		member.receiver = map[language]string{langKotlin: "this", langSwift: "self"}[lang]
	case receiver.Kind() == "super_expression":
		member.receiver = "super"
	case lang == langSwift && receiver.Kind() == "navigation_expression":
		if receiver.NamedChildCount() > 0 && receiver.NamedChild(0).Kind() == "self_expression" {
			if field := namedChildOfKind(namedChildOfKind(receiver, "navigation_suffix"), "simple_identifier"); field != nil {
				member.receiver = "self." + x.text(field)
			}
		}
	}
	if lang == langKotlin && member.receiver == "" {
		return callee{}, false
	}
	return member, true
}

func (x *extractor) describeSwift(node *sitter.Node, ctx walkCtx) *defDescriptor {
	typeName := x.text(namedChildOfKind(node, "type_identifier"))
	switch node.Kind() {
	case "class_declaration":
		keyword := ""
		for _, child := range children(node) {
			if slices.Contains([]string{"class", "struct", "enum", "actor", "extension"}, child.Kind()) {
				keyword = child.Kind()
				break
			}
		}
		name := typeName
		if keyword == "extension" {
			name = ""
			if ids := namedChildrenOfKind(namedChildOfKind(node, "user_type"), "type_identifier"); len(ids) > 0 {
				name = x.text(ids[len(ids)-1])
			}
		}
		if name == "" {
			return nil
		}
		kind := map[string]Kind{"extension": "module", "struct": "struct", "enum": "enum"}[keyword]
		return &defDescriptor{name: name, kind: cmp.Or(kind, "class"), headerEnd: childBodyStart(node, "class_body", "enum_class_body"), hashNode: node}
	case "protocol_declaration":
		if typeName == "" {
			return nil
		}
		return &defDescriptor{name: typeName, kind: "interface", headerEnd: childBodyStart(node, "protocol_body"), hashNode: node}
	case "function_declaration", "protocol_function_declaration":
		name := x.text(namedChildOfKind(node, "simple_identifier"))
		if name == "" {
			return nil
		}
		kind := Kind("function")
		if node.Kind() == "protocol_function_declaration" || slices.Contains(swiftTypeKinds, ctx.enclosingKind) {
			kind = "method"
		}
		desc := &defDescriptor{name: name, kind: kind, headerEnd: childBodyStart(node, "function_body"), hashNode: node}
		desc.arity, desc.variadic = swiftArity(node)
		return desc
	case "init_declaration":
		if ctx.enclosingClass == "" {
			return nil
		}
		desc := &defDescriptor{name: ctx.enclosingClass, kind: "method", headerEnd: childBodyStart(node, "function_body"), hashNode: node}
		desc.arity, desc.variadic = swiftArity(node)
		return desc
	case "typealias_declaration":
		if typeName == "" {
			return nil
		}
		return &defDescriptor{name: typeName, kind: "type", headerEnd: node.EndByte(), hashNode: node}
	case "property_declaration":
		if ctx.enclosingKind != "" {
			return nil
		}
		name := x.text(namedChildOfKind(namedChildOfKind(node, "pattern"), "simple_identifier"))
		if name == "" {
			return nil
		}
		return &defDescriptor{name: name, kind: "variable", headerEnd: node.EndByte(), hashNode: node}
	}
	return nil
}

// swiftArity is the required parameter count; variadic marks a default or
// variadic parameter, making the arity a minimum.
func swiftArity(node *sitter.Node) (*int, bool) {
	var parameters, defaults int
	variadic := false
	for _, child := range children(node) {
		switch child.Kind() {
		case "parameter":
			parameters++
			variadic = variadic || slices.ContainsFunc(children(child), func(c *sitter.Node) bool { return c.Kind() == "..." })
		case "=":
			defaults++
		}
	}
	return new(max(0, parameters-defaults)), variadic || defaults > 0
}

func swiftArgCount(node *sitter.Node) *int {
	suffix := namedChildOfKind(node, "call_suffix")
	if suffix == nil {
		return nil
	}
	count := len(namedChildrenOfKind(namedChildOfKind(suffix, "value_arguments"), "value_argument"))
	if namedChildOfKind(suffix, "lambda_literal") != nil {
		count++
	}
	return &count
}

func (x *extractor) swiftExported(node *sitter.Node) bool {
	visibility := namedChildOfKind(namedChildOfKind(node, "modifiers"), "visibility_modifier")
	return visibility == nil || (x.text(visibility) != "private" && x.text(visibility) != "fileprivate")
}

// swiftLastTypeIdentifier is a user_type's last direct type_identifier.
func (x *extractor) swiftLastTypeIdentifier(userType *sitter.Node) string {
	if ids := namedChildrenOfKind(userType, "type_identifier"); len(ids) > 0 {
		return x.text(ids[len(ids)-1])
	}
	return ""
}

func (x *extractor) swiftSuperClassName(node *sitter.Node) string {
	return x.swiftLastTypeIdentifier(namedChildOfKind(namedChildOfKind(node, "inheritance_specifier"), "user_type"))
}

func (x *extractor) swiftHeritage(node *sitter.Node, classID string, ctx walkCtx) []rawEdge {
	var edges []rawEdge
	for _, specifier := range namedChildrenOfKind(node, "inheritance_specifier") {
		if name := x.swiftLastTypeIdentifier(namedChildOfKind(specifier, "user_type")); name != "" {
			edges = append(edges, rawEdge{source: classID, relation: "extends", name: name, file: ctx.rel})
		}
	}
	return edges
}

func (w *bindingWalk) swiftDefName(node *sitter.Node) (string, bool) {
	switch node.Kind() {
	case "class_declaration":
		if slices.ContainsFunc(children(node), func(c *sitter.Node) bool { return c.Kind() == "extension" }) {
			ids := namedChildrenOfKind(namedChildOfKind(node, "user_type"), "type_identifier")
			if len(ids) == 0 {
				return "", false
			}
			return w.text(ids[len(ids)-1]), true
		}
		fallthrough
	case "protocol_declaration":
		name := namedChildOfKind(node, "type_identifier")
		return w.text(name), name != nil
	case "function_declaration", "protocol_function_declaration":
		name := namedChildOfKind(node, "simple_identifier")
		return w.text(name), name != nil
	case "init_declaration":
		if body := node.Parent(); body != nil {
			if owner := body.Parent(); owner != nil {
				return w.swiftDefName(owner)
			}
		}
	}
	return "", false
}

func (w *bindingWalk) swiftTypeName(node *sitter.Node) string {
	switch {
	case node == nil:
	case node.Kind() == "optional_type":
		return w.swiftTypeName(namedChildOfKind(node, "user_type"))
	case node.Kind() == "user_type":
		if ids := namedChildrenOfKind(node, "type_identifier"); len(ids) > 0 {
			return w.text(ids[len(ids)-1])
		}
	}
	return ""
}

func (w *bindingWalk) handleSwift(node *sitter.Node, scope []string, classScope string) {
	scopePath := strings.Join(scope, ".")
	switch node.Kind() {
	case "parameter":
		ids := namedChildrenOfKind(node, "simple_identifier")
		typeName := w.swiftTypeName(namedChildOfKind(node, "user_type", "optional_type"))
		if len(ids) > 0 && typeName != "" {
			w.bindings.set(scopePath, w.text(ids[len(ids)-1]), typeName)
		}
	case "property_declaration":
		name := w.text(namedChildOfKind(namedChildOfKind(node, "pattern"), "simple_identifier"))
		if name == "" {
			return
		}
		typeName := w.swiftTypeName(namedChildOfKind(namedChildOfKind(node, "type_annotation"), "user_type", "optional_type"))
		if typeName == "" {
			if call := namedChildOfKind(node, "call_expression"); call != nil && call.NamedChildCount() > 0 {
				if function := call.NamedChild(0); function.Kind() == "simple_identifier" && startsUpper(w.text(function)) {
					typeName = w.text(function)
				}
			}
		}
		if typeName == "" {
			return
		}
		parent := node.Parent()
		isField := parent != nil && (parent.Kind() == "class_body" || parent.Kind() == "protocol_body")
		target := scopePath
		if isField {
			target = cmp.Or(classScope, scopePath)
		}
		w.bindings.set(target, name, typeName)
		if isField {
			w.bindings.set(target, "self."+name, typeName)
		}
	}
}

// startsUpper matches the TypeScript `/^[A-Z]/` test.
func startsUpper(text string) bool {
	return text != "" && text[0] >= 'A' && text[0] <= 'Z'
}
