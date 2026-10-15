package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestBuildProgressModel(t *testing.T) {
	var model tea.Model = newBuildProgress("")
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

func TestBuildProgressLanguageServerStages(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		repo    string
		msgs    []tea.Msg
		want    []string
		notWant []string
	}{
		{
			name: "a workspace repo names its place",
			repo: "[3/51] dhcore-go",
			msgs: []tea.Msg{buildStepMsg{index: 2, total: 9, file: "src/app.go"}},
			want: []string{"[3/51] dhcore-go", "parsing 3/9"},
		},
		{
			name: "starting",
			msgs: []tea.Msg{buildLSPMsg{progress: graph.LSPProgress{Stage: graph.LSPStarting, Server: "/go/bin/gopls", Total: 40}, at: start}},
			want: []string{"gopls: starting"},
		},
		{
			name: "indexing counts the wait",
			msgs: []tea.Msg{
				buildLSPMsg{progress: graph.LSPProgress{Stage: graph.LSPIndexing, Server: "/go/bin/gopls", Total: 40}, at: start},
				spinner.TickMsg{Time: start.Add(42 * time.Second)},
			},
			want: []string{"gopls: indexing", "42s"},
		},
		{
			name: "asking estimates what is left",
			msgs: []tea.Msg{
				buildLSPMsg{progress: graph.LSPProgress{Stage: graph.LSPAsking, Server: "/go/bin/gopls", Done: 1, Total: 401, File: "a.go"}, at: start},
				buildLSPMsg{progress: graph.LSPProgress{Stage: graph.LSPAsking, Server: "/go/bin/gopls", Done: 101, Total: 401, File: "internal/b.go"}, at: start.Add(20 * time.Second)},
			},
			// 100 files in 20 s leaves 300 files for 60 s.
			want: []string{"gopls 101/401", " 25%", "internal/b.go", "~1m0s left"},
		},
		{
			name: "asking gives no estimate before it has a rate",
			msgs: []tea.Msg{
				buildLSPMsg{progress: graph.LSPProgress{Stage: graph.LSPAsking, Server: "/go/bin/gopls", Done: 1, Total: 401, File: "a.go"}, at: start},
				buildLSPMsg{progress: graph.LSPProgress{Stage: graph.LSPAsking, Server: "/go/bin/gopls", Done: 3, Total: 401, File: "c.go"}, at: start.Add(time.Second)},
			},
			want:    []string{"gopls 3/401"},
			notWant: []string{"left"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var model tea.Model = newBuildProgress(tc.repo)
			for _, msg := range tc.msgs {
				model, _ = model.Update(msg)
			}
			content := model.View().Content
			for _, want := range tc.want {
				if !strings.Contains(content, want) {
					t.Errorf("View() after %#v = %q, want it to contain %q", tc.msgs, content, want)
				}
			}
			for _, unwanted := range tc.notWant {
				if strings.Contains(content, unwanted) {
					t.Errorf("View() after %#v = %q, want no %q", tc.msgs, content, unwanted)
				}
			}
		})
	}
}

func TestBuildProgressWorkspaceRepoLeavesNoSummary(t *testing.T) {
	var model tea.Model = newBuildProgress("[3/51] dhcore-go")
	model, _ = model.Update(buildStepMsg{index: 8, total: 9, file: "src/app.go"})
	model, _ = model.Update(buildDoneMsg{})
	// The repo's own ✓ line follows, so the progress leaves nothing behind.
	if content := model.View().Content; content != "" {
		t.Errorf("View() when a workspace repo is done = %q, want empty", content)
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
