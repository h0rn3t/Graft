package graph

import (
	"maps"
	"path"
	"slices"
	"strings"
)

// goTypeKinds are the Go declarations a type name can denote.
var goTypeKinds = []Kind{"struct", "interface", "type"}

// resolveGoEmbed binds an embedded type: a bare `Base` to the type of that
// name in the embedding type's package, a qualified `pkg.Base` to the one Base
// in an in-repo directory named pkg. Anything else stays an external target.
func (ix *resolveIndex) resolveGoEmbed(edge rawEdge, add func(string, string, Relation, Confidence)) {
	pkg, name, qualified := strings.Cut(edge.name, ".")
	if !qualified {
		pkg, name = "", edge.name
	}
	var hits []NodeV1
	for _, candidate := range ix.globalName[name] {
		if !slices.Contains(goTypeKinds, candidate.Kind) || !strings.HasSuffix(candidate.Path, ".go") {
			continue
		}
		dir := path.Dir(candidate.Path)
		if (!qualified && dir == path.Dir(edge.file)) || (qualified && path.Base(dir) == pkg) {
			hits = append(hits, candidate)
		}
	}
	switch {
	case len(hits) != 1:
		add(edge.source, edge.name, "extends", "inferred")
	case hits[0].Path == edge.file:
		add(edge.source, hits[0].ID, "extends", "extracted")
	default:
		add(edge.source, hits[0].ID, "extends", "inferred")
	}
}

// resolveGoPackageCall binds `pkg.F()` to the function F of the in-repo
// package the file imports as pkg. A test file's copy of F is chosen only
// when the call is made from a test file too.
func (ix *resolveIndex) resolveGoPackageCall(edge rawEdge, add func(string, string, Relation, Confidence)) {
	file := resolveGoImport(edge.specifier, ix.goModules, ix.goFilesByDir)
	if file == edge.specifier {
		ix.unresolved.ExternalPackage++
		return
	}
	dir := path.Dir(file)
	fromTest := strings.HasSuffix(edge.file, "_test.go")
	var hits []NodeV1
	for _, candidate := range ix.globalName[edge.name] {
		if candidate.Kind == "function" && path.Dir(candidate.Path) == dir && strings.HasSuffix(candidate.Path, ".go") &&
			(fromTest || !strings.HasSuffix(candidate.Path, "_test.go")) {
			hits = append(hits, candidate)
		}
	}
	switch len(hits) {
	case 0:
		ix.unresolved.NameNotInGraph++
	case 1:
		add(edge.source, hits[0].ID, "calls", "inferred")
	default:
		ix.unresolved.NameAmbiguous++
	}
}

// addGoImplements links Go types to the in-repo interfaces they satisfy, and
// each satisfying method to the interface method it implements. A type
// satisfies an interface when every interface method, embedded interfaces
// included, has a method of the same name, arity and variadic form on the type
// or on a type it embeds; an unexported interface method can only be satisfied
// inside the interface's package. Parameter types are not compared, so every
// such edge is inferred. An interface that embeds a type outside the graph has
// an unknown method set and is skipped.
func (ix *resolveIndex) addGoImplements(nodes []NodeV1, edges []EdgeV1, add func(string, string, Relation, Confidence)) {
	embeds := make(map[string][]string)
	for _, edge := range edges {
		if edge.Relation == "extends" && strings.HasSuffix(ix.byID[edge.Source].Path, ".go") {
			embeds[edge.Source] = append(embeds[edge.Source], edge.Target)
		}
	}
	interfaces, concrete, own := goTypes(nodes)
	if len(interfaces) == 0 {
		return
	}

	// member is a method in a method set, with the embedding depth that
	// promotes it; tied marks a name two embeddings promote at that depth.
	type member struct {
		method NodeV1
		depth  int
		tied   bool
	}
	// members is a type's own methods plus those promoted from the types it
	// embeds, the shallowest winning; complete is false when an embedded type
	// is not a graph node or the embedding is deeper than depth.
	var members func(id string, depth int) (map[string]member, bool)
	members = func(id string, depth int) (map[string]member, bool) {
		set := make(map[string]member, len(own[id]))
		for name, method := range own[id] {
			set[name] = member{method: method}
		}
		promoted := make(map[string]member)
		complete := true
		for _, embedded := range embeds[id] {
			if _, ok := ix.byID[embedded]; !ok || depth == 0 {
				complete = false
				continue
			}
			inner, innerComplete := members(embedded, depth-1)
			complete = complete && innerComplete
			for name, candidate := range inner {
				candidate.depth++
				switch current, seen := promoted[name]; {
				case !seen || candidate.depth < current.depth:
					promoted[name] = candidate
				case candidate.depth == current.depth:
					current.tied = true
					promoted[name] = current
				}
			}
		}
		for name, candidate := range promoted {
			if _, shadowed := set[name]; !shadowed {
				set[name] = candidate
			}
		}
		return set, complete
	}
	// methodSet flattens members. A concrete type loses a tied name, as the
	// selector would be ambiguous in Go; an interface keeps it, since embedded
	// interfaces may repeat a method.
	methodSet := func(id string, interfaceType bool) (map[string]NodeV1, bool) {
		const maxEmbedDepth = 3
		all, complete := members(id, maxEmbedDepth)
		set := make(map[string]NodeV1, len(all))
		for name, candidate := range all {
			if interfaceType || !candidate.tied {
				set[name] = candidate.method
			}
		}
		return set, complete
	}
	sets := make(map[string]map[string]NodeV1, len(concrete))
	withMethod := make(map[string][]NodeV1) // method name → concrete types that have it
	for _, node := range concrete {
		set, _ := methodSet(node.ID, false)
		sets[node.ID] = set
		for name := range set {
			withMethod[name] = append(withMethod[name], node)
		}
	}

	for _, iface := range interfaces {
		required, complete := methodSet(iface.ID, true)
		if !complete || len(required) == 0 {
			continue
		}
		names := slices.Sorted(maps.Keys(required))
		rarest := slices.MinFunc(names, func(a, b string) int { return len(withMethod[a]) - len(withMethod[b]) })
		for _, candidate := range withMethod[rarest] {
			set := sets[candidate.ID]
			if !goSatisfies(set, required, path.Dir(candidate.Path), path.Dir(iface.Path)) {
				continue
			}
			add(candidate.ID, iface.ID, "implements", "inferred")
			for _, name := range names {
				// A struct that embeds the interface promotes its methods as they are.
				if implementation := set[name]; implementation.ID != required[name].ID {
					add(implementation.ID, required[name].ID, "implements", "inferred")
				}
			}
		}
	}
}

// goTypes splits the Go type declarations into interfaces and concrete types,
// and indexes each type's own methods by name. A method belongs to the type
// of its owner's name in the method's package.
func goTypes(nodes []NodeV1) (interfaces, concrete []NodeV1, own map[string]map[string]NodeV1) {
	typeAt := make(map[string]string) // package dir + "\x00" + type name → type id
	for _, node := range nodes {
		if !strings.HasSuffix(node.Path, ".go") || !slices.Contains(goTypeKinds, node.Kind) {
			continue
		}
		typeAt[path.Dir(node.Path)+"\x00"+node.Name] = node.ID
		if node.Kind == "interface" {
			interfaces = append(interfaces, node)
		} else {
			concrete = append(concrete, node)
		}
	}
	own = make(map[string]map[string]NodeV1)
	for _, node := range nodes {
		if node.Kind != "method" || node.Owner == nil || !strings.HasSuffix(node.Path, ".go") {
			continue
		}
		owner, ok := typeAt[path.Dir(node.Path)+"\x00"+*node.Owner]
		if !ok {
			continue
		}
		if own[owner] == nil {
			own[owner] = make(map[string]NodeV1)
		}
		own[owner][node.Name] = node
	}
	return interfaces, concrete, own
}

// goSatisfies reports whether a method set has every required method with a
// matching shape, and unexported ones only within the interface's package.
func goSatisfies(set, required map[string]NodeV1, typeDir, interfaceDir string) bool {
	for name, want := range required {
		got, ok := set[name]
		if !ok || got.Arity == nil || want.Arity == nil || *got.Arity != *want.Arity || (got.Variadic != nil) != (want.Variadic != nil) {
			return false
		}
		gotResults, gotRead := goResultCount(got.Signature)
		wantResults, wantRead := goResultCount(want.Signature)
		if gotRead && wantRead && gotResults != wantResults {
			return false
		}
		if !goExported(name) && typeDir != interfaceDir {
			return false
		}
	}
	return true
}

// goResultCount counts the results a Go function, method or interface method
// signature declares: `(n int, err error)` and `(a, b int)` are two each. read
// is false for a signature it cannot follow, which then matches any count.
func goResultCount(signature *string) (count int, read bool) {
	if signature == nil {
		return 0, false
	}
	var code strings.Builder
	for line := range strings.Lines(*signature) {
		text, _, _ := strings.Cut(line, "//")
		code.WriteString(text)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(code.String()), "func"))
	if strings.HasPrefix(rest, "(") { // a method's receiver
		if rest, _, read = goGroup(rest); !read {
			return 0, false
		}
	}
	open := strings.IndexAny(rest, "([")
	if open < 0 {
		return 0, false
	}
	rest = rest[open:]
	if strings.HasPrefix(rest, "[") { // type parameters
		if rest, _, read = goGroup(rest); !read {
			return 0, false
		}
	}
	if !strings.HasPrefix(rest, "(") {
		return 0, false
	}
	if rest, _, read = goGroup(rest); !read { // parameters
		return 0, false
	}
	results := strings.TrimSpace(rest)
	if !strings.HasPrefix(results, "(") {
		return min(len(results), 1), true
	}
	after, commas, read := goGroup(results)
	if !read {
		return 0, false
	}
	if strings.TrimSpace(results[1:len(results)-len(after)-1]) == "" {
		return 0, true
	}
	return commas + 1, true
}

// goGroup reads the bracketed group text opens with: it returns what follows
// the matching close and the commas directly inside the group, or read false
// when the group never closes.
func goGroup(text string) (after string, commas int, read bool) {
	depth := 0
	for index, r := range text {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return text[index+1:], commas, true
			}
		case ',':
			if depth == 1 {
				commas++
			}
		}
	}
	return "", 0, false
}
