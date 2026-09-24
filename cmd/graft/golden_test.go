package main

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

type goldenCase struct {
	Args          []string          `json:"args"`
	CWD           string            `json:"cwd"`
	Root          string            `json:"root"`
	Env           map[string]string `json:"env"`
	Tracked       []string          `json:"tracked"`
	Dirs          []string          `json:"dirs"`
	Setup         []goldenCommand   `json:"setup"`
	InitialInputs map[string]string `json:"initialInputs"`
	Inputs        map[string]string `json:"inputs"`
	Stdin         any               `json:"stdin,omitempty"`
	Messages      []any             `json:"messages"`
	Steps         []goldenStep      `json:"steps"`
	NormalizeMS   bool              `json:"normalizeMS,omitempty"`
	Status        int               `json:"status"`
	Stdout        string            `json:"stdout"`
	Stderr        string            `json:"stderr"`
	Files         map[string]string `json:"files"`
	CheckFiles    bool              `json:"checkFiles"`
	GitDirs       []string          `json:"gitDirs"`
	GitCommands   []goldenCommand   `json:"gitCommands"`
	GitEnv        map[string]string `json:"gitEnv"`
	Mutations     goldenMutation    `json:"mutations"`
}

type goldenCommand struct {
	Args     []string          `json:"args"`
	CWD      string            `json:"cwd"`
	Writes   map[string]string `json:"writes,omitempty"`
	AfterGit bool              `json:"afterGit,omitempty"`
}

type goldenStep struct {
	Send  any               `json:"send"`
	Reply bool              `json:"reply"`
	ID    int               `json:"id,omitempty"`
	Edit  map[string]string `json:"edit,omitempty"`
}

type goldenMutation struct {
	Writes  map[string]string `json:"writes"`
	Deletes []string          `json:"deletes"`
}

func decodeGolden(data []byte) (goldenCase, error) {
	var golden goldenCase
	if err := json.Unmarshal(data, &golden); err != nil {
		return golden, err
	}
	return golden, nil
}

func loadGolden(t *testing.T, name string) goldenCase {
	t.Helper()
	path := filepath.Join("testdata", "goldens", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v, want nil", path, err)
	}
	golden, err := decodeGolden(data)
	if err != nil {
		t.Fatalf("decodeGolden(%q) error = %v, want nil", path, err)
	}
	return golden
}

func TestDecodeGolden(t *testing.T) {
	t.Parallel()
	data := []byte(`{"args":["build","{{ROOT}}"],"cwd":"<REPO>/src","env":{"GRAFT_NO_REFRESH":"1"},"tracked":["src/a.ts"],"dirs":["repo","home"],"setup":[{"args":["build"],"cwd":"<REPO>"}],"gitDirs":[".","packages/core"],"gitCommands":[{"args":["add","-A"],"writes":{"repo/src/a.ts":"changed\n"}}],"gitEnv":{"GIT_AUTHOR_DATE":"2000-01-01T00:00:00Z"},"initialInputs":{"src/a.ts":"before\n"},"inputs":{"src/a.ts":"export function a() {}\n"},"messages":[{"jsonrpc":"2.0","id":1}],"steps":[{"send":{"jsonrpc":"2.0","id":1},"reply":true,"id":1}],"mutations":{"writes":{"repo/src/a.ts":"changed\n"},"deletes":["repo/old.ts"]},"status":1,"stdout":"out","stderr":"err","files":{"graft/.graph/wiring.json":"{}\n"},"checkFiles":true}`)
	want := goldenCase{
		Args:          []string{"build", "{{ROOT}}"},
		CWD:           "<REPO>/src",
		Env:           map[string]string{"GRAFT_NO_REFRESH": "1"},
		Tracked:       []string{"src/a.ts"},
		Dirs:          []string{"repo", "home"},
		Setup:         []goldenCommand{{Args: []string{"build"}, CWD: "<REPO>"}},
		InitialInputs: map[string]string{"src/a.ts": "before\n"},
		Inputs:        map[string]string{"src/a.ts": "export function a() {}\n"},
		Messages:      []any{map[string]any{"jsonrpc": "2.0", "id": float64(1)}},
		Steps:         []goldenStep{{Send: map[string]any{"jsonrpc": "2.0", "id": float64(1)}, Reply: true, ID: 1}},
		Status:        1,
		Stdout:        "out",
		Stderr:        "err",
		Files:         map[string]string{"graft/.graph/wiring.json": "{}\n"},
		CheckFiles:    true,
		GitDirs:       []string{".", "packages/core"},
		GitCommands:   []goldenCommand{{Args: []string{"add", "-A"}, Writes: map[string]string{"repo/src/a.ts": "changed\n"}}},
		GitEnv:        map[string]string{"GIT_AUTHOR_DATE": "2000-01-01T00:00:00Z"},
		Mutations: goldenMutation{
			Writes:  map[string]string{"repo/src/a.ts": "changed\n"},
			Deletes: []string{"repo/old.ts"},
		},
	}

	got, err := decodeGolden(data)
	if err != nil {
		t.Fatalf("decodeGolden(%q) error = %v, want nil", data, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeGolden(%q) = %#v, want %#v", data, got, want)
	}
}

func TestBuildGoldensMatchGo(t *testing.T) {
	names := []string{
		"build-go-parity/ts-js-cold",
		"build-go-parity/ts-js-incremental",
		"build-go-parity/python-go-cold",
		"build-go-parity/python-go-incremental",
		"build-go-parity/rust-cold",
		"build-go-parity/rust-incremental",
		"build-go-parity/c-cpp-cold",
		"build-go-parity/c-cpp-incremental",
		"build-go-parity/java-cold",
		"build-go-parity/java-incremental",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			golden := loadGolden(t, name)
			root := t.TempDir()
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("GRAFT_DIR", "")
			t.Setenv("GRAFT_NO_REFRESH", "1")
			for key, value := range golden.Env {
				t.Setenv(key, value)
			}
			normalize := func(value string) string {
				if cwd, err := os.Getwd(); err == nil {
					if rel, err := filepath.Rel(cwd, filepath.Join(root, "graft")); err == nil {
						value = strings.ReplaceAll(value, filepath.ToSlash(rel), "{{ROOT}}/graft")
					}
				}
				return strings.ReplaceAll(value, root, "{{ROOT}}")
			}

			initialInputs := golden.Inputs
			if golden.InitialInputs != nil {
				initialInputs = golden.InitialInputs
			}
			for name, content := range initialInputs {
				writeCheckSource(t, root, filepath.FromSlash(name), content)
			}
			git := exec.Command("git", "init", "-q")
			git.Dir = root
			if output, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git init -q in %q error = %v, output = %q", root, err, output)
			}

			args := make([]string, len(golden.Args))
			for i, arg := range golden.Args {
				args[i] = strings.ReplaceAll(arg, "{{ROOT}}", root)
			}
			if golden.InitialInputs != nil {
				var stdout, stderr bytes.Buffer
				if status := run(args, &stdout, &stderr); status != 0 {
					t.Fatalf("run(%v) warmup status = %d, want 0; stderr = %q", args, status, stderr.String())
				}
				for name := range golden.InitialInputs {
					if _, ok := golden.Inputs[name]; ok {
						continue
					}
					path := filepath.Join(root, filepath.FromSlash(name))
					if err := os.Remove(path); err != nil {
						t.Fatalf("os.Remove(%q) error = %v, want nil", path, err)
					}
				}
				for name, content := range golden.Inputs {
					writeCheckSource(t, root, filepath.FromSlash(name), content)
				}
			}

			var stdout, stderr bytes.Buffer
			if status := run(args, &stdout, &stderr); status != golden.Status {
				t.Errorf("run(%v) status = %d, want %d; stderr = %q", args, status, golden.Status, stderr.String())
			}
			if got := normalize(stdout.String()); got != golden.Stdout {
				t.Errorf("run(%v) stdout = %q, want %q", args, got, golden.Stdout)
			}
			if got := normalize(stderr.String()); got != golden.Stderr {
				t.Errorf("run(%v) stderr = %q, want %q", args, got, golden.Stderr)
			}
			for name, want := range golden.Files {
				path := filepath.Join(root, filepath.FromSlash(name))
				got, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("os.ReadFile(%q) error = %v, want nil", path, err)
					continue
				}
				if value := normalize(string(got)); value != want {
					t.Errorf("run(%v) file %q = %q, want %q", args, name, value, want)
				}
			}
		})
	}
}

type cliGraphNode struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type cliGraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type cliGraph struct {
	Nodes []cliGraphNode `json:"nodes"`
	Edges []cliGraphEdge `json:"edges"`
}

func copyCLIFixture(t *testing.T, root, name string) {
	t.Helper()
	sourceRoot := filepath.Join("testdata", "cli-go-acceptance", name)
	var copyDir func(string, string)
	copyDir = func(source, rel string) {
		entries, err := os.ReadDir(source)
		if err != nil {
			t.Fatalf("os.ReadDir(%q) error = %v, want nil", source, err)
		}
		for _, entry := range entries {
			childRel := filepath.Join(rel, entry.Name())
			child := filepath.Join(source, entry.Name())
			if entry.IsDir() {
				copyDir(child, childRel)
				continue
			}
			data, err := os.ReadFile(child)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) error = %v, want nil", child, err)
			}
			writeCheckSource(t, root, childRel, string(data))
		}
	}
	copyDir(sourceRoot, "")
}

func readCLIGraph(t *testing.T, root string) cliGraph {
	t.Helper()
	path := filepath.Join(root, "graft", ".graph", "wiring.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v, want nil", path, err)
	}
	var graph cliGraph
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v, want nil", path, err)
	}
	return graph
}

func runGoldenCLI(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := run(args, &stdout, &stderr)
	return status, stdout.String(), stderr.String()
}

func TestCLIAcceptanceSQL(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	copyCLIFixture(t, root, "sql")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GRAFT_DIR", "")
	t.Setenv("GRAFT_NO_REFRESH", "1")
	initGoldenGit(t, root, []string{"."}, nil)
	t.Chdir(root)

	args := []string{"build"}
	if status, _, stderr := runGoldenCLI(t, args); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr)
	}
	graph := readCLIGraph(t, root)
	byID := make(map[string]string, len(graph.Nodes))
	for _, node := range graph.Nodes {
		byID[node.ID] = node.Kind
	}
	for _, item := range []struct{ id, kind string }{
		{"db/schema.sql#billing.status", "type"},
		{"db/schema.sql#billing.customers", "type"},
		{"db/schema.sql#billing.invoices", "type"},
		{"db/schema.sql#billing.open_invoices", "type"},
		{"db/schema.sql#billing.total_due", "function"},
		{"db/schema.sql#billing.close_all", "function"},
		{"db/seed.sql", "file"},
	} {
		if got := byID[item.id]; got != item.kind {
			t.Errorf("build node %q kind = %q, want %q", item.id, got, item.kind)
		}
	}
	seedFiles := 0
	for _, node := range graph.Nodes {
		if node.Path == "db/seed.sql" {
			seedFiles++
		}
	}
	if seedFiles != 1 {
		t.Errorf("build db/seed.sql file nodes = %d, want 1", seedFiles)
	}
	hasEdge := func(source, target string) bool {
		for _, edge := range graph.Edges {
			if edge.Source == source && edge.Target == target {
				return true
			}
		}
		return false
	}
	for _, edge := range []struct{ source, target string }{
		{"db/schema.sql#billing.invoices", "db/schema.sql#billing.customers"},
		{"db/schema.sql#billing.open_invoices", "db/schema.sql#billing.invoices"},
		{"db/schema.sql#billing.open_invoices", "db/schema.sql#billing.customers"},
	} {
		if !hasEdge(edge.source, edge.target) {
			t.Errorf("build edge %q → %q missing", edge.source, edge.target)
		}
	}

	before, err := os.ReadFile(filepath.Join(root, "graft", ".graph", "wiring.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"build"}, {"build", "--no-reuse"}} {
		if status, _, stderr := runGoldenCLI(t, args); status != 0 {
			t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr)
		}
		got, err := os.ReadFile(filepath.Join(root, "graft", ".graph", "wiring.json"))
		if err != nil || !bytes.Equal(got, before) {
			t.Errorf("run(%v) wiring changed; read error = %v", args, err)
		}
	}
	for _, item := range []struct {
		args []string
		want string
	}{
		{[]string{"callers", "billing.customers"}, "billing.invoices"},
		{[]string{"skeleton", "db/schema.sql"}, "CREATE TABLE billing.customers"},
		{[]string{"skeleton", "db/schema.sql"}, "CREATE FUNCTION billing.total_due"},
		{[]string{"ask", "open invoices", "--json"}, "db/schema.sql:"},
		{[]string{"grep", "REFERENCES"}, "billing.invoices"},
	} {
		status, stdout, stderr := runGoldenCLI(t, item.args)
		if status != 0 || !strings.Contains(stdout, item.want) {
			t.Errorf("run(%v) = (%d, %q, %q), want stdout containing %q", item.args, status, stdout, stderr, item.want)
		}
	}
	if status, stdout, stderr := runGoldenCLI(t, []string{"check"}); status != 0 {
		t.Errorf("run(check) status = %d, want 0; stdout = %q; stderr = %q", status, stdout, stderr)
	}

	writeCheckSource(t, root, filepath.Join("db", "schema.sql"), "CREATE TABLE billing.broken (id int REFERENCES;\n")
	status, _, _ := runGoldenCLI(t, []string{"build"})
	after, err := os.ReadFile(filepath.Join(root, "graft", ".graph", "wiring.json"))
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 && !bytes.Equal(after, before) {
		t.Errorf("failed SQL build changed the previous graph")
	}
	if status == 0 && bytes.Contains(after, []byte("billing.customers")) {
		t.Errorf("successful SQL rebuild retained the replaced customer table")
	}
}

func TestCLIAcceptanceExcludedLanguages(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	copyCLIFixture(t, root, "excluded")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GRAFT_DIR", "")
	t.Setenv("GRAFT_NO_REFRESH", "1")
	initGoldenGit(t, root, []string{"."}, nil)
	t.Chdir(root)

	args := []string{"build"}
	if status, _, stderr := runGoldenCLI(t, args); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr)
	}
	graph := readCLIGraph(t, root)
	paths := make(map[string]struct{})
	for _, node := range graph.Nodes {
		paths[node.Path] = struct{}{}
	}
	if len(paths) != 1 {
		t.Fatalf("build paths = %#v, want only src/main.ts", paths)
	}
	if _, ok := paths["src/main.ts"]; !ok {
		t.Errorf("build paths = %#v, want src/main.ts", paths)
	}
	for _, edge := range graph.Edges {
		if strings.Contains(edge.Source+edge.Target, ".rb#") || strings.Contains(edge.Source+edge.Target, ".kt#") || strings.Contains(edge.Source+edge.Target, ".php#") || strings.Contains(edge.Source+edge.Target, ".cs#") {
			t.Errorf("build edge = %#v, touches an excluded language", edge)
		}
	}
	if status, _, stderr := runGoldenCLI(t, []string{"check"}); status != 0 {
		t.Errorf("run(check) status = %d, want 0; stderr = %q", status, stderr)
	}
	if _, stdout, _ := runGoldenCLI(t, []string{"grep", "render"}); strings.Contains(stdout, "widget.rb") {
		t.Errorf("run(grep) stdout = %q, contains an excluded Ruby file", stdout)
	}
	if _, stdout, _ := runGoldenCLI(t, []string{"skeleton", "lib/widget.rb"}); !strings.Contains(stdout, "not in the graph") && !strings.Contains(stdout, "no match") && !strings.Contains(stdout, "no definitions") {
		t.Errorf("run(skeleton) stdout = %q, want a missing-file note", stdout)
	}
	before, err := os.ReadFile(filepath.Join(root, "graft", ".graph", "wiring.json"))
	if err != nil {
		t.Fatal(err)
	}
	explicit := []string{"build", "-e", ".rb"}
	status, _, stderr := runGoldenCLI(t, explicit)
	if status == 0 || (!strings.Contains(stderr, "unsupported") && !strings.Contains(stderr, "widget.rb")) {
		t.Errorf("run(%v) = (%d, %q), want an unsupported-language error", explicit, status, stderr)
	}
	after, err := os.ReadFile(filepath.Join(root, "graft", ".graph", "wiring.json"))
	if err != nil || !bytes.Equal(after, before) {
		t.Errorf("run(%v) changed the previous graph; read error = %v", explicit, err)
	}
}

func TestSeededQueryGoldensMatchGo(t *testing.T) {
	base := t.TempDir()
	repo, elsewhere := filepath.Join(base, "repo"), filepath.Join(base, "elsewhere")
	home := filepath.Join(repo, "home")
	for _, dir := range []string{repo, home, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v, want nil", dir, err)
		}
	}
	copyCLIFixture(t, repo, "real")
	writeCheckSource(t, repo, "package.json", `{"name":"real"}`)
	writeCheckSource(t, repo, "go.mod", "module example.com/real\n\ngo 1.22\n")
	initGoldenGit(t, repo, []string{"."}, nil)
	graphFixture := loadGolden(t, "cli-go-acceptance/seeded-graph")
	for rel, content := range graphFixture.Files {
		writeCheckSource(t, repo, filepath.FromSlash(rel), materializeGolden(content, base, repo, home, elsewhere))
	}

	for _, name := range cliGoldenNames(t, "seeded") {
		t.Run(filepath.Base(name), func(t *testing.T) {
			golden := loadGolden(t, name)
			setGoldenEnvironment(t, golden.Env, base, repo, home, elsewhere)
			cwd := materializeGolden(golden.CWD, base, repo, home, elsewhere)
			if cwd == "" {
				cwd = repo
			}
			t.Chdir(cwd)
			args := make([]string, len(golden.Args))
			for i, arg := range golden.Args {
				args[i] = materializeGolden(arg, base, repo, home, elsewhere)
			}
			status, stdout, stderr := runGoldenCLI(t, args)
			if status != golden.Status {
				t.Errorf("run(%v) status = %d, want %d; stderr = %q", args, status, golden.Status, stderr)
			}
			if got := normalizeGoldenText(stdout, base, repo, home, elsewhere); got != golden.Stdout {
				t.Errorf("run(%v) stdout = %q, want %q", args, got, golden.Stdout)
			}
			if got := normalizeGoldenText(stderr, base, repo, home, elsewhere); got != golden.Stderr {
				t.Errorf("run(%v) stderr = %q, want %q", args, got, golden.Stderr)
			}
		})
	}
}

var goldenDuration = regexp.MustCompile(`\b\d+(\.\d+)?\s?(ms|s)\b`)
var goldenTime = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z`)
var goldenCheckedAt = regexp.MustCompile(`"checkedAt":\s*\d+`)

func normalizeGoldenText(value, base, repo, home, elsewhere string) string {
	value = strings.ReplaceAll(value, elsewhere, "<ELSEWHERE>")
	value = strings.ReplaceAll(value, repo, "<REPO>")
	value = strings.ReplaceAll(value, home, "<HOME>")
	value = strings.ReplaceAll(value, base, "<BASE>")
	value = goldenDuration.ReplaceAllString(value, "<DURATION>")
	value = goldenTime.ReplaceAllString(value, "<TIME>")
	return goldenCheckedAt.ReplaceAllString(value, `"checkedAt":<TIME>`)
}

func materializeGolden(value, base, repo, home, elsewhere string) string {
	value = strings.ReplaceAll(value, "<ELSEWHERE>", elsewhere)
	value = strings.ReplaceAll(value, "<REPO>", repo)
	value = strings.ReplaceAll(value, "<HOME>", home)
	value = strings.ReplaceAll(value, "<WORKTREE>", filepath.Join(repo, "..", "worktree"))
	return strings.ReplaceAll(value, "<BASE>", base)
}

func cliGoldenNames(t *testing.T, suite string) []string {
	t.Helper()
	dir := filepath.Join("cli-go-acceptance", suite)
	paths, err := filepath.Glob(filepath.Join("testdata", "goldens", dir, "*.json"))
	if err != nil {
		t.Fatalf("filepath.Glob(%q) error = %v, want nil", dir, err)
	}
	if len(paths) == 0 {
		t.Fatalf("filepath.Glob(%q) found no CLI goldens", dir)
	}
	names := make([]string, len(paths))
	for i, path := range paths {
		rel, err := filepath.Rel(filepath.Join("testdata", "goldens"), path)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q) error = %v, want nil", filepath.Join("testdata", "goldens"), path, err)
		}
		names[i] = strings.TrimSuffix(filepath.ToSlash(rel), ".json")
	}
	return names
}

func initGoldenGit(t *testing.T, repo string, gitDirs, tracked []string) {
	t.Helper()
	for _, rel := range gitDirs {
		root := repo
		if rel != "." {
			root = filepath.Join(repo, filepath.FromSlash(rel))
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v, want nil", root, err)
		}
		command := exec.Command("git", "init", "-q")
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init -q in %q error = %v, output = %q", root, err, output)
		}
	}
	if len(tracked) == 0 {
		return
	}
	gitArgs := []string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}
	add := exec.Command("git", append(append(gitArgs, "add", "--"), tracked...)...)
	add.Dir = repo
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add in %q error = %v, output = %q", repo, err, output)
	}
	commit := exec.Command("git", append(gitArgs, "commit", "-qm", "init")...)
	commit.Dir = repo
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit in %q error = %v, output = %q", repo, err, output)
	}
}

func applyGoldenMutations(t *testing.T, base, repo, home, elsewhere string, mutations goldenMutation) {
	t.Helper()
	for _, rel := range mutations.Deletes {
		path := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.Remove(path); err != nil {
			t.Fatalf("os.Remove(%q) error = %v, want nil", path, err)
		}
	}
	for rel, content := range mutations.Writes {
		writeCheckSource(t, base, filepath.FromSlash(rel), materializeGolden(content, base, repo, home, elsewhere))
	}
}

func setGoldenEnvironment(t *testing.T, env map[string]string, base, repo, home, elsewhere string) {
	t.Helper()
	for _, name := range []string{"GRAFT_DIR", "GRAFT_NO_REFRESH", "CLAUDECODE", "CI", "GITHUB_ACTIONS"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("COLUMNS", "80")
	for name, value := range env {
		t.Setenv(name, materializeGolden(value, base, repo, home, elsewhere))
	}
}

func snapshotGolden(t *testing.T, root, base, repo, home, elsewhere string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	var walk func(string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("os.ReadDir(%q) error = %v, want nil", dir, err)
		}
		for _, entry := range entries {
			if entry.Name() == ".git" && entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				walk(path)
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatalf("filepath.Rel(%q, %q) error = %v, want nil", root, path, err)
			}
			rel = filepath.ToSlash(rel)
			if goldenPrivateCache(rel) {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) error = %v, want nil", path, err)
			}
			files[rel] = normalizeGoldenText(string(data), base, repo, home, elsewhere)
		}
	}
	walk(root)
	return files
}

func goldenPrivateCache(rel string) bool {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if part != ".cache" || i+1 == len(parts) {
			continue
		}
		name := parts[i+1]
		if strings.HasSuffix(name, ".json") && (strings.HasPrefix(name, "extract.") || strings.HasPrefix(name, "fingerprint.")) {
			return true
		}
	}
	return false
}

func TestCLIAcceptanceGoldensMatchGo(t *testing.T) {
	for _, suite := range []string{"main", "monorepo", "workspace"} {
		t.Run(suite, func(t *testing.T) {
			names := cliGoldenNames(t, suite)
			fixture := loadGolden(t, names[0])
			base := t.TempDir()
			repo, home, elsewhere := filepath.Join(base, "repo"), filepath.Join(base, "home"), filepath.Join(base, "elsewhere")
			for _, dir := range []string{repo, home, elsewhere} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v, want nil", dir, err)
				}
			}
			for rel, content := range fixture.Inputs {
				writeCheckSource(t, repo, filepath.FromSlash(rel), content)
			}
			if err := os.MkdirAll(filepath.Join(home, ".graft"), 0o755); err != nil {
				t.Fatalf("os.MkdirAll(%q) error = %v, want nil", filepath.Join(home, ".graft"), err)
			}
			// The frozen goldens record this legacy update cache among the files
			// they compare; graft itself no longer reads or writes it.
			update := fmt.Sprintf("{\n  \"latest\": \"0.0.0\",\n  \"checkedAt\": %d\n}", time.Now().UnixMilli())
			if err := os.WriteFile(filepath.Join(home, ".graft", "update-check.json"), []byte(update), 0o644); err != nil {
				t.Fatalf("os.WriteFile(update-check.json) error = %v, want nil", err)
			}
			initGoldenGit(t, repo, fixture.GitDirs, fixture.Tracked)

			for _, name := range names {
				t.Run(filepath.Base(name), func(t *testing.T) {
					golden := loadGolden(t, name)
					applyGoldenMutations(t, base, repo, home, elsewhere, golden.Mutations)
					setGoldenEnvironment(t, golden.Env, base, repo, home, elsewhere)
					cwd := materializeGolden(golden.CWD, base, repo, home, elsewhere)
					if cwd == "" {
						cwd = repo
					}
					t.Chdir(cwd)
					args := make([]string, len(golden.Args))
					for i, arg := range golden.Args {
						args[i] = materializeGolden(arg, base, repo, home, elsewhere)
					}
					var stdout, stderr bytes.Buffer
					status := run(args, &stdout, &stderr)
					if status != golden.Status {
						t.Errorf("run(%v) status = %d, want %d; stderr = %q", args, status, golden.Status, stderr.String())
					}
					if got := normalizeGoldenText(stdout.String(), base, repo, home, elsewhere); got != golden.Stdout {
						t.Errorf("run(%v) stdout = %q, want %q", args, got, golden.Stdout)
					}
					if got := normalizeGoldenText(stderr.String(), base, repo, home, elsewhere); got != golden.Stderr {
						t.Errorf("run(%v) stderr = %q, want %q", args, got, golden.Stderr)
					}
					if golden.CheckFiles {
						if got := snapshotGolden(t, base, base, repo, home, elsewhere); !reflect.DeepEqual(got, golden.Files) {
							t.Errorf("run(%v) file snapshot = %#v, want %#v", args, got, golden.Files)
						}
					}
				})
			}
		})
	}
}

func TestPerCommandGoldensMatchGo(t *testing.T) {
	for _, suite := range []string{"grep-cli", "map-cli", "skeleton-cli", "graph-traverse-cli", "ask-cli"} {
		t.Run(suite, func(t *testing.T) {
			pattern := filepath.Join("testdata", "goldens", "per-command", suite, "*.json")
			paths, err := filepath.Glob(pattern)
			if err != nil || len(paths) == 0 {
				t.Fatalf("filepath.Glob(%q) paths = %v, error = %v, want goldens", pattern, paths, err)
			}
			for _, path := range paths {
				rel, err := filepath.Rel(filepath.Join("testdata", "goldens"), path)
				if err != nil {
					t.Fatalf("filepath.Rel(%q, %q) error = %v, want nil", filepath.Join("testdata", "goldens"), path, err)
				}
				name := strings.TrimSuffix(filepath.ToSlash(rel), ".json")
				t.Run(filepath.Base(path), func(t *testing.T) {
					golden := loadGolden(t, name)
					base := t.TempDir()
					repo, home, elsewhere := filepath.Join(base, "repo"), filepath.Join(base, "home"), filepath.Join(base, "elsewhere")
					for _, dir := range []string{repo, home, elsewhere} {
						if err := os.MkdirAll(dir, 0o755); err != nil {
							t.Fatalf("os.MkdirAll(%q) error = %v, want nil", dir, err)
						}
					}
					for rel, content := range golden.Inputs {
						writeCheckSource(t, repo, filepath.FromSlash(rel), materializeGolden(content, base, repo, home, elsewhere))
					}
					setGoldenEnvironment(t, golden.Env, base, repo, home, elsewhere)
					args := make([]string, len(golden.Args))
					for i, arg := range golden.Args {
						args[i] = materializeGolden(arg, base, repo, home, elsewhere)
					}
					status, stdout, stderr := runGoldenCLI(t, args)
					if status != golden.Status {
						t.Errorf("run(%v) status = %d, want %d; stderr = %q", args, status, golden.Status, stderr)
					}
					if got := normalizeGoldenText(stdout, base, repo, home, elsewhere); got != golden.Stdout {
						t.Errorf("run(%v) stdout = %q, want %q", args, got, golden.Stdout)
					}
					if got := normalizeGoldenText(stderr, base, repo, home, elsewhere); got != golden.Stderr {
						t.Errorf("run(%v) stderr = %q, want %q", args, got, golden.Stderr)
					}
				})
			}
		})
	}
}

func TestGraphQualityGoldensMatchGo(t *testing.T) {
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "graph-quality")
	build := exec.Command("go", "build", "-o", binary, "./cmd/graph-quality")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/graph-quality error = %v, output = %q", err, output)
	}
	pattern := filepath.Join("testdata", "goldens", "per-command", "graph-quality-cli", "*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) == 0 {
		t.Fatalf("filepath.Glob(%q) paths = %v, error = %v, want goldens", pattern, paths, err)
	}
	for _, path := range paths {
		rel, err := filepath.Rel(filepath.Join("testdata", "goldens"), path)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q) error = %v, want nil", filepath.Join("testdata", "goldens"), path, err)
		}
		name := strings.TrimSuffix(filepath.ToSlash(rel), ".json")
		t.Run(filepath.Base(path), func(t *testing.T) {
			golden := loadGolden(t, name)
			base := t.TempDir()
			repo, home, elsewhere := filepath.Join(base, "repo"), filepath.Join(base, "home"), filepath.Join(base, "elsewhere")
			for _, dir := range []string{repo, home, elsewhere} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v, want nil", dir, err)
				}
			}
			for rel, content := range golden.Inputs {
				writeCheckSource(t, repo, filepath.FromSlash(rel), materializeGolden(content, base, repo, home, elsewhere))
			}
			if len(golden.Inputs) == 0 {
				if err := os.MkdirAll(filepath.Join(repo, "graft"), 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v, want nil", filepath.Join(repo, "graft"), err)
				}
			}
			setGoldenEnvironment(t, golden.Env, base, repo, home, elsewhere)
			args := make([]string, len(golden.Args))
			for i, arg := range golden.Args {
				args[i] = materializeGolden(arg, base, repo, home, elsewhere)
			}
			command := exec.Command(binary, args...)
			command.Dir = moduleRoot
			command.Env = os.Environ()
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			status := 0
			if err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("exec.Command(%q, %v).Run() error = %v, want exit status", binary, args, err)
				}
				status = exit.ExitCode()
			}
			if status != golden.Status {
				t.Errorf("graph-quality %v status = %d, want %d; stderr = %q", args, status, golden.Status, stderr.String())
			}
			gotStdout := normalizeGoldenText(stdout.String(), base, repo, home, elsewhere)
			jsonOutput := false
			for _, arg := range golden.Args {
				jsonOutput = jsonOutput || arg == "--json"
			}
			if jsonOutput && gotStdout != "" && golden.Stdout != "" {
				var gotJSON, wantJSON any
				if err := json.Unmarshal([]byte(gotStdout), &gotJSON); err != nil {
					t.Errorf("json.Unmarshal(graph-quality stdout) error = %v, want nil", err)
				} else if err := json.Unmarshal([]byte(golden.Stdout), &wantJSON); err != nil {
					t.Errorf("json.Unmarshal(graph-quality golden) error = %v, want nil", err)
				} else if !reflect.DeepEqual(gotJSON, wantJSON) {
					t.Errorf("graph-quality %v JSON = %#v, want %#v", args, gotJSON, wantJSON)
				}
			} else if gotStdout != golden.Stdout {
				t.Errorf("graph-quality %v stdout = %q, want %q", args, gotStdout, golden.Stdout)
			}
			if got := normalizeGoldenText(stderr.String(), base, repo, home, elsewhere); got != golden.Stderr {
				t.Errorf("graph-quality %v stderr = %q, want %q", args, got, golden.Stderr)
			}
		})
	}
}

func perCommandGoldenNames(t *testing.T, suite string) []string {
	t.Helper()
	pattern := filepath.Join("testdata", "goldens", "per-command", suite, "*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) == 0 {
		t.Fatalf("filepath.Glob(%q) paths = %v, error = %v, want goldens", pattern, paths, err)
	}
	names := make([]string, len(paths))
	for i, path := range paths {
		rel, err := filepath.Rel(filepath.Join("testdata", "goldens"), path)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q) error = %v, want nil", filepath.Join("testdata", "goldens"), path, err)
		}
		names[i] = strings.TrimSuffix(filepath.ToSlash(rel), ".json")
	}
	return names
}

func TestBlastGoldensMatchGo(t *testing.T) {
	bundle, err := filepath.Abs(filepath.Join("testdata", "cli-go-acceptance", "blast", "fixture.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("main", func(t *testing.T) {
		base := t.TempDir()
		repo, home, elsewhere := filepath.Join(base, "repo"), filepath.Join(base, "home"), filepath.Join(base, "elsewhere")
		clone := exec.Command("git", "clone", "--quiet", bundle, repo)
		if output, err := clone.CombinedOutput(); err != nil {
			t.Fatalf("git clone %q %q error = %v, output = %q", bundle, repo, err, output)
		}
		fetchBase := exec.Command("git", "fetch", "--quiet", bundle, "refs/heads/base:refs/heads/base")
		fetchBase.Dir = repo
		if output, err := fetchBase.CombinedOutput(); err != nil {
			t.Fatalf("git fetch base in %q error = %v, output = %q", repo, err, output)
		}
		for _, args := range [][]string{{"config", "user.name", "Ann Author"}, {"config", "user.email", "ann@example.com"}} {
			config := exec.Command("git", args...)
			config.Dir = repo
			if output, err := config.CombinedOutput(); err != nil {
				t.Fatalf("git %v in %q error = %v, output = %q", args, repo, err, output)
			}
		}
		storePath := filepath.Join(repo, "src", "core", "store.ts")
		store, err := os.ReadFile(storePath)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v, want nil", storePath, err)
		}
		if err := os.WriteFile(storePath, []byte(strings.Replace(string(store), "console.log(key);", "console.error(key);", 1)), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v, want nil", storePath, err)
		}
		for _, dir := range []string{home, elsewhere} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("os.MkdirAll(%q) error = %v, want nil", dir, err)
			}
		}
		names := perCommandGoldenNames(t, filepath.Join("blast", "main"))
		for i, name := range names {
			if i == 1 {
				setGoldenEnvironment(t, map[string]string{"GRAFT_NO_REFRESH": "1"}, base, repo, home, elsewhere)
				if status, _, stderr := runGoldenCLI(t, []string{"build", repo}); status != 0 {
					t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", repo, status, stderr)
				}
			}
			golden := loadGolden(t, name)
			t.Run(filepath.Base(name), func(t *testing.T) {
				setGoldenEnvironment(t, golden.Env, base, repo, home, elsewhere)
				args := make([]string, len(golden.Args))
				for i, arg := range golden.Args {
					args[i] = materializeGolden(arg, base, repo, home, elsewhere)
				}
				status, stdout, stderr := runGoldenCLI(t, args)
				if status != golden.Status {
					t.Errorf("run(%v) status = %d, want %d; stderr = %q", args, status, golden.Status, stderr)
				}
				if got := normalizeGoldenText(stdout, base, repo, home, elsewhere); got != golden.Stdout {
					t.Errorf("run(%v) stdout = %q, want %q", args, got, golden.Stdout)
				}
				if got := normalizeGoldenText(stderr, base, repo, home, elsewhere); got != golden.Stderr {
					t.Errorf("run(%v) stderr = %q, want %q", args, got, golden.Stderr)
				}
			})
		}
	})
}

func TestBlastNoGitGoldenMatchGo(t *testing.T) {
	golden := loadGolden(t, "per-command/blast/no-git/001")
	base := t.TempDir()
	repo, home, elsewhere := filepath.Join(base, "repo"), filepath.Join(base, "home"), filepath.Join(base, "elsewhere")
	for _, dir := range []string{repo, home, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v, want nil", dir, err)
		}
	}
	for rel, content := range golden.Inputs {
		writeCheckSource(t, repo, filepath.FromSlash(rel), materializeGolden(content, base, repo, home, elsewhere))
	}
	setGoldenEnvironment(t, golden.Env, base, repo, home, elsewhere)
	args := make([]string, len(golden.Args))
	for i, arg := range golden.Args {
		args[i] = materializeGolden(arg, base, repo, home, elsewhere)
	}
	status, stdout, stderr := runGoldenCLI(t, args)
	if status != golden.Status {
		t.Errorf("run(%v) status = %d, want %d; stderr = %q", args, status, golden.Status, stderr)
	}
	if got := normalizeGoldenText(stdout, base, repo, home, elsewhere); got != golden.Stdout {
		t.Errorf("run(%v) stdout = %q, want %q", args, got, golden.Stdout)
	}
	if got := normalizeGoldenText(stderr, base, repo, home, elsewhere); got != golden.Stderr {
		t.Errorf("run(%v) stderr = %q, want %q", args, got, golden.Stderr)
	}
}
