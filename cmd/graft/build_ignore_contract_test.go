package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIgnoreFileContract(t *testing.T) {
	tests := []struct {
		name       string
		flags      []string
		gitignore  bool
		searchable bool
	}{
		{name: "default", gitignore: true, searchable: true},
		{name: "skip gitignore", flags: []string{"--no-gitignore"}, searchable: true},
		{name: "skip ripgrep ignore", flags: []string{"--no-ignore"}, gitignore: true},
		{name: "skip both", flags: []string{"--no-gitignore", "--no-ignore"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAFT_NO_GITIGNORE", "false")
			t.Setenv("GRAFT_NO_IGNORE", "false")
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("export function app() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"build", root}, tt.flags...)
			for range 2 {
				var stdout, stderr bytes.Buffer
				if status := run(args, &stdout, &stderr); status != 0 {
					t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
				}
			}
			for _, item := range []struct {
				name    string
				want    bool
				markers []string
			}{
				{".gitignore", tt.gitignore, []string{"/graft/"}},
				{".ignore", tt.searchable, []string{"!graft/", "graft/.cache/", "graft/.graph/"}},
			} {
				data, err := os.ReadFile(filepath.Join(root, item.name))
				if !item.want {
					if !os.IsNotExist(err) {
						t.Errorf("run(%v) %s = %q, %v; want absent", args, item.name, data, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("run(%v) %s error = %v, want file", args, item.name, err)
					continue
				}
				for _, marker := range item.markers {
					if strings.Count(string(data), marker) != 1 {
						t.Errorf("run(%v) %s marker %q count = %d, want 1", args, item.name, marker, strings.Count(string(data), marker))
					}
				}
			}
		})
	}
}

func TestBuildOutsideOutputDoesNotEditRepositoryIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	outDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("export function app() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"build", root, "--dir", outDir}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	for _, name := range []string{".gitignore", ".ignore"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("run(%v) created %s in repository, Stat error = %v, want absent", args, name, err)
		}
	}
}

func TestBuildFooterUsesRawNoGitignoreEnvironmentPresence(t *testing.T) {
	t.Setenv("GRAFT_NO_GITIGNORE", "false")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("export function app() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"build", root}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is a local cache — add it to your gitignore if you want it untracked.") {
		t.Errorf("run(%v) stdout = %q, want the TypeScript no-gitignore footer for any set environment value", args, stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err != nil {
		t.Errorf("run(%v) .gitignore Stat error = %v, want file written because false disables the setting", args, err)
	}
}
