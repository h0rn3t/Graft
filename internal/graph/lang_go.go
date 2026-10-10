package graph

import (
	"cmp"
	"maps"
	"path"
	"regexp"
	"slices"
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

// goValuePosition reports whether an identifier or selector stands where Go
// takes a value — a call argument, the right side of =, := or var, a returned
// value, a composite literal's element — and so may name a function as a
// value. A keyed element's key is a field name.
func goValuePosition(node *sitter.Node) bool {
	if node.Kind() != "identifier" && node.Kind() != "selector_expression" {
		return false
	}
	parent := node.Parent()
	if parent == nil {
		return false
	}
	switch parent.Kind() {
	case "argument_list":
		return true
	case "literal_element":
		keyed := parent.Parent()
		return keyed == nil || keyed.Kind() != "keyed_element" || sameNode(keyed.ChildByFieldName("value"), parent)
	case "expression_list":
		owner := parent.Parent()
		if owner == nil {
			return false
		}
		switch owner.Kind() {
		case "return_statement":
			return true
		case "assignment_statement", "short_var_declaration":
			return sameNode(owner.ChildByFieldName("right"), parent)
		case "var_spec":
			return sameNode(owner.ChildByFieldName("value"), parent)
		}
	}
	return false
}

// goFunctionValue is the references intent of a function or method named as
// a value: `handle` in `register(handle)`, `s.Serve` through the type bound to
// s, `kit.Helper` in an imported package. A bare name a parameter or local
// declares is a variable, and a selector on a value of unknown type names
// nothing to resolve.
func (x *extractor) goFunctionValue(node *sitter.Node, ctx walkCtx) (rawEdge, bool) {
	edge := rawEdge{source: ctx.parentID, relation: "references", file: ctx.rel}
	if node.Kind() == "identifier" {
		edge.name = x.text(node)
		_, local := ctx.goLocals[edge.name]
		return edge, !local
	}
	operand, field := node.ChildByFieldName("operand"), node.ChildByFieldName("field")
	if operand == nil || field == nil {
		return rawEdge{}, false
	}
	edge.name, edge.viaMember = x.text(field), true
	x.goTypeOperand(&edge, operand, ctx)
	if _, local := ctx.goLocals[x.text(operand)]; edge.recvType == "" && operand.Kind() == "identifier" && !local {
		edge.specifier = x.goPackages[x.text(operand)]
	}
	return edge, edge.recvType != "" || edge.specifier != ""
}

// goTypeOperand types the operand a Go selector selects from: a variable
// bound to a type or the method's receiver, a type assertion `x.(T)`, or a
// chain of fields on either, `s.store` in `s.store.Get()`. A type qualified
// by a name the file does not import, like an operand of unknown type, types
// nothing.
func (x *extractor) goTypeOperand(edge *rawEdge, operand *sitter.Node, ctx walkCtx) {
	var fields []string
	for operand != nil && operand.Kind() == "selector_expression" {
		fields = append(fields, x.text(operand.ChildByFieldName("field")))
		operand = operand.ChildByFieldName("operand")
	}
	var typeName string
	switch {
	case operand == nil:
	case operand.Kind() == "identifier":
		typeName = x.bindings.resolveRecvType(x.text(operand), ctx)
	case operand.Kind() == "type_assertion_expression":
		typeName = goTypeName(operand.ChildByFieldName("type"), x.source)
	}
	if name, pkg, ok := x.goQualifiedType(typeName); ok {
		slices.Reverse(fields)
		edge.recvType, edge.recvPackage, edge.recvFields = name, pkg, fields
	}
}

// goFields is the field intent of each struct field of a named type: `client`
// of type `*kit.Client`, and an embedded `kit.Base` under its type's name, as
// Go names it.
func (x *extractor) goFields(spec *sitter.Node, id string, ctx walkCtx) []rawEdge {
	shape := spec.ChildByFieldName("type")
	if shape == nil || shape.Kind() != "struct_type" {
		return nil
	}
	var edges []rawEdge
	for _, field := range namedChildrenOfKind(namedChildOfKind(shape, "field_declaration_list"), "field_declaration") {
		name, pkg, ok := x.goQualifiedType(goTypeName(field.ChildByFieldName("type"), x.source))
		if !ok {
			continue
		}
		edge := rawEdge{source: id, relation: "field", name: name, recvType: name, recvPackage: pkg, file: ctx.rel}
		names := namedChildrenOfKind(field, "field_identifier")
		if len(names) == 0 {
			edges = append(edges, edge)
		}
		for _, fieldName := range names {
			edge.name = x.text(fieldName)
			edges = append(edges, edge)
		}
	}
	return edges
}

// goQualifiedType splits a type name as written into its name and the import
// path of the package declaring it, empty for the file's own package. It
// fails for no name, and for a qualifier the file does not import.
func (x *extractor) goQualifiedType(typeName string) (name, pkg string, ok bool) {
	qualifier, name, qualified := strings.Cut(typeName, ".")
	if !qualified {
		return typeName, "", typeName != ""
	}
	pkg = x.goPackages[qualifier]
	return name, pkg, pkg != ""
}

// goLocalNames lists the names a Go definition declares inside itself, added
// to the enclosing definition's: a function's receiver, parameters, results
// and locals, its closures' included, and the type parameters of a generic
// function, type or receiver. Such a name is a variable or a type parameter,
// not the package function or type of that name.
func (x *extractor) goLocalNames(definition *sitter.Node, enclosing map[string]struct{}) map[string]struct{} {
	names := maps.Clone(enclosing)
	if names == nil {
		names = make(map[string]struct{})
	}
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		var declared []*sitter.Node
		switch node.Kind() {
		case "parameter_declaration", "variadic_parameter_declaration", "var_spec", "const_spec", "type_parameter_declaration":
			declared = namedChildrenOfKind(node, "identifier")
		case "short_var_declaration", "range_clause", "receive_statement":
			declared = namedChildren(node.ChildByFieldName("left"))
		case "type_switch_statement":
			declared = namedChildren(node.ChildByFieldName("alias"))
		case "type_arguments":
			// Inside a receiver, `List[T]` declares T.
			if parameter := goReceiverParameter(definition); parameter != nil && parameter.StartByte() <= node.StartByte() && node.EndByte() <= parameter.EndByte() {
				for _, element := range namedChildren(node) {
					declared = append(declared, namedChildrenOfKind(element, "type_identifier")...)
				}
			}
		}
		for _, name := range declared {
			if name.Kind() == "identifier" || name.Kind() == "type_identifier" {
				names[x.text(name)] = struct{}{}
			}
		}
		for _, child := range namedChildren(node) {
			visit(child)
		}
	}
	visit(definition)
	return names
}

// goPredeclaredTypes never name a type of the repository.
var goPredeclaredTypes = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true, "int8": true, "int16": true,
	"int32": true, "int64": true, "rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
}

// goTypeUse reports whether a type name or qualified type uses that type: a
// parameter, result, field, var, composite literal, assertion or underlying
// type does. A declaration's own name does not, nor the name part of a
// qualified type, which counts as the whole, nor an embedded field or
// interface, which is extends.
func goTypeUse(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil || isDeclarationName(node) || parent.Kind() == "qualified_type" {
		return false
	}
	for child := node; parent.Kind() == "pointer_type" || (parent.Kind() == "generic_type" && sameNode(parent.ChildByFieldName("type"), child)); {
		child, parent = parent, parent.Parent()
		if parent == nil {
			return false
		}
	}
	switch parent.Kind() {
	case "field_declaration":
		return parent.ChildByFieldName("name") != nil
	case "type_elem":
		grandparent := parent.Parent()
		return grandparent == nil || grandparent.Kind() != "interface_type"
	}
	return true
}

// goTypeReference is the references intent of a type use: `Item` to the type
// of that name in the user's package, `kit.Config` through the file's imports.
// A predeclared type, a type parameter and a qualifier that is no import name
// nothing to resolve.
func (x *extractor) goTypeReference(node *sitter.Node, ctx walkCtx) (rawEdge, bool) {
	edge := rawEdge{source: ctx.parentID, relation: "references", file: ctx.rel, kinds: goTypeKinds}
	if node.Kind() == "qualified_type" {
		edge.name = x.text(node.ChildByFieldName("name"))
		edge.specifier = x.goPackages[x.text(node.ChildByFieldName("package"))]
		return edge, edge.name != "" && edge.specifier != ""
	}
	edge.name = x.text(node)
	_, local := ctx.goLocals[edge.name]
	return edge, !local && !goPredeclaredTypes[edge.name]
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

// goTypeName is a type's name as written, unwrapping a pointer and type
// arguments: `store.Store` in `*store.Store[K]`.
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
		return nodeText(node.ChildByFieldName("package"), source) + "." + nodeText(node.ChildByFieldName("name"), source)
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
			// `t, ok := t.(T)` retypes t in its block only, and a binding
			// holds for the whole function.
			if assertion := expressions[index]; assertion.Kind() == "type_assertion_expression" && w.text(assertion.ChildByFieldName("operand")) == w.text(name) {
				continue
			}
			if typeName := w.goExpressionType(expressions[index]); typeName != "" {
				w.bindings.set(scopePath, w.text(name), typeName)
			}
		}
	}
}

// goExpressionType reads the type of a composite literal or a type assertion,
// X from a NewX(...) call, or pkg.X from a pkg.NewX(...) call.
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
	case "composite_literal", "type_assertion_expression":
		return goTypeName(expression.ChildByFieldName("type"), w.source)
	case "call_expression":
		function := expression.ChildByFieldName("function")
		qualifier := ""
		if function != nil && function.Kind() == "selector_expression" {
			operand := function.ChildByFieldName("operand")
			if operand == nil || operand.Kind() != "identifier" {
				return ""
			}
			qualifier, function = w.text(operand)+".", function.ChildByFieldName("field")
		}
		if function != nil && goConstructorName.MatchString(w.text(function)) {
			return qualifier + w.text(function)[len("New"):]
		}
	}
	return ""
}
