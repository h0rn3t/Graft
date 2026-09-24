package graph

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/h0rn3t/Graft/internal/grammars/python"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// language names the tree-sitter grammar a depth-tier file is parsed with.
type language string

const (
	langTypeScript language = "typescript"
	langTSX        language = "tsx"
	langPython     language = "python"
	langGo         language = "go"
	langJava       language = "java"
)

// depthExtensions lists the grammar and build label for each supported suffix,
// longest suffix first.
var depthExtensions = []struct {
	ext     string
	grammar language
	label   string
}{
	{".tsx", langTSX, "tsx"},
	{".jsx", langTSX, "jsx"},
	{".mts", langTypeScript, "typescript"},
	{".cts", langTypeScript, "typescript"},
	{".ts", langTypeScript, "typescript"},
	{".mjs", langTypeScript, "javascript"},
	{".cjs", langTypeScript, "javascript"},
	{".js", langTypeScript, "javascript"},
	{".pyi", langPython, "python"},
	{".py", langPython, "python"},
	{".go", langGo, "go"},
	{".java", langJava, "java"},
}

// languageOf returns the depth-tier grammar for file and its display label.
func languageOf(file string) (grammar language, label string, ok bool) {
	lower := strings.ToLower(file)
	for _, entry := range depthExtensions {
		if strings.HasSuffix(lower, entry.ext) {
			return entry.grammar, entry.label, true
		}
	}
	return "", "", false
}

// grammars holds the tree-sitter languages a native adapter exists for. A
// depth-tier language absent here is reported as unsupported by the builder.
var grammars = map[language]func() *sitter.Language{
	langTypeScript: func() *sitter.Language { return sitter.NewLanguage(typescript.LanguageTypescript()) },
	langTSX:        func() *sitter.Language { return sitter.NewLanguage(typescript.LanguageTSX()) },
	langPython:     func() *sitter.Language { return sitter.NewLanguage(python.Language()) },
	langGo:         func() *sitter.Language { return sitter.NewLanguage(golang.Language()) },
	langJava:       func() *sitter.Language { return sitter.NewLanguage(java.Language()) },
}

type rawEdge struct {
	source       string
	relation     Relation
	file         string
	targetID     string
	specifier    string
	name         string
	viaMember    bool
	recvType     string
	kinds        []Kind
	argCount     *int
	implicitSelf bool
}

type extractResult struct {
	language string
	nodes    []NodeV1
	rawEdges []rawEdge
	// limitation explains a file indexed without its symbols.
	limitation string
}

type importBinding struct {
	name      string
	specifier string
}

// walkCtx mirrors extract.ts WalkCtx; an empty string stands for null.
type walkCtx struct {
	rel            string
	lang           language
	scope          []string
	enclosingKind  Kind
	parentID       string
	enclosingClass string
	goReceiverVar  string
	imported       map[string]importBinding
}

type defDescriptor struct {
	name      string
	idName    string
	kind      Kind
	headerEnd uint
	hashNode  *sitter.Node
	owner     string
	arity     *int
	variadic  bool
	goMethod  bool
}

type extractor struct {
	source   []byte
	lang     language
	bindings *fileBindings
	minted   map[string]struct{}
	nodes    []NodeV1
	edges    []rawEdge
}

const (
	maxBodyChars     = 5000
	maxFileBodyChars = 16000
)

var tsKinds = map[string]Kind{
	"class_declaration":              "class",
	"abstract_class_declaration":     "class",
	"function_declaration":           "function",
	"generator_function_declaration": "function",
	"method_definition":              "method",
	"interface_declaration":          "interface",
	"type_alias_declaration":         "type",
	"enum_declaration":               "enum",
}

var callTypes = map[language][]string{
	langTypeScript: {"call_expression"},
	langTSX:        {"call_expression"},
	langPython:     {"call"},
	langGo:         {"call_expression"},
	langJava:       {"method_invocation", "object_creation_expression"},
}

var functionValueTypes = []string{"arrow_function", "function", "function_expression", "generator_function"}

// extractFile parses one supported source file into its nodes and unresolved edges.
func extractFile(rel, source string) (extractResult, error) {
	if strings.EqualFold(path.Ext(rel), ".sql") {
		return extractSQL(rel, source)
	}
	if generic, ok := genericLanguageOf(rel); ok {
		if grammar, native := genericNativeGrammars[generic]; native {
			return extractGenericTags(rel, source, generic, grammar)
		}
	}
	lang, label, ok := languageOf(rel)
	newGrammar, native := grammars[lang]
	if !ok || !native {
		return extractResult{}, fmt.Errorf("unsupported source extension %q", strings.ToLower(path.Ext(rel)))
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(newGrammar()); err != nil {
		return extractResult{}, fmt.Errorf("set %s grammar: %w", label, err)
	}
	sourceBytes := []byte(source)
	tree := parser.Parse(sourceBytes, nil)
	if tree == nil {
		return extractResult{}, fmt.Errorf("parse %q: tree-sitter returned no tree", rel)
	}
	defer tree.Close()
	root := tree.RootNode()

	chars := savings.Length(source)
	x := &extractor{
		source:   sourceBytes,
		lang:     lang,
		bindings: collectBindings(root, lang, sourceBytes),
		minted:   map[string]struct{}{rel: {}},
		nodes: []NodeV1{{
			ID: rel, Name: path.Base(rel), Kind: "file", Path: rel,
			Span: fmt.Sprintf("L1-L%d", root.EndPosition().Row+1), Exported: true,
			Origin: "ast", BodyHash: sourcefiles.Hash(source), Chars: &chars, SummaryState: "pending",
		}},
	}
	ctx := walkCtx{rel: rel, lang: lang, parentID: rel, imported: x.collectImportedSymbols(root)}
	x.walkNamedChildren(namedChildren(root), ctx)
	x.nodes[0].BodyText = new(fileResidual(source, x.nodes[1:]))
	return extractResult{language: label, nodes: x.nodes, rawEdges: x.edges}, nil
}

// namedChildren lists a node's named children; a nil node has none.
func namedChildren(node *sitter.Node) []*sitter.Node {
	if node == nil {
		return nil
	}
	children := make([]*sitter.Node, 0, node.NamedChildCount())
	for index := range node.NamedChildCount() {
		children = append(children, node.NamedChild(index))
	}
	return children
}

func nodeText(node *sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	return node.Utf8Text(source)
}

func (x *extractor) text(node *sitter.Node) string {
	return nodeText(node, x.source)
}

func sameNode(a, b *sitter.Node) bool {
	return a != nil && b != nil && a.Id() == b.Id()
}

// fileResidual indexes the lines no symbol span covers, on the file node.
func fileResidual(source string, symbols []NodeV1) string {
	lines := strings.Split(source, "\n")
	covered := make([]bool, len(lines)+2)
	for _, symbol := range symbols {
		first, last, ok := parseLineSpan(symbol.Span)
		if !ok {
			continue
		}
		for row := first; row <= last && row < len(covered); row++ {
			covered[row] = true
		}
	}
	kept := make([]string, 0, len(lines))
	for index, line := range lines {
		if !covered[index+1] {
			kept = append(kept, line)
		}
	}
	return searchBody(strings.Join(kept, " "), maxFileBodyChars)
}

func parseLineSpan(span string) (first, last int, ok bool) {
	start, end, found := strings.Cut(span, "-")
	if !found || !strings.HasPrefix(start, "L") || !strings.HasPrefix(end, "L") {
		return 0, 0, false
	}
	first, errFirst := strconv.Atoi(start[1:])
	last, errLast := strconv.Atoi(end[1:])
	return first, last, errFirst == nil && errLast == nil
}

// mintID returns base, or base~N for the first free N, and records it.
func (x *extractor) mintID(base string) string {
	id := base
	for suffix := 2; ; suffix++ {
		if _, taken := x.minted[id]; !taken {
			break
		}
		id = base + "~" + strconv.Itoa(suffix)
	}
	x.minted[id] = struct{}{}
	return id
}

func (x *extractor) walkNamedChildren(children []*sitter.Node, ctx walkCtx) {
	for _, child := range children {
		x.walk(child, ctx)
	}
}

func (x *extractor) walk(node *sitter.Node, ctx walkCtx) {
	if desc := x.describe(node, ctx); desc != nil {
		x.emitDefinition(node, desc, ctx)
		return
	}

	kinds := callTypes[ctx.lang]
	switch {
	case x.isImport(node, ctx.lang):
		if specifier := x.importSpecifier(node, ctx.lang); specifier != "" {
			x.edges = append(x.edges, rawEdge{source: ctx.rel, relation: "imports", specifier: specifier, file: ctx.rel})
		}
		return
	case slices.Contains(kinds, node.Kind()):
		if callee, ok := x.calleeName(node, ctx.lang); ok {
			x.edges = append(x.edges, x.callEdge(node, callee, ctx))
		}
	case node.Kind() == "identifier" && !isDirectCallee(node, kinds) && !isDeclarationName(node):
		if imported, ok := ctx.imported[x.text(node)]; ok {
			x.edges = append(x.edges, rawEdge{source: ctx.parentID, relation: "references", name: imported.name, specifier: imported.specifier, file: ctx.rel})
		}
	}
	if ctx.lang == langJava && node.Kind() == "object_creation_expression" && x.javaAnonymousClass(node, ctx) {
		return
	}
	x.walkNamedChildren(namedChildren(node), ctx)
}

func (x *extractor) callEdge(node *sitter.Node, callee callee, ctx walkCtx) rawEdge {
	edge := rawEdge{source: ctx.parentID, relation: "calls", name: callee.name, viaMember: callee.viaMember, file: ctx.rel, kinds: callee.kinds}
	if ctx.lang == langJava {
		edge.argCount = javaArgCount(node)
	}
	edge.recvType = x.bindings.resolveRecvType(callee.receiver, ctx)
	return edge
}

func (x *extractor) emitDefinition(node *sitter.Node, desc *defDescriptor, ctx walkCtx) {
	idPart := cmp.Or(desc.idName, desc.name)
	id := x.mintID(ctx.rel + "#" + strings.Join(append(slices.Clone(ctx.scope), idPart), "."))
	ownerName := cmp.Or(desc.owner, ctx.enclosingClass)
	if desc.goMethod {
		ownerName = goReceiverType(node, x.source)
	}
	var owner *string
	if desc.kind == "method" && ownerName != "" {
		owner = &ownerName
	}
	body := x.text(desc.hashNode)
	x.nodes = append(x.nodes, NodeV1{
		ID: id, Name: desc.name, Kind: desc.kind, Owner: owner, Path: ctx.rel,
		Span:      spanOf(desc.hashNode),
		Signature: cleanSignature(string(x.source[desc.hashNode.StartByte():desc.headerEnd])),
		Exported:  x.exported(desc, node, ctx), Origin: "ast",
		BodyHash: sourcefiles.Hash(body), BodyText: new(searchBody(body, maxBodyChars)),
		Arity: desc.arity, Variadic: truePointer(desc.variadic), SummaryState: "pending",
	})
	x.edges = append(x.edges, rawEdge{source: ctx.parentID, relation: "contains", targetID: id, file: ctx.rel})
	typeDecl := desc.kind == "class" || (ctx.lang == langJava && slices.Contains(javaTypeKinds, desc.kind))
	if typeDecl {
		x.edges = append(x.edges, x.heritageEdges(node, id, ctx)...)
	}
	if ctx.lang == langJava {
		x.edges = append(x.edges, x.javaAnnotationReferences(node, id, ctx)...)
	}

	child := ctx
	child.scope = append(slices.Clone(ctx.scope), idPart)
	child.enclosingKind = desc.kind
	child.parentID = id
	child.enclosingClass = cmp.Or(desc.owner, ctx.enclosingClass)
	switch {
	case typeDecl:
		child.enclosingClass = desc.name
	case desc.goMethod:
		child.enclosingClass = ownerName
		child.goReceiverVar = goReceiverVar(node, x.source)
	}
	if desc.kind == "function" || desc.kind == "method" {
		child.imported = x.withoutShadowedImports(ctx.imported, node)
	}
	x.walkNamedChildren(namedChildren(node), child)
}

func (x *extractor) exported(desc *defDescriptor, node *sitter.Node, ctx walkCtx) bool {
	switch ctx.lang {
	case langPython:
		return !strings.HasPrefix(desc.name, "_")
	case langGo:
		return goExported(desc.name)
	case langJava:
		return javaExported(node)
	}
	for ancestor := node.Parent(); ancestor != nil; ancestor = ancestor.Parent() {
		if ancestor.Kind() == "export_statement" {
			return true
		}
	}
	return false
}

func (x *extractor) describe(node *sitter.Node, ctx walkCtx) *defDescriptor {
	switch ctx.lang {
	case langGo:
		return x.describeGo(node)
	case langJava:
		return x.describeJava(node)
	case langPython:
		return x.describePython(node, ctx)
	}
	if kind, ok := tsKinds[node.Kind()]; ok {
		name := x.text(node.ChildByFieldName("name"))
		if name == "" {
			return nil
		}
		return &defDescriptor{name: name, kind: kind, headerEnd: headerEnd(node, node.ChildByFieldName("body")), hashNode: node}
	}
	if node.Kind() == "variable_declarator" {
		value := node.ChildByFieldName("value")
		if value != nil && slices.Contains(functionValueTypes, value.Kind()) {
			name := x.text(node.ChildByFieldName("name"))
			if name == "" {
				return nil
			}
			return &defDescriptor{name: name, kind: "function", headerEnd: headerEnd(node, value.ChildByFieldName("body")), hashNode: node}
		}
	}
	return nil
}

// spanOf is a node's 1-based inclusive line span, `Lstart-Lend`.
func spanOf(node *sitter.Node) string {
	return fmt.Sprintf("L%d-L%d", node.StartPosition().Row+1, node.EndPosition().Row+1)
}

// headerEnd is where a definition's signature stops: its body, or its end.
func headerEnd(node, body *sitter.Node) uint {
	if body != nil {
		return body.StartByte()
	}
	return node.EndByte()
}

func (x *extractor) heritageEdges(node *sitter.Node, classID string, ctx walkCtx) []rawEdge {
	switch ctx.lang {
	case langJava:
		return x.javaHeritage(node, classID, ctx)
	case langPython:
		return x.pythonHeritage(node, classID, ctx)
	}
	var edges []rawEdge
	for _, clause := range namedChildren(namedChildOfKind(node, "class_heritage")) {
		relation := map[string]Relation{"implements_clause": "implements", "extends_clause": "extends"}[clause.Kind()]
		if relation == "" {
			continue
		}
		for _, target := range namedChildrenOfKind(clause, "identifier", "type_identifier") {
			edges = append(edges, rawEdge{source: classID, relation: relation, name: x.text(target), file: ctx.rel})
		}
	}
	return edges
}

type callee struct {
	name      string
	viaMember bool
	receiver  string
	kinds     []Kind
}

func (x *extractor) calleeName(node *sitter.Node, lang language) (callee, bool) {
	switch lang {
	case langJava:
		return x.javaCallee(node)
	}
	function := node.ChildByFieldName("function")
	if function == nil {
		return callee{}, false
	}
	switch {
	case function.Kind() == "identifier":
		return callee{name: x.text(function)}, true
	case lang == langPython && function.Kind() == "attribute":
		attribute := cmp.Or(function.ChildByFieldName("attribute"), lastNamedChild(function))
		if attribute == nil {
			return callee{}, false
		}
		return callee{name: x.text(attribute), viaMember: true, receiver: x.pythonReceiver(function)}, true
	case lang == langGo && function.Kind() == "selector_expression":
		field := cmp.Or(function.ChildByFieldName("field"), lastNamedChild(function))
		if field == nil {
			return callee{}, false
		}
		receiver := ""
		if operand := function.ChildByFieldName("operand"); operand != nil && operand.Kind() == "identifier" {
			receiver = x.text(operand)
		}
		return callee{name: x.text(field), viaMember: true, receiver: receiver}, true
	case (lang == langTypeScript || lang == langTSX) && function.Kind() == "member_expression":
		property := cmp.Or(function.ChildByFieldName("property"), lastNamedChild(function))
		if property == nil {
			return callee{}, false
		}
		return callee{name: x.text(property), viaMember: true, receiver: x.tsReceiver(function)}, true
	}
	return callee{}, false
}

func lastNamedChild(node *sitter.Node) *sitter.Node {
	if count := node.NamedChildCount(); count > 0 {
		return node.NamedChild(count - 1)
	}
	return nil
}

func (x *extractor) tsReceiver(function *sitter.Node) string {
	object := function.ChildByFieldName("object")
	if object == nil {
		return ""
	}
	switch object.Kind() {
	case "this":
		return "this"
	case "identifier":
		return x.text(object)
	case "member_expression":
		inner := object.ChildByFieldName("object")
		property := object.ChildByFieldName("property")
		if inner != nil && inner.Kind() == "this" && property != nil {
			return "this." + x.text(property)
		}
	}
	return ""
}

func (x *extractor) isImport(node *sitter.Node, lang language) bool {
	switch lang {
	case langGo:
		return node.Kind() == "import_spec"
	case langJava:
		return node.Kind() == "import_declaration"
	}
	return node.Kind() == "import_statement" || node.Kind() == "import_from_statement"
}

func (x *extractor) importSpecifier(node *sitter.Node, lang language) string {
	switch lang {
	case langPython:
		return x.pythonImportSpecifier(node)
	case langGo:
		return x.goImportSpecifier(node)
	case langJava:
		return x.text(namedChildOfKind(node, "scoped_identifier", "identifier"))
	}
	str := namedChildOfKind(node, "string")
	if str == nil {
		return ""
	}
	if fragment := namedChildOfKind(str, "string_fragment"); fragment != nil {
		return x.text(fragment)
	}
	return trimOneQuote(x.text(str), "'\"")
}

// trimOneQuote drops one leading and one trailing quote character, as the TS
// `/^['"]|['"]$/g` replacement does.
func trimOneQuote(text, quotes string) string {
	if text != "" && strings.ContainsRune(quotes, rune(text[0])) {
		text = text[1:]
	}
	if text != "" && strings.ContainsRune(quotes, rune(text[len(text)-1])) {
		text = text[:len(text)-1]
	}
	return text
}

func isDirectCallee(node *sitter.Node, kinds []string) bool {
	parent := node.Parent()
	if parent == nil || !slices.Contains(kinds, parent.Kind()) {
		return false
	}
	return sameNode(parent.ChildByFieldName("function"), node) || sameNode(parent.ChildByFieldName("name"), node)
}

func isDeclarationName(node *sitter.Node) bool {
	parent := node.Parent()
	return parent != nil && sameNode(parent.ChildByFieldName("name"), node)
}

// collectImportedSymbols records named imports whose local binding can be
// recognized later as a symbol use.
func (x *extractor) collectImportedSymbols(root *sitter.Node) map[string]importBinding {
	imported := make(map[string]importBinding)
	if x.lang != langTypeScript && x.lang != langTSX {
		return imported
	}
	var bindings func(*sitter.Node, string)
	bindings = func(node *sitter.Node, specifier string) {
		if node.Kind() == "import_specifier" {
			name := x.text(node.ChildByFieldName("name"))
			local := cmp.Or(x.text(node.ChildByFieldName("alias")), name)
			if name != "" && local != "" {
				imported[local] = importBinding{name: name, specifier: specifier}
			}
			return
		}
		for _, child := range namedChildren(node) {
			bindings(child, specifier)
		}
	}
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if node.Kind() == "import_statement" {
			if specifier := x.importSpecifier(node, x.lang); specifier != "" {
				bindings(node, specifier)
			}
			return
		}
		for _, child := range namedChildren(node) {
			visit(child)
		}
	}
	visit(root)
	return imported
}

var functionBoundaries = []string{"function_declaration", "generator_function_declaration", "method_definition", "arrow_function", "function_expression", "function"}

// withoutShadowedImports drops imported bindings hidden by locals in this definition.
func (x *extractor) withoutShadowedImports(imports map[string]importBinding, definition *sitter.Node) map[string]importBinding {
	if len(imports) == 0 {
		return imports
	}
	shadowed := make(map[string]struct{})
	definitionValue := definition.ChildByFieldName("value")
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if !sameNode(node, definition) && !sameNode(node, definitionValue) && slices.Contains(functionBoundaries, node.Kind()) {
			if name := node.ChildByFieldName("name"); name != nil && name.Kind() == "identifier" {
				shadowed[x.text(name)] = struct{}{}
			}
			return
		}
		switch node.Kind() {
		case "variable_declarator":
			if name := node.ChildByFieldName("name"); name != nil && name.Kind() == "identifier" {
				shadowed[x.text(name)] = struct{}{}
			}
		case "required_parameter", "optional_parameter":
			if pattern := node.ChildByFieldName("pattern"); pattern != nil && pattern.Kind() == "identifier" {
				shadowed[x.text(pattern)] = struct{}{}
			}
		case "identifier":
			if parent := node.Parent(); parent != nil && parent.Kind() == "formal_parameters" {
				shadowed[x.text(node)] = struct{}{}
			}
		}
		for _, child := range namedChildren(node) {
			visit(child)
		}
	}
	visit(definition)
	filtered := maps.Clone(imports)
	maps.DeleteFunc(filtered, func(local string, _ importBinding) bool {
		_, hidden := shadowed[local]
		return hidden
	})
	if len(filtered) == len(imports) {
		return imports
	}
	return filtered
}

// cleanSignature collapses whitespace and strips one trailing `=>`, `{`, `:` or
// `=`, as extract.ts clean() does; an empty header is null.
func cleanSignature(raw string) *string {
	signature := strings.Join(jsFields(raw), " ")
	if before, ok := strings.CutSuffix(signature, "=>"); ok {
		signature = before
	} else if signature != "" && strings.ContainsRune("{:=", rune(signature[len(signature)-1])) {
		signature = signature[:len(signature)-1]
	}
	signature = strings.Join(jsFields(signature), " ")
	if signature == "" {
		return nil
	}
	return &signature
}

// searchBody is the whitespace-normalized text capped at max UTF-16 code units,
// the unit JavaScript string lengths count in.
func searchBody(text string, max int) string {
	normalized := strings.Join(jsFields(text), " ")
	units := 0
	for index, r := range normalized {
		if units+utf16.RuneLen(r) > max {
			return normalized[:index]
		}
		units += utf16.RuneLen(r)
	}
	return normalized
}

// jsFields splits on the characters JavaScript's `\s` matches, which differ
// from unicode.IsSpace (U+FEFF is included, U+0085 is not).
func jsFields(text string) []string {
	return strings.FieldsFunc(text, isJSSpace)
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// truePointer is nil for false, so an optional boolean field is omitted.
func truePointer(value bool) *bool {
	if !value {
		return nil
	}
	return &value
}
