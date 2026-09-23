// Package brain links a repository to a Trail brain: the stored link, the
// cached symbol-anchored rules, and the fenced rules block written into the
// instruction files agents read.
package brain

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	jsonv2 "encoding/json/v2"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/hosts"
	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
	"github.com/NanoNets/context-graph-engine/internal/repoconfig"
)

const (
	defaultBaseURL = "https://agents.nanonets.com"
	// RulesTTL is how long a cached rule set is served before a refresh.
	RulesTTL = 6 * time.Hour
	// EmptyRulesTTL is the TTL while the cache holds no rules yet.
	EmptyRulesTTL = 2 * time.Minute
	fetchTimeout  = 5 * time.Second
	maxBody       = 16 << 20
	maxFileRules  = 40
)

// Link is the persisted connection to a brain.
type Link struct {
	BrainID string
	Token   string
	BaseURL string
	// stored is the link object as read from the config, so rewriting it keeps
	// fields graft does not know about.
	stored *jsonjs.Object
}

func (link Link) object() *jsonjs.Object {
	if link.stored != nil {
		return link.stored.Clone()
	}
	object := jsonjs.NewObject()
	object.Set("brainId", link.BrainID)
	object.Set("token", link.Token)
	if link.BaseURL != "" {
		object.Set("baseUrl", link.BaseURL)
	}
	return object
}

// ReadLink returns the link for repo, or false when it has none.
// GRAFT_BRAIN_TOKEN and GRAFT_BRAIN_ID together override the stored pair.
func ReadLink(repo string) (Link, bool) {
	token, brainID := os.Getenv("GRAFT_BRAIN_TOKEN"), os.Getenv("GRAFT_BRAIN_ID")
	if token != "" && brainID != "" {
		return Link{BrainID: brainID, Token: token, BaseURL: os.Getenv("GRAFT_BRAIN_URL")}, true
	}
	config, ok := jsonjs.AsObject(repoconfig.Read(repo))
	if !ok {
		return Link{}, false
	}
	value, _ := config.Get("brain")
	stored, ok := jsonjs.AsObject(value)
	if !ok {
		return Link{}, false
	}
	link := Link{stored: stored}
	for key, target := range map[string]*string{"brainId": &link.BrainID, "token": &link.Token, "baseUrl": &link.BaseURL} {
		if value, present := stored.Get(key); jsonjs.Truthy(value, present) {
			*target = jsonjs.String(value)
		}
	}
	if link.BrainID == "" || link.Token == "" {
		return Link{}, false
	}
	return link, true
}

// WriteLink persists link in the repo's .graft/config.json.
func WriteLink(repo string, link Link) error {
	return repoconfig.Patch(repo, []repoconfig.Field{{Key: "brain", Value: link.object()}})
}

// ClearLink forgets the link; the cached rules stay for uninstall to remove.
func ClearLink(repo string) error {
	return repoconfig.Patch(repo, []repoconfig.Field{{Key: "brain"}})
}

// ParseHandoff reads a --brain value: <brainId>:<token>, or a bare brain id
// with the token in GRAFT_BRAIN_TOKEN.
func ParseHandoff(value string) (Link, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return Link{}, errors.New("empty --brain value")
	}
	if cut := strings.Index(raw, ":"); cut > 0 {
		brainID, token := strings.TrimSpace(raw[:cut]), strings.TrimSpace(raw[cut+1:])
		if brainID == "" || token == "" {
			return Link{}, errors.New("expected --brain <brainId>:<token>")
		}
		return Link{BrainID: brainID, Token: token}, nil
	}
	token := os.Getenv("GRAFT_BRAIN_TOKEN")
	if token == "" {
		return Link{}, errors.New("expected --brain <brainId>:<token>, or a bare brain id with the token in GRAFT_BRAIN_TOKEN")
	}
	return Link{BrainID: raw, Token: token}, nil
}

var trailingSlashes = regexp.MustCompile(`/+$`)

// encodeURIComponent escapes like JavaScript's encodeURIComponent, which
// leaves A–Z a–z 0–9 - _ . ! ~ * ' ( ) as they are.
func encodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9', strings.IndexByte("-_.!~*'()", b) >= 0:
			out.WriteByte(b)
		default:
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

// BaseURL is the API host for link, with GRAFT_BRAIN_URL taking precedence.
func BaseURL(link Link) string {
	return trailingSlashes.ReplaceAllString(cmp.Or(os.Getenv("GRAFT_BRAIN_URL"), link.BaseURL, defaultBaseURL), "")
}

// newClient refuses redirects that change host or scheme, so the bearer
// token never follows a redirect off the brain's own origin.
func newClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 0 && (!strings.EqualFold(request.URL.Host, via[0].URL.Host) || request.URL.Scheme != via[0].URL.Scheme) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// getJSON fetches an authenticated JSON document from the brain API.
func getJSON(ctx context.Context, link Link, path string) (jsonjs.Value, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL(link)+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+link.Token)
	request.Header.Set("Accept", "application/json")
	response, err := newClient(fetchTimeout).Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBody)
	}
	return jsonjs.Parse(body)
}

func field(object *jsonjs.Object, key string) jsonjs.Value {
	if object == nil {
		return nil
	}
	value, _ := object.Get(key)
	return value
}

func stringField(object *jsonjs.Object, key string) string {
	value := field(object, key)
	if value == nil {
		return ""
	}
	return jsonjs.String(value)
}

// FetchRules fetches the brain's symbol-anchored rules, or false on any failure.
func FetchRules(ctx context.Context, link Link) ([]graph.BrainRule, bool) {
	value, err := getJSON(ctx, link, "/api/public/brains/"+encodeURIComponent(link.BrainID)+"/rules/anchors")
	if err != nil {
		return nil, false
	}
	body, _ := jsonjs.AsObject(value)
	anchors, ok := jsonjs.AsArray(field(body, "anchors"))
	if !ok {
		return nil, false
	}
	rules := make([]graph.BrainRule, 0, len(anchors))
	for _, item := range anchors {
		anchor, _ := jsonjs.AsObject(item)
		symbol, rule := field(anchor, "symbol"), field(anchor, "rule")
		if !jsonjs.Truthy(symbol, symbol != nil) || !jsonjs.Truthy(rule, rule != nil) {
			continue
		}
		sourceURL := ""
		if source := field(anchor, "source_url"); jsonjs.Truthy(source, source != nil) {
			sourceURL = jsonjs.String(source)
		}
		rules = append(rules, graph.BrainRule{
			RuleID: stringField(anchor, "rule_id"), Symbol: jsonjs.String(symbol),
			Fingerprint: stringField(anchor, "fingerprint"), Rule: jsonjs.String(rule), SourceURL: sourceURL,
		})
	}
	return rules, true
}

// RulesCache is the cached rule set stored beside the graph.
type RulesCache struct {
	BrainID   string            `json:"brainId"`
	FetchedAt int64             `json:"fetchedAt"`
	Rules     []graph.BrainRule `json:"rules"`
	CheckedAt *int64            `json:"checkedAt,omitzero"`
}

// ContextDir is the repo's graph directory, honouring GRAFT_DIR.
func ContextDir(repo string) string {
	override := os.Getenv("GRAFT_DIR")
	if override == "" {
		return filepath.Join(repo, "graft")
	}
	if filepath.IsAbs(override) {
		return override
	}
	return filepath.Join(repo, override)
}

// RulesCachePath is where the rule cache lives.
func RulesCachePath(repo string) string {
	return filepath.Join(ContextDir(repo), ".cache", "brain-rules.json")
}

// ReadRulesCache reads the cached rules, or false when there are none.
func ReadRulesCache(repo string) (RulesCache, bool) {
	data, err := os.ReadFile(RulesCachePath(repo))
	if err != nil {
		return RulesCache{}, false
	}
	var cache RulesCache
	if jsonv2.Unmarshal(data, &cache) != nil {
		return RulesCache{}, false
	}
	return cache, true
}

// WriteRulesCache stores cache compactly, like the TypeScript writer.
func WriteRulesCache(repo string, cache RulesCache) error {
	data, err := jsonv2.Marshal(cache)
	if err != nil {
		return err
	}
	return writeAtomic(RulesCachePath(repo), data)
}

// CacheIsStale reports whether the cache should be refetched: after RulesTTL,
// or EmptyRulesTTL while it holds no rules, since the last attempt.
func CacheIsStale(cache RulesCache, present bool, now time.Time) bool {
	if !present {
		return true
	}
	ttl := RulesTTL
	if len(cache.Rules) == 0 {
		ttl = EmptyRulesTTL
	}
	checked := cache.FetchedAt
	if cache.CheckedAt != nil {
		checked = *cache.CheckedAt
	}
	return now.UnixMilli()-checked > ttl.Milliseconds()
}

// MarkRulesChecked records a refresh attempt without claiming rules arrived.
func MarkRulesChecked(repo string, now time.Time) error {
	cache, ok := ReadRulesCache(repo)
	if !ok {
		return nil
	}
	checked := now.UnixMilli()
	cache.CheckedAt = &checked
	return WriteRulesCache(repo, cache)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// RenderSection renders the rules block for an instruction file: repo-wide
// rules first, then rules grouped under the file they govern.
func RenderSection(rules []graph.BrainRule, repoLabel string) string {
	capped := rules[:min(len(rules), maxFileRules)]
	lines := []string{
		"## House rules for this codebase",
		"",
		"Mined from " + repoLabel + "'s own history — commit messages and pull-request",
		"discussion — by Trail. These are decisions the team has already made, so",
		"follow them and do not re-litigate them in passing. Each is attributed to",
		"what established it.",
		"",
	}
	order := make([]string, 0)
	byFile := make(map[string][]graph.BrainRule)
	for _, rule := range capped {
		file := ""
		if at := strings.Index(rule.Symbol, "#"); at >= 0 {
			file = rule.Symbol[:at]
		}
		if _, ok := byFile[file]; !ok {
			order = append(order, file)
		}
		byFile[file] = append(byFile[file], rule)
	}
	bullet := func(rule graph.BrainRule) string {
		if rule.SourceURL != "" {
			return "- " + rule.Rule + " (" + rule.SourceURL + ")"
		}
		return "- " + rule.Rule
	}
	for _, rule := range byFile[""] {
		lines = append(lines, bullet(rule))
	}
	if len(byFile[""]) > 0 {
		lines = append(lines, "")
	}
	files := slices.DeleteFunc(order, func(file string) bool { return file == "" })
	// Array.prototype.sort on [file, rules] tuples compares "file,…" strings.
	slices.SortStableFunc(files, func(a, b string) int {
		return slices.Compare(utf16.Encode([]rune(a+",")), utf16.Encode([]rune(b+",")))
	})
	for _, file := range files {
		lines = append(lines, "### "+file)
		for _, rule := range byFile[file] {
			lines = append(lines, bullet(rule))
		}
		lines = append(lines, "")
	}
	if len(rules) > len(capped) {
		lines = append(lines, fmt.Sprintf("_%d more rules apply to specific symbols; graft attaches those to each `graft ask` answer._", len(rules)-len(capped)))
	}
	return strings.TrimRightFunc(strings.Join(lines, "\n"), isJSSpace)
}

// isJSSpace matches the characters String.prototype.trimEnd removes.
func isJSSpace(r rune) bool {
	return (unicode.IsSpace(r) && r != '\u0085') || r == '\uFEFF'
}

// SectionTargets lists the section-kind hosts whose files carry the rules:
// the given ids in registry order, or the detected hosts when ids is empty,
// once per file.
func SectionTargets(repo, home string, ids []string) []hosts.Host {
	selected := hosts.DetectHosts(home, repo)
	if len(ids) > 0 {
		selected = slices.DeleteFunc(hosts.Hosts(), func(host hosts.Host) bool { return !slices.Contains(ids, host.ID) })
	}
	seen := make(map[string]bool)
	out := make([]hosts.Host, 0, len(selected))
	for _, host := range selected {
		if host.Kind != hosts.KindSection || seen[host.RelPath] {
			continue
		}
		seen[host.RelPath] = true
		out = append(out, host)
	}
	return out
}

// Write is one rules write to a host's instruction file.
type Write struct {
	ID     string
	Path   string
	Action hosts.UpsertAction
}

// WriteSections writes the rules into each target file, skipping any file
// that cannot be written.
func WriteSections(repo, home string, rules []graph.BrainRule, repoLabel string, ids []string) []Write {
	if len(rules) == 0 {
		return []Write{}
	}
	body := RenderSection(rules, repoLabel)
	writes := make([]Write, 0)
	for _, host := range SectionTargets(repo, home, ids) {
		path := filepath.Join(repo, host.RelPath)
		action, err := hosts.UpsertSection(path, body, hosts.BrainMarkers)
		if err != nil {
			continue
		}
		writes = append(writes, Write{ID: host.ID, Path: path, Action: action})
	}
	return writes
}

// ConnectResult is what connecting a brain did.
type ConnectResult struct {
	RuleCount int
	Writes    []Write
	// Warning is set when the rules could not be fetched; the link is saved.
	Warning string
}

// Connect stores the link, pulls the rules once, and writes them into the
// agent files. The link is stored even when the pull fails.
func Connect(ctx context.Context, repo string, link Link, home string, ids []string, now time.Time) (ConnectResult, error) {
	if err := WriteLink(repo, link); err != nil {
		return ConnectResult{}, err
	}
	rules, ok := FetchRules(ctx, link)
	if !ok {
		return ConnectResult{Writes: []Write{}, Warning: "could not reach the brain to pull its rules — the link is saved; run `graft brain pull` to retry"}, nil
	}
	if err := WriteRulesCache(repo, RulesCache{BrainID: link.BrainID, FetchedAt: now.UnixMilli(), Rules: rules}); err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{RuleCount: len(rules), Writes: WriteSections(repo, home, rules, filepath.Base(repo), ids)}, nil
}

// Pull re-pulls the rules for a linked repo, or reports false when unlinked.
func Pull(ctx context.Context, repo, home string, ids []string, now time.Time) (ConnectResult, bool, error) {
	link, ok := ReadLink(repo)
	if !ok {
		return ConnectResult{}, false, nil
	}
	result, err := Connect(ctx, repo, link, home, ids, now)
	return result, true, err
}
