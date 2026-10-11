package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/h0rn3t/Graft/internal/graph"
)

// The two columns of graft show: who calls the symbol, and what it calls.
const (
	showCallers = iota
	showCallees
)

var showSideNames = [2]string{"callers", "callees"}

const (
	showMinWidth    = 40  // a narrower terminal gets a drawing this wide
	showStaticWidth = 100 // the width of a drawing piped elsewhere
	showBoxRows     = 5
)

// showStop is one symbol opened in graft show, with the column the cursor is
// in and the row it is on in each column.
type showStop struct {
	node   graph.NodeV1
	side   int
	cursor [2]int
}

// showView is the state of graft show: the symbols opened so far, the current
// one last, and the current one's callers and callees.
type showView struct {
	root    string
	wiring  graph.GraphV1
	trail   []showStop
	sides   [2][]graph.EdgeHit
	sources map[string][]string // file lines read for quotes, by path
}

// runShow draws a symbol between its callers and callees. With a terminal on
// both ends it is interactive; otherwise it prints the drawing once.
func runShow(ctx context.Context, opts callersOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	root, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return 1
	}
	matches, err := graph.ResolveSymbol(*loaded, opts.query, graph.ResolveSymbolOptions{In: opts.in})
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if production := slices.DeleteFunc(slices.Clone(matches), graph.IsCopy); len(production) > 0 {
		matches = production
	}
	switch {
	case len(matches) == 0:
		writeDiagnostic(stderr, "✗ no symbol \"%s\" in the graph — check spelling or run graft build%s\n", opts.query, similarSymbols(opts.query, *loaded))
		return 1
	case len(matches) > 1:
		var list strings.Builder
		for _, node := range matches[:min(len(matches), 8)] {
			fmt.Fprintf(&list, "\n  %s", nodeLabel(node))
		}
		if len(matches) > 8 {
			fmt.Fprintf(&list, "\n  … and %d more", len(matches)-8)
		}
		writeDiagnostic(stderr, "✗ \"%s\" names %d symbols — qualify it (Type.method, pkg.Fn) or narrow with --in:%s\n", opts.query, len(matches), list.String())
		return 1
	}

	view := newShowView(root, *loaded, matches[0])
	in, inFile := stdin.(*os.File)
	out, outFile := stdout.(*os.File)
	if inFile && outFile && isTerminal(in) && isTerminal(out) {
		return showInteractive(ctx, view, in, out, stderr)
	}
	width := showStaticWidth
	if outFile {
		if columns, _, err := terminalSize(out); err == nil {
			width = columns
		}
	}
	if _, err := io.WriteString(stdout, strings.Join(view.frame(width, 0, false), "\n")+"\n"); err != nil {
		return 1
	}
	return 0
}

// showInteractive runs the view on the terminal until a quit key: it draws a
// frame on the alternate screen, reads what was typed, and draws again. The
// size is read before each frame, so a resize shows at the next key.
func showInteractive(ctx context.Context, view *showView, in, out *os.File, stderr io.Writer) int {
	restore, err := makeRaw(in)
	if err != nil {
		writeDiagnostic(stderr, "✗ cannot read keys from the terminal: %v\n", err)
		return 1
	}
	leave := sync.OnceFunc(func() {
		_, _ = io.WriteString(out, "\x1b[?25h\x1b[?1049l") // best effort: the terminal is being handed back
		restore()
	})
	defer leave()
	// An interrupt from outside exits the process after a grace period, and
	// the read below never returns to see it: hand the terminal back first.
	defer context.AfterFunc(ctx, leave)()
	if _, err := io.WriteString(out, "\x1b[?1049h\x1b[?25l"); err != nil {
		return 1
	}
	buffer := make([]byte, 256)
	for {
		width, height, err := terminalSize(out)
		if err != nil {
			width, height = showStaticWidth, 24
		}
		// One column short of the edge, so no row leaves the cursor waiting
		// to wrap and the erase after it never eats its last character.
		frame := view.frame(width-1, height, true)
		if _, err := io.WriteString(out, "\x1b[H"+strings.Join(frame, "\x1b[K\r\n")+"\x1b[K\x1b[J"); err != nil {
			return 1
		}
		count, err := in.Read(buffer)
		if err != nil {
			return 0
		}
		for _, key := range showKeys(string(buffer[:count])) {
			if !view.press(key) {
				return 0
			}
		}
	}
}

// newShowView opens node, with the cursor on its callers, or on its callees
// when nothing calls it.
func newShowView(root string, wiring graph.GraphV1, node graph.NodeV1) *showView {
	view := &showView{root: root, wiring: wiring, sources: make(map[string][]string)}
	view.open(node, showCallers)
	if len(view.sides[showCallers]) == 0 {
		view.trail[0].side = showCallees
	}
	return view
}

// open makes node the current symbol, the cursor in column side.
func (v *showView) open(node graph.NodeV1, side int) {
	v.trail = append(v.trail, showStop{node: node, side: side})
	v.sides = showSides(v.wiring, node)
}

// showSides returns node's callers and callees, one edge for each: the
// callers by file and line of the call, the callees in the order it calls them.
func showSides(wiring graph.GraphV1, node graph.NodeV1) [2][]graph.EdgeHit {
	firstEach := func(hits []graph.EdgeHit) []graph.EdgeHit {
		seen := make(map[string]bool, len(hits))
		unique := hits[:0]
		for _, hit := range hits {
			if !seen[hit.ID] {
				seen[hit.ID] = true
				unique = append(unique, hit)
			}
		}
		return unique
	}
	callers := graph.CallersOf(wiring, node)
	file := func(hit graph.EdgeHit) string {
		if hit.Node == nil {
			return hit.ID
		}
		return hit.Node.Path
	}
	slices.SortStableFunc(callers, func(a, b graph.EdgeHit) int {
		return cmp.Or(cmp.Compare(file(a), file(b)), cmp.Compare(a.Line, b.Line))
	})
	callees := graph.CalleesOf(wiring, node)
	slices.SortStableFunc(callees, func(a, b graph.EdgeHit) int { return cmp.Compare(a.Line, b.Line) })
	return [2][]graph.EdgeHit{firstEach(callers), firstEach(callees)}
}

// showKeys splits a chunk read from the terminal into keys, keeping an escape
// sequence such as an arrow whole.
func showKeys(chunk string) []string {
	var keys []string
	for chunk != "" {
		end := 1
		switch {
		case strings.HasPrefix(chunk, "\x1b["):
			end = 2
			for end < len(chunk) && (chunk[end] < '@' || chunk[end] > '~') {
				end++
			}
			end = min(end+1, len(chunk))
		case strings.HasPrefix(chunk, "\x1bO") && len(chunk) > 2:
			end = 3
		}
		keys = append(keys, chunk[:end])
		chunk = chunk[end:]
	}
	return keys
}

// press applies one key to the view and reports whether to go on.
func (v *showView) press(key string) bool {
	stop := &v.trail[len(v.trail)-1]
	hits := v.sides[stop.side]
	switch key {
	case "\x1b[A", "\x1bOA", "k":
		stop.cursor[stop.side] = max(stop.cursor[stop.side]-1, 0)
	case "\x1b[B", "\x1bOB", "j":
		stop.cursor[stop.side] = max(min(stop.cursor[stop.side]+1, len(hits)-1), 0)
	case "\x1b[D", "\x1bOD", "h":
		stop.side = showCallers
	case "\x1b[C", "\x1bOC", "l":
		stop.side = showCallees
	case "\r", "\n":
		if len(hits) > 0 && hits[stop.cursor[stop.side]].Node != nil {
			v.open(*hits[stop.cursor[stop.side]].Node, stop.side)
		}
	case "\x7f", "\b", "b":
		if len(v.trail) > 1 {
			v.trail = v.trail[:len(v.trail)-1]
			v.sides = showSides(v.wiring, v.trail[len(v.trail)-1].node)
		}
	case "q", "\x1b", "\x03":
		return false
	}
	return true
}

// frame draws the view width columns wide. A live frame is height rows: the
// symbols opened, the column heads, the graph, then the selected edge, the
// line of code it stands for, and the keys. A static frame is the heads and
// the graph, as tall as its longer column, with no colors.
func (v *showView) frame(width, height int, live bool) []string {
	width = max(width, showMinWidth)
	stop := v.trail[len(v.trail)-1]
	center := stop.node

	location := ""
	if center.Span != "" {
		location = ":" + center.Span
	}
	box := [3]string{center.Name, string(center.Kind), center.Path + location}
	inner := 0
	for _, text := range box {
		inner = max(inner, utf8.RuneCountInString(text))
	}
	boxWidth := min(max(inner+4, 12), max(width/3, 12))
	inner = boxWidth - 4
	if utf8.RuneCountInString(box[2]) > inner {
		box[2] = clip(path.Base(center.Path)+location, inner)
	}
	widths := [2]int{(width - boxWidth - 8) / 2}
	widths[1] = width - boxWidth - 8 - widths[0]

	rows := max(len(v.sides[showCallers]), len(v.sides[showCallees]), showBoxRows)
	if live {
		rows = max(height-5, showBoxRows)
	}
	boxTop := (rows - showBoxRows) / 2
	mid := boxTop + showBoxRows/2
	var columns [2][]string
	var heads [2]string
	for side := range 2 {
		columns[side], heads[side] = v.column(side, widths[side], rows, mid, live)
	}

	var lines []string
	if live {
		names := make([]string, 0, len(v.trail))
		for _, opened := range v.trail {
			names = append(names, opened.node.Name)
		}
		trail := strings.Join(names, " › ")
		if count := utf8.RuneCountInString(trail); count > width {
			trail = "…" + string([]rune(trail)[count-width+1:])
		}
		lines = append(lines, paint(true, "1", trail))
	}
	leftHead := clip(heads[showCallers], width-widths[showCallees]-1)
	lines = append(lines, paint(live && stop.side == showCallers, "1", leftHead)+
		strings.Repeat(" ", width-widths[showCallees]-utf8.RuneCountInString(leftHead))+
		paint(live && stop.side == showCallees, "1", clip(heads[showCallees], widths[showCallees])))
	styles := [3]string{"1", "2", "2"}
	for row := range rows {
		var middle string
		switch row - boxTop {
		case 0:
			middle = "╭" + strings.Repeat("─", boxWidth-2) + "╮"
		case 1, 2, 3:
			text := clip(box[row-boxTop-1], inner)
			middle = "│ " + paint(live, styles[row-boxTop-1], text) + strings.Repeat(" ", inner-utf8.RuneCountInString(text)) + " │"
		case 4:
			middle = "╰" + strings.Repeat("─", boxWidth-2) + "╯"
		default:
			middle = strings.Repeat(" ", boxWidth)
		}
		lines = append(lines, columns[showCallers][row]+middle+columns[showCallees][row])
	}
	if live {
		lines = append(lines, v.footer(width)...)
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return lines
}

// column draws one side of the graph, rows tall, and names it in its head.
// Each visible edge is a line to a bus that meets the box at row mid, its
// arrow pointing the way the call goes: dashed when the edge is inferred.
// The callers' column is width+3 wide, the callees' width+5.
func (v *showView) column(side, width, rows, mid int, live bool) ([]string, string) {
	stop := v.trail[len(v.trail)-1]
	hits := v.sides[side]
	cursor := stop.cursor[side]
	count := min(len(hits), rows)
	offset := min(max(cursor-rows/2, 0), len(hits)-count)
	top := min(max(mid-count/2, 0), rows-count)
	first, last := min(top, mid), max(top+count-1, mid)

	head := fmt.Sprintf("%s (%d)", showSideNames[side], len(hits))
	if offset > 0 {
		head += fmt.Sprintf(" ↑%d", offset)
	}
	if below := len(hits) - offset - count; below > 0 {
		head += fmt.Sprintf(" ↓%d", below)
	}

	lines := make([]string, rows)
	for row := range rows {
		index := offset + row - top
		listed := row >= top && row < top+count
		text, used, dash := "", 0, "─"
		switch {
		case listed:
			hit := hits[index]
			if hit.Confidence == "inferred" {
				dash = "╌"
			}
			labelWidth := width
			if side == showCallers {
				labelWidth -= 2 // room for the line to the bus
			}
			name, loc := showLabel(hit, side, labelWidth)
			text, used = name, utf8.RuneCountInString(name)
			if loc != "" {
				text += " " + paint(live, "2", loc)
				used += 1 + utf8.RuneCountInString(loc)
			}
			if live && side == stop.side && index == cursor {
				text = "\x1b[7m" + text + "\x1b[0m"
			}
		case count == 0 && row == mid:
			empty := clip("no "+showSideNames[side], width)
			text, used = paint(live, "2", empty), utf8.RuneCountInString(empty)
		}
		toBox := count > 0 && row == mid
		bus := " "
		if count > 0 && row >= first && row <= last {
			if side == showCallers {
				bus = junction(row > first, row < last, listed, toBox)
			} else {
				bus = junction(row > first, row < last, toBox, listed)
			}
		}
		if side == showCallers {
			fill, arrow := strings.Repeat(" ", width-used), "  "
			if listed {
				fill = " " + strings.Repeat(dash, width-used-1)
			}
			if toBox {
				arrow = "─▶"
			}
			lines[row] = text + fill + bus + arrow
			continue
		}
		shaft, arrow := " ", "   "
		if toBox {
			shaft = "─"
		}
		if listed {
			arrow = dash + "▶ "
		}
		lines[row] = shaft + bus + arrow + text + strings.Repeat(" ", width-used)
	}
	return lines, head
}

// footer ends a live frame: the selected edge, the line of code it stands
// for, and the keys.
func (v *showView) footer(width int) []string {
	stop := v.trail[len(v.trail)-1]
	hits := v.sides[stop.side]
	keys := paint(true, "2", clip("↑↓ move  ←→ callers/callees  enter open  backspace back  q quit", width))
	if len(hits) == 0 {
		return []string{paint(true, "2", "no "+showSideNames[stop.side]), "", keys}
	}
	hit := hits[stop.cursor[stop.side]]
	relation := string(hit.Relation)
	if hit.Confidence == "inferred" {
		relation += " (inferred)"
	}
	// An incoming edge's line is in the caller's file, an outgoing one's in
	// the symbol's own.
	arrow, lineFile := "←", stop.node.Path
	if stop.side == showCallees {
		arrow = "→"
	}
	other := hit.ID + ", outside the graph"
	if hit.Node != nil {
		other = nodeLabel(*hit.Node)
		if stop.side == showCallers {
			lineFile = hit.Node.Path
		}
	}
	quote := ""
	mention := regexp.MustCompile(`(^|[^[:alnum:]_$])` + regexp.QuoteMeta(stop.node.Name) + `([^[:alnum:]_$]|$)`)
	if line, number, ok := quoteFor(v.root, mention, hit, lineFile, v.sources); ok {
		code := strings.ReplaceAll(strings.TrimSpace(line), "\t", " ")
		quote = paint(true, "2", clip(fmt.Sprintf("%d: %s", number, code), width))
	}
	return []string{clip(relation+" "+arrow+" "+other, width), quote, keys}
}

// showLabel names the other end of an edge in at most width runes: its name,
// then, when it fits, the file and line the edge stands at — a caller's line
// of the call, a callee's definition. The footer gives the whole path.
func showLabel(hit graph.EdgeHit, side, width int) (string, string) {
	if hit.Node == nil {
		return clip(hit.ID, width), ""
	}
	name := clip(hit.Node.Name, width)
	line := hit.Line
	if start, _, ok := spanLines(hit.Node.Span); ok && (side == showCallees || line == 0) {
		line = start
	}
	loc := path.Base(hit.Node.Path)
	if line > 0 {
		loc += fmt.Sprintf(":%d", line)
	}
	if utf8.RuneCountInString(name)+1+utf8.RuneCountInString(loc) > width {
		return name, ""
	}
	return name, loc
}

// clip cuts text to at most width runes, ending a cut with "…".
func clip(text string, width int) string {
	switch {
	case utf8.RuneCountInString(text) <= width:
		return text
	case width <= 0:
		return ""
	}
	return string([]rune(text)[:width-1]) + "…"
}

// paint wraps text in an SGR attribute when on, so a static drawing stays plain.
func paint(on bool, attribute, text string) string {
	if !on || text == "" {
		return text
	}
	return "\x1b[" + attribute + "m" + text + "\x1b[0m"
}

// junction is the box-drawing character whose lines leave a cell up, down,
// left and right.
func junction(up, down, left, right bool) string {
	index := 0
	for bit, set := range []bool{up, down, left, right} {
		if set {
			index |= 1 << bit
		}
	}
	return string([]rune(" ╵╷│─┘┐┤─└┌├─┴┬┼")[index])
}
