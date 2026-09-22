package graph

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

const (
	// Bump when extraction output or grammar versions change.
	graphExtractorID        = "go-v1"
	defaultSourceExtensions = `
		.tsx .jsx .mts .cts .ts .mjs .cjs .js
		.pyi .py .go .java .kt .kts .swift .php .r .rs
		.c .h .cpp .cc .cxx .hpp .hh .cs .scala .sc .ex .exs
		.sol .ml .mli .zig .dart .clj .cljs .cljc .bb .nix .lua .vue
	`
)

// BuildResult contains a graph and its known coverage limitations.
type BuildResult struct {
	Graph GraphV1
	// Parsed is the number of supported source files parsed during this build.
	Parsed int
	// Reused is the number of supported source files replayed from the extraction cache.
	Reused int
	// Fingerprints maps repository-relative paths to stats and decoded-content hashes.
	// Hash is empty when the source could not be read or decoded.
	Fingerprints map[string]FingerprintFile
	// Unsupported lists files with an unimplemented language adapter or encoding.
	Unsupported []string
	// Errors lists files that could not be read or parsed.
	Errors []string
	// Limitations lists graph features not covered by this native extraction slice.
	Limitations []string
}

// BuildGraph walks root and builds a GraphV1 from TypeScript and JavaScript files.
// opts controls source visibility; an empty Extensions slice uses the known source
// extensions so files without an adapter are reported in Unsupported. Graph remains
// partial when Unsupported, Errors, or Limitations is non-empty.
// When opts.OutDir is set, an extractor-specific parse cache is stored in its
// `.cache` directory. Persist the returned fingerprints only after Write succeeds.
func BuildGraph(root string, opts sourcefiles.Options) (BuildResult, error) {
	const cacheVersion = 1
	type cachedEdge struct {
		Source    string   `json:"source"`
		Relation  Relation `json:"relation"`
		TargetID  string   `json:"targetId,omitempty"`
		Specifier string   `json:"specifier,omitempty"`
		Name      string   `json:"name,omitempty"`
		ViaMember bool     `json:"viaMember,omitempty"`
		File      string   `json:"file"`
	}
	type cachedFile struct {
		Size     int64        `json:"size"`
		MTimeMS  float64      `json:"mtimeMs"`
		Hash     string       `json:"hash"`
		Nodes    []NodeV1     `json:"nodes"`
		RawEdges []cachedEdge `json:"rawEdges"`
		Error    string       `json:"error,omitempty"`
	}
	type extractCache struct {
		Version   int                   `json:"version"`
		Extractor string                `json:"extractor"`
		Files     map[string]cachedFile `json:"files"`
	}
	if len(opts.Extensions) == 0 {
		opts.Extensions = strings.Fields(defaultSourceExtensions)
	}
	files, err := sourcefiles.Walk(root, opts)
	if err != nil {
		return BuildResult{}, fmt.Errorf("walk source files: %w", err)
	}
	cachePath := ""
	outDir := opts.OutDir
	if outDir != "" {
		if !filepath.IsAbs(outDir) {
			outDir = filepath.Join(root, outDir)
		}
		cachePath = filepath.Join(outDir, ".cache", "extract."+graphExtractorID+".json")
	}
	prior := extractCache{}
	if cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil {
			if err := json.Unmarshal(data, &prior); err != nil {
				prior = extractCache{}
			}
		}
	}
	if prior.Version != cacheVersion || prior.Extractor != graphExtractorID || prior.Files == nil {
		prior.Files = make(map[string]cachedFile)
	}
	current := extractCache{Version: cacheVersion, Extractor: graphExtractorID, Files: make(map[string]cachedFile, len(files))}
	result := BuildResult{
		Fingerprints: make(map[string]FingerprintFile, len(files)),
		Unsupported:  make([]string, 0),
		Errors:       make([]string, 0),
		Limitations: []string{
			"member-call edges without a resolved receiver type are omitted",
			"multi-scope metadata is not yet discovered",
		},
	}
	nodes := make([]NodeV1, 0, len(files))
	rawEdges := make([]rawEdge, 0)
	languageSet := make(map[string]struct{})
	for _, file := range files {
		label, _, supported := sourceGrammar(file.Rel)
		source, readable, readErr := sourcefiles.Read(file.Abs)
		fingerprint := FingerprintFile{Size: file.Size, MTimeMS: file.MTimeMS}
		if readErr == nil && readable {
			fingerprint.Hash = sourcefiles.Hash(source)
		}
		result.Fingerprints[file.Rel] = fingerprint
		if !supported {
			result.Unsupported = append(result.Unsupported, file.Rel)
			continue
		}
		if readErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", file.Rel, readErr))
			continue
		}
		if !readable {
			result.Unsupported = append(result.Unsupported, file.Rel)
			continue
		}
		hash := fingerprint.Hash
		cached, ok := prior.Files[file.Rel]
		cacheHit := ok && cached.Hash == hash &&
			((cached.Error != "" && len(cached.Nodes) == 0) ||
				(cached.Error == "" && len(cached.Nodes) > 0 &&
					cached.Nodes[0].ID == file.Rel && cached.Nodes[0].Path == file.Rel &&
					cached.Nodes[0].BodyHash == hash && cached.Nodes[0].Kind == Kind("file")))
		if cacheHit {
			cached.Size = file.Size
			cached.MTimeMS = file.MTimeMS
			current.Files[file.Rel] = cached
			result.Reused++
			if cached.Error != "" {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", file.Rel, cached.Error))
				continue
			}
			nodes = append(nodes, cached.Nodes...)
			for _, edge := range cached.RawEdges {
				rawEdges = append(rawEdges, rawEdge{
					source: edge.Source, relation: edge.Relation, targetID: edge.TargetID,
					specifier: edge.Specifier, name: edge.Name, viaMember: edge.ViaMember, file: edge.File,
				})
			}
			languageSet[label] = struct{}{}
			continue
		}
		result.Parsed++
		extracted, err := extractFile(file.Rel, source)
		entry := cachedFile{
			Size: file.Size, MTimeMS: file.MTimeMS, Hash: hash,
			Nodes: make([]NodeV1, 0), RawEdges: make([]cachedEdge, 0),
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", file.Rel, err))
			entry.Error = err.Error()
			current.Files[file.Rel] = entry
			continue
		}
		entry.Nodes = extracted.nodes
		for _, edge := range extracted.rawEdges {
			entry.RawEdges = append(entry.RawEdges, cachedEdge{
				Source: edge.source, Relation: edge.relation, TargetID: edge.targetID,
				Specifier: edge.specifier, Name: edge.name, ViaMember: edge.viaMember, File: edge.file,
			})
		}
		current.Files[file.Rel] = entry
		nodes = append(nodes, extracted.nodes...)
		rawEdges = append(rawEdges, extracted.rawEdges...)
		languageSet[extracted.language] = struct{}{}
	}
	if cachePath != "" {
		data, err := json.Marshal(current)
		if err != nil {
			return BuildResult{}, fmt.Errorf("encode graph extraction cache: %w", err)
		}
		_ = writeAtomicSidecar(cachePath, data) // A cache write failure only costs reuse on the next build.
	}
	languages := slices.AppendSeq(make([]string, 0, len(languageSet)), maps.Keys(languageSet))
	slices.Sort(languages)
	edges := resolveRawEdges(nodes, rawEdges)
	result.Graph = GraphV1{
		Meta:  GraphMeta{Version: 1, NodeCount: len(nodes), EdgeCount: len(edges), Languages: languages},
		Nodes: nodes,
		Edges: edges,
	}
	slices.Sort(result.Unsupported)
	slices.Sort(result.Errors)
	return result, nil
}

func sourceGrammar(file string) (label, grammar string, ok bool) {
	switch strings.ToLower(path.Ext(file)) {
	case ".ts", ".mts", ".cts":
		return "typescript", "typescript", true
	case ".tsx":
		return "tsx", "tsx", true
	case ".js", ".mjs", ".cjs":
		return "javascript", "javascript", true
	case ".jsx":
		return "jsx", "javascript", true
	default:
		return "", "", false
	}
}

func resolveRawEdges(nodes []NodeV1, rawEdges []rawEdge) []EdgeV1 {
	fileIDs := make(map[string]string)
	perFileName := make(map[string]map[string][]NodeV1)
	globalName := make(map[string][]NodeV1)
	for _, node := range nodes {
		if node.Kind == "file" {
			fileIDs[node.Path] = node.ID
			continue
		}
		if perFileName[node.Path] == nil {
			perFileName[node.Path] = make(map[string][]NodeV1)
		}
		perFileName[node.Path][node.Name] = append(perFileName[node.Path][node.Name], node)
		globalName[node.Name] = append(globalName[node.Name], node)
	}
	resolveImport := func(specifier, file string) string {
		if !strings.HasPrefix(specifier, ".") {
			return specifier
		}
		base := path.Clean(path.Join(path.Dir(file), specifier))
		noExtension := base
		for _, extension := range []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".py"} {
			if before, ok := strings.CutSuffix(base, extension); ok {
				noExtension = before
				break
			}
		}
		candidates := []string{base}
		for _, extension := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".py"} {
			candidates = append(candidates, noExtension+extension)
		}
		for _, extension := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".py"} {
			candidates = append(candidates, path.Join(noExtension, "index"+extension))
		}
		for _, candidate := range candidates {
			if _, ok := fileIDs[candidate]; ok {
				return candidate
			}
		}
		return specifier
	}
	resolveName := func(name, file string, kinds []Kind) (string, Confidence, bool) {
		local := perFileName[file][name]
		match := ""
		for _, node := range local {
			if slices.Contains(kinds, node.Kind) {
				if match != "" {
					return "", "", false
				}
				match = node.ID
			}
		}
		if match != "" {
			return match, "extracted", true
		}
		for _, node := range globalName[name] {
			if !slices.Contains(kinds, node.Kind) {
				continue
			}
			if match != "" {
				return "", "", false
			}
			match = node.ID
		}
		if match != "" {
			return match, "inferred", true
		}
		return "", "", false
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
		switch edge.relation {
		case "contains":
			add(edge.source, edge.targetID, edge.relation, "extracted")
		case "imports":
			add(edge.source, resolveImport(edge.specifier, edge.file), edge.relation, "extracted")
		case "references":
			if edge.specifier == "" {
				continue
			}
			targetFile := resolveImport(edge.specifier, edge.file)
			if _, ok := fileIDs[targetFile]; !ok {
				continue
			}
			candidates := perFileName[targetFile][edge.name]
			if len(candidates) == 1 {
				add(edge.source, candidates[0].ID, edge.relation, "extracted")
			}
		case "extends", "implements":
			kinds := []Kind{"class", "interface"}
			if edge.relation == "implements" {
				kinds = []Kind{"interface"}
			}
			if target, confidence, ok := resolveName(edge.name, edge.file, kinds); ok {
				add(edge.source, target, edge.relation, confidence)
			} else {
				add(edge.source, edge.name, edge.relation, "inferred")
			}
		case "calls":
			if edge.viaMember {
				continue
			}
			if target, confidence, ok := resolveName(edge.name, edge.file, []Kind{"function"}); ok {
				add(edge.source, target, edge.relation, confidence)
			}
		}
	}
	return edges
}
