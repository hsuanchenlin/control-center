package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hsuanchenlin/control-center/internal/config"
	"github.com/hsuanchenlin/control-center/internal/executor"
	"github.com/hsuanchenlin/control-center/internal/form"
	"github.com/hsuanchenlin/control-center/internal/registry"
)

const modelManifest = `
[[tool]]
id = "multi"
name = "Multi Tool"
description = "two actions"
executable = "multi-cli"

[[tool.action]]
name = "go"
description = "go somewhere"
args = ["go"]

[[tool.action.param]]
key = "dest"
label = "Destination"
type = "text"
required = true
flag = "--dest"

[[tool.action]]
name = "bare"
description = "no params"
args = ["bare"]

[[tool]]
id = "solo"
name = "Solo Tool"
description = "one action with params"
executable = "solo-cli"

[[tool.action]]
name = "run"
description = "the only action"
args = ["run"]

[[tool.action.param]]
key = "n"
label = "N"
type = "number"
flag = "--n"

[[tool]]
id = "plain"
name = "Plain Tool"
description = "one action, no params"
executable = "plain-cli"

[[tool.action]]
name = "status"
description = "show status"
args = ["status"]
`

type fakeClipboard struct {
	last string
	err  error
}

func (f *fakeClipboard) WriteString(s string) error {
	f.last = s
	return f.err
}

type nopTerminal struct{}

func (nopTerminal) Release() error { return nil }
func (nopTerminal) Restore() error { return nil }

func newTestModel(t *testing.T) (Model, *fakeClipboard) {
	t.Helper()
	cfg, err := config.Parse([]byte(modelManifest), "test")
	if err != nil {
		t.Fatal(err)
	}
	clip := &fakeClipboard{}
	runner := &executor.Runner{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		StartProcess: func(ctx context.Context, path string, args []string, stdout, stderr io.Writer) (func() (int, error), error) {
			return func() (int, error) { return 0, nil }, nil
		},
	}
	m := New(Deps{
		Registry:  registry.New(cfg),
		Runner:    runner,
		Clipboard: clip,
		Terminal:  nopTerminal{},
	})
	m.width, m.height = 100, 40
	return m, clip
}

func key(t tea.KeyType, runes ...rune) tea.KeyMsg {
	return tea.KeyMsg{Type: t, Runes: runes}
}

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func asModel(t *testing.T, tm tea.Model) Model {
	t.Helper()
	m, ok := tm.(Model)
	if !ok {
		t.Fatalf("unexpected model type %T", tm)
	}
	return m
}

func TestPaletteInitialState(t *testing.T) {
	m, _ := newTestModel(t)
	if m.screen != screenPalette || len(m.matches) != 3 {
		t.Fatalf("screen=%v matches=%d", m.screen, len(m.matches))
	}
	if view := m.View(); !strings.Contains(view, "Multi Tool") {
		t.Fatalf("view missing tools:\n%s", view)
	}
}

func TestPaletteFilterNarrows(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("solo"))
	m = asModel(t, tm)
	if len(m.matches) != 1 || m.matches[0].ID != "solo" {
		t.Fatalf("matches = %v", m.matches)
	}
}

func TestPaletteQQuitsOnlyWhenFilterEmpty(t *testing.T) {
	m, _ := newTestModel(t)
	// Typing 'q' into a nonempty filter must not quit.
	tm, _ := m.Update(runes("a"))
	m = asModel(t, tm)
	tm, cmd := m.Update(runes("q"))
	m = asModel(t, tm)
	if m.quitting {
		t.Fatal("q stolen from filter: app quit")
	}
	if m.filter.Value() != "aq" {
		t.Fatalf("q not typed into filter: %q", m.filter.Value())
	}
	// With an empty filter, q quits.
	tm, _ = m.Update(key(tea.KeyEsc))
	m = asModel(t, tm)
	if m.filter.Value() != "" {
		t.Fatalf("esc did not clear filter: %q", m.filter.Value())
	}
	tm, cmd = m.Update(runes("q"))
	m = asModel(t, tm)
	if !m.quitting || cmd == nil {
		t.Fatal("q did not quit from empty-filter palette")
	}
}

func TestPaletteCtrlPNNavigation(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(key(tea.KeyCtrlN))
	m = asModel(t, tm)
	if m.palCursor != 1 {
		t.Fatalf("ctrl+n cursor = %d", m.palCursor)
	}
	tm, _ = m.Update(key(tea.KeyCtrlP))
	m = asModel(t, tm)
	if m.palCursor != 0 {
		t.Fatalf("ctrl+p cursor = %d", m.palCursor)
	}
	// Cursor clamps at the top.
	tm, _ = m.Update(key(tea.KeyCtrlP))
	m = asModel(t, tm)
	if m.palCursor != 0 {
		t.Fatalf("cursor escaped top: %d", m.palCursor)
	}
}

func TestMultiActionToolShowsActionScreen(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(key(tea.KeyEnter)) // "multi" is first
	m = asModel(t, tm)
	if m.screen != screenAction || m.tool.ID != "multi" {
		t.Fatalf("screen=%v tool=%v", m.screen, m.tool.ID)
	}
}

func TestSingleActionSkipsActionScreen(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("solo"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	if m.screen != screenForm {
		t.Fatalf("screen = %v, want form (action screen skipped)", m.screen)
	}
}

func TestSingleActionNoParamsSkipsToConfirm(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	if m.screen != screenConfirm {
		t.Fatalf("screen = %v, want confirm", m.screen)
	}
	if m.spec.Display != "plain-cli status" {
		t.Fatalf("spec = %q", m.spec.Display)
	}
}

func TestActionScreenBack(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEsc))
	m = asModel(t, tm)
	if m.screen != screenPalette {
		t.Fatalf("screen = %v", m.screen)
	}
}

func TestFormEscBackPreservesEdits(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(key(tea.KeyEnter)) // multi
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // action "go" -> form
	m = asModel(t, tm)
	if m.screen != screenForm {
		t.Fatalf("screen = %v", m.screen)
	}
	// Type into the huh input, then back out.
	tm, _ = m.Update(runes("x"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEsc))
	m = asModel(t, tm)
	if m.screen != screenAction {
		t.Fatalf("esc did not return to action: %v", m.screen)
	}
	if m.preserved["dest"] != "x" {
		t.Fatalf("edits lost: %v", m.preserved)
	}
	// Re-entering the form keeps the edit.
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	if m.form.Snapshot()["dest"] != "x" {
		t.Fatalf("form not rehydrated: %v", m.form.Snapshot())
	}
}

func TestConfirmCopyUsesClipboardBoundary(t *testing.T) {
	m, clip := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, cmd := m.Update(runes("e"))
	m = asModel(t, tm)
	if cmd == nil {
		t.Fatal("e produced no copy command")
	}
	msg := cmd()
	if cm, ok := msg.(copiedMsg); !ok || cm.err != nil {
		t.Fatalf("msg = %v", msg)
	}
	if clip.last != "plain-cli status" {
		t.Fatalf("clipboard = %q", clip.last)
	}
}

func TestConfirmCopyFailureReported(t *testing.T) {
	m, clip := newTestModel(t)
	clip.err = errors.New("no clipboard")
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, cmd := m.Update(runes("e"))
	m = asModel(t, tm)
	tm, _ = m.Update(cmd())
	m = asModel(t, tm)
	if !strings.Contains(m.notice, "copy failed") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestConfirmEscReturnsToForm(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("solo"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // form
	m = asModel(t, tm)
	m.preserved = form.Values{"n": "3"}
	tm, _ = m.enterConfirm(m.preserved)
	m = asModel(t, tm)
	if m.screen != screenConfirm {
		t.Fatalf("screen = %v", m.screen)
	}
	tm, _ = m.Update(key(tea.KeyEsc))
	m = asModel(t, tm)
	if m.screen != screenForm {
		t.Fatalf("screen = %v", m.screen)
	}
	if m.form.Snapshot()["n"] != "3" {
		t.Fatalf("value lost: %v", m.form.Snapshot())
	}
}

func TestRunFlowAndOutputDismiss(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // confirm
	m = asModel(t, tm)
	tm, cmd := m.Update(key(tea.KeyEnter)) // run
	m = asModel(t, tm)
	if m.screen != screenOutput || !m.running || cmd == nil {
		t.Fatalf("screen=%v running=%v", m.screen, m.running)
	}
	// Simulate child completion.
	tm, _ = m.Update(childDoneMsg(executor.Result{ExitCode: 0}))
	m = asModel(t, tm)
	if m.running || m.result == nil {
		t.Fatal("result not recorded")
	}
	if v := m.View(); !strings.Contains(v, "done") {
		t.Fatalf("view missing status:\n%s", v)
	}
	// q dismisses back to the palette.
	tm, _ = m.Update(runes("q"))
	m = asModel(t, tm)
	if m.screen != screenPalette {
		t.Fatalf("q did not dismiss output: %v", m.screen)
	}
}

func TestCtrlCInterruptsChildBeforeQuitting(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // start run
	m = asModel(t, tm)
	if !m.running {
		t.Fatal("not running")
	}
	// First Ctrl-C interrupts the child, does not quit.
	tm, cmd := m.Update(key(tea.KeyCtrlC))
	m = asModel(t, tm)
	if m.quitting || cmd != nil {
		t.Fatal("first ctrl+c quit the app instead of interrupting the child")
	}
	// Second Ctrl-C quits.
	m.running = false
	m.cancel = nil
	tm, cmd = m.Update(key(tea.KeyCtrlC))
	m = asModel(t, tm)
	if !m.quitting || cmd == nil {
		t.Fatal("second ctrl+c did not quit")
	}
}

func TestMissingExecutableShowsOutputError(t *testing.T) {
	m, _ := newTestModel(t)
	m.deps.Runner.LookPath = func(string) (string, error) { return "", errors.New("nope") }
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // run
	m = asModel(t, tm)
	if m.screen != screenOutput || m.result == nil || m.result.Err == nil {
		t.Fatalf("screen=%v result=%v", m.screen, m.result)
	}
	if v := m.View(); !strings.Contains(v, "not found in PATH") {
		t.Fatalf("view missing error:\n%s", v)
	}
}

func TestWindowResizeSafe(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	m = asModel(t, tm)
	tm, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = asModel(t, tm).View() // must not panic
}

func TestQuitFromPaletteViaQ(t *testing.T) {
	m, _ := newTestModel(t)
	tm, cmd := m.Update(runes("q"))
	m = asModel(t, tm)
	if !m.quitting || cmd == nil {
		t.Fatal("q did not quit")
	}
	if m.View() != "" {
		t.Fatal("quitting view should be empty")
	}
}

const passthroughManifest = `
[[tool]]
id = "inter"
name = "Interactive Tool"
description = "owns the terminal"
executable = "inter-cli"
output = "passthrough"

[[tool.action]]
name = "shell"
description = "interactive session"
args = ["shell"]
`

func TestNoticeIsClearedWhenRunStartsAndFinishes(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // confirm
	m = asModel(t, tm)
	tm, cmd := m.Update(runes("e")) // copy
	m = asModel(t, tm)
	tm, _ = m.Update(cmd())
	m = asModel(t, tm)
	if m.notice == "" {
		t.Fatal("copy notice not set")
	}
	tm, _ = m.Update(key(tea.KeyEnter)) // run
	m = asModel(t, tm)
	if m.notice != "" {
		t.Fatalf("stale notice carried into the output screen: %q", m.notice)
	}
	tm, _ = m.Update(key(tea.KeyCtrlC)) // interrupt notice
	m = asModel(t, tm)
	if m.notice == "" {
		t.Fatal("interrupt notice not set")
	}
	tm, _ = m.Update(childDoneMsg(executor.Result{Interrupted: true}))
	m = asModel(t, tm)
	if m.notice != "" {
		t.Fatalf("interrupt notice outlived the child: %q", m.notice)
	}
	if v := m.View(); !strings.Contains(v, "interrupted") {
		t.Fatalf("interrupted run not labelled:\n%s", v)
	}
}

func TestRunningOutputFollowsPartialLines(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // run
	m = asModel(t, tm)
	m.buffer.Write([]byte("progress 10%"))
	tm, _ = m.Update(outputTickMsg(time.Time{}))
	m = asModel(t, tm)
	if !strings.Contains(m.viewport.View(), "progress 10%") {
		t.Fatalf("viewport missing streamed output:\n%s", m.viewport.View())
	}
	m.buffer.Write([]byte("\rprogress 20%"))
	tm, _ = m.Update(outputTickMsg(time.Time{}))
	m = asModel(t, tm)
	if !strings.Contains(m.viewport.View(), "progress 20%") {
		t.Fatalf("viewport did not follow output appended to the current line:\n%s", m.viewport.View())
	}
}

func TestPassthroughWithoutTerminalFailsInsteadOfPanicking(t *testing.T) {
	cfg, err := config.Parse([]byte(passthroughManifest), "test")
	if err != nil {
		t.Fatal(err)
	}
	m := New(Deps{
		Registry: registry.New(cfg),
		Runner: &executor.Runner{
			LookPath:        func(name string) (string, error) { return "/usr/bin/" + name, nil },
			StdinIsTerminal: func() bool { return true },
		},
		Clipboard: &fakeClipboard{},
	})
	m.width, m.height = 100, 40
	tm, _ := m.Update(key(tea.KeyEnter)) // single action, no params -> confirm
	m = asModel(t, tm)
	if m.screen != screenConfirm {
		t.Fatalf("screen = %v", m.screen)
	}
	tm, cmd := m.Update(key(tea.KeyEnter)) // run
	m = asModel(t, tm)
	if cmd == nil {
		t.Fatal("run produced no command")
	}
	msg := cmd()
	failed, ok := msg.(runFailedMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	tm, _ = m.Update(failed)
	m = asModel(t, tm)
	if v := m.View(); !strings.Contains(v, "terminal boundary") {
		t.Fatalf("view missing the wiring error:\n%s", v)
	}
}
