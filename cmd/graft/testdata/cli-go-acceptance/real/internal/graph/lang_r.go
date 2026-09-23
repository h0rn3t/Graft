package graph

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

var (
	rAssignOperators      = []string{"<-", "<<-", "="}
	rRightAssignOperators = []string{"->", "->>"}
	rImportCalls          = []string{"library", "require", "source"}
	rRoxygenExport        = regexp.MustCompile(`^#'\s*@export\b`)
	// rBaseGenerics are base-R S3 generics assumed without a local UseMethod().
	rBaseGenerics = []string{
		"print", "format", "summary", "plot", "str", "toString", "as.character", "as.list",
		"as.data.frame", "as.vector", "as.numeric", "as.matrix", "length", "dim", "names", "rev",
		"sort", "unique", "predict", "coef", "residuals", "fitted", "update", "merge", "all.equal",
		"anova", "confint", "vcov", "logLik",
	}
)

// rCalleeName is a call's bare or namespace-qualified function name.
func rCalleeName(node *sitter.Node, source []byte) string {
	function := node.ChildByFieldName("function")
	switch {
	case function == nil:
	case function.Kind() == "identifier":
		return nodeText(function, source)
	case function.Kind() == "namespace_operator":
		if rhs := function.ChildByFieldName("rhs"); rhs != nil && rhs.Kind() == "identifier" {
			return nodeText(rhs, source)
		}
	}
	return ""
}

func rCallArgs(node *sitter.Node) []*sitter.Node {
	return namedChildrenOfKind(node.ChildByFieldName("arguments"), "argument")
}

// rStringContent is a string node's unquoted text; ok is false for a non-string.
func rStringContent(node *sitter.Node, source []byte) (string, bool) {
	if node == nil || node.Kind() != "string" {
		return "", false
	}
	content := namedChildOfKind(node, "string_content")
	return nodeText(content, source), content != nil
}

func rNamedArg(node *sitter.Node, name string, source []byte) *sitter.Node {
	for _, argument := range rCallArgs(node) {
		if nodeText(argument.ChildByFieldName("name"), source) == name {
			return argument.ChildByFieldName("value")
		}
	}
	return nil
}

func rIsMixinContainer(node *sitter.Node, source []byte) bool {
	return slices.ContainsFunc(rCallArgs(node), func(argument *sitter.Node) bool {
		name := nodeText(argument.ChildByFieldName("name"), source)
		value := argument.ChildByFieldName("value")
		return (name == "public" || name == "private") && value != nil && value.Kind() == "call" && rCalleeName(value, source) == "list"
	})
}

func rOperator(node *sitter.Node, source []byte) string {
	return nodeText(node.ChildByFieldName("operator"), source)
}

// collectRGenerics records the S3 generics this file registers via UseMethod().
func collectRGenerics(root *sitter.Node, source []byte) map[string]struct{} {
	generics := make(map[string]struct{})
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		var definition *sitter.Node
		ownName := ""
		switch node.Kind() {
		case "binary_operator":
			lhs, rhs := node.ChildByFieldName("lhs"), node.ChildByFieldName("rhs")
			if slices.Contains(rAssignOperators, rOperator(node, source)) && lhs != nil && lhs.Kind() == "identifier" && rhs != nil && rhs.Kind() == "function_definition" {
				definition, ownName = rhs, nodeText(lhs, source)
			}
		case "function_definition":
			if body := node.ChildByFieldName("body"); body != nil && body.Kind() == "binary_operator" {
				if rhs := body.ChildByFieldName("rhs"); slices.Contains(rRightAssignOperators, rOperator(body, source)) && rhs != nil && rhs.Kind() == "identifier" {
					definition, ownName = node, nodeText(rhs, source)
				}
			}
		}
		if definition != nil && ownName != "" {
			if generic, found := findUseMethodArg(definition.ChildByFieldName("body"), source); found {
				generics[cmp.Or(generic, ownName)] = struct{}{}
			}
		}
		for _, child := range namedChildren(node) {
			visit(child)
		}
	}
	visit(root)
	return generics
}

// findUseMethodArg finds a UseMethod() call: its string generic, or "" for no argument.
func findUseMethodArg(node *sitter.Node, source []byte) (string, bool) {
	if node == nil {
		return "", false
	}
	if node.Kind() == "call" && rCalleeName(node, source) == "UseMethod" {
		if args := rCallArgs(node); len(args) > 0 {
			if first := args[0].ChildByFieldName("value"); first != nil && first.Kind() == "string" {
				text, _ := rStringContent(first, source)
				return text, true
			}
		}
		return "", true
	}
	for _, child := range namedChildren(node) {
		if generic, found := findUseMethodArg(child, source); found {
			return generic, true
		}
	}
	return "", false
}

func (x *extractor) describeR(node *sitter.Node, ctx walkCtx) *defDescriptor {
	switch node.Kind() {
	case "binary_operator":
		lhs, rhs := node.ChildByFieldName("lhs"), node.ChildByFieldName("rhs")
		if !slices.Contains(rAssignOperators, rOperator(node, x.source)) || lhs == nil || lhs.Kind() != "identifier" || rhs == nil {
			return nil
		}
		switch {
		case rhs.Kind() == "function_definition":
			return x.rFunctionDescriptor(x.text(lhs), rhs, rhs.ChildByFieldName("body"))
		case rhs.Kind() == "call" && (rCalleeName(rhs, x.source) == "R6Class" || (rCalleeName(rhs, x.source) == "list" && rIsMixinContainer(rhs, x.source))):
			return &defDescriptor{name: x.text(lhs), kind: "class", headerEnd: rhs.EndByte(), hashNode: rhs}
		}
	case "function_definition":
		body := node.ChildByFieldName("body")
		if body == nil || body.Kind() != "binary_operator" || !slices.Contains(rRightAssignOperators, rOperator(body, x.source)) {
			return nil
		}
		if rhs := body.ChildByFieldName("rhs"); rhs != nil && rhs.Kind() == "identifier" {
			return x.rFunctionDescriptor(x.text(rhs), node, body.ChildByFieldName("lhs"))
		}
	case "call":
		return x.describeRTopLevelCall(node)
	case "argument":
		name, value := node.ChildByFieldName("name"), node.ChildByFieldName("value")
		if ctx.rR6Access == "" || name == nil || name.Kind() != "identifier" || value == nil || value.Kind() != "function_definition" {
			return nil
		}
		return &defDescriptor{name: x.text(name), kind: "method", headerEnd: headerEnd(value, value.ChildByFieldName("body")), hashNode: value}
	}
	return nil
}

func (x *extractor) rFunctionDescriptor(name string, hashNode, body *sitter.Node) *defDescriptor {
	end := headerEnd(hashNode, body)
	parts := strings.Split(name, ".")
	for count := len(parts) - 1; count >= 1; count-- {
		generic := strings.Join(parts[:count], ".")
		if _, local := x.rGenerics[generic]; local || slices.Contains(rBaseGenerics, generic) {
			class := strings.Join(parts[count:], ".")
			return &defDescriptor{name: generic, idName: class + "." + generic, kind: "method", headerEnd: end, hashNode: hashNode, owner: class}
		}
	}
	return &defDescriptor{name: name, kind: "function", headerEnd: end, hashNode: hashNode}
}

func (x *extractor) describeRTopLevelCall(node *sitter.Node) *defDescriptor {
	args := rCallArgs(node)
	argString := func(index int) string {
		if index >= len(args) {
			return ""
		}
		text, _ := rStringContent(args[index].ChildByFieldName("value"), x.source)
		return text
	}
	switch rCalleeName(node, x.source) {
	case "setClass":
		if name := argString(0); name != "" {
			return &defDescriptor{name: name, kind: "class", headerEnd: node.EndByte(), hashNode: node}
		}
	case "setMethod":
		generic, class := argString(0), argString(1)
		var definition *sitter.Node
		for _, argument := range args {
			if x.text(argument.ChildByFieldName("name")) == "definition" {
				definition = argument
				break
			}
		}
		if definition == nil && len(args) > 2 {
			definition = args[2]
		}
		if definition != nil {
			definition = definition.ChildByFieldName("value")
		}
		if generic == "" || class == "" || definition == nil || definition.Kind() != "function_definition" {
			return nil
		}
		return &defDescriptor{name: generic, idName: class + "." + generic, kind: "method", headerEnd: headerEnd(definition, definition.ChildByFieldName("body")), hashNode: definition, owner: class}
	}
	return nil
}

func (x *extractor) rR6ParentClass(node *sitter.Node) string {
	call := node
	if node.Kind() == "binary_operator" {
		call = node.ChildByFieldName("rhs")
	}
	if call == nil || call.Kind() != "call" || rCalleeName(call, x.source) != "R6Class" {
		return ""
	}
	if value := rNamedArg(call, "inherit", x.source); value != nil && value.Kind() == "identifier" {
		return x.text(value)
	}
	return ""
}

func (x *extractor) rExported(name string, ctx walkCtx, node *sitter.Node) bool {
	if ctx.rR6Access != "" {
		return ctx.rR6Access != "private"
	}
	sawRoxygen, exported := false, false
	for sibling := node.PrevNamedSibling(); sibling != nil && sibling.Kind() == "comment"; sibling = sibling.PrevNamedSibling() {
		text := strings.TrimFunc(x.text(sibling), isJSSpace)
		if !strings.HasPrefix(text, "#'") {
			break
		}
		sawRoxygen = true
		exported = exported || rRoxygenExport.MatchString(text)
	}
	if sawRoxygen {
		return exported
	}
	return !strings.HasPrefix(name, ".")
}

func (x *extractor) rHeritage(node *sitter.Node, classID string, ctx walkCtx) []rawEdge {
	call := node
	if node.Kind() == "binary_operator" {
		call = node.ChildByFieldName("rhs")
	}
	if call == nil || call.Kind() != "call" {
		return nil
	}
	var edges []rawEdge
	switch rCalleeName(call, x.source) {
	case "R6Class":
		if value := rNamedArg(call, "inherit", x.source); value != nil && value.Kind() == "identifier" {
			edges = append(edges, rawEdge{source: classID, relation: "extends", name: x.text(value), file: ctx.rel})
		}
	case "setClass":
		for _, name := range x.rStringOrCVector(rNamedArg(call, "contains", x.source)) {
			edges = append(edges, rawEdge{source: classID, relation: "extends", name: name, file: ctx.rel})
		}
	}
	return edges
}

func (x *extractor) rStringOrCVector(value *sitter.Node) []string {
	if value == nil {
		return nil
	}
	if single, ok := rStringContent(value, x.source); ok && single != "" {
		return []string{single}
	}
	if value.Kind() != "call" || rCalleeName(value, x.source) != "c" {
		return nil
	}
	var names []string
	for _, argument := range rCallArgs(value) {
		if text, _ := rStringContent(argument.ChildByFieldName("value"), x.source); text != "" {
			names = append(names, text)
		}
	}
	return names
}

func (x *extractor) rCallee(node *sitter.Node) (callee, bool) {
	function := node.ChildByFieldName("function")
	if function == nil {
		return callee{}, false
	}
	if function.Kind() == "identifier" {
		return callee{name: x.text(function)}, true
	}
	if function.Kind() != "extract_operator" && function.Kind() != "namespace_operator" {
		return callee{}, false
	}
	rhs := function.ChildByFieldName("rhs")
	if rhs == nil || rhs.Kind() != "identifier" {
		return callee{}, false
	}
	if function.Kind() == "namespace_operator" {
		return callee{name: x.text(rhs)}, true
	}
	if lhs := function.ChildByFieldName("lhs"); lhs != nil && lhs.Kind() == "identifier" {
		switch x.text(lhs) {
		case "self", "private":
			return callee{name: x.text(rhs), viaMember: true, receiver: "self"}, true
		case "super":
			return callee{name: x.text(rhs), viaMember: true, receiver: "super"}, true
		}
	}
	return callee{name: x.text(rhs), kinds: []Kind{"function", "method"}}, true
}

func (x *extractor) rIsImport(node *sitter.Node) bool {
	if node.Kind() != "call" {
		return false
	}
	function := node.ChildByFieldName("function")
	return function != nil && function.Kind() == "identifier" && slices.Contains(rImportCalls, x.text(function))
}

func (x *extractor) rImportSpecifier(node *sitter.Node) string {
	args := rCallArgs(node)
	if len(args) == 0 {
		return ""
	}
	value := args[0].ChildByFieldName("value")
	if value != nil && value.Kind() == "identifier" {
		return x.text(value)
	}
	text, _ := rStringContent(value, x.source)
	return text
}

// rConsumedClassCall is the R6Class(...) or mixin list(...) its binary_operator
// already defined, which must not also become a call edge.
func (x *extractor) rConsumedClassCall(node *sitter.Node) bool {
	if node.Kind() != "call" {
		return false
	}
	name := rCalleeName(node, x.source)
	return name == "R6Class" || (name == "list" && rIsMixinContainer(node, x.source))
}

// rR6AccessList returns the access level when node is a `public =`/`private =`/
// `active =` list argument directly inside an R6 class definition.
func (x *extractor) rR6AccessList(node *sitter.Node) (string, *sitter.Node) {
	name, value := node.ChildByFieldName("name"), node.ChildByFieldName("value")
	if name == nil || name.Kind() != "identifier" || value == nil || value.Kind() != "call" || rCalleeName(value, x.source) != "list" {
		return "", nil
	}
	switch access := x.text(name); access {
	case "public", "private", "active":
		return access, value
	}
	return "", nil
}

func (w *bindingWalk) rDefName(node *sitter.Node) (string, bool) {
	switch node.Kind() {
	case "binary_operator":
		lhs, rhs := node.ChildByFieldName("lhs"), node.ChildByFieldName("rhs")
		if slices.Contains(rAssignOperators, rOperator(node, w.source)) && lhs != nil && lhs.Kind() == "identifier" && rhs != nil && rhs.Kind() == "function_definition" {
			return w.text(lhs), true
		}
	case "function_definition":
		body := node.ChildByFieldName("body")
		if body == nil || body.Kind() != "binary_operator" || !slices.Contains(rRightAssignOperators, rOperator(body, w.source)) {
			return "", false
		}
		if rhs := body.ChildByFieldName("rhs"); rhs != nil && rhs.Kind() == "identifier" {
			return w.text(rhs), true
		}
	}
	return "", false
}
