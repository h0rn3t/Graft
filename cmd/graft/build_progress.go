package main

import (
	"fmt"
	"io"
	"os"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// buildProgressLegacyWidth pads the plain parsing line piped output keeps.
// buildProgressFileWidth caps the file the terminal's progress line names.
const (
	buildProgressLegacyWidth = 50
	buildProgressFileWidth   = 30
)

// buildProgress draws graft build's parsing on a terminal in Bubble Tea: a
// spinner, a bar filling as files are read, and the file being read.
type buildProgress struct {
	spinner      spinner.Model
	bar          progress.Model
	index, total int
	file         string
	done         bool
}

// buildStepMsg says the build is reading file, the index-th of total from zero.
type buildStepMsg struct {
	index, total int
	file         string
}

// buildDoneMsg says the build has read every file.
type buildDoneMsg struct{}

// buildProgressEnabled reports whether stderr is a terminal the progress can
// redraw. Piped output keeps the legacy plain "parsing i/n: file" line so
// tests, goldens, and logs stay stable.
func buildProgressEnabled(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminal(file)
}

// startBuildProgress shows the build's progress on the terminal w until
// finish is called; finish leaves the count of files read on w's last line,
// as the plain progress leaves its last, and returns once w is free.
func startBuildProgress(w io.Writer) (step func(index, total int, file string), finish func()) {
	program := tea.NewProgram(newBuildProgress(), tea.WithInput(nil), tea.WithOutput(w))
	done := make(chan struct{})
	var final tea.Model
	go func() {
		defer close(done)
		final, _ = program.Run() // best effort: the progress only decorates the build
	}()
	step = func(index, total int, file string) {
		program.Send(buildStepMsg{index: index, total: total, file: file})
	}
	finish = func() {
		program.Send(buildDoneMsg{})
		<-done
		// Bubble Tea erases its line on the way out.
		if model, ok := final.(buildProgress); ok {
			writeDiagnostic(w, "%s", model.summary())
		}
	}
	return step, finish
}

func newBuildProgress() buildProgress {
	return buildProgress{
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		bar:     progress.New(progress.WithWidth(20), progress.WithoutPercentage()),
	}
}

func (m buildProgress) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m buildProgress) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case buildStepMsg:
		m.index, m.total, m.file = msg.index, msg.total, msg.file
	case buildDoneMsg:
		m.done = true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// summary is the progress's last word: how many files the build read. Files
// replayed from the cache count too, so read, not parsed.
func (m buildProgress) summary() string {
	return fmt.Sprintf("✓ read %d file%s", m.total, pluralSuffix(m.total))
}

func (m buildProgress) View() tea.View {
	switch {
	case m.done:
		return tea.NewView(m.summary())
	case m.total == 0:
		return tea.NewView(m.spinner.View() + " reading the repository…")
	}
	percent := float64(m.index+1) / float64(m.total)
	return tea.NewView(fmt.Sprintf("%s parsing %d/%d %s %3d%% %s",
		m.spinner.View(), m.index+1, m.total, m.bar.ViewAs(percent), int(percent*100), clip(m.file, buildProgressFileWidth)))
}
