package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// buildProgressBarWidth is the number of cells in the TTY parsing bar.
// buildProgressFileWidth caps the file label so the whole line stays within
// an 80-column terminal alongside the counter, bar, and percent.
const (
	buildProgressBarWidth      = 20
	buildProgressFileWidth     = 30
	buildProgressLegacyWidth   = 50
	buildProgressTerminalWidth = 80
)

// buildProgressEnabled reports whether stderr is a terminal that can repaint
// one line with carriage returns. Piped output keeps the legacy plain
// "parsing i/n: file" line so tests, goldens, and logs stay stable.
func buildProgressEnabled(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminal(file)
}

// formatBuildProgress renders one repaintable parsing line with a bar:
// "\rparsing 3/9: [██████░░░░░░░░░░░░░░]  33% src/store.ts".
// index is zero-based; a non-positive total renders a full bar instead of
// dividing by zero.
func formatBuildProgress(index, total int, file string) string {
	percent := 100
	filled := buildProgressBarWidth
	if total > 0 {
		percent = (index + 1) * 100 / total
		filled = (index + 1) * buildProgressBarWidth / total
	}
	if filled < 0 {
		filled = 0
	}
	if filled > buildProgressBarWidth {
		filled = buildProgressBarWidth
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", buildProgressBarWidth-filled)
	runes := []rune(file)
	if len(runes) > buildProgressFileWidth {
		runes = runes[:buildProgressFileWidth]
	}
	return fmt.Sprintf("\rparsing %d/%d: [%s] %3d%% %-30s", index+1, total, bar, percent, string(runes))
}
