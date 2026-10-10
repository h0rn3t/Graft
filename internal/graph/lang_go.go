package graph

import (
	"cmp"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
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
			edges = append(edges, rawEdge{source: id, relation: "extends", name: x.text(embed), file: ctx.rel, line: lineOf(embed)})
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

// goValueKinds are the Go declarations a name in a value position can denote;
// goDataKinds those a name in any other expression can.
var (
	goValueKinds = []Kind{"function", "constant", "variable"}
	goDataKinds  = []Kind{"constant", "variable"}
)

// goWritten reports whether an expression is assigned or stepped: the left
// side of = or op=, the operand of ++ or --, or what an index expression on
// such a side stores into, `s.cache` in `s.cache[k] = v`.
func goWritten(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil {
		return false
	}
	switch parent.Kind() {
	case "expression_list":
		owner := parent.Parent()
		return owner != nil && owner.Kind() == "assignment_statement" && sameNode(owner.ChildByFieldName("left"), parent)
	case "inc_statement", "dec_statement":
		return true
	case "index_expression":
		return sameNode(parent.ChildByFieldName("operand"), node) && goWritten(parent)
	case "parenthesized_expression":
		return goWritten(parent)
	}
	return false
}

// goUseIntent starts the intent of a Go name or selector used where node
// stands: writes when it is assigned, else references; a function, constant
// or variable in a value position, and a constant or variable elsewhere.
func goUseIntent(node *sitter.Node, name string, ctx walkCtx) rawEdge {
	edge := rawEdge{source: ctx.parentID, relation: "references", name: name, file: ctx.rel, line: lineOf(node), kinds: goDataKinds}
	if goWritten(node) {
		edge.relation = "writes"
	}
	if goValuePosition(node) {
		edge.kinds = goValueKinds
	}
	return edge
}

// goNameUse is the intent of a bare name used in an expression: `handle` in
// `register(handle)`, `maxBodyChars` in `n > maxBodyChars`, `count` in
// `count++`. A name a parameter or local declares, an import name, the blank
// name and a declaration's own name are none; a composite literal's key is
// goLiteralKey's.
func (x *extractor) goNameUse(node *sitter.Node, ctx walkCtx) (rawEdge, bool) {
	name := x.text(node)
	_, local := ctx.goLocals[name]
	_, imported := x.goPackages[name]
	parent := node.Parent()
	switch {
	case local, imported, name == "_", parent == nil, isDeclarationName(node):
		return rawEdge{}, false
	case parent.Kind() == "literal_element":
		if keyed := parent.Parent(); keyed != nil && keyed.Kind() == "keyed_element" && sameNode(keyed.ChildByFieldName("key"), parent) {
			return x.goLiteralKey(node, keyed, ctx)
		}
	}
	switch parent.Kind() {
	case "const_spec", "var_spec", "parameter_declaration", "variadic_parameter_declaration", "type_parameter_declaration":
		return rawEdge{}, false
	}
	return goUseIntent(node, name, ctx), true
}

// goMemberUse is the intent of a selector that is no call: a field read or
// written, `s.store` and `s.count++` through the type bound to s, a method
// value `s.Serve`, or `kit.Default` in an imported package. A selector on a
// value of unknown type keeps no type: it links nothing, and the resolver
// counts its name as unresolved.
func (x *extractor) goMemberUse(node *sitter.Node, ctx walkCtx) (rawEdge, bool) {
	operand, field := node.ChildByFieldName("operand"), node.ChildByFieldName("field")
	if operand == nil || field == nil {
		return rawEdge{}, false
	}
	edge := goUseIntent(node, x.text(field), ctx)
	edge.viaMember = true
	x.goTypeOperand(&edge, operand, ctx)
	if _, local := ctx.goLocals[x.text(operand)]; edge.recvType == "" && operand.Kind() == "identifier" && !local {
		edge.specifier = x.goPackages[x.text(operand)]
	}
	return edge, true
}

// goLiteralKey is the intent of a composite literal's bare key: the field it
// writes, `Name` in `T{Name: v}` and in an element `[]T{{Name: v}}` whose type
// is elided, or a value, `KindFile` in `map[Kind]int{KindFile: 1}`. A key of
// a literal whose type nothing names, or names outside the file's imports, is
// neither.
func (x *extractor) goLiteralKey(key, keyed *sitter.Node, ctx walkCtx) (rawEdge, bool) {
	typ := goLiteralType(keyed.Parent())
	if typ == nil {
		return rawEdge{}, false
	}
	switch typ.Kind() {
	case "map_type", "slice_type", "array_type", "implicit_length_array_type":
		return goUseIntent(key, x.text(key), ctx), true
	}
	name, pkg, ok := x.goQualifiedType(goTypeName(typ, x.source))
	if !ok {
		return rawEdge{}, false
	}
	edge := rawEdge{
		source: ctx.parentID, relation: "writes", name: x.text(key), viaMember: true, literalKey: true,
		recvType: name, recvPackage: pkg, file: ctx.rel, line: lineOf(key),
	}
	return edge, true
}

// goLiteralType is the type node of the composite literal a literal_value is
// the body of, followed through elided element types: T for the inner body
// of `[]T{{Name: "a"}}` and of `map[string]*T{"a": {}}`. It is nil when no
// type is written for it.
func goLiteralType(body *sitter.Node) *sitter.Node {
	parent := body.Parent()
	if parent == nil {
		return nil
	}
	if parent.Kind() == "composite_literal" {
		return parent.ChildByFieldName("type")
	}
	if parent.Kind() != "literal_element" {
		return nil
	}
	outer := parent.Parent()
	if outer != nil && outer.Kind() == "keyed_element" {
		if !sameNode(outer.ChildByFieldName("value"), parent) {
			return nil
		}
		outer = outer.Parent()
	}
	if outer == nil || outer.Kind() != "literal_value" {
		return nil
	}
	container := goLiteralType(outer)
	switch {
	case container == nil:
		return nil
	case container.Kind() == "map_type":
		return container.ChildByFieldName("value")
	case container.Kind() == "slice_type", container.Kind() == "array_type", container.Kind() == "implicit_length_array_type":
		return container.ChildByFieldName("element")
	}
	return nil
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

// goField emits a node for each name a struct field declares, `client` in
// `client *kit.Client`, with the field intent naming its type as Go names it,
// and walks the field's type as that node's. An embedded field declares no
// node, and its intent names it by its type's name, as Go does.
func (x *extractor) goField(field *sitter.Node, ctx walkCtx) {
	typeNode := field.ChildByFieldName("type")
	intent := rawEdge{source: ctx.parentID, relation: "field", file: ctx.rel}
	typeName, pkg, typed := x.goQualifiedType(goTypeName(typeNode, x.source))
	if typed {
		intent.recvType, intent.recvPackage = typeName, pkg
	}
	names := namedChildrenOfKind(field, "field_identifier")
	if len(names) == 0 {
		if typed {
			intent.name = typeName
			x.edges = append(x.edges, intent)
		}
		x.walkNamedChildren(namedChildren(field), ctx)
		return
	}
	owner := ctx.scope[len(ctx.scope)-1]
	signature, _, _ := strings.Cut(x.text(field), "\n")
	for _, name := range names {
		member := ctx
		member.parentID = x.emitGoMember(field, x.text(name), "field", &owner, signature, ctx)
		member.enclosingKind = "field"
		intent.name, intent.targetID = x.text(name), member.parentID
		x.edges = append(x.edges, intent)
		if typeNode != nil {
			x.walk(typeNode, member)
		}
	}
}

// goValueSpec emits a node for each name a package-level const or var spec
// declares, `maxBodyChars` in `const maxBodyChars = 5000`, and walks the
// spec's type and values as that node's: its own value when the spec pairs
// names with values, else every value. A spec of blank names declares
// nothing, and its values are the file's.
func (x *extractor) goValueSpec(spec *sitter.Node, ctx walkCtx) {
	kind, keyword := Kind("constant"), "const "
	if spec.Kind() == "var_spec" {
		kind, keyword = "variable", "var "
	}
	firstLine, _, _ := strings.Cut(x.text(spec), "\n")
	names := namedChildrenOfKind(spec, "identifier")
	values := namedChildren(spec.ChildByFieldName("value"))
	declared := false
	for index, name := range names {
		if x.text(name) == "_" {
			continue
		}
		declared = true
		member := ctx
		member.parentID = x.emitGoMember(spec, x.text(name), kind, nil, keyword+firstLine, ctx)
		member.enclosingKind = kind
		if typ := spec.ChildByFieldName("type"); typ != nil {
			x.walk(typ, member)
		}
		if len(values) == len(names) {
			x.walk(values[index], member)
		} else {
			x.walkNamedChildren(values, member)
		}
	}
	if !declared {
		x.walkNamedChildren(namedChildren(spec), ctx)
	}
}

// emitGoMember appends the node of a field, constant or variable that decl
// declares as name, with the contains edge from what holds it, and returns
// its ID.
func (x *extractor) emitGoMember(decl *sitter.Node, name string, kind Kind, owner *string, signature string, ctx walkCtx) string {
	id := x.mintID(ctx.rel + "#" + strings.Join(append(slices.Clone(ctx.scope), name), "."))
	body := x.text(decl)
	x.nodes = append(x.nodes, NodeV1{
		ID: id, Name: name, Kind: kind, Owner: owner, Path: ctx.rel, Span: spanOf(decl),
		Signature: cleanSignature(signature), Exported: goExported(name), Origin: "ast",
		BodyHash: sourcefiles.Hash(body), BodyText: new(searchBody(body, maxBodyChars)), SummaryState: "pending",
	})
	x.edges = append(x.edges, rawEdge{source: ctx.parentID, relation: "contains", targetID: id, file: ctx.rel})
	return id
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
	edge := rawEdge{source: ctx.parentID, relation: "references", file: ctx.rel, kinds: goTypeKinds, line: lineOf(node)}
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
