package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWarnsWhenGraftIsNotOnPath(t *testing.T) {
	onPath := t.TempDir()
	for _, name := range []string{"graft", "graft.exe"} {
		if err := os.WriteFile(filepath.Join(onPath, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name string
		path string
		want bool
	}{
		{name: "graft missing", path: t.TempDir(), want: true},
		{name: "graft on PATH", path: onPath, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, home := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("DO_NOT_TRACK", "1")
			t.Setenv("PATH", tt.path)
			var stdout, stderr bytes.Buffer
			args := []string{"init", repo, "--agents", "claude", "--no-build", "--no-global"}
			if status := runWithInput(args, strings.NewReader(""), &stdout, &stderr); status != 0 {
				t.Fatalf("run(%v) status = %d, want 0; stderr = %q", args, status, stderr.String())
			}
			if got := strings.Contains(stderr.String(), "graft is not on PATH"); got != tt.want {
				t.Errorf("run(%v) with PATH=%s warns = %t, want %t; stderr = %q", args, tt.name, got, tt.want, stderr.String())
			}
		})
	}
}
