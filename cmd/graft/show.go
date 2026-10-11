package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
	"github.com/charmbracelet/x/term"

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

// The styles of the live view; a static drawing has none.
var (
	showBold   = lipgloss.NewStyle().Bold(true)
	showFaint  = lipgloss.NewStyle().Faint(true)
	showChosen = lipgloss.NewStyle().Reverse(true)
)

// showStop is one symbol opened in graft show, with the column the drawing's
// cursor is in and the row it is on in each column.
type showStop struct {
	node   graph.NodeV1
	side   int
	cursor [2]int
}

// showView is the state of graft show: the symbols opened so far, the current
// one last, and the call tree or the symbol search while either is shown
// instead of the drawing. With no symbol opened yet, the search is open.
type showView struct {
	root      string
	wiring    graph.GraphV1
	degrees   map[string]int                // edges into each node, by ID, to rank the search
	neighbors map[string][2][]graph.EdgeHit // each node's callers and callees, by ID, once worked out
	trail     []showStop
	tree      *showTree
	search    *showSearch
	sources   map[string][]string // file lines read for quotes, by path
}

// showSearch is the symbol search of graft show: what was typed, the symbols
// that match it, best first, and the row the cursor is on.
type showSearch struct {
	query  string
	found  []graph.NodeV1
	cursor int
}

// showTree is the call tree of graft show: the way it grows from the current
// symbol, the rows opened, by row key, and the row the cursor is on.
type showTree struct {
	side   int
	open   map[string]bool
	cursor int
}

// showRow is one row of the call tree: a symbol, and the edge that leads to it
// from the row it hangs from.
type showRow struct {
	key    string        // the IDs from the root down to it, which tell rows of one symbol apart
	node   *graph.NodeV1 // nil outside the graph
	hit    graph.EdgeHit // the edge from its parent; the root has none
	parent *graph.NodeV1 // nil for the root
	depth  int
	open   bool
	leaf   bool // no edge goes on from it the way the tree grows
	cycle  bool // it is one of the rows above it, so the tree stops there
}

// showModel runs graft show in Bubble Tea: a spinner while load works out the
// view, then the view at the terminal's size.
type showModel struct {
	load          tea.Cmd
	spinner       spinner.Model
	view          *showView
	notes         string // what loading said, printed once the terminal is handed back
	failed        bool
	width, height int
}

// showLoadedMsg is load's answer: the view, nil when loading failed, and
// what loading said.
type showLoadedMsg struct {
	view  *showView
	notes string
}

// runShow draws a symbol between its callers and callees, the most used one
// when none is named. With a terminal on both ends it runs in Bubble Tea;
// otherwise it prints the drawing once.
func runShow(ctx context.Context, opts callersOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	in, inFile := stdin.(*os.File)
	out, outFile := stdout.(*os.File)
	if inFile && outFile && isTerminal(in) && isTerminal(out) {
		return showInteractive(ctx, opts, in, out, stderr)
	}
	view := loadShowView(opts, stderr)
	switch {
	case view == nil:
		return 1
	case view.search != nil:
		writeDiagnostic(stderr, "✗ the graph has no symbol to draw — run graft build, or name one: graft show <symbol>\n")
		return 1
	}
	width := showStaticWidth
	if outFile {
		if columns, _, err := term.GetSize(out.Fd()); err == nil {
			width = columns
		}
	}
	if _, err := io.WriteString(stdout, strings.Join(view.frame(width, 0, false), "\n")+"\n"); err != nil {
		return 1
	}
	return 0
}

// loadShowView loads the graph and opens the symbol opts names, or the most
// used symbol when it names none. It returns nil, having said why on
// stderr, when there is no graph or the name picks no single symbol.
func loadShowView(opts callersOptions, stderr io.Writer) *showView {
	root, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return nil
	}
	if opts.query == "" {
		return newShowView(root, *loaded, nil)
	}
	matches, err := graph.ResolveSymbol(*loaded, opts.query, graph.ResolveSymbolOptions{In: opts.in})
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return nil
	}
	if production := slices.DeleteFunc(slices.Clone(matches), graph.IsCopy); len(production) > 0 {
		matches = production
	}
	switch {
	case len(matches) == 0:
		writeDiagnostic(stderr, "✗ no symbol \"%s\" in the graph — check spelling or run graft build%s\n", opts.query, similarSymbols(opts.query, *loaded))
		return nil
	case len(matches) > 1:
		var list strings.Builder
		for _, node := range matches[:min(len(matches), 8)] {
			fmt.Fprintf(&list, "\n  %s", nodeLabel(node))
		}
		if len(matches) > 8 {
			fmt.Fprintf(&list, "\n  … and %d more", len(matches)-8)
		}
		writeDiagnostic(stderr, "✗ \"%s\" names %d symbols — qualify it (Type.method, pkg.Fn) or narrow with --in:%s\n", opts.query, len(matches), list.String())
		return nil
	}
	return newShowView(root, *loaded, &matches[0])
}

// showInteractive runs graft show in Bubble Tea on the terminal. What loading
// the graph said — a refresh note, or why it failed — would tear the screen,
// so it is printed once the terminal is handed back.
func showInteractive(ctx context.Context, opts callersOptions, in, out *os.File, stderr io.Writer) int {
	load := func() tea.Msg {
		var notes bytes.Buffer
		view := loadShowView(opts, &notes)
		return showLoadedMsg{view: view, notes: notes.String()}
	}
	final, err := tea.NewProgram(newShowModel(load), tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	model, _ := final.(showModel)
	writeDiagnostic(stderr, "%s", model.notes)
	switch {
	case ctx.Err() != nil || errors.Is(err, tea.ErrInterrupted):
		return 130
	case err != nil:
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	case model.failed:
		return 1
	}
	return 0
}

func newShowModel(load tea.Cmd) showModel {
	return showModel{load: load, spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(showFaint))}
}

func (m showModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.load)
}

func (m showModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case showLoadedMsg:
		m.view, m.notes, m.failed = msg.view, msg.notes, msg.view == nil
		if m.failed {
			return m, tea.Quit
		}
	case spinner.TickMsg:
		if m.view == nil {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	case tea.KeyPressMsg:
		switch {
		case m.view != nil:
			if !m.view.press(msg.Key()) {
				return m, tea.Quit
			}
		case msg.String() == "ctrl+c" || msg.String() == "q" || msg.String() == "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m showModel) View() tea.View {
	content := m.spinner.View() + " loading the graph…"
	if m.view != nil {
		content = strings.Join(m.view.frame(m.width, m.height, true), "\n")
	}
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}

// newShowView starts graft show at node or, when node is nil, at the most
// used symbol; it starts at the symbol search only when the graph has no
// symbol to draw.
func newShowView(root string, wiring graph.GraphV1, node *graph.NodeV1) *showView {
	view := &showView{
		root: root, wiring: wiring, degrees: graph.InDegree(wiring.Edges),
		neighbors: make(map[string][2][]graph.EdgeHit), sources: make(map[string][]string),
	}
	if node == nil {
		view.search = &showSearch{}
		view.find("")
		if len(view.search.found) == 0 {
			return view
		}
		node, view.search = &view.search.found[0], nil
	}
	view.jump(*node)
	return view
}

// current is the symbol open now.
func (v *showView) current() *showStop {
	return &v.trail[len(v.trail)-1]
}

// jump opens node from outside the graph — the command line or the search —
// with the drawing's cursor on its callers, or on its callees when nothing
// calls it.
func (v *showView) jump(node graph.NodeV1) {
	side := showCallers
	if len(v.edges(node)[showCallers]) == 0 {
		side = showCallees
	}
	v.open(node, side)
}

// open makes node the current symbol, the drawing's cursor in column side; a
// call tree on show starts again from it.
func (v *showView) open(node graph.NodeV1, side int) {
	v.trail = append(v.trail, showStop{node: node, side: side})
	if v.tree != nil {
		v.growTree(v.tree.side)
	}
}

// back returns to the symbol opened before the current one, if there is one.
func (v *showView) back() {
	if len(v.trail) < 2 {
		return
	}
	v.trail = v.trail[:len(v.trail)-1]
	if v.tree != nil {
		v.growTree(v.tree.side)
	}
}

// growTree shows the call tree of the current symbol growing toward side,
// its first level open.
func (v *showView) growTree(side int) {
	v.tree = &showTree{side: side, open: map[string]bool{v.current().node.ID: true}}
}

// edges returns node's callers and callees, one edge for each and none for a
// value: the callers by file and line of the call, the callees in the order it
// calls them.
func (v *showView) edges(node graph.NodeV1) [2][]graph.EdgeHit {
	if sides, ok := v.neighbors[node.ID]; ok {
		return sides
	}
	// One edge for each neighbor, and none for the fields, constants and
	// variables it reads or writes: they are no calls.
	firstEach := func(hits []graph.EdgeHit) []graph.EdgeHit {
		seen := make(map[string]bool, len(hits))
		unique := hits[:0]
		for _, hit := range hits {
			if !seen[hit.ID] && (hit.Node == nil || !showValue(hit.Node.Kind)) {
				seen[hit.ID] = true
				unique = append(unique, hit)
			}
		}
		return unique
	}
	callers := graph.CallersOf(v.wiring, node)
	file := func(hit graph.EdgeHit) string {
		if hit.Node == nil {
			return hit.ID
		}
		return hit.Node.Path
	}
	slices.SortStableFunc(callers, func(a, b graph.EdgeHit) int {
		return cmp.Or(cmp.Compare(file(a), file(b)), cmp.Compare(a.Line, b.Line))
	})
	callees := graph.CalleesOf(v.wiring, node)
	slices.SortStableFunc(callees, func(a, b graph.EdgeHit) int { return cmp.Compare(a.Line, b.Line) })
	sides := [2][]graph.EdgeHit{firstEach(callers), firstEach(callees)}
	v.neighbors[node.ID] = sides
	return sides
}

// find fills the search with the symbols matching query, case aside: an exact
// name first, then names that start with it, then names holding it, each by
// how many edges point at them, test and fixture copies last. With no query
// it lists the most used symbols, leaving copies and values out.
func (v *showView) find(query string) {
	lower := strings.ToLower(query)
	type match struct {
		node           graph.NodeV1
		class, copyish int
	}
	var matches []match
	for _, node := range v.wiring.Nodes {
		if node.Kind == "file" {
			continue
		}
		_, qualified, ok := strings.Cut(node.ID, "#")
		if !ok {
			qualified = node.Name
		}
		name, qualified := strings.ToLower(node.Name), strings.ToLower(qualified)
		copyish := 0
		if graph.IsCopy(node) {
			copyish = 1
		}
		class := 0
		switch {
		case query == "":
			if copyish == 1 || showValue(node.Kind) {
				continue
			}
		case name == lower || qualified == lower:
		case strings.HasPrefix(name, lower):
			class = 1
		case strings.Contains(qualified, lower):
			class = 2
		default:
			continue
		}
		matches = append(matches, match{node: node, class: class, copyish: copyish})
	}
	slices.SortFunc(matches, func(a, b match) int {
		return cmp.Or(
			cmp.Compare(a.class, b.class),
			cmp.Compare(a.copyish, b.copyish),
			cmp.Compare(v.degrees[b.node.ID], v.degrees[a.node.ID]),
			cmp.Compare(a.node.Name, b.node.Name),
			cmp.Compare(a.node.ID, b.node.ID),
		)
	})
	found := make([]graph.NodeV1, 0, len(matches))
	for _, match := range matches {
		found = append(found, match.node)
	}
	v.search.query, v.search.found, v.search.cursor = query, found, 0
}

// press applies a key to the view and reports whether to go on.
func (v *showView) press(key tea.Key) bool {
	if v.search != nil {
		return v.pressSearch(key)
	}
	switch key.String() {
	case "q", "esc", "ctrl+c":
		return false
	case "backspace", "b":
		v.back()
	case "/":
		v.search = &showSearch{}
		v.find("")
	case "t":
		if v.tree != nil {
			v.tree = nil
		} else {
			v.growTree(v.current().side)
		}
	default:
		if v.tree != nil {
			v.pressTree(key.String())
		} else {
			v.pressDrawing(key.String())
		}
	}
	return true
}

// pressSearch types into the search or picks one of its matches, and reports
// whether to go on.
func (v *showView) pressSearch(key tea.Key) bool {
	search := v.search
	switch key.String() {
	case "ctrl+c":
		return false
	case "esc":
		if len(v.trail) == 0 {
			return false
		}
		v.search = nil
	case "up":
		search.cursor = max(search.cursor-1, 0)
	case "down":
		search.cursor = max(min(search.cursor+1, len(search.found)-1), 0)
	case "enter":
		if len(search.found) > 0 {
			v.search = nil
			v.jump(search.found[search.cursor])
		}
	case "backspace":
		_, size := utf8.DecodeLastRuneInString(search.query)
		v.find(search.query[:len(search.query)-size])
	default:
		if key.Text != "" {
			v.find(search.query + key.Text)
		}
	}
	return true
}

// pressDrawing moves the drawing's cursor, or opens the symbol it is on.
func (v *showView) pressDrawing(key string) {
	stop := v.current()
	hits := v.edges(stop.node)[stop.side]
	switch key {
	case "up", "k":
		stop.cursor[stop.side] = max(stop.cursor[stop.side]-1, 0)
	case "down", "j":
		stop.cursor[stop.side] = max(min(stop.cursor[stop.side]+1, len(hits)-1), 0)
	case "left", "h":
		stop.side = showCallers
	case "right", "l":
		stop.side = showCallees
	case "enter":
		if len(hits) > 0 && hits[stop.cursor[stop.side]].Node != nil {
			v.open(*hits[stop.cursor[stop.side]].Node, stop.side)
		}
	}
}

// pressTree moves the tree's cursor, opens and closes rows, turns the tree
// around, or opens the symbol the cursor is on.
func (v *showView) pressTree(key string) {
	rows := v.treeRows()
	row := rows[v.tree.cursor]
	switch key {
	case "up", "k":
		v.tree.cursor = max(v.tree.cursor-1, 0)
	case "down", "j":
		v.tree.cursor = min(v.tree.cursor+1, len(rows)-1)
	case "right", "l":
		switch {
		case row.leaf:
		case !row.open:
			v.tree.open[row.key] = true
		default: // into its first row
			v.tree.cursor++
		}
	case "left", "h":
		switch {
		case row.open:
			delete(v.tree.open, row.key)
		case row.depth > 0: // up to the row it hangs from
			for v.tree.cursor--; rows[v.tree.cursor].depth >= row.depth; v.tree.cursor-- {
			}
		}
	case "tab":
		v.growTree(1 - v.tree.side)
	case "enter":
		if row.depth > 0 && row.node != nil {
			v.open(*row.node, v.tree.side)
		}
	}
}

// treeRows lists the call tree's rows in order, the current symbol first and
// each open row followed by the rows it leads to.
func (v *showView) treeRows() []showRow {
	root := v.current().node
	var rows []showRow
	var grow func(row showRow)
	grow = func(row showRow) {
		var next []graph.EdgeHit
		if row.node != nil && !row.cycle {
			next = v.edges(*row.node)[v.tree.side]
		}
		row.leaf = len(next) == 0
		row.open = v.tree.open[row.key] && !row.leaf
		rows = append(rows, row)
		if !row.open {
			return
		}
		above := strings.Split(row.key, "\x00")
		for _, hit := range next {
			grow(showRow{
				key: row.key + "\x00" + hit.ID, node: hit.Node, hit: hit, parent: row.node,
				depth: row.depth + 1, cycle: slices.Contains(above, hit.ID),
			})
		}
	}
	grow(showRow{key: root.ID, node: &root})
	return rows
}

// marker shows whether a row opens: ▾ open, ▸ closed, · a leaf, ↻ a symbol
// already above it.
func (row showRow) marker() string {
	switch {
	case row.cycle:
		return "↻"
	case row.leaf:
		return "·"
	case row.open:
		return "▾"
	}
	return "▸"
}

// name is the row's symbol name, or the ID of a target outside the graph.
func (row showRow) name() string {
	if row.node == nil {
		return row.hit.ID
	}
	return row.node.Name
}

// frame draws the view width columns wide: the symbol search or the call tree
// while shown, else the drawing. A live frame is height rows: the symbols
// opened, the column heads, the drawing, then the selected edge, the line of
// code it stands for, and the keys. A static frame is the heads and the
// drawing, as tall as its longer column, with no styles.
func (v *showView) frame(width, height int, live bool) []string {
	width = max(width, showMinWidth)
	switch {
	case v.search != nil:
		return v.searchFrame(width, height)
	case v.tree != nil:
		return v.treeFrame(width, height)
	}
	stop := v.current()
	center := stop.node
	sides := v.edges(center)

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
	box[0], box[1] = clip(box[0], inner), clip(box[1], inner)
	if utf8.RuneCountInString(box[2]) > inner {
		box[2] = clip(path.Base(center.Path)+location, inner)
	}
	if live {
		box = [3]string{showBold.Render(box[0]), showFaint.Render(box[1]), showFaint.Render(box[2])}
	}
	boxLines := strings.Split(lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(boxWidth).Render(strings.Join(box[:], "\n")), "\n")
	widths := [2]int{(width - boxWidth - 8) / 2}
	widths[1] = width - boxWidth - 8 - widths[0]

	rows := max(len(sides[showCallers]), len(sides[showCallees]), showBoxRows)
	if live {
		rows = max(height-5, showBoxRows)
	}
	boxTop := (rows - showBoxRows) / 2
	mid := boxTop + showBoxRows/2
	var columns [2][]string
	var heads [2]string
	for side := range 2 {
		columns[side], heads[side] = v.column(side, sides[side], widths[side], rows, mid, live)
	}

	var lines []string
	if live {
		lines = append(lines, v.trailLine(width))
	}
	leftHead := clip(heads[showCallers], width-widths[showCallees]-1)
	lines = append(lines, paint(live && stop.side == showCallers, showBold, leftHead)+
		strings.Repeat(" ", width-widths[showCallees]-utf8.RuneCountInString(leftHead))+
		paint(live && stop.side == showCallees, showBold, clip(heads[showCallees], widths[showCallees])))
	for row := range rows {
		middle := strings.Repeat(" ", boxWidth)
		if row >= boxTop && row < boxTop+showBoxRows {
			middle = boxLines[row-boxTop]
		}
		lines = append(lines, columns[showCallers][row]+middle+columns[showCallees][row])
	}
	if live {
		hits := sides[stop.side]
		about := []string{showFaint.Render("no " + showSideNames[stop.side]), ""}
		if len(hits) > 0 {
			about = v.edgeLines(width, center, hits[stop.cursor[stop.side]], stop.side)
		}
		keys := "↑↓ move  ←→ callers/callees  enter open  t tree  / search  backspace back  q quit"
		lines = append(append(lines, about...), showFaint.Render(clip(keys, width)))
	}
	return trimLines(lines)
}

// column draws one side of the drawing, rows tall, and names it in its head.
// Each visible edge is a line to a bus that meets the box at row mid, its
// arrow pointing the way the call goes: dashed when the edge is inferred.
// The callers' column is width+3 wide, the callees' width+5.
func (v *showView) column(side int, hits []graph.EdgeHit, width, rows, mid int, live bool) ([]string, string) {
	stop := v.current()
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
				text += " " + paint(live, showFaint, loc)
				used += 1 + utf8.RuneCountInString(loc)
			}
			if live && side == stop.side && index == cursor {
				text = showChosen.Render(text)
			}
		case count == 0 && row == mid:
			empty := clip("no "+showSideNames[side], width)
			text, used = paint(live, showFaint, empty), utf8.RuneCountInString(empty)
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

// treeFrame draws the call tree height rows tall: the symbols opened, the way
// the tree grows, the rows around the cursor's, then the selected edge, the
// line of code it stands for, and the keys.
func (v *showView) treeFrame(width, height int) []string {
	rows := v.treeRows()
	cursor := v.tree.cursor
	label := func(i int) string {
		row := rows[i]
		var text string
		if row.depth == 0 {
			text = showBold.Render(row.marker() + " " + clip(nodeLabel(*row.node), width-2))
		} else {
			name, loc := showLabel(row.hit, v.tree.side, width-4*row.depth-2)
			text = row.marker() + " " + name
			if loc != "" {
				text += " " + showFaint.Render(loc)
			}
		}
		if i == cursor {
			text = showChosen.Render(text)
		}
		return text
	}
	trees := []*tree.Tree{tree.Root(label(0)).Enumerator(tree.RoundedEnumerator).EnumeratorStyle(showFaint.PaddingRight(1)).IndenterStyle(showFaint)}
	for i := 1; i < len(rows); i++ {
		sub := tree.Root(label(i))
		trees = trees[:rows[i].depth]
		trees[len(trees)-1].Child(sub)
		trees = append(trees, sub)
	}
	drawn := strings.Split(trees[0].String(), "\n")

	listRows := max(height-5, 1)
	offset := min(max(cursor-listRows/2, 0), max(len(drawn)-listRows, 0))
	heading := showBold.Render(fmt.Sprintf("%s of %s", showSideNames[v.tree.side], rows[0].name())) +
		showFaint.Render("  tab: "+showSideNames[1-v.tree.side])
	lines := []string{v.trailLine(width), heading}
	for row := range listRows {
		line := ""
		if offset+row < len(drawn) {
			line = drawn[offset+row]
		}
		lines = append(lines, line)
	}
	about := []string{clip(nodeLabel(*rows[0].node), width), ""}
	if row := rows[cursor]; row.depth > 0 {
		about = v.edgeLines(width, *row.parent, row.hit, v.tree.side)
	}
	keys := "↑↓ move  ←→ close/open  tab callers/callees  enter open  t drawing  / search  backspace back  q quit"
	lines = append(append(lines, about...), showFaint.Render(clip(keys, width)))
	return trimLines(lines)
}

// searchFrame draws the symbol search height rows tall: what was typed and
// how many symbols match, the matches around the cursor's, and the keys.
func (v *showView) searchFrame(width, height int) []string {
	search := v.search
	count := fmt.Sprintf("%d symbol%s", len(search.found), pluralSuffix(len(search.found)))
	prompt := "› " + search.query
	gap := max(width-utf8.RuneCountInString(prompt)-1-utf8.RuneCountInString(count), 1)
	lines := []string{prompt + showChosen.Render(" ") + strings.Repeat(" ", gap) + showFaint.Render(count)}
	rows := max(height-2, 1)
	shown := min(len(search.found), rows)
	offset := min(max(search.cursor-rows/2, 0), len(search.found)-shown)
	for row := range rows {
		index := offset + row
		switch {
		case index < len(search.found):
			node := search.found[index]
			degree := ""
			if in := v.degrees[node.ID]; in > 0 {
				degree = fmt.Sprintf("%d←", in)
			}
			label := clip(nodeLabel(node), width-utf8.RuneCountInString(degree)-2)
			if index == search.cursor {
				label = showChosen.Render(label)
			}
			lines = append(lines, label+"  "+showFaint.Render(degree))
		case row == 0:
			lines = append(lines, showFaint.Render("nothing matches"))
		default:
			lines = append(lines, "")
		}
	}
	escape := "esc back"
	if len(v.trail) == 0 {
		escape = "esc quit"
	}
	lines = append(lines, showFaint.Render(clip("type to search  ↑↓ choose  enter open  "+escape, width)))
	return trimLines(lines)
}

// trailLine names the symbols opened, the current one last, keeping the end
// when they are more than width runes.
func (v *showView) trailLine(width int) string {
	names := make([]string, 0, len(v.trail))
	for _, opened := range v.trail {
		names = append(names, opened.node.Name)
	}
	trail := strings.Join(names, " › ")
	if count := utf8.RuneCountInString(trail); count > width {
		trail = "…" + string([]rune(trail)[count-width+1:])
	}
	return showBold.Render(trail)
}

// edgeLines names hit, an edge between from and one of its callers or callees
// as side says, and quotes the line of code it stands for.
func (v *showView) edgeLines(width int, from graph.NodeV1, hit graph.EdgeHit, side int) []string {
	relation := string(hit.Relation)
	if hit.Confidence == "inferred" {
		relation += " (inferred)"
	}
	// An incoming edge's line is in the caller's file, an outgoing one's in
	// the symbol's own.
	arrow, lineFile := "←", from.Path
	if side == showCallees {
		arrow = "→"
	}
	other := hit.ID + ", outside the graph"
	if hit.Node != nil {
		other = nodeLabel(*hit.Node)
		if side == showCallers {
			lineFile = hit.Node.Path
		}
	}
	quote := ""
	mention := regexp.MustCompile(`(^|[^[:alnum:]_$])` + regexp.QuoteMeta(from.Name) + `([^[:alnum:]_$]|$)`)
	if line, number, ok := quoteFor(v.root, mention, hit, lineFile, v.sources); ok {
		code := strings.ReplaceAll(strings.TrimSpace(line), "\t", " ")
		quote = showFaint.Render(clip(fmt.Sprintf("%d: %s", number, code), width))
	}
	return []string{clip(relation+" "+arrow+" "+other, width), quote}
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

// showValue reports whether kind is a value — a field, constant or variable —
// rather than code that calls.
func showValue(kind graph.Kind) bool {
	return kind == "field" || kind == "constant" || kind == "variable"
}

// trimLines drops the spaces that pad the end of each line.
func trimLines(lines []string) []string {
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return lines
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

// paint renders text in style when on, so a static drawing stays plain.
func paint(on bool, style lipgloss.Style, text string) string {
	if !on || text == "" {
		return text
	}
	return style.Render(text)
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
