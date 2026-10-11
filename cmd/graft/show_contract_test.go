package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

// buildShowRepo writes a Go repository where Serve has two callers and two
// callees, and builds its graph.
func buildShowRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, source := range map[string]string{
		"go.mod": "module example.com/app\n",
		"app/app.go": "package app\n\nfunc Serve() {\n\tparse()\n\treply()\n}\n\n" +
			"func Admin() {\n\tServe()\n}\n\nfunc Main() {\n\tServe()\n}\n\nfunc parse() {}\n\nfunc reply() {}\n",
		"app/app_test.go": "package app_test\n\nfunc Admin() {}\n",
		"web/web.go":      "package web\n\nfunc Serve() {}\n",
	} {
		writeCheckSource(t, root, filepath.FromSlash(rel), source)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root, "--no-lsp"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	return root
}

// showRepoView opens the view of one node of the built show repository.
func showRepoView(t *testing.T, root, id string) *showView {
	t.Helper()
	loaded, err := graph.Read(graph.WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatalf("graph.Read() error = %v", err)
	}
	for _, node := range loaded.Nodes {
		if node.ID == id {
			return newShowView(root, *loaded, node)
		}
	}
	t.Fatalf("no node %q in the graph", id)
	return nil
}

// showGraph is a graph where target is called by callers and calls callees,
// all of them functions in their own file.
func showGraph(target string, callers, callees []string) graph.GraphV1 {
	var wiring graph.GraphV1
	add := func(name string) string {
		id := name + ".go#" + name
		wiring.Nodes = append(wiring.Nodes, graph.NodeV1{ID: id, Name: name, Kind: "function", Path: name + ".go", Span: "L1-L3"})
		return id
	}
	targetID := add(target)
	for _, name := range callers {
		wiring.Edges = append(wiring.Edges, graph.EdgeV1{Source: add(name), Target: targetID, Relation: "calls", Confidence: "extracted", Line: 2})
	}
	for _, name := range callees {
		wiring.Edges = append(wiring.Edges, graph.EdgeV1{Source: targetID, Target: add(name), Relation: "calls", Confidence: "extracted", Line: 2})
	}
	return wiring
}

func TestShowCommandContract(t *testing.T) {
	root := buildShowRepo(t)
	tests := []struct {
		name       string
		args       []string
		wantStatus int
		want       []string // substrings of stdout, or of stderr when the status is 1
		notWant    []string
	}{
		{
			name:    "piped output is one plain drawing",
			args:    []string{"show", "Serve", root, "--in", "app"},
			want:    []string{"callers (2)", "callees (2)", "Admin app.go:9 ─", "─▶ parse app.go:16", "│ Serve", "│ app/app.go:L3-L6"},
			notWant: []string{"\x1b["},
		},
		{
			name: "a test copy yields to the production definition",
			args: []string{"show", "Admin", root},
			want: []string{"callers (0)", "callees (1)", "│ app/app.go:L8-L10"},
		},
		{name: "unknown symbol", args: []string{"show", "nothing", root}, wantStatus: 1, want: []string{`no symbol "nothing"`}},
		{
			name:       "ambiguous symbol",
			args:       []string{"show", "Serve", root},
			wantStatus: 1,
			want:       []string{`"Serve" names 2 symbols`, "Serve · function · app/app.go:L3-L6", "Serve · function · web/web.go:L3-L3"},
		},
		{name: "unknown prefix", args: []string{"show", "Serve", root, "--in", "nowhere"}, wantStatus: 1, want: []string{"nowhere"}},
		{name: "no graph", args: []string{"show", "Serve", t.TempDir(), "--no-refresh"}, wantStatus: 1, want: []string{"no graph"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tt.args, &stdout, &stderr)
			if status != tt.wantStatus {
				t.Fatalf("run(%q) status = %d, want %d; stderr = %q", tt.args, status, tt.wantStatus, stderr.String())
			}
			got := stdout.String()
			if tt.wantStatus != 0 {
				got = stderr.String()
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("run(%q) = %q, want it to contain %q", tt.args, got, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("run(%q) = %q, want no %q", tt.args, got, notWant)
				}
			}
		})
	}
}

func TestShowDrawing(t *testing.T) {
	root := buildShowRepo(t)
	view := showRepoView(t, root, "app/app.go#Serve")
	want := strings.Join([]string{
		"callers (2)                                 callees (2)",
		"                   ╭──────────────────╮",
		"Admin app.go:9 ─┐  │ Serve            │ ┌─▶ parse app.go:16",
		"Main app.go:13 ─┴─▶│ function         │─┴─▶ reply app.go:18",
		"                   │ app/app.go:L3-L6 │",
		"                   ╰──────────────────╯",
	}, "\n")
	if got := strings.Join(view.frame(60, 0, false), "\n"); got != want {
		t.Errorf("frame(60, 0, static) =\n%s\nwant\n%s", got, want)
	}

	live := view.frame(60, 14, true)
	if len(live) != 14 {
		t.Errorf("frame(60, 14, live) has %d rows, want 14", len(live))
	}
	text := strings.Join(live, "\n")
	for _, want := range []string{
		"Serve",
		"\x1b[7mAdmin", // the cursor starts on the first caller
		"calls ← Admin · function · app/app.go:L8-L10", // what the selected edge is
		"9: Serve()", // and the line it stands for
		"enter open",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("frame(60, 14, live) =\n%s\nwant it to contain %q", text, want)
		}
	}
}

func TestShowKeys(t *testing.T) {
	root := buildShowRepo(t)
	tests := []struct {
		name       string
		keys       string
		wantTrail  string
		wantSide   int
		wantCursor [2]int
		wantQuit   bool
	}{
		{name: "starts on the callers", keys: "", wantTrail: "Serve"},
		{name: "down", keys: "j", wantTrail: "Serve", wantCursor: [2]int{1, 0}},
		{name: "application-mode arrows", keys: "\x1bOB\x1bOC", wantTrail: "Serve", wantSide: showCallees, wantCursor: [2]int{1, 0}},
		{name: "down stops at the last", keys: "\x1b[B\x1b[Bj", wantTrail: "Serve", wantCursor: [2]int{1, 0}},
		{name: "up stops at the first", keys: "jkk\x1b[A", wantTrail: "Serve"},
		{name: "right goes to the callees", keys: "\x1b[C", wantTrail: "Serve", wantSide: showCallees},
		{name: "left comes back", keys: "l\x1b[D", wantTrail: "Serve", wantSide: showCallers},
		{name: "enter opens the selected callee", keys: "l\r", wantTrail: "Serve › parse", wantSide: showCallees},
		{name: "enter opens the selected caller", keys: "j\r", wantTrail: "Serve › Main", wantCursor: [2]int{0, 0}},
		{name: "backspace goes back to where it was", keys: "lj\r\x7f", wantTrail: "Serve", wantSide: showCallees, wantCursor: [2]int{0, 1}},
		{name: "back at the start stays", keys: "\x7fb", wantTrail: "Serve"},
		{name: "enter on an empty side stays", keys: "l\rl\r", wantTrail: "Serve › parse", wantSide: showCallees},
		{name: "q quits", keys: "jq", wantTrail: "Serve", wantCursor: [2]int{1, 0}, wantQuit: true},
		{name: "ctrl-c quits", keys: "\x03j", wantTrail: "Serve", wantQuit: true},
		{name: "escape quits", keys: "\x1b", wantTrail: "Serve", wantQuit: true},
		{name: "unknown keys do nothing", keys: "x\x1b[5~", wantTrail: "Serve"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := showRepoView(t, root, "app/app.go#Serve")
			quit := false
			for _, key := range showKeys(tt.keys) {
				if !view.press(key) {
					quit = true
					break
				}
			}
			names := make([]string, 0, len(view.trail))
			for _, stop := range view.trail {
				names = append(names, stop.node.Name)
			}
			stop := view.trail[len(view.trail)-1]
			if got := strings.Join(names, " › "); got != tt.wantTrail || stop.side != tt.wantSide || stop.cursor != tt.wantCursor || quit != tt.wantQuit {
				t.Errorf("press(%q): trail %q, side %d, cursor %v, quit %t; want %q, %d, %v, %t",
					tt.keys, got, stop.side, stop.cursor, quit, tt.wantTrail, tt.wantSide, tt.wantCursor, tt.wantQuit)
			}
		})
	}
}

func TestShowEdges(t *testing.T) {
	callers := make([]string, 10)
	for i := range callers {
		callers[i] = fmt.Sprintf("c%d", i)
	}
	tests := []struct {
		name    string
		wiring  graph.GraphV1
		keys    string
		width   int
		height  int // 0 draws the static view
		want    []string
		notWant []string
	}{
		{
			name:   "no callers starts on the callees",
			wiring: showGraph("t", nil, []string{"a"}),
			width:  60, height: 12,
			want: []string{"no callers", "calls → a · function · a.go:L1-L3"},
		},
		{
			name:   "nothing either way",
			wiring: showGraph("t", nil, nil),
			width:  60,
			want:   []string{"no callers", "no callees", "│ t"},
		},
		{
			name:   "long lists scroll with the cursor",
			wiring: showGraph("t", callers, nil),
			width:  60, height: 10,
			want:    []string{"callers (10) ↓5", "c4.go:2"},
			notWant: []string{"c5.go:2"},
		},
		{
			name:   "scrolled to the end",
			wiring: showGraph("t", callers, nil),
			keys:   strings.Repeat("j", 9),
			width:  60, height: 10,
			want:    []string{"callers (10) ↑5", "c9.go:2"},
			notWant: []string{"c4.go:2"},
		},
		{
			name: "a target outside the graph is drawn and stays closed",
			wiring: graph.GraphV1{
				Nodes: []graph.NodeV1{{ID: "t.go#t", Name: "t", Kind: "function", Path: "t.go", Span: "L1-L3"}},
				Edges: []graph.EdgeV1{{Source: "t.go#t", Target: "ext.Thing", Relation: "extends", Confidence: "inferred"}},
			},
			keys:  "\r",
			width: 60, height: 12,
			want: []string{"╌▶ \x1b[7mext.Thing", "extends (inferred) → ext.Thing, outside the graph"},
		},
		{
			name:   "a narrow terminal still fits",
			wiring: showGraph("averyveryverylongfunctionname", []string{"caller"}, []string{"callee"}),
			width:  20,
			want:   []string{"│ averyver… │"}, // the drawing keeps its 40-column minimum
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := newShowView(t.TempDir(), tt.wiring, tt.wiring.Nodes[0])
			for _, key := range showKeys(tt.keys) {
				view.press(key)
			}
			lines := view.frame(tt.width, tt.height, tt.height > 0)
			if tt.height > 0 && len(lines) != tt.height {
				t.Errorf("frame(%d, %d) has %d rows, want %d", tt.width, tt.height, len(lines), tt.height)
			}
			text := strings.Join(lines, "\n")
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Errorf("frame(%d, %d) =\n%s\nwant it to contain %q", tt.width, tt.height, text, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(text, notWant) {
					t.Errorf("frame(%d, %d) =\n%s\nwant no %q", tt.width, tt.height, text, notWant)
				}
			}
		})
	}
}
