package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/hsuanchenlin/control-center/internal/config"
)

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	cursorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	groupStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	errStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	cmdStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	sectionStyle   = lipgloss.NewStyle().Bold(true)
	recentStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	pinnedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	highlightStyle = lipgloss.NewStyle().Reverse(true)
)

// groupTag renders the palette's "[Group] " prefix, or "" for ungrouped
// tools.
func groupTag(t config.Tool) string {
	if t.Group == "" {
		return ""
	}
	return groupStyle.Render("[" + t.Group + "] ")
}

// highlightMatches wraps every case-insensitive occurrence of lowerQuery (an
// already-lowercased, non-empty query) in the highlight style.
func highlightMatches(line, lowerQuery string) string {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, lowerQuery) {
		return line
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, lowerQuery)
		if i < 0 {
			b.WriteString(line)
			return b.String()
		}
		b.WriteString(line[:i])
		b.WriteString(highlightStyle.Render(line[i : i+len(lowerQuery)]))
		line = line[i+len(lowerQuery):]
		lower = lower[i+len(lowerQuery):]
	}
}

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
	b.WriteString(titleStyle.Render("control-center") + dimStyle.Render(" - pick a tool") + "\n\n")
	b.WriteString(m.filter.View() + "\n\n")
	if len(m.items) == 0 {
		b.WriteString(dimStyle.Render("  no tools match") + "\n")
	} else {
		for i, item := range m.items {
			cursor := "  "
			line := m.paletteItemLine(item)
			if i == m.palCursor {
				cursor = cursorStyle.Render("› ")
				line = m.paletteItemLineCursor(item)
			}
			b.WriteString(cursor + line + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ or Ctrl-P/Ctrl-N/Ctrl-K/Ctrl-J move · Enter select · Esc clear filter · Ctrl-C exit"))
	return b.String()
}

// paletteItemLine renders one palette row.
func (m Model) paletteItemLine(item paletteItem) string {
	switch item.kind {
	case itemAction:
		return itemTags(item, "") + fmt.Sprintf("%s · %s - %s", item.tool.Name, item.action.Name, item.action.Description)
	case itemRecent:
		return itemTags(item, recentStyle.Render("[Recent] ")) +
			fmt.Sprintf("%s · %s - %s", item.tool.Name, item.action.Name, item.action.Description) +
			dimStyle.Render(" · "+relativeAge(m.now(), item.at))
	default:
		return itemTags(item, "") + fmt.Sprintf("%s%s - %s", groupTag(item.tool), item.tool.Name, item.tool.Description)
	}
}

// paletteItemLineCursor renders the highlighted palette row.
func (m Model) paletteItemLineCursor(item paletteItem) string {
	switch item.kind {
	case itemAction:
		return itemTags(item, "") + cursorStyle.Render(item.tool.Name+" · "+item.action.Name) + dimStyle.Render(" - "+item.action.Description)
	case itemRecent:
		return itemTags(item, recentStyle.Render("[Recent] ")) +
			cursorStyle.Render(item.tool.Name+" · "+item.action.Name) + dimStyle.Render(" - "+item.action.Description+" · "+relativeAge(m.now(), item.at))
	default:
		return itemTags(item, "") + groupTag(item.tool) + cursorStyle.Render(item.tool.Name) + dimStyle.Render(" - "+item.tool.Description)
	}
}

// now reports the current time, honoring the injected clock.
func (m Model) now() time.Time {
	if m.deps.Clock != nil {
		return m.deps.Clock.Now()
	}
	return time.Now()
}

// relativeAge renders how long ago a recent run happened, compactly:
// "just now", "5m ago", "2h ago", "3d ago", or the date past a month.
func relativeAge(now, at time.Time) string {
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return at.Format("2006-01-02")
	}
}

// itemTags renders the pinned tag plus any row-specific leading tag.
func itemTags(item paletteItem, lead string) string {
	if item.pinned {
		return pinnedStyle.Render("[Pinned] ") + lead
	}
	return lead
}

func (m Model) viewAction() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.tool.Name) + dimStyle.Render(" - pick an action") + "\n\n")
	for i, a := range m.tool.Actions {
		cursor := "  "
		line := fmt.Sprintf("%s - %s", a.Name, a.Description)
		if i == m.actCursor {
			cursor = cursorStyle.Render("› ")
			line = cursorStyle.Render(a.Name) + dimStyle.Render(" - "+a.Description)
		}
		b.WriteString(cursor + line + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("j/k or ↑/↓ move · l/Enter select · h/←/Esc back · Ctrl-C exit"))
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
	b.WriteString(titleStyle.Render(m.tool.Name+" · "+m.action.Name) + dimStyle.Render(" - review command") + "\n\n")
	b.WriteString(sectionStyle.Render("About to run:") + "\n\n")
	b.WriteString("  " + cmdStyle.Render(m.spec.Display) + "\n")
	if m.tool.OutputFor(m.action) == "passthrough" {
		b.WriteString("\n" + dimStyle.Render("This tool takes over the terminal; Ctrl-C is handled by the tool."))
	}
	if m.notice != "" {
		b.WriteString("\n\n" + okStyle.Render(m.notice))
	}
	b.WriteString("\n\n" + dimStyle.Render("l/Enter run · e copy command · h/←/Esc back · Ctrl-C exit"))
	return b.String()
}

func (m Model) viewOutput() string {
	var b strings.Builder
	header := titleStyle.Render(m.tool.Name+" · "+m.action.Name) + " "
	switch {
	case m.running:
		header += dimStyle.Render("- running…")
	case m.result != nil && m.result.Err != nil:
		header += errStyle.Render("- failed: " + m.result.Err.Error())
	case m.result != nil && m.result.Interrupted:
		header += errStyle.Render(fmt.Sprintf("- interrupted after %s", m.result.Elapsed.Round(1e6)))
	case m.result != nil && m.result.ExitCode != 0:
		header += errStyle.Render(fmt.Sprintf("- exit %d in %s", m.result.ExitCode, m.result.Elapsed.Round(1e6)))
	case m.result != nil:
		header += okStyle.Render(fmt.Sprintf("- done in %s", m.result.Elapsed.Round(1e6)))
	}
	b.WriteString(header + "\n")
	b.WriteString("\n")
	b.WriteString(m.viewport.View())
	if m.saving {
		b.WriteString("\n" + m.saveInput.View())
	} else if m.searching {
		b.WriteString("\n" + m.searchInput.View())
	}
	if m.notice != "" {
		b.WriteString("\n" + dimStyle.Render(m.notice))
	}
	if m.running {
		switch {
		case m.shuttingDown:
			b.WriteString("\n" + dimStyle.Render("waiting for the child to be reaped before exiting"))
		case m.tool.OutputFor(m.action) == config.OutputPassthrough:
			b.WriteString("\n" + dimStyle.Render("Ctrl-C belongs to the child · waiting for it to exit"))
		default:
			b.WriteString("\n" + dimStyle.Render("j/k or ↑/↓ scroll · Ctrl-C interrupt child · Ctrl-C again force-stop and exit"))
		}
	} else {
		footer := "j/k or ↑/↓ scroll · d/u half page · g/G top/bottom · / search · c/y copy output · s save to file · q/h/←/Esc back to palette · Ctrl-C exit"
		if m.searchQuery != "" {
			footer = "j/k or ↑/↓ scroll · n/N next/previous match · / search · c/y copy output · s save to file · q/h/←/Esc back to palette · Ctrl-C exit"
		}
		b.WriteString("\n" + dimStyle.Render(footer))
	}
	return b.String()
}
