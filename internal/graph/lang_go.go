package graph

import (
	"cmp"
	"path"
	"regexp"
	"strings"
	"unicode"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

var goConstructorName = regexp.MustCompile(`^New[A-Z]`)

func (x *extractor) describeGo(node *sitter.Node) *defDescriptor {
	name := x.text(node.ChildByFieldName("name"))
	switch {
	case name == "":
		return nil
	case node.Kind() == "function_declaration":
		arity, variadic := goArity(node.ChildByFieldName("parameters"))
		return &defDescriptor{name: name, kind: "function", headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node, arity: arity, variadic: variadic}
	case node.Kind() == "method_declaration":
		idName := name
		if receiver := goReceiverType(node, x.source); receiver != "" {
			idName = receiver + "." + name
		}
		arity, variadic := goArity(node.ChildByFieldName("parameters"))
		return &defDescriptor{name: name, idName: idName, kind: "method", headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node, arity: arity, variadic: variadic, goMethod: true}
	case node.Kind() == "method_elem":
		// Only a named interface's methods are symbols; an inline
		// `interface{ M() }` type has no name to own them.
		shape := node.Parent()
		spec := shape.Parent()
		if shape.Kind() != "interface_type" || spec == nil || spec.Kind() != "type_spec" || !sameNode(spec.ChildByFieldName("type"), shape) {
			return nil
		}
		arity, variadic := goArity(node.ChildByFieldName("parameters"))
		return &defDescriptor{name: name, kind: "method", owner: x.text(spec.ChildByFieldName("name")), headerEnd: node.EndByte(), hashNode: node, arity: arity, variadic: variadic}
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

// goArity counts a parameter list's parameters, `a, b int` as two; a variadic
// parameter counts as one.
func goArity(parameters *sitter.Node) (*int, bool) {
	if parameters == nil {
		return nil, false
	}
	count, variadic := 0, false
	for _, parameter := range namedChildren(parameters) {
		switch parameter.Kind() {
		case "parameter_declaration":
			count += max(1, len(namedChildrenOfKind(parameter, "identifier")))
		case "variadic_parameter_declaration":
			count++
			variadic = true
		}
	}
	return &count, variadic
}

// goEmbeds is the extends intent of a struct's embedded fields and of an
// interface's embedded interfaces: `Base` and `io.Reader` in
// `struct{ Base; *io.Reader }`. A type-set element such as `~int | string`
// constrains a type parameter and embeds nothing.
func (x *extractor) goEmbeds(spec *sitter.Node, id string, ctx walkCtx) []rawEdge {
	shape := spec.ChildByFieldName("type")
	var embedded []*sitter.Node
	switch shape.Kind() {
	case "struct_type":
		for _, field := range namedChildrenOfKind(namedChildOfKind(shape, "field_declaration_list"), "field_declaration") {
			if field.ChildByFieldName("name") == nil {
				embedded = append(embedded, field.ChildByFieldName("type"))
			}
		}
	case "interface_type":
		for _, element := range namedChildrenOfKind(shape, "type_elem") {
			if element.NamedChildCount() == 1 {
				embedded = append(embedded, element.NamedChild(0))
			}
		}
	}
	var edges []rawEdge
	for _, embed := range embedded {
		if embed != nil && embed.Kind() == "pointer_type" {
			embed = lastNamedChild(embed)
		}
		if embed = goGenericBase(embed); embed != nil && (embed.Kind() == "type_identifier" || embed.Kind() == "qualified_type") {
			edges = append(edges, rawEdge{source: id, relation: "extends", name: x.text(embed), file: ctx.rel})
		}
	}
	return edges
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

// goTypeName is a declared type's bare name, unwrapping a pointer, type
// arguments and a package qualifier: `Store` in `*store.Store[K]`.
func goTypeName(node *sitter.Node, source []byte) string {
	if node != nil && node.Kind() == "pointer_type" {
		node = lastNamedChild(node)
	}
	node = goGenericBase(node)
	switch {
	case node == nil:
	case node.Kind() == "type_identifier":
		return nodeText(node, source)
	case node.Kind() == "qualified_type":
		return nodeText(node.ChildByFieldName("name"), source)
	}
	return ""
}

// goAssumedPackageName is the name goimports assumes an unaliased import binds:
// the path's last element, or the one before a major-version element such as
// /v2, without a go- prefix and cut where an identifier ends, so
// gopkg.in/yaml.v3 binds yaml and github.com/mattn/go-sqlite3 binds sqlite3.
func goAssumedPackageName(importPath string) string {
	name := path.Base(importPath)
	if version, ok := strings.CutPrefix(name, "v"); ok && version != "" && strings.Trim(version, "0123456789") == "" {
		if dir := path.Dir(importPath); dir != "." {
			name = path.Base(dir)
		}
	}
	name = strings.TrimPrefix(name, "go-")
	if end := strings.IndexFunc(name, func(r rune) bool { return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) }); end >= 0 {
		name = name[:end]
	}
	return name
}

// collectGoPackages maps each import's local package name to its path: the
// alias when there is one, else the name goimports assumes. Blank and dot
// imports bind no name.
func (x *extractor) collectGoPackages(root *sitter.Node) map[string]string {
	packages := make(map[string]string)
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if node.Kind() != "import_spec" {
			for _, child := range namedChildren(node) {
				visit(child)
			}
			return
		}
		importPath := x.goImportSpecifier(node)
		local := x.text(node.ChildByFieldName("name"))
		if local == "" {
			local = goAssumedPackageName(importPath)
		}
		if importPath != "" && local != "_" && local != "." {
			packages[local] = importPath
		}
	}
	visit(root)
	return packages
}

func (w *bindingWalk) handleGo(node *sitter.Node, scope []string) {
	scopePath := strings.Join(scope, ".")
	switch node.Kind() {
	case "var_spec", "parameter_declaration":
		typeName := goTypeName(node.ChildByFieldName("type"), w.source)
		if typeName == "" {
			return
		}
		for _, name := range namedChildrenOfKind(node, "identifier") {
			w.bindings.set(scopePath, w.text(name), typeName)
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

// goExpressionType reads a composite literal's type, or X from a NewX(...) or
// pkg.NewX(...) call.
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
		return goTypeName(expression.ChildByFieldName("type"), w.source)
	case "call_expression":
		function := expression.ChildByFieldName("function")
		if function != nil && function.Kind() == "selector_expression" {
			function = function.ChildByFieldName("field")
		}
		if function != nil && goConstructorName.MatchString(w.text(function)) {
			return w.text(function)[len("New"):]
		}
	}
	return ""
}
