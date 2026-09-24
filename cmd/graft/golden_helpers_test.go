package main

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

type goldenRuntime struct {
	base        string
	repo        string
	home        string
	elsewhere   string
	root        string
	normalizeMS bool
}

func newGoldenRuntime(t *testing.T, golden goldenCase) goldenRuntime {
	t.Helper()
	base := t.TempDir()
	runtime := goldenRuntime{
		base:        base,
		repo:        filepath.Join(base, "repo"),
		home:        filepath.Join(base, "home"),
		elsewhere:   filepath.Join(base, "elsewhere"),
		normalizeMS: golden.NormalizeMS,
	}
	for _, rel := range golden.Dirs {
		path := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v, want nil", path, err)
		}
	}
	for rel, content := range golden.InitialInputs {
		writeGoldenInput(t, base, rel, content)
	}
	for rel, content := range golden.Inputs {
		writeGoldenInput(t, base, rel, content)
	}
	for _, dir := range []string{"repo", "home", "elsewhere"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v, want nil", dir, err)
		}
	}
	if _, err := os.Stat(runtime.bin()); err == nil {
		if err := os.Chmod(runtime.bin(), 0o755); err != nil {
			t.Fatalf("os.Chmod(%q) error = %v, want nil", runtime.bin(), err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(runtime.bin()), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(bin) error = %v, want nil", err)
	}
	for _, dir := range golden.GitDirs {
		root := runtime.repo
		if dir != "repo" {
			root = filepath.Join(base, filepath.FromSlash(dir))
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v, want nil", root, err)
		}
		command := exec.Command("git", "init", "-q")
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init in %q error = %v, output = %q", root, err, output)
		}
	}
	runtime.setEnvironment(t, golden.Env)
	runGoldenSetup(t, &runtime, golden.Setup, false)
	for _, commandCase := range golden.GitCommands {
		for rel, content := range commandCase.Writes {
			writeGoldenInput(t, base, rel, content)
		}
		if len(commandCase.Args) == 0 {
			continue
		}
		args := make([]string, len(commandCase.Args))
		for i, arg := range commandCase.Args {
			args[i] = materializeGolden(arg, base, runtime.repo, runtime.home, runtime.elsewhere)
		}
		command := exec.Command("git", args...)
		command.Dir = runtime.repo
		command.Env = append(os.Environ(), "HOME="+runtime.home, "USERPROFILE="+runtime.home, "GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.com", "GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.com")
		for key, value := range golden.GitEnv {
			command.Env = append(command.Env, key+"="+value)
		}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v error = %v, output = %q", args, err, output)
		}
	}
	runGoldenSetup(t, &runtime, golden.Setup, true)
	runtime.root = runtime.repo
	if golden.Root != "" {
		runtime.root = materializeGolden(golden.Root, base, runtime.repo, runtime.home, runtime.elsewhere)
	}
	return runtime
}

func (runtime goldenRuntime) bin() string {
	return filepath.Join(runtime.base, "bin", "gh")
}

func writeGoldenInput(t *testing.T, base, rel, content string) {
	t.Helper()
	path := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v, want nil", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", path, err)
	}
}

func runGoldenSetup(t *testing.T, runtime *goldenRuntime, commands []goldenCommand, afterGit bool) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v, want nil", err)
	}
	defer t.Chdir(original)
	for _, commandCase := range commands {
		if commandCase.AfterGit != afterGit {
			continue
		}
		for rel, content := range commandCase.Writes {
			writeGoldenInput(t, runtime.base, rel, content)
		}
		args := make([]string, len(commandCase.Args))
		for i, arg := range commandCase.Args {
			args[i] = materializeGolden(arg, runtime.base, runtime.repo, runtime.home, runtime.elsewhere)
		}
		cwd := runtime.repo
		if commandCase.CWD != "" {
			cwd = materializeGolden(commandCase.CWD, runtime.base, runtime.repo, runtime.home, runtime.elsewhere)
		}
		t.Chdir(cwd)
		var stdout, stderr bytes.Buffer
		if status := run(args, &stdout, &stderr); status != 0 {
			t.Fatalf("setup %v status = %d, want 0; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
		}
	}
}

func (runtime *goldenRuntime) setEnvironment(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "CLAUDECODE", "GRAFT_DIR", "GRAFT_NO_REFRESH", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", runtime.home)
	t.Setenv("USERPROFILE", runtime.home)
	t.Setenv("COLUMNS", "80")
	if err := os.Unsetenv("CLAUDECODE"); err != nil {
		t.Fatalf("os.Unsetenv(CLAUDECODE) error = %v, want nil", err)
	}
	t.Setenv("GRAFT_MCP_COMMAND", "graft")
	if runtime.bin() != "" {
		if _, err := os.Stat(runtime.bin()); err == nil {
			t.Setenv("PATH", runtime.bin()+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	for name, value := range env {
		t.Setenv(name, materializeGolden(value, runtime.base, runtime.repo, runtime.home, runtime.elsewhere))
	}
}

func (runtime goldenRuntime) normalize(value string) string {
	if runtime.root != runtime.repo {
		value = strings.ReplaceAll(value, runtime.root, "<WORKTREE>")
	}
	if runtime.root != runtime.repo {
		if main, err := filepath.EvalSymlinks(runtime.repo); err == nil {
			value = strings.ReplaceAll(value, main, "<MAIN>")
		}
	}
	value = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`).ReplaceAllString(value, "<ISO>")
	value = normalizeGoldenText(value, runtime.base, runtime.repo, runtime.home, runtime.elsewhere)
	value = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`).ReplaceAllString(value, "<UUID>")
	value = regexp.MustCompile(`"pid":\s*\d+`).ReplaceAllString(value, `"pid":<PID>`)
	if runtime.normalizeMS {
		value = regexp.MustCompile(`"checkedAt":\s*(?:<TIME>|\d+)`).ReplaceAllString(value, `"checkedAt":<MS>`)
	} else {
		value = regexp.MustCompile(`"checkedAt":<TIME>`).ReplaceAllString(value, `"checkedAt": 4102444800000`)
	}
	value = regexp.MustCompile(`const BAKED = ".*";`).ReplaceAllString(value, `const BAKED = "<BAKED>";`)
	if !runtime.normalizeMS {
		value = regexp.MustCompile(`"at": "[^"]+"`).ReplaceAllString(value, `"at": "<AT>"`)
	}
	return value
}

func (runtime goldenRuntime) files(t *testing.T, withModes bool) map[string]string {
	t.Helper()
	files := make(map[string]string)
	for _, root := range []struct {
		path string
		name string
	}{{runtime.root, "repo"}, {runtime.home, "home"}, {runtime.elsewhere, "elsewhere"}} {
		if err := filepath.WalkDir(root.path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Name() == ".git" {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root.path, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if goldenPrivateCache(rel) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			key := root.name + "/" + rel
			if withModes {
				info, err := os.Stat(path)
				if err != nil {
					return err
				}
				key += fmt.Sprintf(" [%o]", info.Mode().Perm())
			}
			files[key] = runtime.normalize(string(data))
			return nil
		}); err != nil {
			t.Fatalf("filepath.WalkDir(%q) error = %v, want nil", root.path, err)
		}
	}
	return files
}

func (runtime *goldenRuntime) runCLI(t *testing.T, golden goldenCase) {
	t.Helper()
	runtime.setEnvironment(t, golden.Env)
	t.Chdir(runtime.repo)
	applyGoldenMutations(t, runtime.base, runtime.repo, runtime.home, runtime.elsewhere, golden.Mutations)
	args := make([]string, len(golden.Args))
	for i, arg := range golden.Args {
		args[i] = materializeGolden(arg, runtime.base, runtime.repo, runtime.home, runtime.elsewhere)
	}
	var status int
	var stdout, stderr bytes.Buffer
	if golden.Stdin == nil {
		status = runWithInput(args, strings.NewReader(""), &stdout, &stderr)
	} else {
		input := materializeGoldenValue(golden.Stdin, runtime.base, runtime.repo, runtime.home, runtime.elsewhere)
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("json.Marshal(stdin) error = %v, want nil", err)
		}
		status = runWithInput(args, bytes.NewReader(data), &stdout, &stderr)
	}
	if status != golden.Status {
		t.Errorf("run(%v) status = %d, want %d; stderr = %q", args, status, golden.Status, stderr.String())
	}
	if got := runtime.normalize(stdout.String()); got != golden.Stdout {
		t.Errorf("run(%v) stdout = %q, want %q", args, got, golden.Stdout)
	}
	if got := runtime.normalize(stderr.String()); got != golden.Stderr {
		t.Errorf("run(%v) stderr = %q, want %q", args, got, golden.Stderr)
	}
	if golden.CheckFiles {
		compareGoldenFiles(t, runtime.files(t, hasFileModes(golden.Files)), golden.Files)
	}
}

func materializeGoldenValue(value any, base, repo, home, elsewhere string) any {
	switch value := value.(type) {
	case string:
		return materializeGolden(value, base, repo, home, elsewhere)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = materializeGoldenValue(item, base, repo, home, elsewhere)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = materializeGoldenValue(item, base, repo, home, elsewhere)
		}
		return out
	default:
		return value
	}
}

func hasFileModes(files map[string]string) bool {
	for name := range files {
		if strings.Contains(name, " [") && strings.HasSuffix(name, "]") {
			return true
		}
	}
	return false
}

func compareGoldenFiles(t *testing.T, got, want map[string]string) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	keys := make([]string, 0, len(got)+len(want))
	for key := range got {
		keys = append(keys, key)
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		gotValue, gotOK := got[key]
		wantValue, wantOK := want[key]
		if gotOK != wantOK || gotValue != wantValue {
			t.Errorf("file %q = %q, want %q", key, truncateGolden(gotValue), truncateGolden(wantValue))
			if len(keys) > 8 {
				break
			}
		}
	}
}

func truncateGolden(value string) string {
	if len(value) > 240 {
		return value[:240] + "..."
	}
	return value
}

var goldenServerVersion = regexp.MustCompile(`"version":"[^"]*"`)

func runGoldenMCP(t *testing.T, runtime *goldenRuntime, golden goldenCase) {
	t.Helper()
	runtime.setEnvironment(t, golden.Env)
	t.Chdir(runtime.repo)
	applyGoldenMutations(t, runtime.base, runtime.repo, runtime.home, runtime.elsewhere, golden.Mutations)
	replies := make(map[int]any)
	stray := make([]string, 0)
	waiting := make(map[int]chan struct{})
	var stderr bytes.Buffer
	reader, writer := io.Pipe()
	status := make(chan int, 1)
	go func() {
		status <- runMCP(t.Context(), callersOptions{root: runtime.root}, reader, &mcpReplayWriter{
			replies: replies,
			stray:   &stray,
			waiting: waiting,
		}, &stderr)
	}()
	for _, step := range golden.Steps {
		if len(step.Edit) > 0 {
			for rel, content := range step.Edit {
				path := filepath.Join(runtime.base, filepath.FromSlash(rel))
				if !strings.HasPrefix(rel, "repo/") && !strings.HasPrefix(rel, "home/") && !strings.HasPrefix(rel, "elsewhere/") {
					path = filepath.Join(runtime.repo, filepath.FromSlash(rel))
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v, want nil", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v, want nil", path, err)
				}
			}
			continue
		}
		var line []byte
		if text, ok := step.Send.(string); ok {
			line = []byte(text)
		} else {
			var err error
			line, err = json.Marshal(step.Send)
			if err != nil {
				t.Fatalf("json.Marshal(%v) error = %v, want nil", step.Send, err)
			}
		}
		if step.Reply {
			waiting[step.ID] = make(chan struct{})
		}
		if _, err := io.WriteString(writer, string(line)+"\n"); err != nil {
			t.Fatalf("write MCP step %v error = %v, want nil", step.Send, err)
		}
		if step.Reply {
			select {
			case <-waiting[step.ID]:
				delete(waiting, step.ID)
			case <-time.After(30 * time.Second):
				t.Fatalf("MCP step %v timeout, want reply %d", step.Send, step.ID)
			}
		} else {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close MCP stdin error = %v, want nil", err)
	}
	gotStatus := <-status
	if gotStatus != golden.Status {
		t.Errorf("run MCP status = %d, want %d; stderr = %q", gotStatus, golden.Status, stderr.String())
	}
	encoded, err := json.Marshal(map[string]any{"replies": replies, "stray": stray})
	if err != nil {
		t.Fatalf("json.Marshal(replies) error = %v, want nil", err)
	}
	gotJSON := runtime.normalize(goldenServerVersion.ReplaceAllString(string(encoded), `"version":"<V>"`))
	wantJSON := runtime.normalize(goldenServerVersion.ReplaceAllString(golden.Stdout, `"version":"<V>"`))
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(gotJSON), &gotValue); err != nil {
		t.Fatalf("json.Unmarshal(got MCP stdout) error = %v, want nil", err)
	}
	if err := json.Unmarshal([]byte(wantJSON), &wantValue); err != nil {
		t.Fatalf("json.Unmarshal(golden MCP stdout) error = %v, want nil", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		gotObject, gotOK := gotValue.(map[string]any)
		wantObject, wantOK := wantValue.(map[string]any)
		if !gotOK || !wantOK {
			t.Errorf("run MCP output = %#v, want %#v", gotValue, wantValue)
		} else {
			gotReplies, gotRepliesOK := gotObject["replies"].(map[string]any)
			wantReplies, wantRepliesOK := wantObject["replies"].(map[string]any)
			if !gotRepliesOK || !wantRepliesOK {
				t.Errorf("run MCP replies = %#v, want %#v", gotObject["replies"], wantObject["replies"])
			} else {
				for id, wantReply := range wantReplies {
					gotReply, ok := gotReplies[id]
					if !ok {
						t.Errorf("run MCP reply %s = missing, want %#v", id, wantReply)
						continue
					}
					if !reflect.DeepEqual(gotReply, wantReply) {
						t.Errorf("run MCP reply %s = %#v, want %#v", id, gotReply, wantReply)
					}
				}
			}
		}
	}
	if got := runtime.normalize(stderr.String()); got != golden.Stderr {
		t.Errorf("run MCP stderr = %q, want %q", got, golden.Stderr)
	}
	if golden.CheckFiles {
		compareGoldenFiles(t, runtime.files(t, hasFileModes(golden.Files)), golden.Files)
	}
}

type mcpReplayWriter struct {
	replies map[int]any
	stray   *[]string
	waiting map[int]chan struct{}
	buffer  []byte
}

func (writer *mcpReplayWriter) Write(data []byte) (int, error) {
	writer.buffer = append(writer.buffer, data...)
	for {
		cut := bytes.IndexByte(writer.buffer, '\n')
		if cut < 0 {
			return len(data), nil
		}
		line := bytes.TrimSpace(writer.buffer[:cut])
		writer.buffer = writer.buffer[cut+1:]
		if len(line) == 0 {
			continue
		}
		var message struct {
			ID *int `json:"id"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			*writer.stray = append(*writer.stray, string(line))
			continue
		}
		var value any
		if err := json.Unmarshal(line, &value); err != nil {
			*writer.stray = append(*writer.stray, string(line))
			continue
		}
		id := -1
		if message.ID != nil {
			id = *message.ID
		}
		writer.replies[id] = value
		if done := writer.waiting[id]; done != nil {
			close(done)
		}
	}
}

func goldenNames(t *testing.T, suite string) []string {
	t.Helper()
	pattern := filepath.Join("testdata", "goldens", suite, "*.json")
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
	sort.Strings(names)
	return names
}
