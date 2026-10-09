package graph

import (
	"cmp"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Route is an HTTP endpoint declared in source.
type Route struct {
	// Method is GET, POST and so on, several joined by "|", or ANY.
	Method string
	Path   string
	// File and Line locate the declaration: the registering call, or the
	// decorator or annotation on the handler.
	File string
	Line int
	// Handler is the graph node that handles the route, when the handler is a
	// named function or method the graph holds.
	Handler *NodeV1
	// HandlerText is the handler as written when it is not a graph node: an
	// inline function, or an expression the graph cannot name.
	HandlerText string
}

// routeCandidate is a cheap textual test for a file that may declare routes.
var routeCandidate = regexp.MustCompile(`(?i)(get|post|put|patch|delete|head|options|all|any|route|handle(func)?|mapping|path)\s*\(`)

// routeVerbs maps a registering call, decorator or annotation name, lower
// case, onto its HTTP method.
var routeVerbs = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE", "head": "HEAD", "options": "OPTIONS",
	"all": "ANY", "any": "ANY", "handle": "ANY", "handlefunc": "ANY", "route": "GET", "api_route": "GET", "websocket": "WS",
	"getmapping": "GET", "postmapping": "POST", "putmapping": "PUT", "patchmapping": "PATCH", "deletemapping": "DELETE", "requestmapping": "ANY",
}

// goRouteCalls, pythonRouteDecorators and expressRouteCalls are the routeVerbs
// names each framework family registers handlers with.
var (
	goRouteCalls          = []string{"get", "post", "put", "patch", "delete", "head", "options", "any", "handle", "handlefunc"}
	pythonRouteDecorators = []string{"get", "post", "put", "patch", "delete", "head", "options", "route", "api_route", "websocket"}
	expressRouteCalls     = []string{"get", "post", "put", "patch", "delete", "head", "options", "all"}
	nestRouteDecorators   = []string{"Get", "Post", "Put", "Patch", "Delete", "Head", "Options", "All"}
)

// httpClients are receivers whose get/post calls send requests rather than
// register handlers.
var httpClients = map[string]bool{
	"axios": true, "http": true, "https": true, "fetch": true, "client": true, "request": true,
	"superagent": true, "got": true, "ky": true, "$http": true, "httpClient": true, "api": true,
}

// FindRoutes lists the HTTP routes declared in production files under the in
// prefix: Go net/http, gin, echo and chi registrations; Flask, FastAPI and
// Django decorators and URL patterns; Express-style registrations and NestJS
// decorators; Spring mapping annotations. A group prefix is applied for
// NestJS controllers and Spring classes only.
func FindRoutes(graph GraphV1, repoRoot, in string) ([]Route, error) {
	prefix := normalizePathPrefix(in)
	if in != "" {
		if err := assertPrefixIndexed(graph, prefix); err != nil {
			return nil, err
		}
	}
	definitions := make(map[string][]NodeV1) // path → functions and methods
	for _, node := range graph.Nodes {
		if node.Kind == "function" || node.Kind == "method" {
			definitions[node.Path] = append(definitions[node.Path], node)
		}
	}
	var routes []Route
	for _, file := range graph.Nodes {
		lang, _, ok := languageOf(file.Path)
		if file.Kind != "file" || !ok || IsCopyPath(file.Path) || (in != "" && !pathUnderPrefix(file.Path, prefix)) {
			continue
		}
		source, readable, err := sourcefiles.Read(filepath.Join(repoRoot, filepath.FromSlash(file.Path)))
		if err != nil || !readable || !routeCandidate.MatchString(source) {
			continue // a file deleted or unreadable since the build declares nothing
		}
		scan := routeScan{file: file.Path, source: []byte(source), definitions: definitions}
		if err := scan.parse(lang); err != nil {
			continue
		}
		routes = append(routes, scan.routes...)
	}
	slices.SortFunc(routes, func(a, b Route) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Method, b.Method), cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	return routes, nil
}

// routeScan collects the routes of one file.
type routeScan struct {
	file        string
	source      []byte
	definitions map[string][]NodeV1
	routes      []Route
}

func (scan *routeScan) parse(lang language) error {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(grammars[lang]()); err != nil {
		return err
	}
	tree := parser.Parse(scan.source, nil)
	defer tree.Close()
	scan.visit(tree.RootNode(), lang, "")
	return nil
}

// visit walks the tree; group is the path prefix of the enclosing NestJS
// controller or Spring class.
func (scan *routeScan) visit(node *sitter.Node, lang language, group string) {
	typescript := lang == langTypeScript || lang == langTSX
	switch kind := node.Kind(); {
	case lang == langGo && kind == "call_expression":
		scan.goRegistration(node)
	case lang == langPython && kind == "decorated_definition":
		scan.pythonDecorated(node)
	case lang == langPython && kind == "call" && path.Base(scan.file) == "urls.py":
		scan.djangoPattern(node)
	case typescript && kind == "call_expression":
		scan.expressRegistration(node)
	case typescript && kind == "class_declaration":
		group = scan.nestController(node.ChildByFieldName("decorator"))
	case typescript && kind == "method_definition":
		scan.nestMethod(node, group)
	case lang == langJava && kind == "class_declaration":
		if _, routePath, ok := scan.springMapping(node); ok {
			group = routePath
		}
	case lang == langJava && kind == "method_declaration":
		if method, routePath, ok := scan.springMapping(node); ok {
			scan.add(method, joinRoute(group, routePath), node, scan.definitionAt(node), "")
		}
	}
	for _, child := range namedChildren(node) {
		scan.visit(child, lang, group)
	}
}

func (scan *routeScan) text(node *sitter.Node) string {
	return nodeText(node, scan.source)
}

func (scan *routeScan) add(method, routePath string, at *sitter.Node, handler *NodeV1, handlerText string) {
	scan.routes = append(scan.routes, Route{
		Method: method, Path: routePath, File: scan.file, Line: int(at.StartPosition().Row) + 1,
		Handler: handler, HandlerText: handlerText,
	})
}

// definitionAt is the function or method node whose span starts at node.
func (scan *routeScan) definitionAt(node *sitter.Node) *NodeV1 {
	line := int(node.StartPosition().Row) + 1
	definitions := scan.definitions[scan.file]
	for index := range definitions {
		if first, _, ok := parseLineSpan(definitions[index].Span); ok && first == line {
			return &definitions[index]
		}
	}
	return nil
}

// handlerNamed resolves a handler expression to the one function or method of
// that name in the file's directory: `getUser`, `h.getUser`, `views.index`.
// An inline function, or a name the directory does not define exactly once,
// is returned as text.
func (scan *routeScan) handlerNamed(expression *sitter.Node) (*NodeV1, string) {
	text := scan.text(expression)
	switch expression.Kind() {
	case "identifier", "selector_expression", "member_expression", "attribute":
	default:
		return nil, "inline function"
	}
	name := text[strings.LastIndexByte(text, '.')+1:]
	var match *NodeV1
	for file, definitions := range scan.definitions {
		if path.Dir(file) != path.Dir(scan.file) {
			continue
		}
		for index := range definitions {
			if definitions[index].Name != name {
				continue
			}
			if match != nil {
				return nil, text
			}
			match = &definitions[index]
		}
	}
	if match == nil {
		return nil, text
	}
	return match, ""
}

// stringValue is a string literal's content without quotes; a template
// string with a substitution is no constant path.
func (scan *routeScan) stringValue(node *sitter.Node) (string, bool) {
	if node == nil {
		return "", false
	}
	switch node.Kind() {
	case "interpreted_string_literal", "raw_string_literal", "string", "template_string", "string_literal":
	default:
		return "", false
	}
	var content strings.Builder
	for _, part := range namedChildren(node) {
		switch part.Kind() {
		case "interpreted_string_literal_content", "raw_string_literal_content", "string_content", "string_fragment":
			content.WriteString(scan.text(part))
		case "template_substitution":
			return "", false
		}
	}
	return content.String(), true
}

// goRegistration reads `mux.HandleFunc("GET /users/{id}", h.get)`,
// `r.GET("/users", list)` and the like.
func (scan *routeScan) goRegistration(call *sitter.Node) {
	function := call.ChildByFieldName("function")
	if function == nil || function.Kind() != "selector_expression" {
		return
	}
	verb := strings.ToLower(scan.text(function.ChildByFieldName("field")))
	arguments := namedChildren(call.ChildByFieldName("arguments"))
	if !slices.Contains(goRouteCalls, verb) || len(arguments) < 2 {
		return
	}
	pattern, ok := scan.stringValue(arguments[0])
	if !ok {
		return
	}
	method := routeVerbs[verb]
	if prefix, rest, found := strings.Cut(pattern, " "); found && prefix == strings.ToUpper(prefix) && strings.HasPrefix(rest, "/") {
		method, pattern = prefix, rest // a Go 1.22 method pattern
	}
	if !strings.HasPrefix(pattern, "/") {
		return
	}
	handler, handlerText := scan.handlerNamed(arguments[len(arguments)-1])
	scan.add(method, pattern, call, handler, handlerText)
}

// pythonDecorated reads `@app.get("/users")` and
// `@bp.route("/users", methods=["GET", "POST"])` above a function.
func (scan *routeScan) pythonDecorated(decorated *sitter.Node) {
	definition := decorated.ChildByFieldName("definition")
	for _, decorator := range namedChildrenOfKind(decorated, "decorator") {
		call := namedChildOfKind(decorator, "call")
		if call == nil {
			continue
		}
		function := call.ChildByFieldName("function")
		if function == nil || function.Kind() != "attribute" {
			continue
		}
		verb := scan.text(function.ChildByFieldName("attribute"))
		if !slices.Contains(pythonRouteDecorators, verb) {
			continue
		}
		method, routePath, found := routeVerbs[verb], "", false
		for _, argument := range namedChildren(call.ChildByFieldName("arguments")) {
			if value, isString := scan.stringValue(argument); isString && !found {
				routePath, found = value, true
			}
			if argument.Kind() == "keyword_argument" && scan.text(argument.ChildByFieldName("name")) == "methods" {
				var methods []string
				for _, item := range namedChildren(argument.ChildByFieldName("value")) {
					if value, isString := scan.stringValue(item); isString {
						methods = append(methods, strings.ToUpper(value))
					}
				}
				if len(methods) > 0 {
					method = strings.Join(methods, "|")
				}
			}
		}
		if found && strings.HasPrefix(routePath, "/") {
			scan.add(method, routePath, decorator, scan.definitionAt(definition), "")
		}
	}
}

// djangoPattern reads `path("users/<int:id>/", views.user)` in a urls.py.
func (scan *routeScan) djangoPattern(call *sitter.Node) {
	name := scan.text(call.ChildByFieldName("function"))
	arguments := namedChildren(call.ChildByFieldName("arguments"))
	if (name != "path" && name != "re_path") || len(arguments) < 2 {
		return
	}
	pattern, ok := scan.stringValue(arguments[0])
	if !ok {
		return
	}
	handler, handlerText := scan.handlerNamed(arguments[1])
	scan.add("ANY", "/"+strings.TrimPrefix(pattern, "/"), call, handler, handlerText)
}

// expressRegistration reads `app.get("/users", list)`. A call whose last
// argument is not a handler, or whose receiver is an HTTP client, sends a
// request rather than declaring a route.
func (scan *routeScan) expressRegistration(call *sitter.Node) {
	function := call.ChildByFieldName("function")
	if function == nil || function.Kind() != "member_expression" {
		return
	}
	verb := scan.text(function.ChildByFieldName("property"))
	arguments := namedChildren(call.ChildByFieldName("arguments"))
	if !slices.Contains(expressRouteCalls, verb) || len(arguments) < 2 || httpClients[scan.text(function.ChildByFieldName("object"))] {
		return
	}
	routePath, ok := scan.stringValue(arguments[0])
	last := arguments[len(arguments)-1]
	if !ok || !strings.HasPrefix(routePath, "/") ||
		!slices.Contains([]string{"identifier", "member_expression", "arrow_function", "function_expression", "function"}, last.Kind()) {
		return
	}
	handler, handlerText := scan.handlerNamed(last)
	scan.add(routeVerbs[verb], routePath, call, handler, handlerText)
}

// nestDecorator is the name and path argument of a decorator call such as
// `@Get(":id")`; the path is "" when the call has none.
func (scan *routeScan) nestDecorator(decorator *sitter.Node) (name, routePath string) {
	call := namedChildOfKind(decorator, "call_expression")
	if call == nil {
		return "", ""
	}
	routePath, _ = scan.stringValue(namedChildOfKind(call.ChildByFieldName("arguments"), "string", "template_string"))
	return scan.text(call.ChildByFieldName("function")), routePath
}

// nestController is the path of a class's `@Controller("users")` decorator,
// "" when the class has none.
func (scan *routeScan) nestController(decorator *sitter.Node) string {
	for ; decorator != nil && decorator.Kind() == "decorator"; decorator = decorator.NextNamedSibling() {
		if name, routePath := scan.nestDecorator(decorator); name == "Controller" {
			return routePath
		}
	}
	return ""
}

// nestMethod reads `@Get(":id")` decorating a controller method.
func (scan *routeScan) nestMethod(method *sitter.Node, group string) {
	for decorator := method.PrevNamedSibling(); decorator != nil && decorator.Kind() == "decorator"; decorator = decorator.PrevNamedSibling() {
		if name, routePath := scan.nestDecorator(decorator); slices.Contains(nestRouteDecorators, name) {
			scan.add(routeVerbs[strings.ToLower(name)], joinRoute(group, routePath), decorator, scan.definitionAt(method), "")
		}
	}
}

// springMapping reads a Spring mapping annotation on a class or method:
// `@GetMapping("/x")`, `@RequestMapping(value = "/y", method = RequestMethod.POST)`.
func (scan *routeScan) springMapping(declaration *sitter.Node) (method, routePath string, ok bool) {
	for _, annotation := range namedChildrenOfKind(namedChildOfKind(declaration, "modifiers"), "annotation", "marker_annotation") {
		name := scan.text(annotation.ChildByFieldName("name"))
		verb, known := routeVerbs[strings.ToLower(name)]
		if !known || !strings.HasSuffix(name, "Mapping") {
			continue
		}
		method = verb
		for _, argument := range namedChildren(annotation.ChildByFieldName("arguments")) {
			if value, isString := scan.stringValue(argument); isString {
				routePath = value
			}
			if argument.Kind() != "element_value_pair" {
				continue
			}
			value := argument.ChildByFieldName("value")
			switch scan.text(argument.ChildByFieldName("key")) {
			case "value", "path":
				routePath, _ = scan.stringValue(value)
			case "method":
				text := scan.text(value)
				method = strings.ToUpper(text[strings.LastIndexByte(text, '.')+1:])
			}
		}
		return method, routePath, true
	}
	return "", "", false
}

// joinRoute joins a group prefix and a route path with single slashes.
func joinRoute(group, routePath string) string {
	return "/" + strings.Trim(strings.Trim(group, "/")+"/"+strings.Trim(routePath, "/"), "/")
}
