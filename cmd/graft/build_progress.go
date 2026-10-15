package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/h0rn3t/Graft/internal/graph"
)

// buildProgressLegacyWidth pads the plain parsing line piped output keeps.
// buildProgressFileWidth caps the file the terminal's progress line names.
const (
	buildProgressLegacyWidth = 50
	buildProgressFileWidth   = 30
)

// buildProgressRateWindow is how long the language server must have been
// asked about files before the progress estimates the time left.
const buildProgressRateWindow = 5 * time.Second

// buildProgress draws graft build's progress on a terminal in Bubble Tea: a
// spinner, the files being parsed, then the language server's stages, with a
// bar while files are parsed or asked about. In a workspace build, repo names
// the repository and its place, as "[3/51] dhcore-go".
type buildProgress struct {
	spinner      spinner.Model
	bar          progress.Model
	repo         string
	index, total int
	file         string
	lsp          graph.LSPProgress // the language server's stage, once its pass starts
	stageAt      time.Time         // when that stage began
	askedAt      time.Time         // when the first file was handed to the server
	askedFrom    int               // files handed over by then
	left         time.Duration     // estimated time to ask about the rest; 0 while unknown
	now          time.Time
	width        int
	done         bool
}

// buildStepMsg says the build is reading file, the index-th of total from zero.
type buildStepMsg struct {
	index, total int
	file         string
}

// buildLSPMsg says the language-server pass reached progress at at.
type buildLSPMsg struct {
	progress graph.LSPProgress
	at       time.Time
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

// startBuildProgress shows the build's progress on the terminal w, labeled
// with repo in a workspace build, until finish is called. finish may be
// called more than once; outside a workspace it leaves the count of files read
// on w's last line, as the plain progress leaves its last, and it returns once
// w is free.
func startBuildProgress(w io.Writer, repo string) (step func(index, total int, file string), lsp func(graph.LSPProgress), finish func()) {
	program := tea.NewProgram(newBuildProgress(repo), tea.WithInput(nil), tea.WithOutput(w))
	done := make(chan struct{})
	var final tea.Model
	go func() {
		defer close(done)
		final, _ = program.Run() // best effort: the progress only decorates the build
	}()
	step = func(index, total int, file string) {
		program.Send(buildStepMsg{index: index, total: total, file: file})
	}
	lsp = func(progress graph.LSPProgress) {
		program.Send(buildLSPMsg{progress: progress, at: time.Now()})
	}
	finish = sync.OnceFunc(func() {
		program.Send(buildDoneMsg{})
		<-done
		// Bubble Tea erases its line on the way out.
		if model, ok := final.(buildProgress); ok && repo == "" {
			writeDiagnostic(w, "%s", model.summary())
		}
	})
	return step, lsp, finish
}

func newBuildProgress(repo string) buildProgress {
	return buildProgress{
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		bar:     progress.New(progress.WithWidth(20), progress.WithoutPercentage()),
		repo:    repo,
	}
}

func (m buildProgress) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m buildProgress) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case buildStepMsg:
		m.index, m.total, m.file = msg.index, msg.total, msg.file
	case buildLSPMsg:
		if msg.progress.Stage != m.lsp.Stage {
			m.stageAt = msg.at
			if msg.progress.Stage == graph.LSPAsking {
				m.askedAt, m.askedFrom = msg.at, msg.progress.Done
			}
		}
		m.lsp, m.now, m.left = msg.progress, msg.at, 0
		if asking := msg.at.Sub(m.askedAt); msg.progress.Stage == graph.LSPAsking && asking >= buildProgressRateWindow && msg.progress.Done > m.askedFrom {
			perFile := asking / time.Duration(msg.progress.Done-m.askedFrom)
			m.left = perFile * time.Duration(msg.progress.Total-msg.progress.Done)
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case buildDoneMsg:
		m.done = true
		return m, tea.Quit
	case spinner.TickMsg:
		m.now = msg.Time
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
	if m.done {
		if m.repo != "" {
			return tea.NewView("") // The repo's own ✓ line follows.
		}
		return tea.NewView(m.summary())
	}
	line := m.spinner.View() + " "
	if m.repo != "" {
		line += m.repo + " · "
	}
	server := filepath.Base(m.lsp.Server)
	switch m.lsp.Stage {
	case graph.LSPStarting:
		line += server + ": starting…"
	case graph.LSPIndexing:
		line += fmt.Sprintf("%s: indexing… %s", server, max(m.now.Sub(m.stageAt), 0).Round(time.Second))
	case graph.LSPAsking:
		percent := float64(m.lsp.Done) / float64(max(m.lsp.Total, 1))
		line += fmt.Sprintf("%s %d/%d %s %3d%% %s",
			server, m.lsp.Done, m.lsp.Total, m.bar.ViewAs(percent), int(percent*100), clip(m.lsp.File, buildProgressFileWidth))
		if m.left > 0 {
			line += fmt.Sprintf(" · ~%s left", m.left.Round(time.Second))
		}
	default:
		if m.total == 0 {
			line += "reading the repository…"
			break
		}
		percent := float64(m.index+1) / float64(m.total)
		line += fmt.Sprintf("parsing %d/%d %s %3d%% %s", m.index+1, m.total, m.bar.ViewAs(percent), int(percent*100), clip(m.file, buildProgressFileWidth))
	}
	if m.width > 0 {
		line = lipgloss.NewStyle().MaxWidth(m.width).Render(line)
	}
	return tea.NewView(line)
}
