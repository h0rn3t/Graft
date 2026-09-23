package graph

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

// ExtractorID names the native extractor in its cache and fingerprint sidecars.
// Bump it whenever extraction output or a grammar version changes, so a graph
// built by an older extractor is never trusted as fresh.
const ExtractorID = "go-v2"

var goModuleLine = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

// SourceExtensions lists every extension a depth, breadth, or container tier
// claims, sorted and de-duplicated (source-files.ts supportedExtensions).
func SourceExtensions() []string {
	set := make(map[string]struct{})
	for _, entry := range depthExtensions {
		set[entry.ext] = struct{}{}
	}
	for _, lang := range genericLanguages {
		for _, extension := range lang.extensions {
			set[extension] = struct{}{}
		}
	}
	for _, lang := range containerLanguages {
		for _, extension := range lang.extensions {
			set[extension] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

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

type cachedEdge struct {
	Source       string   `json:"source"`
	Relation     Relation `json:"relation"`
	TargetID     string   `json:"targetId,omitempty"`
	Specifier    string   `json:"specifier,omitempty"`
	Name         string   `json:"name,omitempty"`
	ViaMember    bool     `json:"viaMember,omitempty"`
	RecvType     string   `json:"recvType,omitempty"`
	Kinds        []Kind   `json:"kinds,omitempty"`
	ArgCount     *int     `json:"argCount,omitempty"`
	ImplicitSelf bool     `json:"implicitSelf,omitempty"`
	File         string   `json:"file"`
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

// extractCacheVersion changes with the on-disk shape of extractCache.
const extractCacheVersion = 2

// BuildGraph walks root and builds a GraphV1 (graph/build.ts buildGraph).
// opts controls source visibility; an empty Extensions slice uses
// SourceExtensions, so files without a native adapter are reported in
// Unsupported. The graph is partial when Unsupported or Errors is non-empty.
// When opts.OutDir is set, an extractor-specific parse cache is stored in its
// `.cache` directory and the prior graph's meaning layer is carried over.
// Persist the returned fingerprints only after Write succeeds.
func BuildGraph(root string, opts sourcefiles.Options) (BuildResult, error) {
	extensions := make(map[string]struct{})
	wanted := opts.Extensions
	if len(wanted) == 0 {
		wanted = SourceExtensions()
	}
	for _, extension := range wanted {
		extension = strings.ToLower(strings.TrimSpace(extension))
		if !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		extensions[extension] = struct{}{}
	}
	walk := opts
	walk.Extensions = nil
	repoFiles, err := sourcefiles.Walk(root, walk)
	if err != nil {
		return BuildResult{}, fmt.Errorf("walk source files: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return BuildResult{}, fmt.Errorf("resolve source root: %w", err)
	}
	relFiles := make([]string, 0, len(repoFiles))
	var files []sourcefiles.File
	var modules []goModule
	for _, file := range repoFiles {
		relFiles = append(relFiles, file.Rel)
		if _, ok := extensions[strings.ToLower(path.Ext(file.Rel))]; ok {
			files = append(files, file)
		}
		if path.Base(file.Rel) != "go.mod" {
			continue
		}
		if data, err := os.ReadFile(file.Abs); err == nil {
			if match := goModuleLine.FindSubmatch(data); match != nil {
				modules = append(modules, goModule{module: string(match[1]), dir: path.Dir(file.Rel)})
			}
		}
	}

	outDir := opts.OutDir
	cachePath := ""
	if outDir != "" {
		if !filepath.IsAbs(outDir) {
			outDir = filepath.Join(root, outDir)
		}
		cachePath = filepath.Join(outDir, ".cache", "extract."+ExtractorID+".json")
	}
	prior := extractCache{}
	if cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(data, &prior) != nil {
			prior = extractCache{}
		}
	}
	if prior.Version != extractCacheVersion || prior.Extractor != ExtractorID || prior.Files == nil {
		prior.Files = make(map[string]cachedFile)
	}
	current := extractCache{Version: extractCacheVersion, Extractor: ExtractorID, Files: make(map[string]cachedFile, len(files))}
	result := BuildResult{
		Fingerprints: make(map[string]FingerprintFile, len(files)),
		Unsupported:  make([]string, 0),
		Errors:       make([]string, 0),
		Limitations:  make([]string, 0),
	}
	nodes := make([]NodeV1, 0, len(files))
	rawEdges := make([]rawEdge, 0)
	languageSet := make(map[string]struct{})
	for _, file := range files {
		_, label, _ := languageOf(file.Rel)
		source, readable, readErr := sourcefiles.Read(file.Abs)
		fingerprint := FingerprintFile{Size: file.Size, MTimeMS: file.MTimeMS}
		if readErr == nil && readable {
			fingerprint.Hash = sourcefiles.Hash(source)
		}
		result.Fingerprints[file.Rel] = fingerprint
		if !nativeSupported(file.Rel) {
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
					specifier: edge.Specifier, name: edge.Name, viaMember: edge.ViaMember,
					recvType: edge.RecvType, kinds: edge.Kinds, argCount: edge.ArgCount,
					implicitSelf: edge.ImplicitSelf, file: edge.File,
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
				Specifier: edge.specifier, Name: edge.name, ViaMember: edge.viaMember,
				RecvType: edge.recvType, Kinds: edge.kinds, ArgCount: edge.argCount,
				ImplicitSelf: edge.implicitSelf, File: edge.file,
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

	edges := resolveEdges(nodes, rawEdges, modules)
	scopes := applyMinSubstanceGuard(discoverScopes(absRoot, relFiles), nodes)
	if outDir != "" {
		if priorGraph, err := Read(WiringPath(outDir)); err == nil {
			carryMeaning(nodes, priorGraph.Nodes)
		}
	}
	result.Graph = GraphV1{
		Meta:  GraphMeta{Version: 1, NodeCount: len(nodes), EdgeCount: len(edges), Languages: slices.Sorted(maps.Keys(languageSet)), Scopes: &scopes},
		Nodes: nodes,
		Edges: edges,
	}
	slices.Sort(result.Unsupported)
	slices.Sort(result.Errors)
	return result, nil
}

// nativeSupported reports whether a native Go adapter extracts file.
func nativeSupported(file string) bool {
	lang, _, ok := languageOf(file)
	_, native := grammars[lang]
	return ok && native
}

// carryMeaning folds the prior graph's summaries into nodes without an LLM
// (enrich.ts enrichGraph with no summarizer): an unchanged ready body keeps its
// summary; a changed one keeps it as a stale hint.
func carryMeaning(nodes, prior []NodeV1) {
	byID := make(map[string]NodeV1, len(prior))
	for _, node := range prior {
		byID[node.ID] = node
	}
	for index := range nodes {
		was, ok := byID[nodes[index].ID]
		switch {
		case !ok:
		case was.SummaryState == "ready" && was.BodyHash == nodes[index].BodyHash:
			nodes[index].Summary, nodes[index].Crux, nodes[index].SummaryState = was.Summary, was.Crux, "ready"
		case was.Summary != nil && *was.Summary != "":
			nodes[index].Summary, nodes[index].Crux, nodes[index].SummaryState = was.Summary, was.Crux, "stale"
		}
	}
}
