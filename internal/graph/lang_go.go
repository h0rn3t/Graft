package graph

import (
	"cmp"
	"regexp"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

var goConstructorName = regexp.MustCompile(`^New[A-Z]`)

func (x *extractor) describeGo(node *sitter.Node) *defDescriptor {
	name := x.text(node.ChildByFieldName("name"))
	switch {
	case name == "":
		return nil
	case node.Kind() == "function_declaration":
		return &defDescriptor{name: name, kind: "function", headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node}
	case node.Kind() == "method_declaration":
		idName := name
		if receiver := goReceiverType(node, x.source); receiver != "" {
			idName = receiver + "." + name
		}
		return &defDescriptor{name: name, idName: idName, kind: "method", headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node, goMethod: true}
	case node.Kind() == "type_spec":
		shape := node.ChildByFieldName("type")
		kind := Kind("type")
		end := node.EndByte()
		if shape != nil && (shape.Kind() == "struct_type" || shape.Kind() == "interface_type") {
			kind, end = "struct", shape.StartByte()
			if shape.Kind() == "interface_type" {
				kind = "interface"
			}
		}
		return &defDescriptor{name: name, kind: kind, headerEnd: end, hashNode: node}
	}
	return nil
}

func goReceiverParameter(node *sitter.Node) *sitter.Node {
	receiver := node.ChildByFieldName("receiver")
	if receiver == nil {
		return nil
	}
	for _, child := range namedChildren(receiver) {
		if child.Kind() == "parameter_declaration" {
			return child
		}
	}
	return nil
}

// goReceiverType is a method receiver's base type, unwrapping a pointer and
// type parameters: `Stack` in `func (s *Stack[T])`.
func goReceiverType(node *sitter.Node, source []byte) string {
	parameter := goReceiverParameter(node)
	if parameter == nil {
		return ""
	}
	receiverType := parameter.ChildByFieldName("type")
	if receiverType != nil && receiverType.Kind() == "pointer_type" {
		receiverType = lastNamedChild(receiverType)
	}
	receiverType = goGenericBase(receiverType)
	if receiverType == nil || receiverType.Kind() != "type_identifier" {
		return ""
	}
	return nodeText(receiverType, source)
}

// goGenericBase is the named type of an instantiated generic type, `Stack` in
// `Stack[T]`; any other node is returned unchanged.
func goGenericBase(node *sitter.Node) *sitter.Node {
	if node != nil && node.Kind() == "generic_type" {
		return node.ChildByFieldName("type")
	}
	return node
}

// goReceiverVar is the receiver parameter's name: `w` in `func (w *Worker)`.
func goReceiverVar(node *sitter.Node, source []byte) string {
	if parameter := goReceiverParameter(node); parameter != nil {
		return nodeText(parameter.ChildByFieldName("name"), source)
	}
	return ""
}

// goExported applies Go visibility to a symbol's own name, as extract.ts
// goExported does with JavaScript case mapping of its first character.
func goExported(name string) bool {
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	for _, r := range name {
		first := string(r)
		return first != strings.ToLower(first) && first == strings.ToUpper(first)
	}
	return false
}

func (x *extractor) goImportSpecifier(node *sitter.Node) string {
	importPath := cmp.Or(node.ChildByFieldName("path"), lastNamedChild(node))
	if importPath == nil {
		return ""
	}
	return trimOneQuote(x.text(importPath), "\"`")
}

func (w *bindingWalk) goDefName(node *sitter.Node) (string, bool) {
	name := node.ChildByFieldName("name")
	switch node.Kind() {
	case "method_declaration":
		if name == nil {
			return "", false
		}
		if receiver := goReceiverType(node, w.source); receiver != "" {
			return receiver + "." + w.text(name), true
		}
		return w.text(name), true
	case "function_declaration", "type_spec":
		return w.text(name), name != nil
	}
	return "", false
}

func (w *bindingWalk) handleGo(node *sitter.Node, scope []string) {
	scopePath := strings.Join(scope, ".")
	switch node.Kind() {
	case "var_spec":
		name, varType := node.ChildByFieldName("name"), node.ChildByFieldName("type")
		if varType != nil && varType.Kind() == "pointer_type" {
			varType = lastNamedChild(varType)
		}
		varType = goGenericBase(varType)
		if name != nil && name.Kind() == "identifier" && varType != nil && varType.Kind() == "type_identifier" {
			w.bindings.set(scopePath, w.text(name), w.text(varType))
		}
	case "short_var_declaration":
		left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
		if left == nil || right == nil {
			return
		}
		expressions := namedChildren(right)
		for index, name := range namedChildren(left) {
			if name.Kind() != "identifier" || index >= len(expressions) {
				continue
			}
			if typeName := w.goExpressionType(expressions[index]); typeName != "" {
				w.bindings.set(scopePath, w.text(name), typeName)
			}
		}
	}
}

// goExpressionType reads a composite literal's type, or X from a NewX(...) call.
func (w *bindingWalk) goExpressionType(expression *sitter.Node) string {
	if expression.Kind() == "unary_expression" {
		for _, child := range namedChildren(expression) {
			if child.Kind() == "composite_literal" {
				expression = child
				break
			}
		}
	}
	switch expression.Kind() {
	case "composite_literal":
		if literalType := goGenericBase(expression.ChildByFieldName("type")); literalType != nil && literalType.Kind() == "type_identifier" {
			return w.text(literalType)
		}
	case "call_expression":
		if function := expression.ChildByFieldName("function"); function != nil && function.Kind() == "identifier" && goConstructorName.MatchString(w.text(function)) {
			return w.text(function)[len("New"):]
		}
	}
	return ""
}
