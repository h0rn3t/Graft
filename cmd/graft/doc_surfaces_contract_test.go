package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocLineSurfacesContract(t *testing.T) {
	root := t.TempDir()
	source := "package a\n\n// Parse reads a config file.\n// It returns an error for unknown keys.\nfunc Parse() error { return nil }\n\nfunc bare() {}\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile(a.go) error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}

	tests := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{
			name:    "skeleton human",
			args:    []string{"skeleton", "a.go", root},
			want:    []string{"- L5-L5 func Parse() error — Parse reads a config file.\n", "- L7-L7 func bare()\n"},
			notWant: []string{"unknown keys"},
		},
		{
			name:    "skeleton json",
			args:    []string{"skeleton", "a.go", root, "--json"},
			want:    []string{`"summary": "Parse reads a config file."`},
			notWant: []string{"unknown keys"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout.Reset()
			stderr.Reset()
			if status := run(tt.args, &stdout, &stderr); status != 0 {
				t.Fatalf("run(%v) status = %d, want 0; stderr = %q", tt.args, status, stderr.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("run(%v) = %q, want it to contain %q", tt.args, stdout.String(), want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(stdout.String(), notWant) {
					t.Errorf("run(%v) = %q, want it without %q", tt.args, stdout.String(), notWant)
				}
			}
		})
	}

	card, err := os.ReadFile(filepath.Join(root, "graft", "a.md"))
	if err != nil {
		t.Fatalf("ReadFile(graft/a.md) error = %v", err)
	}
	for _, want := range []string{"- Parse · function · L5-L5 — Parse reads a config file.\n", "- bare · function · L7-L7 — func bare()\n"} {
		if !strings.Contains(string(card), want) {
			t.Errorf("card graft/a.md = %q, want it to contain %q", card, want)
		}
	}
}
