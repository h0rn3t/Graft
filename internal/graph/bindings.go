package graph

import (
	"cmp"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// fileBindings maps a variable, parameter, or field to its bare type name, keyed
// by the same scope path the extraction walk mints ids with (bindings.ts).
type fileBindings struct {
	types map[string]string
}

func (b *fileBindings) set(scopePath, name, typeName string) {
	b.types[scopePath+"|"+name] = typeName
}

// lookup tries the innermost scope first: for ["a","b"] and x, `a.b|x`, `a|x`, `|x`.
func (b *fileBindings) lookup(scope []string, name string) string {
	for depth := len(scope); depth >= 0; depth-- {
		if hit := b.types[strings.Join(scope[:depth], ".")+"|"+name]; hit != "" {
			return hit
		}
	}
	return ""
}

// resolveRecvType resolves a call's receiver text to a bound type name.
func (b *fileBindings) resolveRecvType(receiver string, ctx walkCtx) string {
	switch {
	case receiver == "":
		return ""
	case receiver == "self" || receiver == "cls" || receiver == "this":
		return ctx.enclosingClass
	case receiver == "super":
		return ctx.rSuperClass
	case strings.HasPrefix(receiver, "self.") || strings.HasPrefix(receiver, "this."):
		if hit := b.lookup(ctx.scope, receiver); hit != "" {
			return hit
		}
		if rest, ok := strings.CutPrefix(receiver, "this."); ok {
			return b.lookup(ctx.scope, "self."+rest)
		}
		return ""
	case ctx.lang == langPHP && !strings.HasPrefix(receiver, "$"):
		return receiver
	}
	if ctx.lang == langGo && receiver == ctx.goReceiverVar && ctx.enclosingClass != "" {
		return ctx.enclosingClass
	}
	if hit := b.lookup(ctx.scope, receiver); hit != "" {
		return hit
	}
	if ctx.lang == langSwift && receiver[0] >= 'A' && receiver[0] <= 'Z' {
		return receiver
	}
	return ""
}

var tsDefinitionTypes = []string{
	"class_declaration", "abstract_class_declaration", "function_declaration",
	"generator_function_declaration", "method_definition", "interface_declaration",
	"type_alias_declaration", "enum_declaration",
}

type bindingWalk struct {
	source   []byte
	lang     language
	bindings *fileBindings
	aliases  map[string]string
}

// collectBindings is the pre-order pass that records variable→type bindings.
func collectBindings(root *sitter.Node, lang language, source []byte) *fileBindings {
	w := bindingWalk{source: source, lang: lang, bindings: &fileBindings{types: make(map[string]string)}, aliases: make(map[string]string)}
	w.collectAliases(root)
	w.visit(root, nil, "")
	return w.bindings
}

func (w *bindingWalk) text(node *sitter.Node) string {
	return nodeText(node, w.source)
}

func (w *bindingWalk) collectAliases(node *sitter.Node) {
	if w.lang == langPython {
		w.collectPythonAlias(node)
	}
	if (w.lang == langTypeScript || w.lang == langTSX) && node.Kind() == "import_specifier" {
		name, alias := node.ChildByFieldName("name"), node.ChildByFieldName("alias")
		if name != nil && alias != nil {
			w.aliases[w.text(alias)] = w.text(name)
		}
	}
	for _, child := range namedChildren(node) {
		w.collectAliases(child)
	}
}

// visit mirrors the extraction walk's scope stack; classScope is the scope path
// of the nearest enclosing class, where `this.field` bindings live.
func (w *bindingWalk) visit(node *sitter.Node, scope []string, classScope string) {
	switch w.lang {
	case langPython:
		w.handlePython(node, scope, classScope)
	case langGo:
		w.handleGo(node, scope)
	case langR:
	case langJava:
		w.handleJava(node, scope, classScope)
	case langSwift:
		w.handleSwift(node, scope, classScope)
	case langPHP:
		w.handlePHP(node, scope)
	default:
		w.handleTS(node, scope, classScope)
	}
	childScope, childClassScope := scope, classScope
	if name, ok := w.defName(node); ok {
		childScope = append(slices.Clone(scope), name)
		if w.isClassNode(node) {
			childClassScope = strings.Join(childScope, ".")
		}
	}
	for _, child := range namedChildren(node) {
		w.visit(child, childScope, childClassScope)
	}
}

func (w *bindingWalk) defName(node *sitter.Node) (string, bool) {
	switch w.lang {
	case langJava:
		if _, ok := javaKinds[node.Kind()]; ok {
			name := node.ChildByFieldName("name")
			return w.text(name), name != nil
		}
		return "", false
	case langGo:
		return w.goDefName(node)
	case langR:
		return w.rDefName(node)
	case langSwift:
		return w.swiftDefName(node)
	case langPHP:
		return w.phpDefName(node)
	case langPython:
		if node.Kind() == "class_definition" || node.Kind() == "function_definition" {
			name := node.ChildByFieldName("name")
			return w.text(name), name != nil
		}
		return "", false
	}
	if slices.Contains(tsDefinitionTypes, node.Kind()) {
		name := node.ChildByFieldName("name")
		return w.text(name), name != nil
	}
	if node.Kind() == "variable_declarator" {
		if value := node.ChildByFieldName("value"); value != nil && slices.Contains(functionValueTypes, value.Kind()) {
			name := node.ChildByFieldName("name")
			return w.text(name), name != nil
		}
	}
	return "", false
}

func (w *bindingWalk) isClassNode(node *sitter.Node) bool {
	switch w.lang {
	case langPython:
		return node.Kind() == "class_definition"
	case langJava:
		return slices.Contains(javaTypeDeclarations, node.Kind())
	case langSwift:
		return node.Kind() == "class_declaration" || node.Kind() == "protocol_declaration"
	case langTypeScript, langTSX:
	default:
		return false
	}
	return node.Kind() == "class_declaration" || node.Kind() == "abstract_class_declaration"
}

func (w *bindingWalk) resolveAlias(name string) string {
	if original, ok := w.aliases[name]; ok {
		return original
	}
	return name
}

func (w *bindingWalk) tsAnnotationTypeName(annotation *sitter.Node) string {
	if annotation == nil || annotation.Kind() != "type_annotation" || annotation.NamedChildCount() == 0 {
		return ""
	}
	if first := annotation.NamedChild(0); first.Kind() == "type_identifier" {
		return w.resolveAlias(w.text(first))
	}
	return ""
}

func (w *bindingWalk) tsNewTypeName(value *sitter.Node) string {
	if value == nil || value.Kind() != "new_expression" {
		return ""
	}
	if constructor := value.ChildByFieldName("constructor"); constructor != nil && constructor.Kind() == "identifier" {
		return w.resolveAlias(w.text(constructor))
	}
	return ""
}

func (w *bindingWalk) handleTS(node *sitter.Node, scope []string, classScope string) {
	scopePath := strings.Join(scope, ".")
	fieldScope := cmp.Or(classScope, scopePath)
	switch node.Kind() {
	case "variable_declarator":
		value := node.ChildByFieldName("value")
		if value != nil && slices.Contains(functionValueTypes, value.Kind()) {
			return
		}
		name := node.ChildByFieldName("name")
		if name == nil || name.Kind() != "identifier" {
			return
		}
		if typeName := cmp.Or(w.tsNewTypeName(value), w.tsAnnotationTypeName(node.ChildByFieldName("type"))); typeName != "" {
			w.bindings.set(scopePath, w.text(name), typeName)
		}
	case "public_field_definition":
		name := node.ChildByFieldName("name")
		if name == nil {
			return
		}
		if typeName := cmp.Or(w.tsAnnotationTypeName(node.ChildByFieldName("type")), w.tsNewTypeName(node.ChildByFieldName("value"))); typeName != "" {
			w.bindings.set(fieldScope, "this."+w.text(name), typeName)
		}
	case "required_parameter":
		pattern := node.ChildByFieldName("pattern")
		if pattern == nil || pattern.Kind() != "identifier" {
			return
		}
		typeName := w.tsAnnotationTypeName(node.ChildByFieldName("type"))
		if typeName == "" {
			return
		}
		w.bindings.set(scopePath, w.text(pattern), typeName)
		for index := range node.ChildCount() {
			switch node.Child(index).Kind() {
			case "accessibility_modifier", "readonly", "override_modifier":
				w.bindings.set(fieldScope, "this."+w.text(pattern), typeName)
				return
			}
		}
	}
}
