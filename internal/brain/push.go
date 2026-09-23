package brain

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	jsonv2 "encoding/json/v2"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

const (
	githubAPI          = "https://api.github.com"
	maxCommits         = 1000
	maxThreads         = 200
	maxCommentPages    = 3
	threadConcurrency  = 8
	maxSymbols         = 4000
	maxSourceChars     = 24000
	maxTotalChars      = 300000
	maxSources         = 120
	maxReverts         = 40
	maxTestNames       = 300
	maxDeclined        = 40
	commitRecord       = "\x01"
	commitField        = "\x02"
	truncatedSuffix    = "\n[…truncated]"
	symbolsPerCommit   = 20
	sourceKindUnranked = 1 << 30
)

// Commit is one commit's rule-bearing content.
type Commit struct {
	SHA     string   `json:"sha"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	Files   []string `json:"files"`
	Symbols []string `json:"symbols"`
}

// Comment is one pull-request comment.
type Comment struct {
	Body string `json:"body"`
	Path string `json:"path,omitempty"`
}

// Thread is one closed pull request's discussion.
type Thread struct {
	Number   float64   `json:"number"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	MergeSHA string    `json:"merge_sha"`
	Comments []Comment `json:"comments"`
}

// Symbol is one exported symbol a rule can be anchored to.
type Symbol struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Signature   string `json:"signature"`
	Fingerprint string `json:"fingerprint"`
}

// Source is one artefact in the repo that already states a rule.
type Source struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

// Digest is the payload posted to the brain: messages, titles, comments,
// symbol ids and hashes — never file contents.
type Digest struct {
	Provider      string   `json:"provider"`
	Owner         string   `json:"owner"`
	Name          string   `json:"name"`
	HeadSHA       string   `json:"head_sha"`
	DefaultBranch string   `json:"default_branch"`
	IsPrivate     bool     `json:"is_private"`
	Commits       []Commit `json:"commits"`
	Threads       []Thread `json:"threads"`
	Symbols       []Symbol `json:"symbols"`
	Sources       []Source `json:"sources"`
	AutoApprove   bool     `json:"auto_approve"`
}

// trimJS trims like String.prototype.trim.
func trimJS(text string) string {
	return strings.TrimFunc(text, isJSSpace)
}

func units(text string) []uint16 {
	return utf16.Encode([]rune(text))
}

func git(root string, args ...string) (string, bool) {
	command := exec.Command("git", append([]string{"-c", "core.quotePath=false"}, args...)...)
	command.Dir = root
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if command.Run() != nil {
		return "", false
	}
	return stdout.String(), true
}

var githubRemote = regexp.MustCompile(`(?i)github\.com[:/]+([^/]+)/(.+?)(?:\.git)?/?$`)

// RepoSlug is owner/name of the checkout's GitHub origin, or false.
func RepoSlug(root string) (string, string, bool) {
	out, ok := git(root, "remote", "get-url", "origin")
	if !ok {
		return "", "", false
	}
	match := githubRemote.FindStringSubmatch(trimJS(out))
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

func currentBranch(root string) string {
	out, ok := git(root, "rev-parse", "--abbrev-ref", "HEAD")
	head := trimJS(out)
	if !ok || head == "" || head == "HEAD" {
		return ""
	}
	return head
}

// GitHubToken reads GH_TOKEN or GITHUB_TOKEN, then `gh auth token`.
func GitHubToken() string {
	if token := cmp.Or(os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	command := exec.Command("gh", "auth", "token")
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if command.Run() != nil {
		return ""
	}
	return trimJS(stdout.String())
}

// ReadCommits reads subjects, bodies and touched files, oldest first.
func ReadCommits(root string) []Commit {
	out, ok := git(root, "log", "--no-merges", "--reverse", "-n", strconv.Itoa(maxCommits),
		"--format="+commitRecord+"%H"+commitField+"%s"+commitField+"%b"+commitField, "--name-only")
	if !ok || out == "" {
		return nil
	}
	commits := make([]Commit, 0)
	for record := range strings.SplitSeq(out, commitRecord) {
		if trimJS(record) == "" {
			continue
		}
		parts := strings.Split(record, commitField)
		for len(parts) < 4 {
			parts = append(parts, "")
		}
		sha, subject, body, rest := parts[0], parts[1], parts[2], parts[3]
		if trimJS(sha) == "" || trimJS(subject) == "" {
			continue
		}
		files := make([]string, 0)
		for line := range strings.SplitSeq(rest, "\n") {
			if file := trimJS(line); file != "" {
				files = append(files, file)
			}
		}
		commits = append(commits, Commit{SHA: trimJS(sha), Subject: trimJS(subject), Body: trimJS(body), Files: files})
	}
	return commits
}

// ReadSymbols lists exported symbols, largest first.
func ReadSymbols(wiring *graph.GraphV1) []Symbol {
	if wiring == nil {
		return []Symbol{}
	}
	nodes := slices.DeleteFunc(slices.Clone(wiring.Nodes), func(node graph.NodeV1) bool { return !node.Exported || node.Kind == "file" })
	chars := func(node graph.NodeV1) int {
		if node.Chars == nil {
			return 0
		}
		return *node.Chars
	}
	slices.SortStableFunc(nodes, func(a, b graph.NodeV1) int { return chars(b) - chars(a) })
	symbols := make([]Symbol, 0, min(len(nodes), maxSymbols))
	for _, node := range nodes[:min(len(nodes), maxSymbols)] {
		signature := ""
		if node.Signature != nil {
			signature = *node.Signature
		}
		symbols = append(symbols, Symbol{ID: node.ID, Path: node.Path, Name: node.Name, Kind: string(node.Kind), Signature: signature, Fingerprint: node.BodyHash})
	}
	return symbols
}

type github struct {
	token string
	api   string
}

func (client github) get(ctx context.Context, path string) (jsonjs.Value, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.api+path, nil)
	if err != nil {
		return nil, false
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "graft-app")
	if client.token != "" {
		request.Header.Set("Authorization", "Bearer "+client.token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, false
	}
	value, err := jsonjs.Parse(body)
	return value, err == nil
}

func isPrivate(ctx context.Context, client github, owner, name string) bool {
	if client.token == "" {
		return true
	}
	value, ok := client.get(ctx, "/repos/"+owner+"/"+name)
	if !ok {
		return true
	}
	object, _ := jsonjs.AsObject(value)
	private, _ := objectGet(object, "private")
	return private != false
}

func objectGet(object *jsonjs.Object, key string) (jsonjs.Value, bool) {
	if object == nil {
		return nil, false
	}
	return object.Get(key)
}

func trimmedString(object *jsonjs.Object, key string) string {
	value, _ := objectGet(object, key)
	if text, ok := value.(string); ok {
		return trimJS(text)
	}
	return ""
}

// readThreads reads closed pull requests with discussion, most-discussed first.
func readThreads(ctx context.Context, client github, owner, name string) []Thread {
	pulls := make([]*jsonjs.Object, 0)
	for page := 1; len(pulls) < maxThreads && page <= (maxThreads+99)/100; page++ {
		value, ok := client.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=100&page=%d", owner, name, page))
		batch, isArray := jsonjs.AsArray(value)
		if !ok || !isArray || len(batch) == 0 {
			break
		}
		for _, item := range batch {
			object, _ := jsonjs.AsObject(item)
			pulls = append(pulls, object)
		}
	}
	candidates := make([]*jsonjs.Object, 0, len(pulls))
	for _, pull := range pulls {
		if number, _ := objectGet(pull, "number"); number != nil {
			if _, isNumber := number.(float64); isNumber {
				candidates = append(candidates, pull)
			}
		}
	}
	candidates = candidates[:min(len(candidates), maxThreads)]
	results := make([]*Thread, len(candidates))
	for start := 0; start < len(candidates); start += threadConcurrency {
		var wait sync.WaitGroup
		for index := start; index < min(start+threadConcurrency, len(candidates)); index++ {
			wait.Go(func() {
				pull := candidates[index]
				number, _ := objectGet(pull, "number")
				prefix := fmt.Sprintf("/repos/%s/%s/", owner, name)
				id := jsonjs.FormatNumber(number.(float64))
				comments := append(readComments(ctx, client, prefix+"issues/"+id+"/comments"), readComments(ctx, client, prefix+"pulls/"+id+"/comments")...)
				if len(comments) == 0 {
					return
				}
				results[index] = &Thread{
					Number: number.(float64), Title: trimmedString(pull, "title"), Body: trimmedString(pull, "body"),
					MergeSHA: trimmedString(pull, "merge_commit_sha"), Comments: comments,
				}
			})
		}
		wait.Wait()
	}
	threads := make([]Thread, 0)
	for _, thread := range results {
		if thread != nil {
			threads = append(threads, *thread)
		}
	}
	slices.SortStableFunc(threads, func(a, b Thread) int { return len(b.Comments) - len(a.Comments) })
	return threads
}

func readComments(ctx context.Context, client github, path string) []Comment {
	out := make([]Comment, 0)
	for page := 1; page <= maxCommentPages; page++ {
		value, ok := client.get(ctx, fmt.Sprintf("%s?per_page=100&page=%d", path, page))
		batch, isArray := jsonjs.AsArray(value)
		if !ok || !isArray || len(batch) == 0 {
			break
		}
		for _, item := range batch {
			comment, _ := jsonjs.AsObject(item)
			body := trimmedString(comment, "body")
			if body == "" {
				continue
			}
			user, _ := objectGet(comment, "user")
			userObject, _ := jsonjs.AsObject(user)
			if kind, _ := objectGet(userObject, "type"); kind == "Bot" {
				continue
			}
			path, _ := objectGet(comment, "path")
			text, _ := path.(string)
			out = append(out, Comment{Body: body, Path: text})
		}
		if len(batch) < 100 {
			break
		}
	}
	return out
}

func readText(root, rel string) string {
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	trimmed := trimJS(string([]rune(string(data))))
	if encoded := units(trimmed); len(encoded) > maxSourceChars {
		return string(utf16.Decode(encoded[:maxSourceChars])) + truncatedSuffix
	}
	return trimmed
}

func filesIn(root, rel string, match func(string) bool) []string {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if match(entry.Name()) {
			names = append(names, rel+"/"+entry.Name())
		}
	}
	slices.SortFunc(names, func(a, b string) int { return slices.Compare(units(a), units(b)) })
	return names
}

func suffix(extensions ...string) func(string) bool {
	return func(name string) bool {
		return slices.ContainsFunc(extensions, func(extension string) bool { return strings.HasSuffix(name, extension) })
	}
}

var managedBlock = regexp.MustCompile(`<!--\s*graft:(brain:)?start\s*-->[\s\S]*?<!--\s*graft:(brain:)?end\s*-->`)

func readAgentInstructions(root string) []Source {
	candidates := append([]string{"CLAUDE.md", "AGENTS.md", "GEMINI.md", ".github/copilot-instructions.md", ".windsurf/rules/graft.md"},
		filesIn(root, ".cursor/rules", suffix(".mdc", ".md"))...)
	out := make([]Source, 0)
	for _, rel := range candidates {
		if text := trimJS(managedBlock.ReplaceAllString(readText(root, rel), "")); text != "" {
			out = append(out, Source{Kind: "agent_instructions", Path: rel, Text: text})
		}
	}
	return out
}

func readFiles(root, kind string, candidates []string) []Source {
	out := make([]Source, 0)
	for _, rel := range candidates {
		if text := readText(root, rel); text != "" {
			out = append(out, Source{Kind: kind, Path: rel, Text: text})
		}
	}
	return out
}

func readDecisionDocs(root string) []Source {
	markdown := suffix(".md")
	candidates := slices.Concat([]string{"ARCHITECTURE.md", "CONTRIBUTING.md", "docs/ARCHITECTURE.md", "docs/CONTRIBUTING.md"},
		filesIn(root, "docs/adr", markdown), filesIn(root, "docs/decisions", markdown), filesIn(root, "adr", markdown))
	return readFiles(root, "decision_doc", candidates)
}

func readCodifiedRules(root string) []Source {
	lint := []string{".golangci.yml", ".golangci.yaml", ".eslintrc.json", ".eslintrc.js", "eslint.config.js", "eslint.config.mjs",
		"ruff.toml", ".ruff.toml", ".prettierrc", ".prettierrc.json", "commitlint.config.cjs", "commitlint.config.js", ".editorconfig"}
	return append(readFiles(root, "lint_config", lint), readFiles(root, "ci_config", filesIn(root, ".github/workflows", suffix(".yml", ".yaml")))...)
}

func readCodeowners(root string) []Source {
	for _, rel := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		if text := readText(root, rel); text != "" {
			return []Source{{Kind: "codeowners", Path: rel, Text: text}}
		}
	}
	return nil
}

func readReverts(root string) []Source {
	out, ok := git(root, "log", "--grep=^Revert", "-n", strconv.Itoa(maxReverts), "--format=%H%x02%s%x02%b%x01")
	if !ok || out == "" {
		return nil
	}
	sources := make([]Source, 0)
	for record := range strings.SplitSeq(out, "\x01") {
		if trimJS(record) == "" {
			continue
		}
		parts := strings.Split(record, "\x02")
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		sha, subject, body := trimJS(parts[0]), trimJS(parts[1]), trimJS(parts[2])
		if sha == "" || subject == "" {
			continue
		}
		text := subject
		if body != "" {
			text += "\n" + body
		}
		sources = append(sources, Source{Kind: "revert", Path: "revert " + string([]rune(sha)[:min(12, len([]rune(sha)))]), Text: text})
	}
	return sources
}

var (
	testPath       = regexp.MustCompile(`(?i)(^|[/.])(test|spec)[s]?[/.]|_test\.|\.test\.|\.spec\.`)
	testPrefix     = regexp.MustCompile(`(?i)^(Test|it|test|should|describe)[_\s]*`)
	lettersOnly    = regexp.MustCompile(`^[A-Za-z]+$`)
	camelBoundary  = regexp.MustCompile(`[a-z][A-Z]`)
	camelSplit     = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	separatorRuns  = regexp.MustCompile(`[_-]+`)
	whitespaceRuns = regexp.MustCompile(`\s+`)
)

func humanizeTestName(name string) string {
	text := testPrefix.ReplaceAllString(name, "")
	if lettersOnly.MatchString(text) && camelBoundary.MatchString(text) {
		text = camelSplit.ReplaceAllString(text, "$1 $2")
	}
	text = trimJS(whitespaceRuns.ReplaceAllString(separatorRuns.ReplaceAllString(text, " "), " "))
	if len(strings.Split(text, " ")) < 3 {
		return ""
	}
	return strings.ToLower(text)
}

func readTestNames(symbols []Symbol) []Source {
	out := make([]Source, 0)
	for _, symbol := range symbols {
		if !testPath.MatchString(symbol.Path) {
			continue
		}
		sentence := humanizeTestName(symbol.Name)
		if sentence == "" {
			continue
		}
		out = append(out, Source{Kind: "test_name", Path: symbol.Path, Text: sentence})
		if len(out) == maxTestNames {
			break
		}
	}
	return out
}

func readDeclinedIssues(ctx context.Context, client github, owner, name string) []Source {
	value, ok := client.get(ctx, fmt.Sprintf("/repos/%s/%s/issues?state=closed&per_page=100&sort=updated", owner, name))
	items, isArray := jsonjs.AsArray(value)
	if !ok || !isArray {
		return nil
	}
	out := make([]Source, 0)
	for _, item := range items {
		issue, _ := jsonjs.AsObject(item)
		if pull, present := objectGet(issue, "pull_request"); jsonjs.Truthy(pull, present) {
			continue
		}
		if reason, _ := objectGet(issue, "state_reason"); reason != "not_planned" {
			continue
		}
		title := trimmedString(issue, "title")
		if title == "" {
			continue
		}
		text := title
		if body := trimmedString(issue, "body"); body != "" {
			text += "\n" + body
		}
		if encoded := units(text); len(encoded) > maxSourceChars {
			text = string(utf16.Decode(encoded[:maxSourceChars]))
		}
		number, _ := objectGet(issue, "number")
		link, _ := objectGet(issue, "html_url")
		htmlURL, _ := link.(string)
		out = append(out, Source{Kind: "declined_issue", Path: "issue #" + jsonjs.String(orUndefined(number)), Text: text, URL: htmlURL})
		if len(out) == maxDeclined {
			break
		}
	}
	return out
}

// orUndefined stands in for an absent value, which JavaScript prints as "undefined".
func orUndefined(value jsonjs.Value) jsonjs.Value {
	if value == nil {
		return "undefined"
	}
	return value
}

func readBranchProtection(ctx context.Context, client github, owner, name, branch string) []Source {
	if branch == "" {
		return nil
	}
	value, ok := client.get(ctx, fmt.Sprintf("/repos/%s/%s/branches/%s/protection", owner, name, encodeURIComponent(branch)))
	protection, isObject := jsonjs.AsObject(value)
	if !ok || !isObject {
		return nil
	}
	lines := make([]string, 0, 4)
	checksValue, _ := objectGet(asObject(objectValue(protection, "required_status_checks")), "contexts")
	if checks, ok := jsonjs.AsArray(checksValue); ok && len(checks) > 0 {
		lines = append(lines, fmt.Sprintf("Checks that must pass before merging to %s: %s.", branch, jsonjs.String(checks)))
	}
	reviews := asObject(objectValue(protection, "required_pull_request_reviews"))
	if count, present := objectGet(reviews, "required_approving_review_count"); jsonjs.Truthy(count, present) {
		lines = append(lines, fmt.Sprintf("%s approving review(s) required to merge to %s.", jsonjs.String(count), branch))
	}
	if owners, present := objectGet(reviews, "require_code_owner_reviews"); jsonjs.Truthy(owners, present) {
		lines = append(lines, "A code owner must approve changes to "+branch+".")
	}
	if enabled, _ := objectGet(asObject(objectValue(protection, "allow_force_pushes")), "enabled"); enabled == false {
		lines = append(lines, "Force pushes to "+branch+" are not allowed.")
	}
	if len(lines) == 0 {
		return nil
	}
	return []Source{{Kind: "branch_protection", Path: "branch protection: " + branch, Text: strings.Join(lines, "\n")}}
}

func objectValue(object *jsonjs.Object, key string) jsonjs.Value {
	value, _ := objectGet(object, key)
	return value
}

func asObject(value jsonjs.Value) *jsonjs.Object {
	object, _ := jsonjs.AsObject(value)
	return object
}

var kindRank = []string{"agent_instructions", "decision_doc", "codeowners", "branch_protection", "lint_config", "ci_config", "revert", "declined_issue", "test_name"}

// budgetSources keeps the best kinds first within the character and count caps.
func budgetSources(sources []Source) []Source {
	rank := func(kind string) int {
		if at := slices.Index(kindRank, kind); at >= 0 {
			return at
		}
		return sourceKindUnranked
	}
	ranked := slices.Clone(sources)
	slices.SortStableFunc(ranked, func(a, b Source) int { return rank(a.Kind) - rank(b.Kind) })
	out := make([]Source, 0)
	spent := 0
	for _, source := range ranked {
		if len(out) >= maxSources {
			break
		}
		size := len(units(source.Text))
		if spent+size > maxTotalChars {
			continue
		}
		out = append(out, source)
		spent += size
	}
	return out
}

// BuildDigest reads the checkout at root into a digest, returning a warning
// when discussion could not be read.
func BuildDigest(ctx context.Context, root string, wiring *graph.GraphV1, autoApprove bool) (Digest, string, error) {
	owner, name, ok := RepoSlug(root)
	if !ok {
		return Digest{}, "", errors.New("this directory has no GitHub `origin` remote — graft can only push a GitHub repository today")
	}
	client := github{token: GitHubToken(), api: githubAPI}
	commits := ReadCommits(root)
	if len(commits) == 0 {
		return Digest{}, "", errors.New("no commits found here — is this a shallow clone with no history?")
	}
	symbols := ReadSymbols(wiring)
	threads := []Thread{}
	warning := "no GitHub token found (`gh auth login`, or GH_TOKEN) — mining commits and repo files only, without pull-request discussion"
	if client.token != "" {
		threads, warning = readThreads(ctx, client, owner, name), ""
	}
	branch := currentBranch(root)
	sources := slices.Concat(readAgentInstructions(root), readDecisionDocs(root), readCodeowners(root), readCodifiedRules(root),
		readReverts(root), readTestNames(symbols))
	if client.token != "" {
		sources = slices.Concat(sources, readBranchProtection(ctx, client, owner, name, branch), readDeclinedIssues(ctx, client, owner, name))
	}
	head, _ := git(root, "rev-parse", "HEAD")
	byPath := make(map[string][]string)
	for _, symbol := range symbols {
		byPath[symbol.Path] = append(byPath[symbol.Path], symbol.ID)
	}
	for i, commit := range commits {
		ids := make([]string, 0)
		for _, file := range commit.Files {
			ids = append(ids, byPath[file]...)
		}
		commits[i].Symbols = ids[:min(len(ids), symbolsPerCommit)]
	}
	return Digest{
		Provider: "github", Owner: owner, Name: name, HeadSHA: trimJS(head), DefaultBranch: branch,
		IsPrivate: isPrivate(ctx, client, owner, name), Commits: commits, Threads: threads, Symbols: symbols,
		Sources: budgetSources(sources), AutoApprove: autoApprove,
	}, warning, nil
}

// PushDigest posts the digest and returns the job id.
func PushDigest(ctx context.Context, link Link, digest Digest) (string, error) {
	body, err := jsonv2.Marshal(digest)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL(link)+"/api/public/brains/"+encodeURIComponent(link.BrainID)+"/repo", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+link.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := newClient(0).Do(request)
	if err != nil {
		return "", errors.New("could not reach the brain: fetch failed")
	}
	defer func() { _ = response.Body.Close() }()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return "", errors.New("could not reach the brain: fetch failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		cut := units(string(text))
		return "", fmt.Errorf("the brain refused the ingest: %d %s", response.StatusCode, string(utf16.Decode(cut[:min(len(cut), 200)])))
	}
	value, err := jsonjs.Parse(text)
	if err != nil {
		return "", fmt.Errorf("could not reach the brain: %w", err)
	}
	job, present := objectGet(asObject(value), "job_id")
	if !present || job == nil {
		return "", nil
	}
	return jsonjs.String(job), nil
}

// ExpectedRepo is the repository a brain is waiting for.
type ExpectedRepo struct {
	Slug      string
	Status    string
	BrainName string
}

// FetchExpectedRepo returns what the brain expects, or false on any failure.
func FetchExpectedRepo(ctx context.Context, link Link) (ExpectedRepo, bool) {
	value, err := getJSON(ctx, link, "/api/public/brains/"+encodeURIComponent(link.BrainID)+"/repo")
	if err != nil {
		return ExpectedRepo{}, false
	}
	body := asObject(value)
	repo := asObject(objectValue(body, "repo"))
	slug, present := objectGet(repo, "slug")
	if !jsonjs.Truthy(slug, present) {
		return ExpectedRepo{}, false
	}
	status, _ := objectGet(repo, "status")
	name, _ := objectGet(body, "brain_name")
	return ExpectedRepo{Slug: jsonjs.String(slug), Status: jsString(status), BrainName: jsString(name)}, true
}

// jsString is String(value ?? "").
func jsString(value jsonjs.Value) string {
	if value == nil {
		return ""
	}
	return jsonjs.String(value)
}

// SameRepo compares repo slugs case-insensitively.
func SameRepo(a, b string) bool {
	return strings.EqualFold(trimJS(a), trimJS(b))
}
