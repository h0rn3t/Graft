package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

func TestBuildPersistsWalkOptionsAndKeepsUnrelatedConfig(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"src/app.ts", "vendor/dep.ts"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("export function mark() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(root, ".graft", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"brain":{"brainId":"b","token":"secret"},"followSubmodules":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"build", root, "--include-dir", "vendor", "--no-follow-submodules", "--follow-nested-repos"}
	var stdout, stderr bytes.Buffer
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["followSubmodules"] != false || got["followNestedRepos"] != true {
		t.Errorf("run(%v) config = %s, want explicit follow flags", args, data)
	}
	if dirs, ok := got["includeDirs"].([]any); !ok || len(dirs) != 1 || dirs[0] != "vendor" {
		t.Errorf("run(%v) config = %s, want includeDirs [vendor]", args, data)
	}
	if _, ok := got["brain"]; !ok {
		t.Errorf("run(%v) config = %s, want existing brain", args, data)
	}
	loaded, err := graph.Read(graph.WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.NodeCount < 4 {
		t.Errorf("run(%v) graph nodeCount = %d, want both source files", args, loaded.Meta.NodeCount)
	}
	ignored, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil || !strings.Contains(string(ignored), "/.graft/") {
		t.Errorf("run(%v) .gitignore = %q, %v; want .graft entry", args, ignored, err)
	}
}

func TestBuildRejectsInvalidIncludeDirBeforeWritingConfig(t *testing.T) {
	for _, name := range []string{".hidden", "some/path"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			var stdout, stderr bytes.Buffer
			args := []string{"build", root, "--include-dir", name}
			if status := run(args, &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), "--include-dir") {
				t.Errorf("run(%v) = status %d, stderr %q; want validation failure", args, status, stderr.String())
			}
			if _, err := os.Stat(filepath.Join(root, ".graft", "config.json")); !os.IsNotExist(err) {
				t.Errorf("run(%v) wrote config, Stat error = %v; want absent", args, err)
			}
		})
	}
}

func TestBuildExtensionSelection(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"app.ts", "app.py"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("# source\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	args := []string{"build", root, "-e", ".ts"}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	fingerprint, err := graph.ReadFingerprint(filepath.Join(root, "graft"), graph.ExtractorID)
	if err != nil || fingerprint == nil || len(fingerprint.Files) != 1 {
		t.Errorf("run(%v) fingerprint = %#v, %v; want one selected file", args, fingerprint, err)
	} else if _, ok := fingerprint.Files["app.ts"]; !ok {
		t.Errorf("run(%v) fingerprint files = %#v, want app.ts", args, fingerprint.Files)
	}

	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "unsupported.xyz"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	args = []string{"build", root, "--extensions", ".xyz"}
	if status := run(args, &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), "unsupported.xyz") {
		t.Errorf("run(%v) = status %d, stderr %q; want unsupported diagnostic", args, status, stderr.String())
	}
	if _, err := os.Stat(graph.WiringPath(filepath.Join(root, "graft"))); !os.IsNotExist(err) {
		t.Errorf("run(%v) graph Stat error = %v, want absent", args, err)
	}
}

func TestBuildWritesCardAndIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "app.ts"), []byte("export function app() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"build", root}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	for _, rel := range []string{"INDEX.md", "src/app.md"} {
		if _, err := os.Stat(filepath.Join(root, "graft", rel)); err != nil {
			t.Errorf("run(%v) card %s Stat error = %v, want present", args, rel, err)
		}
	}
	if !strings.Contains(stdout.String(), "cards [typescript]") {
		t.Errorf("run(%v) stdout = %q, want card count and language", args, stdout.String())
	}
	if !strings.Contains(stderr.String(), "\rparsing 1/1: src/app.ts") || !strings.HasSuffix(stderr.String(), "\n") {
		t.Errorf("run(%v) stderr = %q, want parse progress and newline", args, stderr.String())
	}
}

func TestBuildDirectoryEnvironmentAndFlagPrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("export function app() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envDir := filepath.Join(root, "env-graph")
	flagDir := filepath.Join(root, "flag-graph")
	t.Setenv("GRAFT_DIR", envDir)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"build", root}, envDir},
		{[]string{"build", root, "--dir", flagDir}, flagDir},
	} {
		var stdout, stderr bytes.Buffer
		if status := run(tt.args, &stdout, &stderr); status != 0 {
			t.Fatalf("run(%v) status = %d, want 0; stderr = %q", tt.args, status, stderr.String())
		}
		if _, err := os.Stat(graph.WiringPath(tt.want)); err != nil {
			t.Errorf("run(%v) graph at %q Stat error = %v, want present", tt.args, tt.want, err)
		}
	}
	if _, err := os.Stat(graph.WiringPath(filepath.Join(root, "graft"))); !os.IsNotExist(err) {
		t.Errorf("run(build, GRAFT_DIR=%q) default graph Stat error = %v, want absent", envDir, err)
	}
}

func TestBuildEmptyRepositoryEmitsTerminalProgressNewline(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"build", root}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	if got := stderr.String(); got != "\n" {
		t.Errorf("run(%v) stderr = %q, want a terminal progress newline", args, got)
	}
}
