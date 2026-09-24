package blast

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/llm"
)

// Cluster is one group of files to name.
type Cluster struct {
	Key     string
	Files   []string
	Symbols []string
}

// Namer turns clusters into names keyed by cluster key. A key it declines to
// name is absent from the result.
type Namer interface {
	Name(ctx context.Context, clusters []Cluster) (map[string]string, error)
}

// NameStats reports what a naming pass did.
type NameStats struct {
	Cached   int
	Named    int
	Declined int
	// Err is set when a naming request failed outright.
	Err error
}

const (
	symbolsPerCluster = 8
	clustersPerCall   = 12
	areasCacheFile    = "areas.json"
)

const namingSystemPrompt = `You name parts of a codebase for a pull-request reviewer.

You are given clusters of files that a change can affect. For each cluster, answer with the FEATURE or SUBSYSTEM those files implement, as a developer on the team would refer to it in conversation.

Rules:
- 2 to 4 words, Title Case. No trailing "Module", "System", "Layer", "Manager" or "Handler" unless the team would genuinely say it.
- Name the RESPONSIBILITY, not the mechanics: "Query Freshness Gate", "Workspace Federation", "Asset Bundling". Never restate a path or a directory name, and never name it after the language or the file type.
- If the files in a cluster serve two or more unrelated responsibilities, answer exactly "mixed" for that cluster. A wrong name is worse than no name, because a reviewer will trust it.
- Answer for every cluster key you are given, and invent no other keys.`

var namesSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"names": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":  map[string]any{"type": "string", "description": "the cluster key, copied verbatim"},
					"name": map[string]any{"type": "string", "description": `2-4 word Title Case name, or "mixed"`},
				},
				"required":             []string{"key", "name"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"names"},
	"additionalProperties": false,
}

func clusterPrompt(clusters []Cluster) string {
	parts := make([]string, len(clusters))
	for i, cluster := range clusters {
		symbols := strings.Join(cluster.Symbols[:min(len(cluster.Symbols), symbolsPerCluster)], ", ")
		if symbols == "" {
			symbols = "(none extracted)"
		}
		parts[i] = fmt.Sprintf("key: %s\n  files: %s\n  symbols: %s", cluster.Key, strings.Join(cluster.Files[:min(len(cluster.Files), 10)], ", "), symbols)
	}
	return strings.Join(parts, "\n\n")
}

// ChatNamer names every cluster in one forced-tool call.
type ChatNamer struct {
	Client *llm.Client
}

var mixedName = regexp.MustCompile(`(?i)^mixed$`)

// Name asks the model for one name per cluster, dropping "mixed" answers.
func (namer ChatNamer) Name(ctx context.Context, clusters []Cluster) (map[string]string, error) {
	temperature := 0.0
	args, err := namer.Client.CallTool(ctx, llm.Request{
		System: namingSystemPrompt,
		User:   clusterPrompt(clusters),
		Tool:   llm.Tool{Name: "record_names", Description: "Record one name per cluster.", Parameters: namesSchema},
		// Deterministic answers keep the cached names comparable across runs.
		Temperature: &temperature,
		MaxTokens:   1024,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	var payload struct {
		Names []struct {
			Key  any `json:"key"`
			Name any `json:"name"`
		} `json:"names"`
	}
	if len(args) == 0 || json.Unmarshal(args, &payload) != nil {
		return out, nil
	}
	for _, row := range payload.Names {
		key, keyOK := row.Key.(string)
		name, nameOK := row.Name.(string)
		if !keyOK || !nameOK || key == "" || name == "" {
			continue
		}
		clean := Sanitize(name)
		if clean == "" || mixedName.MatchString(clean) {
			continue
		}
		out[key] = clean
	}
	return out, nil
}

var (
	lineBreaks   = regexp.MustCompile(`[\r\n]+`)
	unsafeLabel  = regexp.MustCompile("[<>\"'`|(){}\\[\\]#]")
	whitespaceJS = regexp.MustCompile(`[\s\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)
)

// Sanitize strips characters that could break a Mermaid label or a table cell
// from a model-written name, then shortens it.
func Sanitize(raw string) string {
	flat := lineBreaks.ReplaceAllString(raw, " ")
	flat = unsafeLabel.ReplaceAllString(flat, "")
	flat = trimJS(whitespaceJS.ReplaceAllString(flat, " "))
	return ShortLabel(flat)
}

// ClusterHash identifies a cluster by its member paths and their content hashes.
func ClusterHash(files []string, hashes map[string]string) string {
	sorted := slices.Clone(files)
	sortCodeUnits(sorted)
	parts := make([]string, len(sorted))
	for i, file := range sorted {
		hash, ok := hashes[file]
		if !ok {
			hash = "?"
		}
		parts[i] = file + ":" + hash
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// nameCache is areas.json with its key order kept, as JSON.parse keeps it.
type nameCache struct {
	keys   []string
	values map[string]json.RawMessage
}

func (cache *nameCache) get(key string) (string, bool) {
	var name string
	raw, ok := cache.values[key]
	if !ok || json.Unmarshal(raw, &name) != nil || name == "" {
		return "", false
	}
	return name, true
}

func (cache *nameCache) set(key, name string) {
	if _, ok := cache.values[key]; !ok {
		cache.keys = append(cache.keys, key)
	}
	encoded, err := jsonv2.Marshal(name)
	if err != nil {
		return
	}
	cache.values[key] = encoded
}

func loadNameCache(contextDir string) *nameCache {
	cache := &nameCache{values: make(map[string]json.RawMessage)}
	data, err := os.ReadFile(filepath.Join(contextDir, ".cache", areasCacheFile))
	if err != nil {
		return cache
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return cache
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return &nameCache{values: make(map[string]json.RawMessage)}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return &nameCache{values: make(map[string]json.RawMessage)}
		}
		if _, seen := cache.values[key]; !seen {
			cache.keys = append(cache.keys, key)
		}
		cache.values[key] = value
	}
	return cache
}

func saveNameCache(contextDir string, cache *nameCache) {
	var body bytes.Buffer
	body.WriteByte('{')
	for i, key := range cache.keys {
		if i > 0 {
			body.WriteByte(',')
		}
		encoded, err := jsonv2.Marshal(key)
		if err != nil {
			return
		}
		body.Write(encoded)
		body.WriteByte(':')
		body.Write(cache.values[key])
	}
	body.WriteByte('}')
	var indented bytes.Buffer
	if json.Indent(&indented, body.Bytes(), "", "  ") != nil {
		return
	}
	indented.WriteByte('\n')
	path := filepath.Join(contextDir, ".cache", areasCacheFile)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = os.WriteFile(path, indented.Bytes(), 0o644) // an unwritable cache costs a call next time
}

type labelled interface {
	labelTarget() (*string, *LabelSource, []string, []string)
}

func (module *ImpactedModule) labelTarget() (*string, *LabelSource, []string, []string) {
	names := make([]string, len(module.Symbols))
	for i, symbol := range module.Symbols {
		names[i] = symbol.Name
	}
	return &module.Label, &module.LabelSource, module.Files, names
}

func (area *ChangedArea) labelTarget() (*string, *LabelSource, []string, []string) {
	return &area.Label, &area.LabelSource, area.Files, area.SeedNames
}

type pendingCluster struct {
	cluster Cluster
	targets []labelled
}

// ApplyNames names every module and area still on its symbol backstop, from
// the cache first and then from namer when one is given.
func ApplyNames(ctx context.Context, wiring graph.GraphV1, report *Report, contextDir string, namer Namer) NameStats {
	stats := NameStats{}
	order, byHash := backstopClusters(wiring, report)
	if len(order) == 0 {
		return stats
	}
	cache := loadNameCache(contextDir)
	pending := make([]pendingCluster, 0)
	for _, hash := range order {
		group := byHash[hash]
		if hit, ok := cache.get(hash); ok {
			relabel(group, hit)
			stats.Cached++
			continue
		}
		_, _, files, symbols := group[0].labelTarget()
		pending = append(pending, pendingCluster{cluster: Cluster{Key: hash, Files: files, Symbols: symbols}, targets: group})
	}
	if len(pending) == 0 || namer == nil {
		return stats
	}
	named, err := requestNames(ctx, namer, pending)
	stats.Err = err
	for _, item := range pending {
		name, ok := named[item.cluster.Key]
		if !ok || name == "" {
			stats.Declined++
			continue
		}
		relabel(item.targets, name)
		cache.set(item.cluster.Key, name)
		stats.Named++
	}
	if stats.Named > 0 {
		saveNameCache(contextDir, cache)
	}
	return stats
}

// backstopClusters groups the modules and areas on a symbol label by cluster
// hash, in first-seen order. Test modules are never named.
func backstopClusters(wiring graph.GraphV1, report *Report) ([]string, map[string][]labelled) {
	targets := make([]labelled, 0)
	for _, module := range report.Modules {
		if module.LabelSource == LabelSymbol {
			targets = append(targets, module)
		}
	}
	for _, area := range report.Areas {
		if area.LabelSource == LabelSymbol {
			targets = append(targets, area)
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	hashes := make(map[string]string)
	for _, node := range wiring.Nodes {
		if node.Kind == "file" {
			hashes[node.Path] = node.BodyHash
		}
	}
	order := make([]string, 0)
	byHash := make(map[string][]labelled)
	for _, target := range targets {
		_, _, files, _ := target.labelTarget()
		hash := ClusterHash(files, hashes)
		if _, ok := byHash[hash]; !ok {
			order = append(order, hash)
		}
		byHash[hash] = append(byHash[hash], target)
	}
	return order, byHash
}

// requestNames asks namer in batches. A failed batch stops the pass but keeps
// the names earlier batches returned.
func requestNames(ctx context.Context, namer Namer, pending []pendingCluster) (map[string]string, error) {
	named := make(map[string]string)
	for start := 0; start < len(pending); start += clustersPerCall {
		batch := pending[start:min(start+clustersPerCall, len(pending))]
		clusters := make([]Cluster, len(batch))
		for i, item := range batch {
			clusters[i] = item.cluster
		}
		names, err := namer.Name(ctx, clusters)
		if err != nil {
			return named, err
		}
		maps.Copy(named, names)
	}
	return named, nil
}

func relabel(targets []labelled, name string) {
	for _, target := range targets {
		label, source, _, _ := target.labelTarget()
		*label, *source = name, LabelNamed
	}
}

// NameReport runs the naming pass with the environment's provider and returns
// the note a CLI prints, or "" when there is nothing to say.
func NameReport(ctx context.Context, wiring graph.GraphV1, report *Report, contextDir string) (string, error) {
	cfg := llm.ResolveConfig()
	var namer Namer
	if cfg.APIKey != "" {
		client, err := llm.New(cfg)
		if err != nil {
			return "", err
		}
		namer = ChatNamer{Client: client}
	}
	stats := ApplyNames(ctx, wiring, report, contextDir, namer)
	switch {
	case namer == nil:
		return "no API key (GRAFT_API_KEY), so areas keep their symbol names", nil
	case stats.Err != nil:
		return fmt.Sprintf("naming failed (%s) — areas keep their symbol names", stats.Err), nil
	case stats.Named+stats.Cached+stats.Declined > 0:
		note := fmt.Sprintf("%d named, %d cached", stats.Named, stats.Cached)
		if stats.Declined > 0 {
			note += fmt.Sprintf(", %d left as symbols (mixed)", stats.Declined)
		}
		return note, nil
	default:
		return "", nil
	}
}
