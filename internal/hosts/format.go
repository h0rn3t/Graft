package hosts

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

func indigo(text string) string { return "\x1b[38;2;84;111;255m" + text + "\x1b[0m" }
func muted(text string) string  { return "\x1b[38;5;244m" + text + "\x1b[0m" }
func amber(text string) string  { return "\x1b[38;5;179m" + text + "\x1b[0m" }
func plain(text string) string  { return text }

// width counts UTF-16 code units, the length JavaScript pads by.
func width(text string) int {
	return len(utf16.Encode([]rune(text)))
}

func padEnd(text string, size int) string {
	if gap := size - width(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

// Tilde shortens a path under home to ~/…, for display only.
func Tilde(path, home string) string {
	if path == home || strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

func relative(from, to string) string {
	rel, err := filepath.Rel(from, to)
	if err != nil {
		return to
	}
	if rel == "." {
		return ""
	}
	return rel
}

func commonDir(paths []string) string {
	split := make([][]string, len(paths))
	for i, path := range paths {
		split[i] = strings.Split(filepath.Dir(path), string(os.PathSeparator))
	}
	if len(split) == 0 {
		return ""
	}
	first := split[0]
	i := 0
	for i < len(first) && !slices.ContainsFunc(split, func(parts []string) bool { return i >= len(parts) || parts[i] != first[i] }) {
		i++
	}
	return strings.Join(first[:i], string(os.PathSeparator))
}

// DescribeWrites summarizes one host's writes: repo-relative paths, then a
// count of machine-wide writes, and a note when the host gets no MCP server.
func DescribeWrites(writes []PlannedWrite, repo, home string, maxShown int) string {
	repoPaths := make([]string, 0)
	globals := make([]string, 0)
	hasMCP := false
	for _, write := range writes {
		if write.Scope == ScopeRepo {
			repoPaths = append(repoPaths, relative(repo, write.Path))
		} else {
			globals = append(globals, write.Path)
		}
		hasMCP = hasMCP || write.Kind == WriteMCP
	}
	parts := make([]string, 0, 3)
	if len(repoPaths) > 0 {
		if len(repoPaths) <= maxShown {
			parts = append(parts, strings.Join(repoPaths, ", "))
		} else {
			parts = append(parts, fmt.Sprintf("%s +%d more", strings.Join(repoPaths[:maxShown], ", "), len(repoPaths)-maxShown))
		}
	}
	if len(globals) > 0 {
		parts = append(parts, fmt.Sprintf("+ %d in %s%c (machine-wide)", len(globals), Tilde(commonDir(globals), home), os.PathSeparator))
	}
	if !hasMCP {
		parts = append(parts, "no MCP")
	}
	return strings.Join(parts, " · ")
}

// FormatNonInteractiveHelp is shown when there is no TTY and no selection flag.
func FormatNonInteractiveHelp(detected []string) string {
	list := "none"
	if len(detected) > 0 {
		list = strings.Join(detected, ", ")
	}
	examples := make([][2]string, 0, 4)
	if len(detected) > 0 {
		examples = append(examples,
			[2]string{"graft init --agents " + strings.Join(detected, " "), "wire these"},
			[2]string{"graft init --yes", "same, without spelling them out"})
	}
	examples = append(examples,
		[2]string{"graft init --agents claude", "Claude Code only"},
		[2]string{"graft init --dry-run", "list every file first"})
	size := 0
	for _, example := range examples {
		size = max(size, width(example[0]))
	}
	lines := []string{
		"graft init: no TTY to prompt on, and no --agents/--yes given — nothing written.",
		"detected: " + list,
		"",
	}
	for _, example := range examples {
		lines = append(lines, "  "+padEnd(example[0], size)+"   # "+example[1])
	}
	return strings.Join(lines, "\n")
}

// FormatPlan renders the --dry-run output: repo writes, then machine-wide ones.
func FormatPlan(plans []HostPlan, ids []string, repo, home string, tty bool) string {
	dim, warn := plain, plain
	if tty {
		dim, warn = muted, amber
	}
	writes := SelectedWrites(plans, ids)
	if len(writes) == 0 {
		return "would write — nothing (no agents selected)"
	}
	pad := func(rows []PlannedWrite, show func(string) string) []string {
		size := 0
		for _, row := range rows {
			size = max(size, width(show(row.Path)))
		}
		lines := make([]string, len(rows))
		for i, row := range rows {
			lines[i] = "  " + padEnd(show(row.Path), size) + "  " + dim(row.What)
		}
		return lines
	}
	repoWrites := slices.DeleteFunc(slices.Clone(writes), func(write PlannedWrite) bool { return write.Scope != ScopeRepo })
	globalWrites := slices.DeleteFunc(slices.Clone(writes), func(write PlannedWrite) bool { return write.Scope != ScopeGlobal })
	lines := make([]string, 0)
	if len(repoWrites) > 0 {
		lines = append(lines, "would write — this repo:")
		lines = append(lines, pad(repoWrites, func(path string) string { return relative(repo, path) })...)
	}
	if len(globalWrites) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, warn("would write — your machine, affects ALL repos:"))
		lines = append(lines, pad(globalWrites, func(path string) string { return Tilde(path, home) })...)
		lines = append(lines, "", dim("suppress the out-of-repo writes with --no-global"))
	}
	lines = append(lines, "", dim("nothing was written (--dry-run)"))
	return strings.Join(lines, "\n")
}

// FormatRetractions groups a retraction report by host.
func FormatRetractions(retractions []Retraction, apply bool) string {
	hit := Changed(retractions)
	if len(hit) == 0 {
		return "· nothing to remove — no graft wiring found here"
	}
	verb := "would remove"
	if apply {
		verb = "removed"
	}
	order := make([]string, 0)
	byHost := make(map[string][]Retraction)
	for _, retraction := range hit {
		if _, ok := byHost[retraction.HostID]; !ok {
			order = append(order, retraction.HostID)
		}
		byHost[retraction.HostID] = append(byHost[retraction.HostID], retraction)
	}
	lines := make([]string, 0)
	for _, host := range order {
		lines = append(lines, "\n"+host+":")
		for _, retraction := range byHost[host] {
			mark, note := "~", " ("+retraction.What+")"
			switch retraction.Action {
			case RetractUnparseable:
				mark, note = "⚠", " — not valid JSON, left untouched (remove the graft entry by hand)"
			case RetractDeleted:
				mark, note = "-", " ("+retraction.What+" — deleted)"
			}
			scope := ""
			if retraction.Scope == ScopeGlobal {
				scope = " [machine-wide]"
			}
			lines = append(lines, fmt.Sprintf("  %s %s: %s%s%s", mark, verb, retraction.Path, scope, note))
		}
	}
	return strings.TrimPrefix(strings.Join(lines, "\n"), "\n")
}

var wordmark = []string{
	"                   __ _",
	"   __ _ _ __ __ _ / _| |_",
	"  / _` | '__/ _` | |_| __|",
	" | (_| | | | (_| |  _| |_",
	"  \\__, |_|  \\__,_|_|  \\__|",
	"  |___/",
}

// Grouped renders an integer with en-US thousands separators.
func Grouped(value int) string {
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	}
	digits := strconv.Itoa(value)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return sign + digits
}

// FormatInitEpilogue renders init's closing wordmark and next steps. nodes and
// edges are shown when a graph exists.
func FormatInitEpilogue(graphBuilt bool, nodes, edges int, tty bool) string {
	mark := slices.Clone(wordmark)
	if tty {
		for i := range mark {
			mark[i] = indigo(mark[i])
		}
	}
	if graphBuilt {
		stats := fmt.Sprintf("  %s nodes · %s edges", Grouped(nodes), Grouped(edges))
		if tty {
			stats = muted(stats)
		}
		mark[4] += stats
	}
	type step struct {
		label, command string
		extra          []string
	}
	steps := make([]step, 0, 4)
	if !graphBuilt {
		steps = append(steps, step{label: "build the graph", command: "graft build"})
	}
	steps = append(steps,
		step{label: "restart your agent", command: "a new session picks up graft automatically"},
		step{label: "code as usual", command: "ask your agent to fix a bug or explain a flow —", extra: []string{"it now answers from the graph"}},
		step{label: "explore by hand", command: `graft ask "where is auth handled?" · graft callers <fn>`},
	)
	labelWidth := 0
	for i, item := range steps {
		labelWidth = max(labelWidth, width(fmt.Sprintf("%d. %s", i+1, item.label)))
	}
	column := 2 + labelWidth + 2
	lines := slices.Clone(mark)
	lines = append(lines, "")
	for i, item := range steps {
		lines = append(lines, padEnd(fmt.Sprintf("  %d. %s", i+1, item.label), column)+item.command)
		for _, extra := range item.extra {
			lines = append(lines, strings.Repeat(" ", column)+extra)
		}
	}
	lines = append(lines, "", "  share it: git add .claude && git commit — teammates run `graft build` for their own local graph")
	return strings.Join(lines, "\n")
}
