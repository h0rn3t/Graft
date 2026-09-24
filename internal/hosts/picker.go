package hosts

import (
	"maps"
	"slices"
	"strings"
)

// PickerRow is one selectable line of the init picker.
type PickerRow struct {
	ID        string
	Label     string
	Detected  bool
	Summary   string
	HasGlobal bool
}

// PickerState is the picker's pure state.
type PickerState struct {
	Rows    []PickerRow
	Cursor  int
	Checked map[string]bool
	Done    bool
	Aborted bool
}

// PickerKey is a keypress the picker acts on.
type PickerKey string

// Picker keys.
const (
	KeyUp    PickerKey = "up"
	KeyDown  PickerKey = "down"
	KeySpace PickerKey = "space"
	KeyAll   PickerKey = "all"
	KeyEnter PickerKey = "enter"
	KeyAbort PickerKey = "abort"
)

// InitialPickerState checks Claude Code.
func InitialPickerState(plans []HostPlan, repo, home string) PickerState {
	state := PickerState{Checked: make(map[string]bool)}
	for _, plan := range plans {
		state.Rows = append(state.Rows, PickerRow{
			ID: plan.ID, Label: plan.ID, Detected: plan.Detected,
			Summary:   DescribeWrites(plan.Writes, repo, home, 3),
			HasGlobal: slices.ContainsFunc(plan.Writes, func(write PlannedWrite) bool { return write.Scope == ScopeGlobal }),
		})
		if plan.ID == "claude" {
			state.Checked["claude"] = true
		}
	}
	return state
}

func keyOf(token string) (PickerKey, bool) {
	switch token {
	case "\x1b[A", "k":
		return KeyUp, true
	case "\x1b[B", "j":
		return KeyDown, true
	case " ":
		return KeySpace, true
	case "a":
		return KeyAll, true
	case "\r", "\n":
		return KeyEnter, true
	case "\x1b", "\x03", "q":
		return KeyAbort, true
	default:
		return "", false
	}
}

// KeysOf splits a raw terminal chunk into keypresses, consuming each CSI
// sequence whole so an arrow key never reads as a bare escape.
func KeysOf(chunk string) []PickerKey {
	keys := make([]PickerKey, 0)
	units := []rune(chunk)
	for i := 0; i < len(units); {
		end := i + 1
		if units[i] == '\x1b' && i+1 < len(units) && units[i+1] == '[' {
			end = i + 2
			for end < len(units) && (units[end] < '@' || units[end] > '~') {
				end++
			}
			end = min(end+1, len(units))
		}
		if key, ok := keyOf(string(units[i:end])); ok {
			keys = append(keys, key)
		}
		i = end
	}
	return keys
}

// ReducePicker applies one keypress.
func ReducePicker(state PickerState, key PickerKey) PickerState {
	count := len(state.Rows)
	next := state
	switch key {
	case KeyUp:
		next.Cursor = (state.Cursor - 1 + count) % count
	case KeyDown:
		next.Cursor = (state.Cursor + 1) % count
	case KeySpace:
		next.Checked = maps.Clone(state.Checked)
		id := state.Rows[state.Cursor].ID
		if next.Checked[id] {
			delete(next.Checked, id)
		} else {
			next.Checked[id] = true
		}
	case KeyAll:
		next.Checked = toggleAllHosts(state)
	case KeyEnter:
		next.Done = true
	case KeyAbort:
		next.Aborted = true
	}
	return next
}

// toggleAllHosts checks every agent, or clears them when all were checked.
func toggleAllHosts(state PickerState) map[string]bool {
	allOn := !slices.ContainsFunc(state.Rows, func(row PickerRow) bool { return !state.Checked[row.ID] })
	checked := make(map[string]bool)
	for _, row := range state.Rows {
		if !allOn {
			checked[row.ID] = true
		}
	}
	return checked
}

// RenderPicker draws the picker, coloured when tty is true.
func RenderPicker(state PickerState, tty bool) string {
	dim, hot, warn := plain, plain, plain
	if tty {
		dim, hot, warn = muted, indigo, amber
	}
	label := func(row PickerRow) string {
		if row.Detected {
			return row.Label
		}
		return row.Label + " (not detected)"
	}
	size := 0
	for _, row := range state.Rows {
		size = max(size, width(label(row)))
	}
	lines := []string{"graft init — select what to wire into this repo:", ""}
	for i, row := range state.Rows {
		here := i == state.Cursor
		box := "[ ]"
		if state.Checked[row.ID] {
			box = "[x]"
		}
		name, pointer := row.Label, " "
		if here {
			name, pointer = hot(row.Label), "›"
		}
		tag := ""
		if !row.Detected {
			tag = dim(" (not detected)")
		}
		summary := dim(row.Summary)
		if row.HasGlobal {
			summary = warn(row.Summary)
		}
		lines = append(lines, pointer+" "+box+" "+name+tag+strings.Repeat(" ", size-width(label(row)))+"  "+summary)
	}
	lines = append(lines, "", dim("↑↓ move · space toggle · a all · enter confirm · esc cancel"))
	return strings.Join(lines, "\n")
}

// PickedHostIDs lists the checked agents in plan order.
func PickedHostIDs(state PickerState) []string {
	ids := make([]string, 0)
	for _, row := range state.Rows {
		if state.Checked[row.ID] {
			ids = append(ids, row.ID)
		}
	}
	return ids
}
