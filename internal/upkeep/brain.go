package upkeep

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

const (
	brainRulesTTL      = 6 * time.Hour
	emptyBrainRulesTTL = 2 * time.Minute
	maxBrainRulesBody  = 16 << 20
)

type brainLink struct {
	BrainID string `json:"brainId"`
	Token   string `json:"token"`
	BaseURL string `json:"baseUrl"`
}

type brainRulesCache struct {
	BrainID   string            `json:"brainId"`
	FetchedAt int64             `json:"fetchedAt"`
	Rules     []graph.BrainRule `json:"rules"`
	CheckedAt *int64            `json:"checkedAt,omitempty"`
}

// MaybeRefreshBrainRules records a stale-cache attempt and starts a detached refresh.
func MaybeRefreshBrainRules(root, contextDir string, now time.Time) bool {
	if readBrainLink(root) == nil {
		return false
	}
	cachePath := brainRulesCachePath(root, contextDir)
	if data, err := os.ReadFile(cachePath); err == nil {
		var cache brainRulesCache
		if json.Unmarshal(data, &cache) == nil {
			if !brainRulesCacheStale(&cache, now) {
				return false
			}
			cache.CheckedAt = new(now.UnixMilli())
			data, err := json.Marshal(cache)
			if err != nil {
				return false
			}
			if err := writeAtomicCache(cachePath, data, "brain rules"); err != nil {
				return false
			}
		}
	}
	executable, err := os.Executable()
	if err != nil || strings.HasSuffix(strings.TrimSuffix(filepath.Base(executable), ".exe"), ".test") {
		return false
	}
	command := exec.Command(executable, "_brain-refresh", root, contextDir)
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return false
	}
	_ = command.Process.Release()
	return true
}

// RefreshBrainRules fetches the linked brain's rules and atomically updates its cache.
func RefreshBrainRules(ctx context.Context, root, contextDir string, now time.Time) error {
	link := readBrainLink(root)
	if link == nil {
		return nil
	}
	base := strings.TrimRight(cmp.Or(os.Getenv("GRAFT_BRAIN_URL"), link.BaseURL, "https://agents.nanonets.com"), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid brain base URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/public/brains/"+url.PathEscape(link.BrainID)+"/rules/anchors", nil)
	if err != nil {
		return fmt.Errorf("create brain rules request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+link.Token)
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 0 && (!strings.EqualFold(request.URL.Host, via[0].URL.Host) || request.URL.Scheme != via[0].URL.Scheme) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}).Do(request)
	if err != nil {
		return fmt.Errorf("fetch brain rules: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("fetch brain rules: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBrainRulesBody+1))
	if err != nil {
		return fmt.Errorf("read brain rules: %w", err)
	}
	if len(body) > maxBrainRulesBody {
		return fmt.Errorf("brain rules response exceeds %d bytes", maxBrainRulesBody)
	}
	var result struct {
		Anchors []struct {
			RuleID      string `json:"rule_id"`
			Symbol      string `json:"symbol"`
			Fingerprint string `json:"fingerprint"`
			Rule        string `json:"rule"`
			SourceURL   string `json:"source_url"`
		} `json:"anchors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode brain rules: %w", err)
	}
	if result.Anchors == nil {
		return errors.New("decode brain rules: missing anchors array")
	}
	rules := make([]graph.BrainRule, 0, len(result.Anchors))
	for _, anchor := range result.Anchors {
		if anchor.Symbol == "" || anchor.Rule == "" {
			continue
		}
		rules = append(rules, graph.BrainRule{
			RuleID:      anchor.RuleID,
			Symbol:      anchor.Symbol,
			Fingerprint: anchor.Fingerprint,
			Rule:        anchor.Rule,
			SourceURL:   anchor.SourceURL,
		})
	}
	data, err := json.Marshal(brainRulesCache{BrainID: link.BrainID, FetchedAt: now.UnixMilli(), Rules: rules})
	if err != nil {
		return fmt.Errorf("encode brain rules cache: %w", err)
	}
	if err := writeAtomicCache(brainRulesCachePath(root, contextDir), data, "brain rules"); err != nil {
		return err
	}
	writeBrainSections(root, contextDir, rules, filepath.Base(root))
	return nil
}

func writeBrainSections(root, contextDir string, rules []graph.BrainRule, repoLabel string) {
	if len(rules) == 0 {
		return
	}
	body := renderBrainSection(rules, repoLabel)
	for _, path := range brainSectionTargets(root, contextDir) {
		if err := upsertBrainSection(filepath.Join(root, path), body); err != nil {
			continue
		}
	}
}

func brainSectionTargets(root, contextDir string) []string {
	var stamp *struct {
		Hosts []string `json:"hosts"`
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(brainRulesCachePath(root, contextDir)), "wiring-stamp.json")); err == nil {
		var decoded *struct {
			Hosts []string `json:"hosts"`
		}
		if json.Unmarshal(data, &decoded) == nil {
			stamp = decoded
		}
	}
	hosts := wiredBrainHostIDs(root)
	if stamp != nil {
		hosts = slices.Concat(stamp.Hosts, hosts)
	}
	targets := []struct {
		id   string
		path string
	}{
		{id: "agents", path: "AGENTS.md"},
		{id: "gemini", path: "GEMINI.md"},
		{id: "hermes", path: "AGENTS.md"},
		{id: "antigravity", path: "AGENTS.md"},
		{id: "copilot", path: filepath.Join(".github", "copilot-instructions.md")},
	}
	seen := make(map[string]struct{}, len(targets))
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		if !slices.Contains(hosts, target.id) {
			continue
		}
		if _, ok := seen[target.path]; ok {
			continue
		}
		seen[target.path] = struct{}{}
		paths = append(paths, target.path)
	}
	return paths
}

func wiredBrainHostIDs(root string) []string {
	agents := hasGraftSection(filepath.Join(root, "AGENTS.md"))
	ids := make([]string, 0, 5)
	if agents {
		ids = append(ids, "agents")
	}
	if hasGraftSection(filepath.Join(root, "GEMINI.md")) {
		ids = append(ids, "gemini")
	}
	if agents {
		ids = append(ids, "hermes", "antigravity")
	}
	if hasGraftSection(filepath.Join(root, ".github", "copilot-instructions.md")) {
		ids = append(ids, "copilot")
	}
	return ids
}

func hasGraftSection(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), "<!-- graft:start -->")
}

func renderBrainSection(rules []graph.BrainRule, repoLabel string) string {
	capped := rules[:min(len(rules), 40)]
	lines := []string{
		"## House rules for this codebase",
		"",
		fmt.Sprintf("Mined from %s's own history — commit messages and pull-request", repoLabel),
		"discussion — by Trail. These are decisions the team has already made, so",
		"follow them and do not re-litigate them in passing. Each is attributed to",
		"what established it.",
		"",
	}
	byFile := make(map[string][]graph.BrainRule, len(capped))
	for _, rule := range capped {
		file, _, ok := strings.Cut(rule.Symbol, "#")
		if !ok {
			file = ""
		}
		byFile[file] = append(byFile[file], rule)
	}
	for _, rule := range byFile[""] {
		line := "- " + rule.Rule
		if rule.SourceURL != "" {
			line += " (" + rule.SourceURL + ")"
		}
		lines = append(lines, line)
	}
	if len(byFile[""]) > 0 {
		lines = append(lines, "")
	}
	files := make([]string, 0, len(byFile))
	for file := range byFile {
		if file != "" {
			files = append(files, file)
		}
	}
	slices.Sort(files)
	for _, file := range files {
		lines = append(lines, "### "+file)
		for _, rule := range byFile[file] {
			line := "- " + rule.Rule
			if rule.SourceURL != "" {
				line += " (" + rule.SourceURL + ")"
			}
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}
	if len(rules) > len(capped) {
		lines = append(lines, fmt.Sprintf("_%d more rules apply to specific symbols; graft attaches those to each `graft ask` answer._", len(rules)-len(capped)))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func upsertBrainSection(path, body string) error {
	const start = "<!-- graft:brain:start -->"
	const endMarker = "<!-- graft:brain:end -->"
	block := start + "\n" + strings.TrimSpace(strings.ReplaceAll(body, "\r", "")) + "\n" + endMarker
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(block+"\n"), 0o644)
	}
	if err != nil {
		return err
	}
	text := string(data)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	startIndex, endIndex := -1, -1
	for index, line := range lines {
		if strings.TrimSpace(line) == start {
			startIndex = index
			for markerIndex := index + 1; markerIndex < len(lines); markerIndex++ {
				if strings.TrimSpace(lines[markerIndex]) == endMarker {
					endIndex = markerIndex
					break
				}
			}
			break
		}
	}
	if startIndex >= 0 && endIndex >= 0 {
		if strings.Join(lines[startIndex:endIndex+1], "\n") == block {
			return nil
		}
		updated := slices.Concat(
			lines[:startIndex],
			strings.Split(strings.ReplaceAll(block, "\n", eol), eol),
			lines[endIndex+1:],
		)
		return os.WriteFile(path, []byte(strings.Join(updated, eol)), 0o644)
	}
	separator := eol + eol
	if strings.HasSuffix(text, separator) {
		separator = ""
	} else if strings.HasSuffix(text, eol) {
		separator = eol
	}
	return os.WriteFile(path, []byte(text+separator+strings.ReplaceAll(block, "\n", eol)+eol), 0o644)
}

func brainRulesCacheStale(cache *brainRulesCache, now time.Time) bool {
	if cache == nil {
		return true
	}
	ttl := brainRulesTTL
	if len(cache.Rules) == 0 {
		ttl = emptyBrainRulesTTL
	}
	checkedAt := cache.FetchedAt
	if cache.CheckedAt != nil {
		checkedAt = *cache.CheckedAt
	}
	return now.UnixMilli()-checkedAt > ttl.Milliseconds()
}

func readBrainLink(root string) *brainLink {
	brainID, token := os.Getenv("GRAFT_BRAIN_ID"), os.Getenv("GRAFT_BRAIN_TOKEN")
	if brainID != "" && token != "" {
		return &brainLink{BrainID: brainID, Token: token, BaseURL: os.Getenv("GRAFT_BRAIN_URL")}
	}
	data, err := os.ReadFile(filepath.Join(root, ".graft", "config.json"))
	if err != nil {
		return nil
	}
	var config struct {
		Brain *brainLink `json:"brain"`
	}
	if json.Unmarshal(data, &config) != nil || config.Brain == nil || config.Brain.BrainID == "" || config.Brain.Token == "" {
		return nil
	}
	return config.Brain
}

func brainRulesCachePath(root, contextDir string) string {
	if contextDir == "" {
		contextDir = os.Getenv("GRAFT_DIR")
	}
	if contextDir == "" {
		contextDir = filepath.Join(root, "graft")
	} else if !filepath.IsAbs(contextDir) {
		contextDir = filepath.Join(root, contextDir)
	}
	return filepath.Join(contextDir, ".cache", "brain-rules.json")
}
