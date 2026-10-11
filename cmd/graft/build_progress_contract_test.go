package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestBuildProgressModel(t *testing.T) {
	var model tea.Model = newBuildProgress()
	if content := model.View().Content; !strings.Contains(content, "reading the repository") {
		t.Errorf("View() before the first file = %q, want it to say it is reading", content)
	}
	model, _ = model.Update(buildStepMsg{index: 2, total: 9, file: "src/app.ts"})
	for _, want := range []string{"parsing 3/9", " 33%", "src/app.ts"} {
		if content := model.View().Content; !strings.Contains(content, want) {
			t.Errorf("View() after file 3 of 9 = %q, want it to contain %q", content, want)
		}
	}
	model, cmd := model.Update(buildDoneMsg{})
	if cmd == nil {
		t.Fatal("Update(done) cmd = nil, want quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Update(done) does not quit")
	}
	// Cached files count too, so it says read, not parsed.
	if content := model.View().Content; !strings.Contains(content, "read 9 files") || strings.Contains(content, "src/app.ts") {
		t.Errorf("View() when done = %q, want the count and no file", content)
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
