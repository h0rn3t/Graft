package graph

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// goModule is a go.mod found in the repository: its module path and the posix
// directory holding it ("." for the root).
type goModule struct {
	module string
	dir    string
}

var (
	importExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".py"}
	cExtension       = regexp.MustCompile(`(?i)\.(c|h|cc|cpp|cxx|hpp|hh|hxx|inl|ipp|c\+\+|h\+\+)$`)
	pyExtension      = regexp.MustCompile(`(?i)\.pyi?$`)
	swiftExtension   = regexp.MustCompile(`(?i)\.swift$`)
	importStripExt   = regexp.MustCompile(`\.(js|jsx|mjs|cjs|ts|tsx|py)$`)
	// languageFamily groups languages whose symbols can reach each other; a
	// cross-file name match may not cross a family boundary.
	languageFamily = map[string]string{
		"typescript": "typescript", "tsx": "typescript",
		"java": "java", "kotlin": "java", "scala": "java", "clojure": "java",
		"c": "c", "cpp": "c",
	}
)

func familyOf(file string) string {
	lang := ""
	if grammar, _, ok := languageOf(file); ok {
		lang = string(grammar)
	} else if generic, ok := genericLanguageOf(file); ok {
		lang = generic
	}
	if family, ok := languageFamily[lang]; ok {
		return family
	}
	return lang
}

// reachable reports whether a reference in file could reach a definition in
// candidate; an unknown family never filters.
func reachable(file, candidate string) bool {
	from := familyOf(file)
	if from == "" {
		return true
	}
	to := familyOf(candidate)
	return to == "" || from == to
}

type resolved struct {
	id         string
	confidence Confidence
	ambiguous  bool
}

type resolveIndex struct {
	byID              map[string]NodeV1
	globalName        map[string][]NodeV1
	perFileName       map[string]map[string][]NodeV1
	ownerMethod       map[string][]NodeV1
	goFilesByDir      map[string][]string
	javaFilesBySuffix map[string][]string
	cFilesBySuffix    map[string][]string
	phpFilesBySuffix  map[string][]string
	rustCrateRoots    []string
	classParents      map[string][]string
	classTraits       map[string][]string
	goModules         []goModule
}

// resolveEdges turns raw edge intents into GraphV1 edges (resolve.ts resolveEdges).
func resolveEdges(nodes []NodeV1, rawEdges []rawEdge, goModules []goModule) []EdgeV1 {
	ix := resolveIndex{
		byID: make(map[string]NodeV1, len(nodes)), globalName: make(map[string][]NodeV1),
		perFileName: make(map[string]map[string][]NodeV1), ownerMethod: make(map[string][]NodeV1),
		goFilesByDir: make(map[string][]string), javaFilesBySuffix: make(map[string][]string),
		cFilesBySuffix: make(map[string][]string), phpFilesBySuffix: make(map[string][]string),
		classParents: make(map[string][]string), classTraits: make(map[string][]string),
		goModules: goModules,
	}
	pushSuffixes := func(index map[string][]string, node NodeV1) {
		parts := strings.Split(node.Path, "/")
		for start := range parts {
			suffix := strings.Join(parts[start:], "/")
			index[suffix] = append(index[suffix], node.ID)
		}
	}
	for _, node := range nodes {
		ix.byID[node.ID] = node
		if node.Kind == "file" {
			if len(goModules) > 0 && strings.HasSuffix(node.Path, ".go") {
				dir := path.Dir(node.Path)
				ix.goFilesByDir[dir] = append(ix.goFilesByDir[dir], node.ID)
			}
			if strings.HasSuffix(node.Path, ".java") {
				pushSuffixes(ix.javaFilesBySuffix, node)
			}
			if cExtension.MatchString(node.Path) {
				pushSuffixes(ix.cFilesBySuffix, node)
			}
			if strings.HasSuffix(node.Path, ".php") {
				pushSuffixes(ix.phpFilesBySuffix, node)
			}
			switch {
			case node.Path == "lib.rs" || node.Path == "main.rs":
				ix.rustCrateRoots = append(ix.rustCrateRoots, "")
			case strings.HasSuffix(node.Path, "/lib.rs") || strings.HasSuffix(node.Path, "/main.rs"):
				ix.rustCrateRoots = append(ix.rustCrateRoots, path.Dir(node.Path))
			}
			continue
		}
		ix.globalName[node.Name] = append(ix.globalName[node.Name], node)
		if ix.perFileName[node.Path] == nil {
			ix.perFileName[node.Path] = make(map[string][]NodeV1)
		}
		ix.perFileName[node.Path][node.Name] = append(ix.perFileName[node.Path][node.Name], node)
		if node.Kind == "method" {
			owner := ""
			if node.Owner != nil {
				owner = *node.Owner
			} else {
				owner = ownerFromMethodID(node.ID)
			}
			if owner != "" {
				key := owner + "." + node.Name
				ix.ownerMethod[key] = append(ix.ownerMethod[key], node)
			}
		}
	}
	for _, edge := range rawEdges {
		if edge.name == "" {
			continue
		}
		source, ok := ix.byID[edge.source]
		if !ok || source.Name == "" {
			continue
		}
		switch {
		case edge.relation == "extends":
			ix.classParents[source.Name] = append(ix.classParents[source.Name], edge.name)
		case edge.relation == "implements" && strings.HasSuffix(edge.file, ".php"):
			ix.classTraits[source.Name] = append(ix.classTraits[source.Name], edge.name)
		}
	}

	edges := make([]EdgeV1, 0, len(rawEdges))
	seen := make(map[string]struct{}, len(rawEdges))
	add := func(source, target string, relation Relation, confidence Confidence) {
		key := source + "\x00" + string(relation) + "\x00" + target
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		edges = append(edges, EdgeV1{Source: source, Target: target, Relation: relation, Confidence: confidence})
	}
	for _, edge := range rawEdges {
		switch {
		case edge.relation == "contains" && edge.targetID != "":
			add(edge.source, edge.targetID, "contains", "extracted")
		case edge.relation == "imports" && edge.specifier != "":
			add(edge.source, ix.resolveImportTarget(edge), "imports", "extracted")
		case edge.relation == "extends" || edge.relation == "implements":
			kinds := []Kind{"class", "interface"}
			if edge.relation == "implements" {
				kinds = []Kind{"interface", "trait"}
			}
			if hit, ok := ix.resolveName(edge.name, edge.file, kinds); ok {
				add(edge.source, hit.id, edge.relation, hit.confidence)
			} else {
				add(edge.source, edge.name, edge.relation, "inferred")
			}
		case edge.relation == "references" && edge.name != "":
			ix.resolveReference(edge, add)
		case edge.relation == "calls":
			ix.resolveCall(edge, add)
		}
	}
	return edges
}

func (ix *resolveIndex) resolveImportTarget(edge rawEdge) string {
	switch {
	case len(ix.goModules) > 0 && strings.HasSuffix(edge.file, ".go"):
		return resolveGoImport(edge.specifier, ix.goModules, ix.goFilesByDir)
	case strings.HasSuffix(edge.file, ".java"):
		return resolveJavaImport(edge.specifier, ix.javaFilesBySuffix)
	case cExtension.MatchString(edge.file):
		return ix.resolveCInclude(edge.specifier, edge.file)
	case strings.HasSuffix(edge.file, ".rs"):
		return ix.resolveRustUse(edge.specifier, edge.file)
	case strings.HasSuffix(edge.file, ".php"):
		return resolvePHPUse(edge.specifier, ix.phpFilesBySuffix)
	}
	return ix.resolveImport(edge.specifier, edge.file)
}

func (ix *resolveIndex) resolveReference(edge rawEdge, add func(string, string, Relation, Confidence)) {
	source := ix.byID[edge.source]
	switch {
	case edge.specifier != "":
		target := ix.resolveImport(edge.specifier, edge.file)
		if strings.HasSuffix(edge.file, ".php") {
			target = resolvePHPUse(edge.specifier, ix.phpFilesBySuffix)
		}
		if _, ok := ix.byID[target]; !ok {
			return
		}
		if candidates := ix.perFileName[target][edge.name]; len(candidates) == 1 {
			add(edge.source, candidates[0].ID, "references", "extracted")
		}
	case strings.HasSuffix(edge.file, ".php") && source.Origin == "ast":
		if hit, ok := ix.resolveName(edge.name, edge.file, []Kind{"class", "interface", "trait", "enum"}); ok && hit.id != edge.source {
			add(edge.source, hit.id, "references", hit.confidence)
		}
	case strings.HasSuffix(edge.file, ".java") && source.Origin == "ast":
		hit, ok := ix.resolveName(edge.name, edge.file, []Kind{"interface"})
		annotation := ix.byID[hit.id]
		if ok && hit.id != edge.source && annotation.Signature != nil && strings.Contains(*annotation.Signature, "@interface") {
			add(edge.source, hit.id, "references", hit.confidence)
		} else {
			add(edge.source, edge.name, "references", "inferred")
		}
	case source.Origin == "generic":
		if hit, ok := ix.resolveName(edge.name, edge.file, []Kind{"class", "interface", "struct", "enum", "type", "module"}); ok && hit.id != edge.source {
			add(edge.source, hit.id, "references", hit.confidence)
		}
	}
}

func (ix *resolveIndex) resolveCall(edge rawEdge, add func(string, string, Relation, Confidence)) {
	if edge.viaMember {
		if edge.recvType == "" {
			return
		}
		hit := ix.resolveTypedMember(edge.recvType, edge.name, edge.file, edge.argCount)
		if hit.ambiguous {
			return
		}
		if hit.id != "" {
			add(edge.source, hit.id, "calls", hit.confidence)
			return
		}
		if !edge.implicitSelf {
			return
		}
	}
	kinds := edge.kinds
	if kinds == nil {
		switch {
		case ix.byID[edge.source].Origin == "generic":
			kinds = []Kind{"function", "method"}
		case strings.HasSuffix(edge.file, ".java"):
			kinds = []Kind{"class", "struct", "enum", "interface"}
		default:
			kinds = []Kind{"function"}
		}
	}
	hit, ok := ix.resolveName(edge.name, edge.file, kinds)
	if !ok && pyExtension.MatchString(edge.file) {
		hit, ok = ix.resolveName(edge.name, edge.file, []Kind{"class"})
	}
	if !ok && swiftExtension.MatchString(edge.file) {
		hit, ok = ix.resolveName(edge.name, edge.file, []Kind{"class", "struct", "enum"})
	}
	if ok {
		add(edge.source, hit.id, "calls", hit.confidence)
	}
}

// ownerFromMethodID derives an owner from a dotted id: `app.php#Loggable.log` → `Loggable`.
func ownerFromMethodID(id string) string {
	post := id
	if parts := strings.Split(id, "#"); len(parts) > 1 {
		post = parts[1]
	}
	segments := strings.Split(post, ".")
	if len(segments) < 2 {
		return ""
	}
	return segments[len(segments)-2]
}

// resolveName matches a bare name: a unique same-file node is `extracted`, a
// unique reachable node elsewhere is `inferred`, anything else is unresolved.
func (ix *resolveIndex) resolveName(name, file string, kinds []Kind) (resolved, bool) {
	var local []NodeV1
	for _, node := range ix.perFileName[file][name] {
		if slices.Contains(kinds, node.Kind) {
			local = append(local, node)
		}
	}
	if len(local) == 1 {
		return resolved{id: local[0].ID, confidence: "extracted"}, true
	}
	var global []NodeV1
	for _, node := range ix.globalName[name] {
		if slices.Contains(kinds, node.Kind) && reachable(file, node.Path) {
			global = append(global, node)
		}
	}
	if len(global) == 1 {
		return resolved{id: global[0].ID, confidence: "inferred"}, true
	}
	return resolved{}, false
}

// narrowByArity keeps the overloads a call of argCount arguments can reach, or
// the original set when narrowing would leave nothing.
func narrowByArity(candidates []NodeV1, argCount *int) []NodeV1 {
	if argCount == nil || len(candidates) < 2 {
		return candidates
	}
	var fits []NodeV1
	for _, candidate := range candidates {
		switch {
		case candidate.Arity == nil,
			candidate.Variadic != nil && *candidate.Variadic && *argCount >= *candidate.Arity-1,
			(candidate.Variadic == nil || !*candidate.Variadic) && *candidate.Arity == *argCount:
			fits = append(fits, candidate)
		}
	}
	if len(fits) == 0 {
		return candidates
	}
	return fits
}

func (ix *resolveIndex) reachableMethods(key, file string) []NodeV1 {
	var out []NodeV1
	for _, candidate := range ix.ownerMethod[key] {
		if reachable(file, candidate.Path) {
			out = append(out, candidate)
		}
	}
	return out
}

// resolveTypedMember resolves recvType.name against the owner-qualified method
// index, walking the receiver's extends chain breadth-first up to depth 3.
func (ix *resolveIndex) resolveTypedMember(recvType, name, file string, argCount *int) resolved {
	const maxDepth = 3
	visited := map[string]struct{}{recvType: {}}
	frontier := []string{recvType}
	for depth := 0; depth <= maxDepth && len(frontier) > 0; depth++ {
		for _, owner := range frontier {
			if all := ix.reachableMethods(owner+"."+name, file); len(all) > 0 {
				candidates := narrowByArity(all, argCount)
				if len(candidates) == 1 {
					return memberHit(candidates[0], file)
				}
				if swiftExtension.MatchString(file) {
					return resolved{ambiguous: true}
				}
				if index := slices.IndexFunc(candidates, func(c NodeV1) bool { return c.Path == file }); index >= 0 {
					return resolved{id: candidates[index].ID, confidence: "extracted"}
				}
				return resolved{ambiguous: true}
			}
			if hit := ix.resolveTraitMember(owner, name, file, argCount); hit.ambiguous || hit.id != "" {
				return hit
			}
		}
		var next []string
		for _, owner := range frontier {
			for _, parent := range ix.classParents[owner] {
				if _, ok := visited[parent]; ok {
					continue
				}
				visited[parent] = struct{}{}
				next = append(next, parent)
			}
		}
		frontier = next
	}
	return resolved{}
}

func memberHit(candidate NodeV1, file string) resolved {
	if candidate.Path == file {
		return resolved{id: candidate.ID, confidence: "extracted"}
	}
	return resolved{id: candidate.ID, confidence: "inferred"}
}

func (ix *resolveIndex) resolveTraitMember(owner, name, file string, argCount *int) resolved {
	var matches []NodeV1
	for _, trait := range ix.classTraits[owner] {
		if all := ix.reachableMethods(trait+"."+name, file); len(all) > 0 {
			matches = append(matches, narrowByArity(all, argCount)...)
		}
	}
	switch len(matches) {
	case 0:
		return resolved{}
	case 1:
		return memberHit(matches[0], file)
	}
	return resolved{ambiguous: true}
}

// resolveImport maps a relative module specifier to an in-repo file id, or
// returns the specifier unchanged.
func (ix *resolveIndex) resolveImport(specifier, file string) string {
	if !strings.HasPrefix(specifier, ".") {
		return specifier
	}
	base := posixJoin(path.Dir(file), specifier)
	noExtension := importStripExt.ReplaceAllString(base, "")
	candidates := []string{base}
	for _, extension := range importExtensions {
		candidates = append(candidates, noExtension+extension)
	}
	for _, extension := range importExtensions {
		candidates = append(candidates, noExtension+"/index"+extension)
	}
	for _, candidate := range candidates {
		if _, ok := ix.byID[candidate]; ok {
			return candidate
		}
	}
	return specifier
}

// posixJoin matches node's posix.normalize(posix.join(...)): the cleaned join,
// "." when empty, and a trailing slash kept when the last element had one.
func posixJoin(elements ...string) string {
	var parts []string
	for _, element := range elements {
		if element != "" {
			parts = append(parts, element)
		}
	}
	joined := strings.Join(parts, "/")
	if joined == "" {
		return "."
	}
	cleaned := path.Clean(joined)
	if strings.HasSuffix(joined, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

func resolveJavaImport(specifier string, bySuffix map[string][]string) string {
	hit := func(fqn string) string {
		if files := bySuffix[strings.ReplaceAll(fqn, ".", "/")+".java"]; len(files) == 1 {
			return files[0]
		}
		return ""
	}
	if direct := hit(specifier); direct != "" {
		return direct
	}
	if dot := strings.LastIndex(specifier, "."); dot > 0 {
		if enclosing := hit(specifier[:dot]); enclosing != "" {
			return enclosing
		}
	}
	return specifier
}

func (ix *resolveIndex) resolveCInclude(specifier, file string) string {
	relative := posixJoin(path.Dir(file), specifier)
	if _, ok := ix.byID[relative]; ok {
		return relative
	}
	trimmed, ok := strings.CutPrefix(specifier, "./")
	if !ok {
		trimmed = strings.TrimPrefix(specifier, "/")
	}
	if hits := ix.cFilesBySuffix[trimmed]; len(hits) == 1 {
		return hits[0]
	}
	return specifier
}

func resolvePHPUse(fqn string, bySuffix map[string][]string) string {
	var parts []string
	for part := range strings.SplitSeq(fqn, "\\") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	for start := range parts {
		hits := bySuffix[strings.Join(parts[start:], "/")+".php"]
		if len(hits) == 1 {
			return hits[0]
		}
		if len(hits) > 1 {
			break
		}
	}
	return fqn
}

func (ix *resolveIndex) resolveRustUse(specifier, file string) string {
	full := specifier
	if rest, ok := strings.CutPrefix(full, "crate"); ok {
		full = strings.TrimPrefix(rest, "/")
	}
	unresolved := "crate"
	if full != "" {
		unresolved = "crate::" + strings.ReplaceAll(full, "/", "::")
	}
	owning := ""
	found := false
	for _, root := range ix.rustCrateRoots {
		if root == "" || file == root || strings.HasPrefix(file, root+"/") {
			if !found || len(root) > len(owning) {
				owning, found = root, true
			}
		}
	}
	if !found {
		return unresolved
	}
	hitsFor := func(rels ...string) []string {
		var hits []string
		for _, rel := range rels {
			candidate := rel
			if owning != "" {
				candidate = owning + "/" + rel
			}
			if _, ok := ix.byID[candidate]; ok && !slices.Contains(hits, candidate) {
				hits = append(hits, candidate)
			}
		}
		return hits
	}
	var segments []string
	if full != "" {
		segments = strings.Split(full, "/")
	}
	for count := len(segments); count >= 1; count-- {
		module := strings.Join(segments[:count], "/")
		hits := hitsFor(module+".rs", module+"/mod.rs")
		if len(hits) == 1 {
			return hits[0]
		}
		if len(hits) > 1 {
			break
		}
	}
	if len(segments) <= 1 {
		if hits := hitsFor("lib.rs", "main.rs"); len(hits) == 1 {
			return hits[0]
		}
	}
	return unresolved
}

func resolveGoImport(specifier string, modules []goModule, filesByDir map[string][]string) string {
	var best *goModule
	subpath := ""
	for index := range modules {
		module := &modules[index]
		sub, ok := "", false
		switch {
		case specifier == module.module:
			ok = true
		case strings.HasPrefix(specifier, module.module+"/"):
			sub, ok = specifier[len(module.module)+1:], true
		}
		if ok && (best == nil || len(module.module) > len(best.module)) {
			best, subpath = module, sub
		}
	}
	if best == nil {
		return specifier
	}
	files := filesByDir[posixJoin(best.dir, subpath)]
	if len(files) == 0 {
		return specifier
	}
	return slices.Min(files)
}
