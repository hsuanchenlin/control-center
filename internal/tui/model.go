// Package tui implements the Bubble Tea state machine: palette, action
// picker, parameter form, confirmation, and output screens.
package tui

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/hsuanchenlin/control-center/internal/command"
	"github.com/hsuanchenlin/control-center/internal/config"
	"github.com/hsuanchenlin/control-center/internal/executor"
	"github.com/hsuanchenlin/control-center/internal/form"
	"github.com/hsuanchenlin/control-center/internal/registry"
)

type screen int

type formStateKey struct {
	toolID     string
	actionName string
}

const (
	screenPalette screen = iota
	screenAction
	screenForm
	screenConfirm
	screenOutput
)

// Deps are the injected boundaries the TUI needs.
type Deps struct {
	Registry  *registry.Registry
	Runner    *executor.Runner
	Clipboard executor.Clipboard
	Terminal  executor.Terminal // required only when a tool uses passthrough
	Signals   <-chan os.Signal
	// MaxOutputLines bounds the retained child output (default 5000).
	MaxOutputLines int
}

// Messages driving the async child lifecycle.
type (
	outputTickMsg time.Time
	childDoneMsg  executor.Result
	copiedMsg     struct{ err error }
	signalMsg     routedSignal
)

type signalAction int

const (
	signalIgnored signalAction = iota
	signalInterrupted
	signalForced
	signalQuit
)

type routedSignal struct {
	generation uint64
	action     signalAction
}

type activeRun struct {
	generation  uint64
	control     *executor.RunControl
	passthrough bool
	interrupted bool
	pendingQuit bool
}

type signalLifecycle struct {
	mu     sync.Mutex
	next   uint64
	active *activeRun
}

func (s *signalLifecycle) begin(control *executor.RunControl, passthrough bool) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.active = &activeRun{generation: s.next, control: control, passthrough: passthrough}
	return s.next
}

func (s *signalLifecycle) route(sig os.Signal) routedSignal {
	s.mu.Lock()
	if s.active == nil {
		s.mu.Unlock()
		if sig == os.Interrupt {
			return routedSignal{action: signalIgnored}
		}
		return routedSignal{action: signalQuit}
	}
	run := s.active
	if run.passthrough && sig == os.Interrupt {
		s.mu.Unlock()
		return routedSignal{generation: run.generation, action: signalIgnored}
	}
	if sig == syscall.SIGTERM || run.interrupted {
		run.pendingQuit = true
		run.interrupted = true
		control := run.control
		generation := run.generation
		s.mu.Unlock()
		control.ForceStop()
		return routedSignal{generation: generation, action: signalForced}
	}
	run.interrupted = true
	control := run.control
	generation := run.generation
	s.mu.Unlock()
	control.Interrupt()
	return routedSignal{generation: generation, action: signalInterrupted}
}

func (s *signalLifecycle) end(generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.generation != generation {
		return false
	}
	pendingQuit := s.active.pendingQuit
	s.active = nil
	return pendingQuit
}

// Model is the root Bubble Tea model.
type Model struct {
	deps   Deps
	screen screen
	width  int
	height int

	// palette
	filter    textinput.Model
	matches   []config.Tool
	palCursor int

	// action picker
	tool      config.Tool
	actCursor int

	// form / confirm
	form      *form.Form
	preserved map[formStateKey]form.Values // per tool/action, survives back navigation and reselect
	action    config.Action
	spec      command.Spec
	notice    string // transient status line (e.g. copy result)

	// output
	viewport           viewport.Model
	buffer             *executor.LineBuffer
	rendered           int // buffer version last drawn into the viewport
	running            bool
	interruptRequested bool
	control            *executor.RunControl
	result             *executor.Result
	quitting           bool
	runGeneration      uint64
	signals            *signalLifecycle
	fatalErr           error
}

// New builds the root model.
func New(deps Deps) Model {
	ti := textinput.New()
	ti.Placeholder = "type to filter tools"
	ti.Prompt = "/ "
	ti.Focus()

	m := Model{
		deps:      deps,
		screen:    screenPalette,
		filter:    ti,
		matches:   deps.Registry.Tools(),
		preserved: map[formStateKey]form.Values{},
		signals:   &signalLifecycle{},
	}
	return m
}

// Init starts the textinput blinker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, waitSignal(m.deps.Signals, m.signals))
}

// Update routes messages by screen.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		vp := &m.viewport
		vp.Width = msg.Width
		vp.Height = max(1, msg.Height-6)
		if m.form != nil {
			m.form.Model.WithWidth(msg.Width).WithHeight(max(1, msg.Height-2))
		}
		return m, nil

	case tea.KeyMsg:
		// Ctrl-C is global: interrupt a running child first, else quit.
		if msg.Type == tea.KeyCtrlC {
			if m.screen == screenOutput && m.running && m.control != nil {
				if m.tool.Output == config.OutputPassthrough {
					return m, nil
				}
				routed := m.signals.route(os.Interrupt)
				if routed.action == signalForced {
					m.quitting = true
					m.notice = "stopping child…"
					return m, nil
				}
				m.interruptRequested = true
				m.notice = "interrupting child… (Ctrl-C again to quit)"
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		}

	case signalMsg:
		routed := routedSignal(msg)
		cmd := waitSignal(m.deps.Signals, m.signals)
		if routed.action == signalQuit {
			m.quitting = true
			return m, tea.Quit
		}
		if routed.generation != m.runGeneration {
			return m, cmd
		}
		switch routed.action {
		case signalInterrupted:
			m.interruptRequested = true
			m.notice = "interrupting child… (Ctrl-C again to quit)"
		case signalForced:
			m.interruptRequested = true
			m.quitting = true
			m.notice = "stopping child…"
		}
		return m, cmd

	case childDoneMsg:
		res := executor.Result(msg)
		m.result = &res
		m.running = false
		m.interruptRequested = false
		pendingQuit := m.signals.end(m.runGeneration)
		m.control = nil
		m.notice = ""
		m.refreshViewport()
		if res.RestoreErr != nil {
			m.fatalErr = res.RestoreErr
			m.quitting = true
			return m, tea.Quit
		}
		if m.quitting || pendingQuit {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case outputTickMsg:
		if m.screen == screenOutput && m.running {
			m.refreshViewport()
			return m, tickCmd()
		}
		return m, nil

	case copiedMsg:
		if msg.err != nil {
			m.notice = "copy failed: " + msg.err.Error()
		} else {
			m.notice = "command copied to clipboard"
		}
		return m, nil

	}

	switch m.screen {
	case screenPalette:
		return m.updatePalette(msg)
	case screenAction:
		return m.updateAction(msg)
	case screenForm:
		return m.updateForm(msg)
	case screenConfirm:
		return m.updateConfirm(msg)
	case screenOutput:
		return m.updateOutput(msg)
	}
	return m, nil
}

func (m Model) updatePalette(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The filter always has focus on this screen, so every printable rune
	// (including q) is literal input; no single-letter shortcut may fire
	// here. Ctrl-C remains the global exit.
	key, ok := msg.(tea.KeyMsg)
	if ok {
		switch msg := key; {
		case msg.Type == tea.KeyEnter:
			if len(m.matches) == 0 {
				return m, nil
			}
			m.tool = m.matches[m.palCursor]
			return m.enterTool()
		case isUp(msg):
			if m.palCursor > 0 {
				m.palCursor--
			}
			return m, nil
		case isDown(msg):
			if m.palCursor < len(m.matches)-1 {
				m.palCursor++
			}
			return m, nil
		case msg.Type == tea.KeyEsc:
			if m.filter.Value() != "" {
				m.filter.SetValue("")
				m.applyFilter()
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.applyFilter()
	return m, cmd
}

func (m *Model) applyFilter() {
	m.matches = m.deps.Registry.Match(m.filter.Value())
	if m.palCursor >= len(m.matches) {
		m.palCursor = max(0, len(m.matches)-1)
	}
}

// formKey identifies a tool/action pair so preserved form values never leak
// between actions that happen to share param keys.
func (m Model) formKey() formStateKey {
	return formStateKey{toolID: m.tool.ID, actionName: m.action.Name}
}

// enterTool moves from the palette into the action picker, or straight into
// the form when the tool has exactly one action.
func (m Model) enterTool() (tea.Model, tea.Cmd) {
	m.actCursor = 0
	if len(m.tool.Actions) == 1 {
		m.action = m.tool.Actions[0]
		return m.enterForm()
	}
	m.screen = screenAction
	return m, nil
}

func (m Model) updateAction(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Type == tea.KeyEnter:
		if len(m.tool.Actions) == 0 {
			return m, nil
		}
		m.action = m.tool.Actions[m.actCursor]
		return m.enterForm()
	case isUp(key):
		if m.actCursor > 0 {
			m.actCursor--
		}
	case isDown(key):
		if m.actCursor < len(m.tool.Actions)-1 {
			m.actCursor++
		}
	case key.Type == tea.KeyEsc:
		m.screen = screenPalette
	}
	return m, nil
}

func (m Model) enterForm() (tea.Model, tea.Cmd) {
	if len(m.action.Params) == 0 {
		// Nothing to edit: go straight to confirmation.
		return m.enterConfirm(form.Values{})
	}
	m.form = form.New(m.action, m.preserved[m.formKey()])
	if m.width > 0 {
		m.form.Model.WithWidth(m.width).WithHeight(max(1, m.height-2))
	}
	m.screen = screenForm
	return m, m.form.Model.Init()
}

func (m Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyEsc {
		// Esc backs out of the form without losing edits.
		m.preserved[m.formKey()] = m.form.Snapshot()
		if len(m.tool.Actions) == 1 {
			m.screen = screenPalette
		} else {
			m.screen = screenAction
		}
		return m, nil
	}
	fm, cmd := m.form.Model.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.form.Model = f
	}
	switch m.form.Model.State {
	case huh.StateCompleted:
		vals, err := m.form.Values()
		if err != nil {
			// Field-level validation normally prevents this; stay put.
			m.notice = err.Error()
			return m, nil
		}
		m.preserved[m.formKey()] = vals
		return m.enterConfirm(vals)
	case huh.StateAborted:
		m.preserved[m.formKey()] = m.form.Snapshot()
		if len(m.tool.Actions) == 1 {
			m.screen = screenPalette
		} else {
			m.screen = screenAction
		}
		return m, nil
	}
	return m, cmd
}

func (m Model) enterConfirm(vals form.Values) (tea.Model, tea.Cmd) {
	spec, err := command.Build(m.tool, m.action, vals)
	if err != nil {
		m.notice = err.Error()
		if len(m.action.Params) == 0 {
			m.screen = screenAction
			if len(m.tool.Actions) == 1 {
				m.screen = screenPalette
			}
			return m, nil
		}
		return m.enterForm()
	}
	m.spec = spec
	m.notice = ""
	m.screen = screenConfirm
	return m, nil
}

func (m Model) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Type == tea.KeyEnter:
		return m.startRun()
	case key.Type == tea.KeyRunes && string(key.Runes) == "e":
		clip := m.deps.Clipboard
		if clip == nil {
			m.notice = "clipboard unavailable"
			return m, nil
		}
		display := m.spec.Display
		return m, func() tea.Msg {
			return copiedMsg{err: clip.WriteString(display)}
		}
	case key.Type == tea.KeyEsc:
		if len(m.action.Params) == 0 {
			m.screen = screenAction
			if len(m.tool.Actions) == 1 {
				m.screen = screenPalette
			}
			return m, nil
		}
		return m.enterForm()
	}
	return m, nil
}

func (m Model) startRun() (tea.Model, tea.Cmd) {
	m.notice = ""
	m.rendered = -1
	m.interruptRequested = false
	if _, err := m.deps.Runner.Resolve(m.spec.Executable); err != nil {
		res := executor.Result{Err: err}
		m.result = &res
		m.buffer = executor.NewLineBuffer(m.deps.MaxOutputLines)
		m.running = false
		m.screen = screenOutput
		m.refreshViewport()
		return m, nil
	}
	m.buffer = executor.NewLineBuffer(m.deps.MaxOutputLines)
	m.result = nil
	m.running = true
	m.screen = screenOutput
	m.viewport = viewport.New(m.width, max(1, m.height-6))
	m.refreshViewport()

	m.control = executor.NewRunControl()
	m.runGeneration = m.signals.begin(m.control, m.tool.Output == config.OutputPassthrough)

	if m.tool.Output == config.OutputPassthrough {
		runner := m.deps.Runner
		term := m.deps.Terminal
		spec := m.spec
		// Errors, interruption, and restore failures all travel in the
		// Result so nothing (including RestoreErr) is dropped.
		return m, func() tea.Msg {
			return childDoneMsg(runner.RunPassthroughControlled(m.control, spec, term))
		}
	}

	runner := m.deps.Runner
	spec := m.spec
	buf := m.buffer
	runCmd := func() tea.Msg {
		return childDoneMsg(runner.RunCaptureControlled(m.control, spec, buf, buf))
	}
	return m, tea.Batch(runCmd, tickCmd())
}

func waitSignal(signals <-chan os.Signal, lifecycle *signalLifecycle) tea.Cmd {
	if signals == nil {
		return nil
	}
	return func() tea.Msg {
		return signalMsg(lifecycle.route(<-signals))
	}
}

func (m Model) FatalError() error {
	return m.fatalErr
}

func (m Model) updateOutput(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Type == tea.KeyEsc, key.Type == tea.KeyRunes && string(key.Runes) == "q":
		if m.running {
			// Never steal keys from a running child's stream; only Ctrl-C
			// interrupts.
			return m, nil
		}
		m.screen = screenPalette
		m.result = nil
		m.notice = ""
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *Model) refreshViewport() {
	if m.buffer == nil {
		return
	}
	version := m.buffer.Version()
	if m.running && version == m.rendered {
		// Nothing new arrived since the last tick; skip the rebuild.
		return
	}
	followOutput := m.viewport.AtBottom()
	m.rendered = version
	content := m.buffer.Content()
	if m.buffer.Dropped() > 0 {
		content = fmt.Sprintf("… %d earlier line(s) dropped (output is bounded) …\n%s", m.buffer.Dropped(), content)
	}
	if content == "" && !m.running && m.result != nil && m.result.Err == nil {
		content = "(no output)"
	}
	m.viewport.SetContent(content)
	if m.running && followOutput {
		m.viewport.GotoBottom()
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return outputTickMsg(t)
	})
}

func isUp(k tea.KeyMsg) bool {
	return k.Type == tea.KeyUp || k.Type == tea.KeyCtrlP
}

func isDown(k tea.KeyMsg) bool {
	return k.Type == tea.KeyDown || k.Type == tea.KeyCtrlN
}
