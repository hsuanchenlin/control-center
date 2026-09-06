package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	cursorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	cmdStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	sectionStyle = lipgloss.NewStyle().Bold(true)
)

// View renders the current screen.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	switch m.screen {
	case screenPalette:
		return m.viewPalette()
	case screenAction:
		return m.viewAction()
	case screenForm:
		return m.viewForm()
	case screenConfirm:
		return m.viewConfirm()
	case screenOutput:
		return m.viewOutput()
	}
	return ""
}

func (m Model) viewPalette() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("control-center") + dimStyle.Render(" — pick a tool") + "\n\n")
	b.WriteString(m.filter.View() + "\n\n")
	if len(m.matches) == 0 {
		b.WriteString(dimStyle.Render("  no tools match") + "\n")
	} else {
		for i, t := range m.matches {
			cursor := "  "
			line := fmt.Sprintf("%s — %s", t.Name, t.Description)
			if i == m.palCursor {
				cursor = cursorStyle.Render("› ")
				line = cursorStyle.Render(t.Name) + dimStyle.Render(" — "+t.Description)
			}
			b.WriteString(cursor + line + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ or Ctrl-P/Ctrl-N move · Enter select · Esc clear filter · q quit · Ctrl-C exit"))
	return b.String()
}

func (m Model) viewAction() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.tool.Name) + dimStyle.Render(" — pick an action") + "\n\n")
	for i, a := range m.tool.Actions {
		cursor := "  "
		line := fmt.Sprintf("%s — %s", a.Name, a.Description)
		if i == m.actCursor {
			cursor = cursorStyle.Render("› ")
			line = cursorStyle.Render(a.Name) + dimStyle.Render(" — "+a.Description)
		}
		b.WriteString(cursor + line + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ move · Enter select · Esc back · Ctrl-C exit"))
	return b.String()
}

func (m Model) viewForm() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.tool.Name+" · "+m.action.Name) + "\n")
	b.WriteString(m.form.Model.View())
	b.WriteString("\n" + dimStyle.Render("Tab/Shift-Tab move fields · Enter submit · Esc back · Ctrl-C exit"))
	if m.notice != "" {
		b.WriteString("\n" + errStyle.Render(m.notice))
	}
	return b.String()
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.tool.Name+" · "+m.action.Name) + dimStyle.Render(" — review command") + "\n\n")
	b.WriteString(sectionStyle.Render("About to run:") + "\n\n")
	b.WriteString("  " + cmdStyle.Render(m.spec.Display) + "\n")
	if m.tool.Output == "passthrough" {
		b.WriteString("\n" + dimStyle.Render("This tool takes over the terminal while it runs."))
	}
	if m.notice != "" {
		b.WriteString("\n\n" + okStyle.Render(m.notice))
	}
	b.WriteString("\n\n" + dimStyle.Render("Enter run · e copy command · Esc back · Ctrl-C exit"))
	return b.String()
}

func (m Model) viewOutput() string {
	var b strings.Builder
	header := titleStyle.Render(m.tool.Name+" · "+m.action.Name) + " "
	switch {
	case m.running:
		header += dimStyle.Render("— running…")
	case m.result != nil && m.result.Err != nil:
		header += errStyle.Render("— failed to start: " + m.result.Err.Error())
	case m.result != nil && m.result.ExitCode != 0:
		header += errStyle.Render(fmt.Sprintf("— exit %d in %s", m.result.ExitCode, m.result.Elapsed.Round(1e6)))
	case m.result != nil:
		header += okStyle.Render(fmt.Sprintf("— done in %s", m.result.Elapsed.Round(1e6)))
	}
	b.WriteString(header + "\n\n")
	b.WriteString(m.viewport.View())
	if m.notice != "" {
		b.WriteString("\n" + dimStyle.Render(m.notice))
	}
	if m.running {
		b.WriteString("\n" + dimStyle.Render("↑/↓ scroll · Ctrl-C interrupt child"))
	} else {
		b.WriteString("\n" + dimStyle.Render("↑/↓ scroll · q/Esc back to palette · Ctrl-C exit"))
	}
	return b.String()
}
