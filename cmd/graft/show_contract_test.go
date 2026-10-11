package main

import (
	"bytes"
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

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

// showRepoView opens the view of one node of the built show repository, or
// its symbol search when id is empty.
func showRepoView(t *testing.T, root, id string) *showView {
	t.Helper()
	loaded, err := graph.Read(graph.WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatalf("graph.Read() error = %v", err)
	}
	if id == "" {
		return newShowView(root, *loaded, nil)
	}
	for _, node := range loaded.Nodes {
		if node.ID == id {
			return newShowView(root, *loaded, &node)
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

// showKey is the key Bubble Tea reports for name: a key name such as "enter"
// or "ctrl+c", or a character typed.
func showKey(name string) tea.Key {
	codes := map[string]rune{
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "enter": tea.KeyEnter,
		"backspace": tea.KeyBackspace, "esc": tea.KeyEscape, "tab": tea.KeyTab, "pgup": tea.KeyPgUp,
	}
	if code, ok := codes[name]; ok {
		return tea.Key{Code: code}
	}
	if name == "ctrl+c" {
		return tea.Key{Code: 'c', Mod: tea.ModCtrl}
	}
	code, _ := utf8.DecodeRuneInString(name)
	return tea.Key{Code: code, Text: name}
}

// pressShow presses the space-separated keys on view, and reports whether one
// of them quit.
func pressShow(view *showView, keys string) bool {
	for name := range strings.FieldsSeq(keys) {
		if !view.press(showKey(name)) {
			return true
		}
	}
	return false
}

// showTrail names the symbols view has opened, the current one last.
func showTrail(view *showView) string {
	names := make([]string, 0, len(view.trail))
	for _, stop := range view.trail {
		names = append(names, stop.node.Name)
	}
	return strings.Join(names, " › ")
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

func TestShowWithoutASymbol(t *testing.T) {
	root := buildShowRepo(t)
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if status := run([]string{"show", "--no-refresh"}, &stdout, &stderr); status != 1 {
		t.Fatalf("run(show) piped status = %d, want 1; stderr = %q", status, stderr.String())
	}
	// Piped, there is no search to open: it says what to name, and names the
	// most used symbols.
	for _, want := range []string{"name a symbol", "graft show <symbol>", "Serve, parse, reply"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run(show) piped stderr = %q, want it to contain %q", stderr.String(), want)
		}
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
		"t tree",
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
		{name: "arrows", keys: "down right", wantTrail: "Serve", wantSide: showCallees, wantCursor: [2]int{1, 0}},
		{name: "down stops at the last", keys: "down down j", wantTrail: "Serve", wantCursor: [2]int{1, 0}},
		{name: "up stops at the first", keys: "j k k up", wantTrail: "Serve"},
		{name: "left comes back", keys: "l left", wantTrail: "Serve", wantSide: showCallers},
		{name: "enter opens the selected callee", keys: "l enter", wantTrail: "Serve › parse", wantSide: showCallees},
		{name: "enter opens the selected caller", keys: "j enter", wantTrail: "Serve › Main", wantCursor: [2]int{0, 0}},
		{name: "backspace goes back to where it was", keys: "l j enter backspace", wantTrail: "Serve", wantSide: showCallees, wantCursor: [2]int{0, 1}},
		{name: "back at the start stays", keys: "backspace b", wantTrail: "Serve"},
		{name: "enter on an empty side stays", keys: "l enter l enter", wantTrail: "Serve › parse", wantSide: showCallees},
		{name: "q quits", keys: "j q", wantTrail: "Serve", wantCursor: [2]int{1, 0}, wantQuit: true},
		{name: "ctrl-c quits", keys: "ctrl+c j", wantTrail: "Serve", wantQuit: true},
		{name: "escape quits", keys: "esc", wantTrail: "Serve", wantQuit: true},
		{name: "unknown keys do nothing", keys: "x pgup", wantTrail: "Serve"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := showRepoView(t, root, "app/app.go#Serve")
			quit := pressShow(view, tt.keys)
			stop := view.trail[len(view.trail)-1]
			if got := showTrail(view); got != tt.wantTrail || stop.side != tt.wantSide || stop.cursor != tt.wantCursor || quit != tt.wantQuit {
				t.Errorf("press(%q): trail %q, side %d, cursor %v, quit %t; want %q, %d, %v, %t",
					tt.keys, got, stop.side, stop.cursor, quit, tt.wantTrail, tt.wantSide, tt.wantCursor, tt.wantQuit)
			}
		})
	}
}

func TestShowSearch(t *testing.T) {
	root := buildShowRepo(t)
	mostUsed := []string{"app/app.go#Serve", "app/app.go#parse", "app/app.go#reply", "app/app.go#Admin", "app/app.go#Main", "web/web.go#Serve"}
	tests := []struct {
		name       string
		keys       string
		wantSearch bool   // the search is open
		wantQuery  string // what it holds, then
		wantFound  []string
		wantTrail  string
		wantQuit   bool
	}{
		{name: "starts with the most used symbols", wantSearch: true, wantFound: mostUsed},
		{name: "a prefix", keys: "p a r", wantSearch: true, wantQuery: "par", wantFound: []string{"app/app.go#parse"}},
		{name: "case does not matter", keys: "P A R", wantSearch: true, wantQuery: "PAR", wantFound: []string{"app/app.go#parse"}},
		{name: "exact names, the most used first", keys: "s e r v e", wantSearch: true, wantQuery: "serve", wantFound: []string{"app/app.go#Serve", "web/web.go#Serve"}},
		{
			name: "anywhere in the name", keys: "e", wantSearch: true, wantQuery: "e",
			wantFound: []string{"app/app.go#Serve", "app/app.go#parse", "app/app.go#reply", "web/web.go#Serve"},
		},
		{name: "test copies come last", keys: "a d m", wantSearch: true, wantQuery: "adm", wantFound: []string{"app/app.go#Admin", "app/app_test.go#Admin"}},
		{name: "backspace deletes", keys: "p a r x backspace", wantSearch: true, wantQuery: "par", wantFound: []string{"app/app.go#parse"}},
		{name: "keys that act elsewhere are typed", keys: "q t / j", wantSearch: true, wantQuery: "qt/j"},
		{name: "enter opens the match", keys: "p a r enter", wantTrail: "parse"},
		{name: "enter opens the chosen match", keys: "down down up enter", wantTrail: "parse"},
		{name: "enter with no match stays", keys: "z z z enter", wantSearch: true, wantQuery: "zzz"},
		{name: "slash searches again", keys: "p a r enter /", wantSearch: true, wantFound: mostUsed, wantTrail: "parse"},
		{name: "escape goes back to the graph", keys: "p a r enter / s e esc", wantTrail: "parse"},
		{name: "escape with no graph quits", keys: "esc", wantSearch: true, wantFound: mostUsed, wantQuit: true},
		{name: "ctrl-c quits", keys: "p a r ctrl+c", wantSearch: true, wantQuery: "par", wantFound: []string{"app/app.go#parse"}, wantQuit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := showRepoView(t, root, "")
			quit := pressShow(view, tt.keys)
			var query string
			var found []string
			if view.search != nil {
				query = view.search.query
				for _, node := range view.search.found {
					found = append(found, node.ID)
				}
			}
			if (view.search != nil) != tt.wantSearch || query != tt.wantQuery || !slices.Equal(found, tt.wantFound) || showTrail(view) != tt.wantTrail || quit != tt.wantQuit {
				t.Errorf("press(%q): search %t %q %q, trail %q, quit %t; want %t %q %q, %q, %t",
					tt.keys, view.search != nil, query, found, showTrail(view), quit, tt.wantSearch, tt.wantQuery, tt.wantFound, tt.wantTrail, tt.wantQuit)
			}
		})
	}

	view := showRepoView(t, root, "")
	pressShow(view, "p a r")
	lines := view.frame(60, 10, true)
	if len(lines) != 10 {
		t.Errorf("search frame(60, 10) has %d rows, want 10", len(lines))
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{"› par", "1 symbol", "\x1b[7mparse · function · app/app.go:L16-L16", "1←", "enter open"} {
		if !strings.Contains(text, want) {
			t.Errorf("search frame(60, 10) =\n%s\nwant it to contain %q", text, want)
		}
	}
}

func TestShowTree(t *testing.T) {
	// a calls b and d, b calls c, and c calls a back.
	wiring := graph.GraphV1{Nodes: []graph.NodeV1{
		{ID: "a.go#a", Name: "a", Kind: "function", Path: "a.go", Span: "L1-L4"},
		{ID: "b.go#b", Name: "b", Kind: "function", Path: "b.go", Span: "L1-L3"},
		{ID: "c.go#c", Name: "c", Kind: "function", Path: "c.go", Span: "L1-L3"},
		{ID: "d.go#d", Name: "d", Kind: "function", Path: "d.go", Span: "L1-L1"},
	}, Edges: []graph.EdgeV1{
		{Source: "a.go#a", Target: "b.go#b", Relation: "calls", Confidence: "extracted", Line: 2},
		{Source: "a.go#a", Target: "d.go#d", Relation: "calls", Confidence: "extracted", Line: 3},
		{Source: "b.go#b", Target: "c.go#c", Relation: "calls", Confidence: "extracted", Line: 2},
		{Source: "c.go#c", Target: "a.go#a", Relation: "calls", Confidence: "extracted", Line: 2},
	}}
	tests := []struct {
		name       string
		keys       string
		wantRows   string // each row's marker and name, the cursor's in brackets
		wantTrail  string
		wantClosed bool // back to the drawing
	}{
		{name: "grows the way the cursor was", keys: "t", wantRows: "[▾a] ▸c"},
		{name: "tab turns it around", keys: "t tab", wantRows: "[▾a] ▸b ·d"},
		{name: "right opens a row", keys: "t tab down right", wantRows: "▾a [▾b] ▸c ·d"},
		{name: "right on an open row goes into it", keys: "t tab down right right", wantRows: "▾a ▾b [▸c] ·d"},
		{name: "a symbol already above stops the tree", keys: "t tab down right right right", wantRows: "▾a ▾b [▾c] ↻a ·d"},
		{name: "right on a leaf stays", keys: "t tab down down right", wantRows: "▾a ▸b [·d]"},
		{name: "left closes an open row", keys: "t tab down right left", wantRows: "▾a [▸b] ·d"},
		{name: "left on a closed row goes to its parent", keys: "t tab down right right left", wantRows: "▾a [▾b] ▸c ·d"},
		{name: "enter opens the symbol", keys: "t tab down enter", wantRows: "[▾b] ▸c", wantTrail: "a › b"},
		{name: "backspace goes back", keys: "t tab down enter backspace", wantRows: "[▾a] ▸b ·d", wantTrail: "a"},
		{name: "t goes back to the drawing", keys: "t t", wantTrail: "a", wantClosed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := newShowView(t.TempDir(), wiring, &wiring.Nodes[0])
			pressShow(view, tt.keys)
			var rows []string
			if view.tree != nil {
				for i, row := range view.treeRows() {
					text := row.marker() + row.name()
					if i == view.tree.cursor {
						text = "[" + text + "]"
					}
					rows = append(rows, text)
				}
			}
			if got := strings.Join(rows, " "); got != tt.wantRows || showTrail(view) != cmp.Or(tt.wantTrail, "a") || (view.tree == nil) != tt.wantClosed {
				t.Errorf("press(%q): rows %q, trail %q, closed %t; want %q, %q, %t", tt.keys, got, showTrail(view), view.tree == nil, tt.wantRows, cmp.Or(tt.wantTrail, "a"), tt.wantClosed)
			}
		})
	}

	view := newShowView(t.TempDir(), wiring, &wiring.Nodes[0])
	pressShow(view, "t tab down right")
	lines := view.frame(60, 12, true)
	if len(lines) != 12 {
		t.Errorf("tree frame(60, 12) has %d rows, want 12", len(lines))
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{"callees of a", "├──", "╰──", "\x1b[7m▾ b", "calls → b · function · b.go:L1-L3", "tab callers/callees"} {
		if !strings.Contains(text, want) {
			t.Errorf("tree frame(60, 12) =\n%s\nwant it to contain %q", text, want)
		}
	}
}

func TestShowModel(t *testing.T) {
	root := buildShowRepo(t)
	quits := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}

	var model tea.Model = newShowModel(func() tea.Msg { return nil })
	model, _ = model.Update(tea.WindowSizeMsg{Width: 70, Height: 16})
	if view := model.View(); !view.AltScreen || !strings.Contains(view.Content, "loading the graph") {
		t.Errorf("View() while loading = %+v, want the alternate screen and a loading line", view)
	}
	model, cmd := model.Update(showLoadedMsg{view: showRepoView(t, root, "app/app.go#Serve"), notes: "[graft] refreshed\n"})
	if quits(cmd) || !strings.Contains(model.View().Content, "╭") {
		t.Errorf("Update(loaded) = %q, quit %t; want the drawing", model.View().Content, quits(cmd))
	}
	if lines := strings.Count(model.View().Content, "\n") + 1; lines != 16 {
		t.Errorf("View() has %d rows, want the terminal's 16", lines)
	}
	model, cmd = model.Update(tea.KeyPressMsg(showKey("right")))
	if quits(cmd) || model.(showModel).view.trail[0].side != showCallees {
		t.Errorf("Update(right) quit %t, side %d; want the callees", quits(cmd), model.(showModel).view.trail[0].side)
	}
	if _, cmd = model.Update(tea.KeyPressMsg(showKey("q"))); !quits(cmd) {
		t.Errorf("Update(q) does not quit")
	}

	failed, cmd := newShowModel(nil).Update(showLoadedMsg{notes: "✗ no graph — run graft build first\n"})
	if !quits(cmd) || !failed.(showModel).failed || failed.(showModel).notes == "" {
		t.Errorf("Update(load failed) quit %t, model %+v; want a quit that keeps the notes", quits(cmd), failed)
	}
	if _, cmd = newShowModel(nil).Update(tea.KeyPressMsg(showKey("ctrl+c"))); !quits(cmd) {
		t.Errorf("Update(ctrl+c) while loading does not quit")
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
			keys:   strings.Repeat("j ", 9),
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
			keys:  "enter",
			width: 60, height: 12,
			want: []string{"╌▶ \x1b[7mext.Thing", "extends (inferred) → ext.Thing, outside the graph"},
		},
		{
			name: "fields, constants and variables stay out of the columns",
			wiring: graph.GraphV1{
				Nodes: []graph.NodeV1{
					{ID: "t.go#t", Name: "t", Kind: "function", Path: "t.go", Span: "L1-L3"},
					{ID: "a.go#a", Name: "a", Kind: "function", Path: "a.go", Span: "L1-L3"},
					{ID: "f.go#Box.size", Name: "size", Kind: "field", Path: "f.go", Span: "L2-L2"},
					{ID: "k.go#limit", Name: "limit", Kind: "constant", Path: "k.go", Span: "L1-L1"},
				},
				Edges: []graph.EdgeV1{
					{Source: "t.go#t", Target: "f.go#Box.size", Relation: "writes", Confidence: "extracted", Line: 1},
					{Source: "t.go#t", Target: "a.go#a", Relation: "calls", Confidence: "extracted", Line: 2},
					{Source: "t.go#t", Target: "k.go#limit", Relation: "references", Confidence: "extracted", Line: 3},
				},
			},
			width:   60,
			want:    []string{"callees (1)", "─▶ a a.go:1"},
			notWant: []string{"size", "limit"},
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
			view := newShowView(t.TempDir(), tt.wiring, &tt.wiring.Nodes[0])
			pressShow(view, tt.keys)
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
