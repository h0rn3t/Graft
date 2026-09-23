package graph

import (
	"cmp"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func (x *extractor) describePython(node *sitter.Node, ctx walkCtx) *defDescriptor {
	kind := Kind("")
	switch node.Kind() {
	case "class_definition":
		kind = "class"
	case "function_definition":
		kind = "function"
		if ctx.enclosingKind == "class" {
			kind = "method"
		}
	default:
		return nil
	}
	name := x.text(node.ChildByFieldName("name"))
	if name == "" {
		return nil
	}
	return &defDescriptor{name: name, kind: kind, headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node}
}

func (x *extractor) pythonHeritage(node *sitter.Node, classID string, ctx walkCtx) []rawEdge {
	var edges []rawEdge
	if superclasses := node.ChildByFieldName("superclasses"); superclasses != nil {
		for _, base := range namedChildren(superclasses) {
			if base.Kind() == "identifier" {
				edges = append(edges, rawEdge{source: classID, relation: "extends", name: x.text(base), file: ctx.rel})
			}
		}
	}
	return edges
}

// pythonReceiver is an attribute call's receiver: a bare identifier, or
// `self.x` for a chained `self.x.y()`.
func (x *extractor) pythonReceiver(function *sitter.Node) string {
	object := function.ChildByFieldName("object")
	switch {
	case object == nil:
	case object.Kind() == "identifier":
		return x.text(object)
	case object.Kind() == "attribute":
		inner, attribute := object.ChildByFieldName("object"), object.ChildByFieldName("attribute")
		if inner != nil && inner.Kind() == "identifier" && x.text(inner) == "self" && attribute != nil {
			return "self." + x.text(attribute)
		}
	}
	return ""
}

func (x *extractor) pythonImportSpecifier(node *sitter.Node) string {
	if module := node.ChildByFieldName("module_name"); module != nil {
		return x.text(module)
	}
	for _, child := range namedChildren(node) {
		if child.Kind() == "dotted_name" || child.Kind() == "relative_import" {
			return x.text(child)
		}
	}
	return ""
}

func (w *bindingWalk) collectPythonAlias(node *sitter.Node) {
	if node.Kind() != "aliased_import" {
		return
	}
	name, alias := node.ChildByFieldName("name"), node.ChildByFieldName("alias")
	if name == nil || alias == nil {
		return
	}
	original := w.text(name)
	if name.Kind() == "dotted_name" {
		if last := lastNamedChild(name); last != nil {
			original = w.text(last)
		}
	}
	w.aliases[w.text(alias)] = original
}

func (w *bindingWalk) pythonTypeName(node *sitter.Node) string {
	switch {
	case node == nil:
	case node.Kind() == "identifier":
		return w.resolveAlias(w.text(node))
	case node.Kind() == "type" && node.NamedChildCount() > 0:
		if inner := node.NamedChild(0); inner.Kind() == "identifier" {
			return w.resolveAlias(w.text(inner))
		}
	}
	return ""
}

func (w *bindingWalk) pythonCallTypeName(node *sitter.Node) string {
	if node == nil || node.Kind() != "call" {
		return ""
	}
	if function := node.ChildByFieldName("function"); function != nil && function.Kind() == "identifier" {
		return w.resolveAlias(w.text(function))
	}
	return ""
}

func (w *bindingWalk) handlePython(node *sitter.Node, scope []string, classScope string) {
	scopePath := strings.Join(scope, ".")
	switch node.Kind() {
	case "typed_parameter":
		for _, child := range namedChildren(node) {
			if child.Kind() != "identifier" {
				continue
			}
			if typeName := w.pythonTypeName(node.ChildByFieldName("type")); typeName != "" {
				w.bindings.set(scopePath, w.text(child), typeName)
			}
			return
		}
	case "assignment":
		w.handlePythonAssignment(node, scopePath, classScope)
	}
}

func (w *bindingWalk) handlePythonAssignment(node *sitter.Node, scopePath, classScope string) {
	left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
	switch {
	case left == nil:
	case left.Kind() == "identifier":
		typeName := w.pythonCallTypeName(right)
		if annotation := node.ChildByFieldName("type"); annotation != nil {
			typeName = w.pythonTypeName(annotation)
		}
		if typeName != "" {
			w.bindings.set(scopePath, w.text(left), typeName)
		}
	case left.Kind() == "attribute":
		object, attribute := left.ChildByFieldName("object"), left.ChildByFieldName("attribute")
		if object == nil || object.Kind() != "identifier" || (w.text(object) != "self" && w.text(object) != "cls") || attribute == nil {
			return
		}
		if typeName := w.pythonCallTypeName(right); typeName != "" {
			w.bindings.set(cmp.Or(classScope, scopePath), "self."+w.text(attribute), typeName)
		}
	}
}
