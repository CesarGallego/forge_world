package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	treeOffset   int // first visible line in the task tree panel
	width        int
	height       int
	stopPresent  bool
	stopContent  string
	busy         bool
	pendingQuit  bool
	err          error
}

func Start(ctx context.Context, st *engine.State) error {
	m := model{state: st, busy: true}
	p := tea.NewProgram(&m, tea.WithAltScreen())
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

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.stopPresent {
			switch msg.String() {
			case "ctrl+c", "Q", "q":
				return m, tea.Quit
			case "y", "Y":
				if err := os.Remove(filepath.Join(m.state.Root, "loop", "stop.md")); err != nil && !os.IsNotExist(err) {
					m.err = fmt.Errorf("no se pudo borrar loop/stop.md: %w", err)
					return m, nil
				}
				m.syncStopState()
				m.err = nil
				m.busy = true
				m.logOffset = 0
				return m, m.runOnceCmd()
			case "n", "N":
				m.err = nil
				return m, nil
			}
			return m, nil
		}
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
		m.syncStopState()
		if m.pendingQuit {
			return m, tea.Quit
		}
		if m.err == nil && !m.stopPresent && m.state.StatusLine != "Plan completado." {
			m.busy = true
			return m, m.runOnceCmd()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		m.syncStopState()
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

func (m *model) syncStopState() {
	path := filepath.Join(m.state.Root, "loop", "stop.md")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			m.stopPresent = false
			m.stopContent = ""
		}
		return
	}
	m.stopPresent = true
	m.stopContent = strings.TrimSpace(string(body))
}

func (m *model) View() string {
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
	selectedTask := ""
	activeRuns := m.state.SnapshotActiveRuns()
	if m.busy && len(activeRuns) > 0 {
		if m.liveRunIndex >= len(activeRuns) {
			m.liveRunIndex = len(activeRuns) - 1
		}
		active := activeRuns[m.liveRunIndex]
		selectedTask = active.TaskName
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
			selectedTask = r.TaskName
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
	if m.stopPresent {
		footer = "EJECUCION DETENIDA por loop/stop.md | y borrar y continuar | n mantener stop | q salir"
	}
	if m.busy {
		spin := spinnerFrames[m.spinnerIndex%len(spinnerFrames)]
		footer = fmt.Sprintf("%s ejecutando... %s", spin, footer)
	}
	if m.pendingQuit {
		footer += "\nsalida pendiente: esperando a que termine la ejecucion actual (usa Q para forzar)"
	}
	footerRendered := lipgloss.NewStyle().Width(width).Render(footer)
	footerLines := lipgloss.Height(footerRendered)
	panelHeight := height - 1 - footerLines - 8 // -1 title, -footerLines footer, -2 separators, -2 box frame, -4 zellij
	if panelHeight < 3 {
		panelHeight = 3
	}

	// Box styles and exact frame sizes (border + padding), so narrow terminals
	// don't overflow due to hardcoded assumptions.
	leftBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		Padding(1).
		Height(panelHeight)
	rightBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		Padding(1).
		Height(panelHeight)
	leftHFrame := leftBox.GetHorizontalFrameSize()
	rightHFrame := rightBox.GetHorizontalFrameSize()

	// 1/3 - 2/3 split over outer widths.
	availableWidth := maxInt(1, width)
	leftOuter := availableWidth / 3
	rightOuter := availableWidth - leftOuter
	// Prevent impossible values (inner width <= 0).
	if leftOuter <= leftHFrame {
		leftOuter = leftHFrame + 1
		rightOuter = availableWidth - leftOuter
	}
	if rightOuter <= rightHFrame {
		rightOuter = rightHFrame + 1
		leftOuter = availableWidth - rightOuter
	}
	leftInner := maxInt(1, leftOuter-leftHFrame)
	rightInner := maxInt(1, rightOuter-rightHFrame)

	// Right content reserves title block inside content area.
	// Compute log viewport first so the tree panel matches its height.
	errorBlock := ""
	if m.err != nil && !m.stopPresent {
		errorBlockBody := "EJECUCION PARADA: la ultima iteracion fallo; pulsa r para reintentar.\n\nError:\n" + m.err.Error()
		errorBlock = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true).
			Render(errorBlockBody)
	}

	titleBlockLines := lipgloss.Height(logTitle) + 1
	if errorBlock != "" {
		titleBlockLines += lipgloss.Height(errorBlock) + 1
	}
	logViewportHeight := panelHeight - titleBlockLines
	if logViewportHeight < 1 {
		logViewportHeight = 1
	}

	treeViewportHeight := logViewportHeight
	treeRaw := m.state.Tree(selectedTask)
	selectedLineIdx := findSelectedLine(treeRaw)
	m.treeOffset, _ = windowHeadScroll(treeRaw, treeViewportHeight, selectedLineIdx, m.treeOffset)
	visibleTree := windowSlice(treeRaw, m.treeOffset, treeViewportHeight)
	visibleTree = highlightSelectedTreeLine(visibleTree)

	left := leftBox.Width(leftInner).Render(visibleTree)
	visibleLog, _ := windowTail(logBody, logViewportHeight, m.logOffset)
	if m.stopPresent {
		logTitle = "EJECUCION DETENIDA (loop/stop.md)"
		stopMsg := strings.TrimSpace(m.stopContent)
		if stopMsg == "" {
			stopMsg = "(stop.md vacio)"
		}
		logBody = "Motivo:\n\n" + stopMsg + "\n\nContinuar ahora?\n- y = borrar stop.md y reanudar\n- n = mantener detenido"
		visibleLog, _ = windowTail(logBody, logViewportHeight, 0)
		visibleLog = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true).Render(visibleLog)
	}
	rightBody := lipgloss.NewStyle().
		Width(rightInner).
		Height(logViewportHeight).
		MaxHeight(logViewportHeight).
		Render(visibleLog)
	rightContent := logTitle + "\n\n"
	if errorBlock != "" {
		rightContent += errorBlock + "\n\n"
	}
	rightContent += rightBody
	right := rightBox.Width(rightInner).Render(rightContent)

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

func findSelectedLine(tree string) int {
	for i, line := range strings.Split(tree, "\n") {
		if strings.Contains(line, "[*]") {
			return i
		}
	}
	return 0
}

// windowHeadScroll returns the new scroll offset using follow semantics:
// it adjusts offset minimally so that selectedLine stays within [offset, offset+height).
func windowHeadScroll(text string, height int, selectedLine int, offset int) (int, int) {
	if height <= 0 {
		return 0, 0
	}
	lines := strings.Split(text, "\n")
	total := len(lines)
	maxOffset := total - height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if selectedLine < offset {
		offset = selectedLine
	}
	if selectedLine >= offset+height {
		offset = selectedLine - height + 1
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}
	return offset, maxOffset
}

func windowSlice(text string, offset int, height int) string {
	lines := strings.Split(text, "\n")
	total := len(lines)
	end := offset + height
	if end > total {
		end = total
	}
	if offset >= total {
		return ""
	}
	return strings.Join(lines[offset:end], "\n")
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

func highlightSelectedTreeLine(tree string) string {
	lines := strings.Split(tree, "\n")
	hl := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	for i, line := range lines {
		if strings.Contains(line, "[*]") {
			lines[i] = hl.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
