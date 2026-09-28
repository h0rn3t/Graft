package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestFormatBuildProgress(t *testing.T) {
	tests := []struct {
		name       string
		index      int
		total      int
		file       string
		wantPrefix string
		wantBar    string
		wantFile   string
	}{
		{name: "first file shows partial bar", index: 0, total: 9, file: "src/app.ts", wantPrefix: "parsing 1/9:", wantBar: "[██░░░░░░░░░░░░░░░░░░]", wantFile: "src/app.ts"},
		{name: "middle file shows half bar", index: 4, total: 10, file: "src/app.ts", wantPrefix: "parsing 5/10:", wantBar: "[██████████░░░░░░░░░░]", wantFile: "src/app.ts"},
		{name: "last file shows full bar", index: 8, total: 9, file: "src/app.ts", wantPrefix: "parsing 9/9:", wantBar: "[████████████████████]", wantFile: "src/app.ts"},
		{name: "single file is complete", index: 0, total: 1, file: "src/app.ts", wantPrefix: "parsing 1/1:", wantBar: "[████████████████████]", wantFile: "src/app.ts"},
		{name: "long path is truncated", index: 0, total: 1, file: "packages/core/src/very-long-file-name-that-exceeds-thirty-runes.ts", wantPrefix: "parsing 1/1:", wantBar: "[████████████████████]", wantFile: "packages/core/src/very-long-fi"},
		{name: "empty total does not panic", index: 0, total: 0, file: "src/app.ts", wantPrefix: "parsing 1/0:", wantBar: "[████████████████████]", wantFile: "src/app.ts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBuildProgress(tt.index, tt.total, tt.file)
			if !strings.Contains(got, tt.wantPrefix) {
				t.Errorf("formatBuildProgress(%d, %d, %q) = %q, want prefix %q", tt.index, tt.total, tt.file, got, tt.wantPrefix)
			}
			if !strings.Contains(got, tt.wantBar) {
				t.Errorf("formatBuildProgress(%d, %d, %q) = %q, want bar %q", tt.index, tt.total, tt.file, got, tt.wantBar)
			}
			if !strings.Contains(got, tt.wantFile) {
				t.Errorf("formatBuildProgress(%d, %d, %q) = %q, want file %q", tt.index, tt.total, tt.file, got, tt.wantFile)
			}
			if !strings.Contains(got, "%") {
				t.Errorf("formatBuildProgress(%d, %d, %q) = %q, want percent sign", tt.index, tt.total, tt.file, got)
			}
		})
	}
}

func TestFormatBuildProgressStaysWithinTerminalWidth(t *testing.T) {
	got := formatBuildProgress(999, 1000, "packages/core/src/store.ts")
	if width := len([]rune(got)); width > 80 {
		t.Errorf("formatBuildProgress(999, 1000, store) width = %d, want <= 80; got %q", width, got)
	}
}

func TestBuildProgressDisabledForNonFileWriters(t *testing.T) {
	var stderr bytes.Buffer
	if buildProgressEnabled(&stderr) {
		t.Errorf("buildProgressEnabled(buffer) = true, want false")
	}
	file, err := os.CreateTemp("", "graft-progress")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if buildProgressEnabled(file) {
		t.Errorf("buildProgressEnabled(temp file) = true, want false for a non-terminal file")
	}
}
