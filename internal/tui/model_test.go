package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
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

[[tool.action]]
name = "goto"
description = "shares the dest key with go"
args = ["goto"]

[[tool.action.param]]
key = "dest"
label = "Destination"
type = "text"
required = true
flag = "--dest"

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

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

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

func newTestModelWithClock(t *testing.T, clock executor.Clock) Model {
	t.Helper()
	m, _ := newTestModel(t)
	m.deps.Clock = clock
	m.signals.clock = clock
	return m
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

func TestPaletteQIsLiteralInput(t *testing.T) {
	// The palette filter always has focus, so q must never quit: every
	// printable rune is literal input (firstmate decision on
	// palette-q-steals-input).
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("q"))
	m = asModel(t, tm)
	if m.quitting {
		t.Fatal("q quit the app from an empty filter")
	}
	if m.filter.Value() != "q" {
		t.Fatalf("q not typed into filter: %q", m.filter.Value())
	}
	tm, _ = m.Update(runes("a"))
	m = asModel(t, tm)
	if m.filter.Value() != "qa" {
		t.Fatalf("filter = %q", m.filter.Value())
	}
	// Esc clears the filter instead of quitting.
	tm, _ = m.Update(key(tea.KeyEsc))
	m = asModel(t, tm)
	if m.filter.Value() != "" || m.quitting {
		t.Fatalf("esc: filter=%q quitting=%v", m.filter.Value(), m.quitting)
	}
	// Ctrl-C remains the global exit.
	tm, cmd := m.Update(key(tea.KeyCtrlC))
	m = asModel(t, tm)
	if !m.quitting || cmd == nil {
		t.Fatal("ctrl+c did not quit the palette")
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
	if m.preserved[formStateKey{toolID: "multi", actionName: "go"}]["dest"] != "x" {
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
	stateKey := formStateKey{toolID: "solo", actionName: "run"}
	m.preserved[stateKey] = form.Values{"n": "3"}
	tm, _ = m.enterConfirm(m.preserved[stateKey])
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
	// Second Ctrl-C requests force-stop but waits for cleanup before quitting.
	tm, cmd = m.Update(key(tea.KeyCtrlC))
	m = asModel(t, tm)
	if !m.shuttingDown || m.quitting || cmd != nil {
		t.Fatal("second ctrl+c did not defer quitting until child completion")
	}
	tm, cmd = m.Update(childDoneMsg(executor.Result{Interrupted: true}))
	m = asModel(t, tm)
	if cmd == nil || m.running || !m.quitting {
		t.Fatal("child completion did not quit after cleanup")
	}
}

func TestForcedShutdownKeepsNoticeVisibleWhileChildIsReaped(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // start run
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyCtrlC))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyCtrlC)) // force-stop, quit deferred
	m = asModel(t, tm)

	view := m.View()
	if view == "" {
		t.Fatal("deferred quit blanked the screen before the child was reaped")
	}
	if !strings.Contains(view, "stopping child") {
		t.Fatalf("shutdown notice not rendered:\n%s", view)
	}
	if !strings.Contains(view, "waiting for the child to be reaped") {
		t.Fatalf("footer still advertises interrupt controls:\n%s", view)
	}
	// Only the actual quit blanks the screen.
	tm, _ = m.Update(childDoneMsg(executor.Result{Interrupted: true}))
	m = asModel(t, tm)
	if m.View() != "" {
		t.Fatalf("view not blanked after quitting:\n%s", m.View())
	}
}

func TestPassthroughCtrlCRemainsOwnedByChild(t *testing.T) {
	m, _ := newTestModel(t)
	m.screen = screenOutput
	m.running = true
	m.tool.Output = config.OutputPassthrough
	m.control = executor.NewRunControl()
	tm, cmd := m.Update(key(tea.KeyCtrlC))
	m = asModel(t, tm)
	if m.quitting || m.shuttingDown || m.notice != "" || cmd != nil {
		t.Fatal("parent handled passthrough Ctrl-C")
	}
	res := executor.Result{RestoreErr: errors.New("restore failed")}
	tm, cmd = m.Update(childDoneMsg(res))
	m = asModel(t, tm)
	if cmd == nil || m.FatalError() == nil {
		t.Fatal("passthrough restore failure was not propagated to shutdown")
	}
}

func TestProcessSignalsFollowActiveRunLifecycle(t *testing.T) {
	m, _ := newTestModel(t)
	m.screen = screenOutput
	m.running = true
	m.control = executor.NewRunControl()
	m.signals.begin(m.control, false)

	tm, _ := m.Update(signalMsg{sig: os.Interrupt})
	m = asModel(t, tm)
	if !strings.Contains(m.notice, "interrupting child") || m.shuttingDown || m.quitting {
		t.Fatal("first process interrupt did not interrupt capture")
	}
	tm, _ = m.Update(signalMsg{sig: syscall.SIGTERM})
	m = asModel(t, tm)
	if !m.shuttingDown || m.quitting {
		t.Fatal("termination signal did not defer quit for cleanup")
	}
	tm, cmd := m.Update(childDoneMsg(executor.Result{Interrupted: true}))
	m = asModel(t, tm)
	if cmd == nil || m.running || !m.quitting {
		t.Fatal("capture cleanup did not complete pending signal shutdown")
	}
}

func TestDelayedPassthroughInterruptCannotQuitIdleModel(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	m := newTestModelWithClock(t, clock)
	m.signals.begin(executor.NewRunControl(), true)
	if pending := m.signals.end(); pending {
		t.Fatal("passthrough ended with unexpected pending quit")
	}
	tm, _ := m.Update(signalMsg{sig: os.Interrupt})
	m = asModel(t, tm)
	if m.quitting {
		t.Fatal("delayed passthrough interrupt quit idle model")
	}

	// Once the passthrough hand-back window closes, an interrupt is the
	// operator asking control-center itself to stop.
	clock.now = clock.now.Add(passthroughInterruptGrace)
	tm, cmd := m.Update(signalMsg{sig: os.Interrupt})
	m = asModel(t, tm)
	if !m.quitting || cmd == nil {
		t.Fatal("interrupt after the passthrough grace window did not quit")
	}
}

func TestIdleExternalInterruptQuits(t *testing.T) {
	// kill -INT with no child running must still stop control-center; the
	// program installs tea.WithoutSignalHandler, so this is the only path.
	m, _ := newTestModel(t)
	tm, cmd := m.Update(signalMsg{sig: os.Interrupt})
	m = asModel(t, tm)
	if !m.quitting || cmd == nil {
		t.Fatal("idle external interrupt did not quit the app")
	}
}

func TestPassthroughGraceWindowIsClosedByALaterRun(t *testing.T) {
	// The suppression window belongs to the passthrough run that opened it. A
	// capture run that starts and finishes inside the window must not inherit
	// it and swallow an operator's kill -INT.
	clock := &fakeClock{now: time.Unix(2000, 0)}
	m := newTestModelWithClock(t, clock)
	m.signals.begin(executor.NewRunControl(), true)
	m.signals.end()

	m.signals.begin(executor.NewRunControl(), false)
	m.signals.end()
	if action := m.signals.route(os.Interrupt); action != signalQuit {
		t.Fatalf("interrupt after a capture run routed as %v, want signalQuit", action)
	}
}

func TestSignalIsRoutedAgainstStateAtDeliveryTime(t *testing.T) {
	// A SIGINT that arrives while the model is idle must not quit past a run
	// that started before Update got to the signal: routing happens inside
	// Update, so the live child is interrupted and reaped instead.
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // confirm
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // start run
	m = asModel(t, tm)
	if !m.running {
		t.Fatal("run did not start")
	}

	tm, _ = m.Update(signalMsg{sig: os.Interrupt})
	m = asModel(t, tm)
	if m.quitting {
		t.Fatal("interrupt quit past a live child instead of interrupting it")
	}
	if !strings.Contains(m.notice, "interrupting child") {
		t.Fatalf("notice = %q", m.notice)
	}
	tm, cmd := m.Update(childDoneMsg(executor.Result{Interrupted: true}))
	m = asModel(t, tm)
	if m.quitting || cmd != nil {
		t.Fatal("a single interrupt quit the app instead of returning to the output screen")
	}
}

func TestSignalWatchRearmsAndForwardsRawSignals(t *testing.T) {
	m, _ := newTestModel(t)
	ch := make(chan os.Signal, 1)
	m.deps.Signals = ch
	m.screen = screenOutput
	m.running = true
	m.control = executor.NewRunControl()
	m.signals.begin(m.control, false)

	tm, cmd := m.Update(signalMsg{sig: os.Interrupt})
	m = asModel(t, tm)
	if cmd == nil {
		t.Fatal("signal watch was not re-armed after routing")
	}
	ch <- syscall.SIGTERM
	next, ok := cmd().(signalMsg)
	if !ok || next.sig != syscall.SIGTERM {
		t.Fatalf("re-armed watch delivered %#v, want the raw SIGTERM", next)
	}
	tm, _ = m.Update(next)
	m = asModel(t, tm)
	if !m.shuttingDown || m.quitting {
		t.Fatal("re-delivered SIGTERM did not force-stop and defer the quit")
	}
}

func TestSignalAfterChildCompletionDoesNotRepaintStaleNotice(t *testing.T) {
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("plain"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // start run
	m = asModel(t, tm)
	tm, _ = m.Update(childDoneMsg(executor.Result{ExitCode: 0}))
	m = asModel(t, tm)

	tm, _ = m.Update(signalMsg{sig: os.Interrupt})
	m = asModel(t, tm)
	if strings.Contains(m.notice, "interrupting child") {
		t.Fatalf("completed run repainted a stale interrupt notice: %q", m.notice)
	}
	if !m.quitting {
		t.Fatal("interrupt on a finished capture run did not quit")
	}
}

func TestRunningFooterMatchesOutputMode(t *testing.T) {
	m, _ := newTestModel(t)
	m.screen = screenOutput
	m.running = true
	m.viewport = viewport.New(80, 10)
	m.tool.Output = config.OutputPassthrough
	if view := m.View(); !strings.Contains(view, "Ctrl-C belongs to the child") || strings.Contains(view, "force-stop") {
		t.Fatalf("passthrough footer = %q", view)
	}
	m.tool.Output = config.OutputCapture
	if view := m.View(); !strings.Contains(view, "force-stop and exit") {
		t.Fatalf("capture footer = %q", view)
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

func TestFormValuesPreservedAcrossReselect(t *testing.T) {
	// Single-action tool: edits survive Esc to the palette and reselecting
	// the tool (firstmate decision on form-values-not-preserved-per-action).
	m, _ := newTestModel(t)
	tm, _ := m.Update(runes("solo"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // straight to form
	m = asModel(t, tm)
	tm, _ = m.Update(runes("7"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEsc)) // back to palette
	m = asModel(t, tm)
	if m.screen != screenPalette {
		t.Fatalf("screen = %v", m.screen)
	}
	if m.preserved[formStateKey{toolID: "solo", actionName: "run"}]["n"] != "7" {
		t.Fatalf("edits lost on esc: %v", m.preserved)
	}
	tm, _ = m.Update(key(tea.KeyEnter)) // reselect same tool
	m = asModel(t, tm)
	if m.screen != screenForm || m.form.Snapshot()["n"] != "7" {
		t.Fatalf("form not rehydrated after reselect: %v", m.form.Snapshot())
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

func TestRunningOutputPreservesScrollPositionUntilBottom(t *testing.T) {
	m, _ := newTestModel(t)
	m.screen = screenOutput
	m.running = true
	m.rendered = -1
	m.buffer = executor.NewLineBuffer(100)
	m.viewport = viewport.New(40, 3)
	m.buffer.Write([]byte("one\ntwo\nthree\nfour\nfive\nsix"))
	m.refreshViewport()
	if !m.viewport.AtBottom() {
		t.Fatal("initial output did not follow to bottom")
	}

	m.viewport.LineUp(2)
	wantOffset := m.viewport.YOffset
	m.buffer.Write([]byte("\nseven"))
	m.refreshViewport()
	if m.viewport.YOffset != wantOffset {
		t.Fatalf("streaming output moved scroll position from %d to %d", wantOffset, m.viewport.YOffset)
	}

	m.viewport.GotoBottom()
	m.buffer.Write([]byte("\neight"))
	m.refreshViewport()
	if !m.viewport.AtBottom() {
		t.Fatal("output did not resume following after returning to bottom")
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
	done, ok := msg.(childDoneMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	res := executor.Result(done)
	if res.Err == nil {
		t.Fatal("missing terminal boundary not reported")
	}
	tm, _ = m.Update(done)
	m = asModel(t, tm)
	if v := m.View(); !strings.Contains(v, "terminal boundary") {
		t.Fatalf("view missing the wiring error:\n%s", v)
	}
}

func TestFormValuesDoNotLeakBetweenActions(t *testing.T) {
	// Actions sharing a param key must not inherit each other's edits.
	m, _ := newTestModel(t)
	tm, _ := m.Update(key(tea.KeyEnter)) // multi
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter)) // action "go"
	m = asModel(t, tm)
	tm, _ = m.Update(runes("x"))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEsc)) // back to action picker
	m = asModel(t, tm)
	// Move to "goto" (index 2) and enter its form.
	tm, _ = m.Update(key(tea.KeyCtrlN))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyCtrlN))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	if m.action.Name != "goto" {
		t.Fatalf("action = %q", m.action.Name)
	}
	if got := m.form.Snapshot()["dest"]; got != "" {
		t.Fatalf("dest leaked across actions: %q", got)
	}
	// And the original action's value is still intact.
	tm, _ = m.Update(key(tea.KeyEsc))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyCtrlP))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyCtrlP))
	m = asModel(t, tm)
	tm, _ = m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	if m.action.Name != "go" || m.form.Snapshot()["dest"] != "x" {
		t.Fatalf("go's edits lost: action=%q snapshot=%v", m.action.Name, m.form.Snapshot())
	}
}

func TestFormStateKeyDoesNotCollideOnSlashes(t *testing.T) {
	first := Model{tool: config.Tool{ID: "a/b"}, action: config.Action{Name: "c"}}
	second := Model{tool: config.Tool{ID: "a"}, action: config.Action{Name: "b/c"}}
	if first.formKey() == second.formKey() {
		t.Fatal("distinct tool/action pairs produced the same form-state key")
	}
}
