package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"forgeworld/internal/engine"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tickMsg struct{}
type runMsg struct{ err error }

type model struct {
	state        *engine.State
	runIndex     int
	liveRunIndex int
	activeSeen   int
	spinnerIndex int
	stream       int // 0 stdout, 1 stderr
	logOffset    int // line offset from the end; 0 means follow tail
	width        int
	height       int
	busy         bool
	pendingQuit  bool
	err          error
}

func Start(ctx context.Context, st *engine.State) error {
	m := model{state: st, busy: true}
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.runOnceCmd(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) runOnceCmd() tea.Cmd {
	return func() tea.Msg {
		err := m.state.LoopOnce(context.Background())
		return runMsg{err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "Q":
			return m, tea.Quit
		case "q":
			if m.busy {
				m.pendingQuit = true
				return m, nil
			}
			return m, tea.Quit
		case "left":
			m.stream = 0
		case "right":
			m.stream = 1
		case "up", "k":
			if m.busy {
				active := m.state.SnapshotActiveRuns()
				if len(active) > 1 && m.liveRunIndex > 0 {
					m.liveRunIndex--
				}
			} else if m.runIndex > 0 {
				m.runIndex--
			}
		case "down", "j":
			if m.busy {
				active := m.state.SnapshotActiveRuns()
				if len(active) > 1 && m.liveRunIndex < len(active)-1 {
					m.liveRunIndex++
				}
			} else {
				runs := m.state.SnapshotLastRuns()
				if m.runIndex < len(runs)-1 {
					m.runIndex++
				}
			}
		case "u":
			m.logOffset += 10
		case "d":
			m.logOffset -= 10
			if m.logOffset < 0 {
				m.logOffset = 0
			}
		case "g":
			m.logOffset = 1 << 30
		case "G":
			m.logOffset = 0
		case "r":
			if m.pendingQuit {
				return m, nil
			}
			m.busy = true
			m.logOffset = 0
			return m, m.runOnceCmd()
		}
	case runMsg:
		m.busy = false
		m.liveRunIndex = 0
		m.activeSeen = 0
		m.logOffset = 0
		m.err = msg.err
		if m.pendingQuit {
			return m, tea.Quit
		}
		if m.err == nil && m.state.StatusLine != "Plan completado." {
			m.busy = true
			return m, m.runOnceCmd()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		m.syncActiveSelection()
		if m.busy {
			m.spinnerIndex = (m.spinnerIndex + 1) % len(spinnerFrames)
		}
		return m, tickCmd()
	}
	return m, nil
}

var spinnerFrames = []string{"|", "/", "-", "\\"}

func (m *model) syncActiveSelection() {
	active := m.state.SnapshotActiveRuns()
	n := len(active)
	if n == 0 {
		m.activeSeen = 0
		m.liveRunIndex = 0
		return
	}
	if n > m.activeSeen {
		// Auto-select latest started task.
		m.liveRunIndex = n - 1
	} else if m.liveRunIndex >= n {
		m.liveRunIndex = n - 1
	}
	m.activeSeen = n
}

func (m model) View() string {
	width := m.width
	height := m.height
	if width <= 0 {
		width = 140
	}
	if height <= 0 {
		height = 40
	}

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("FORGEWORLD")

	logTitle := "stdout"
	logBody := "sin ejecuciones"
	activeRuns := m.state.SnapshotActiveRuns()
	if m.busy && len(activeRuns) > 0 {
		if m.liveRunIndex >= len(activeRuns) {
			m.liveRunIndex = len(activeRuns) - 1
		}
		active := activeRuns[m.liveRunIndex]
		branch := ""
		if len(activeRuns) > 1 {
			branch = fmt.Sprintf(" | rama %d/%d", m.liveRunIndex+1, len(activeRuns))
		}
		spin := spinnerFrames[m.spinnerIndex%len(spinnerFrames)]
		if m.stream == 0 {
			logTitle = fmt.Sprintf("%s stdout (live) | %s | model=%s%s", spin, active.TaskName, active.Model, branch)
			if active.Stdout == "" {
				logBody = fmt.Sprintf("[%s] ejecutando tarea, esperando salida...", spin)
			} else {
				logBody = active.Stdout
			}
		} else {
			logTitle = fmt.Sprintf("%s stderr (live) | %s | model=%s%s", spin, active.TaskName, active.Model, branch)
			if active.Stderr == "" {
				logBody = fmt.Sprintf("[%s] sin stderr por ahora", spin)
			} else {
				logBody = active.Stderr
			}
		}
	} else {
		runs := m.state.SnapshotLastRuns()
		if len(runs) > 0 {
			if m.runIndex >= len(runs) {
				m.runIndex = len(runs) - 1
			}
			r := runs[m.runIndex]
			if m.stream == 0 {
				logTitle = fmt.Sprintf("stdout | %s | model=%s rc=%d", r.TaskName, r.Model, r.Code)
				logBody = r.Stdout
			} else {
				logTitle = fmt.Sprintf("stderr | %s | model=%s rc=%d", r.TaskName, r.Model, r.Code)
				logBody = r.Stderr
			}
		}
	}
	footer := "q salir | r iterar | left/right stdout|stderr | j/k tarea inspeccionada | u/d scroll | g/G inicio/final log"
	if m.busy {
		spin := spinnerFrames[m.spinnerIndex%len(spinnerFrames)]
		footer = fmt.Sprintf("%s ejecutando... %s", spin, footer)
	}
	if m.pendingQuit {
		footer += "\nsalida pendiente: esperando a que termine la ejecucion actual (usa Q para forzar)"
	}
	if m.err != nil {
		footer += "\nerror: " + m.err.Error()
	}
	footerRendered := lipgloss.NewStyle().Width(width).Render(footer)
	footerLines := lipgloss.Height(footerRendered)
	panelHeight := height - 1 - footerLines - 2
	if panelHeight < 3 {
		panelHeight = 3
	}

	// Use live terminal geometry from WindowSizeMsg with a 1/3 - 2/3 split.
	availableWidth := width
	if availableWidth < 24 {
		availableWidth = 24
	}
	leftWidth := availableWidth / 3
	rightWidth := availableWidth - leftWidth
	if leftWidth < 8 {
		leftWidth = 8
		rightWidth = availableWidth - leftWidth
	}
	if rightWidth < 12 {
		rightWidth = 12
		leftWidth = availableWidth - rightWidth
	}
	if leftWidth < 1 {
		leftWidth = 1
	}
	if rightWidth < 1 {
		rightWidth = 1
	}

	treeViewportHeight := panelHeight - 4
	if treeViewportHeight < 1 {
		treeViewportHeight = 1
	}
	visibleTree, _ := windowHead(m.state.Tree(), treeViewportHeight)

	left := lipgloss.NewStyle().
		Width(leftWidth).
		Height(panelHeight).
		Border(lipgloss.NormalBorder()).
		Padding(1).
		Render(visibleTree)

	logViewportHeight := panelHeight - 5
	if logViewportHeight < 1 {
		logViewportHeight = 1
	}
	visibleLog, _ := windowTail(logBody, logViewportHeight, m.logOffset)
	rightBody := lipgloss.NewStyle().
		Width(maxInt(1, rightWidth-4)).
		Height(logViewportHeight).
		MaxHeight(logViewportHeight).
		Render(visibleLog)
	right := lipgloss.NewStyle().
		Width(rightWidth).
		Height(panelHeight).
		Border(lipgloss.NormalBorder()).
		Padding(1).
		Render(logTitle + "\n\n" + rightBody)

	return title + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, left, right) + "\n" + footerRendered + "\n"
}

func windowTail(text string, height int, offset int) (string, int) {
	if height <= 0 {
		return "", 0
	}
	lines := strings.Split(text, "\n")
	maxOffset := len(lines) - height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	end := len(lines) - offset
	if end < 0 {
		end = 0
	}
	start := end - height
	if start < 0 {
		start = 0
	}
	return strings.Join(lines[start:end], "\n"), maxOffset
}

func windowHead(text string, height int) (string, int) {
	if height <= 0 {
		return "", 0
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= height {
		return text, 0
	}
	return strings.Join(lines[:height], "\n"), len(lines) - height
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
