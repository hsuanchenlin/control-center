// Package tui implements the Bubble Tea state machine: palette, action
// picker, parameter form, confirmation, and output screens.
package tui

import (
	"fmt"
	"os"
	"strings"
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
	"github.com/hsuanchenlin/control-center/internal/history"
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
	// Clock bounds the window in which a passthrough run's interrupt is still
	// treated as the child's; defaults to executor.SystemClock.
	Clock executor.Clock
	// MaxOutputLines bounds the retained child output (default 5000).
	MaxOutputLines int
	// History, when set, records confirmed runs and surfaces them on the
	// empty palette; nil disables persistence.
	History *history.Store
	// SaveOutput writes captured output to a path; nil uses the default
	// (~-expanding os.WriteFile).
	SaveOutput func(path, content string) error
}

// Messages driving the async child lifecycle.
type (
	outputTickMsg time.Time
	childDoneMsg  executor.Result
	copiedMsg     struct {
		err error
		ok  string // success notice
	}
	savedMsg struct {
		err  error
		path string
	}
	// signalMsg carries the raw signal; it is routed inside Update so the
	// decision is made against the same model state that applies it.
	signalMsg struct{ sig os.Signal }
)

type signalAction int

const (
	signalIgnored signalAction = iota
	signalInterrupted
	signalForced
	signalQuit
)

type activeRun struct {
	control     *executor.RunControl
	passthrough bool
	interrupted bool
	pendingQuit bool
}

// passthroughInterruptGrace bounds how long after a passthrough run ends the
// parent's copy of the terminal's Ctrl-C may still arrive. Inside the window
// that interrupt belongs to the child that just exited; outside it, an
// interrupt is the operator asking control-center itself to stop.
const passthroughInterruptGrace = 500 * time.Millisecond

type signalLifecycle struct {
	mu         sync.Mutex
	clock      executor.Clock
	active     *activeRun
	graceUntil time.Time
}

func (s *signalLifecycle) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

// begin takes ownership of a freshly started run. Any suppression window left
// by an earlier passthrough run is closed here: the window belongs to the run
// that opened it and never to a later one.
func (s *signalLifecycle) begin(control *executor.RunControl, passthrough bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graceUntil = time.Time{}
	s.active = &activeRun{control: control, passthrough: passthrough}
}

func (s *signalLifecycle) route(sig os.Signal) signalAction {
	s.mu.Lock()
	if s.active == nil {
		delayedPassthrough := sig == os.Interrupt && s.now().Before(s.graceUntil)
		s.mu.Unlock()
		if delayedPassthrough {
			return signalIgnored
		}
		return signalQuit
	}
	run := s.active
	if run.passthrough && sig == os.Interrupt {
		s.mu.Unlock()
		return signalIgnored
	}
	if sig == syscall.SIGTERM || run.interrupted {
		run.pendingQuit = true
		run.interrupted = true
		control := run.control
		s.mu.Unlock()
		control.ForceStop()
		return signalForced
	}
	run.interrupted = true
	control := run.control
	s.mu.Unlock()
	control.Interrupt()
	return signalInterrupted
}

func (s *signalLifecycle) end() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return false
	}
	if s.active.passthrough {
		s.graceUntil = s.now().Add(passthroughInterruptGrace)
	}
	pendingQuit := s.active.pendingQuit
	s.active = nil
	return pendingQuit
}

// itemKind enumerates what a palette row represents.
type itemKind int

const (
	itemTool itemKind = iota
	itemAction
	itemRecent
)

// paletteItem is one selectable palette row: a whole tool, one of its
// actions (search hit or pinned), or a recent run with pre-populated values.
type paletteItem struct {
	kind   itemKind
	tool   config.Tool
	action config.Action // itemAction and itemRecent
	params form.Values   // itemRecent
	at     time.Time     // itemRecent
	pinned bool          // itemTool / itemAction
}

// recentPaletteLimit caps how many recent runs the idle palette lists so the
// section never crowds out the tool catalog.
const recentPaletteLimit = 5

// Model is the root Bubble Tea model.
type Model struct {
	deps   Deps
	screen screen
	width  int
	height int

	// palette
	filter    textinput.Model
	items     []paletteItem
	palCursor int

	// action picker
	tool      config.Tool
	actCursor int

	// form / confirm
	form      *form.Form
	preserved map[formStateKey]form.Values // per tool/action, survives back navigation and reselect
	action    config.Action
	spec      command.Spec
	vals      form.Values // values behind spec, recorded to history on run
	notice    string      // transient status line (e.g. copy result)
	// actionDirect marks that the form/confirm was reached straight from the
	// palette (action search hit or recent run), so backing out returns to
	// the palette rather than the action picker.
	actionDirect bool

	// output
	viewport viewport.Model
	buffer   *executor.LineBuffer
	rendered int // buffer version last drawn into the viewport
	content  string
	running  bool
	control  *executor.RunControl
	result   *executor.Result
	// search prompt over the finished output
	searchInput textinput.Model
	searching   bool
	searchQuery string
	matchLines  []int
	matchPos    int
	// save prompt for the finished output
	saveInput textinput.Model
	saving    bool
	// shuttingDown means a force-stop was requested and the app quits as soon
	// as the child is reaped; quitting means tea.Quit has been returned and
	// the screen is blanked, so the two must never be conflated.
	shuttingDown bool
	quitting     bool
	signals      *signalLifecycle
	fatalErr     error
}

// New builds the root model.
func New(deps Deps) Model {
	ti := textinput.New()
	ti.Placeholder = "type to filter tools"
	ti.Prompt = "/ "
	ti.Focus()

	search := textinput.New()
	search.Placeholder = "search output"
	search.Prompt = "/ "

	save := textinput.New()
	save.Prompt = "save to: "

	m := Model{
		deps:        deps,
		screen:      screenPalette,
		filter:      ti,
		searchInput: search,
		saveInput:   save,
		preserved:   map[formStateKey]form.Values{},
		signals:     &signalLifecycle{clock: deps.Clock},
	}
	m.applyFilter()
	return m
}

// Init starts the textinput blinker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, waitSignal(m.deps.Signals))
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
				if m.tool.OutputFor(m.action) == config.OutputPassthrough {
					return m, nil
				}
				if m.signals.route(os.Interrupt) == signalForced {
					m.shuttingDown = true
					m.notice = "stopping child…"
					return m, nil
				}
				m.notice = "interrupting child… (Ctrl-C again to quit)"
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		}

	case signalMsg:
		if msg.sig == nil {
			return m, nil
		}
		switch m.signals.route(msg.sig) {
		case signalQuit:
			m.quitting = true
			return m, tea.Quit
		case signalInterrupted:
			m.notice = "interrupting child… (Ctrl-C again to quit)"
		case signalForced:
			m.shuttingDown = true
			m.notice = "stopping child…"
		}
		return m, waitSignal(m.deps.Signals)

	case childDoneMsg:
		res := executor.Result(msg)
		m.result = &res
		m.running = false
		pendingQuit := m.signals.end()
		m.control = nil
		m.notice = ""
		m.refreshViewport()
		if res.RestoreErr != nil {
			m.fatalErr = res.RestoreErr
			m.quitting = true
			return m, tea.Quit
		}
		if m.shuttingDown || pendingQuit {
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
			m.notice = msg.ok
		}
		return m, nil

	case savedMsg:
		if msg.err != nil {
			m.notice = "save failed: " + msg.err.Error()
		} else {
			m.notice = "output saved to " + msg.path
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
			if len(m.items) == 0 {
				return m, nil
			}
			return m.enterItem(m.items[m.palCursor])
		case isUp(msg):
			if m.palCursor > 0 {
				m.palCursor--
			}
			return m, nil
		case isDown(msg):
			if m.palCursor < len(m.items)-1 {
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
	if strings.TrimSpace(m.filter.Value()) == "" {
		m.items = m.idleItems()
	} else {
		m.items = m.searchItems(m.filter.Value())
	}
	if m.palCursor >= len(m.items) {
		m.palCursor = max(0, len(m.items)-1)
	}
}

// idleItems builds the palette for an empty filter: pinned actions, pinned
// tools, then the most recent unique runs, then the remaining tools in
// manifest order.
func (m *Model) idleItems() []paletteItem {
	var items []paletteItem
	tools := m.deps.Registry.Tools()
	for _, t := range tools {
		for _, a := range t.Actions {
			if a.Pinned {
				items = append(items, paletteItem{kind: itemAction, tool: t, action: a, pinned: true})
			}
		}
	}
	for _, t := range tools {
		if t.Pinned {
			items = append(items, paletteItem{kind: itemTool, tool: t, pinned: true})
		}
	}
	if h := m.deps.History; h != nil {
		shown := 0
		for _, e := range h.Entries() {
			if shown >= recentPaletteLimit {
				break
			}
			tool, ok := m.deps.Registry.Tool(e.ToolID)
			if !ok {
				continue // action's tool left the manifest
			}
			action, ok := findAction(tool, e.ActionName)
			if !ok {
				continue // action left the manifest
			}
			items = append(items, paletteItem{
				kind: itemRecent, tool: tool, action: action,
				params: form.Values(e.Params), at: e.At,
			})
			shown++
		}
	}
	for _, t := range tools {
		if !t.Pinned {
			items = append(items, paletteItem{kind: itemTool, tool: t})
		}
	}
	return items
}

// searchItems merges scored tool and action matches for a non-empty filter,
// ranked by descending score; ties keep tools ahead of their own actions.
func (m *Model) searchItems(query string) []paletteItem {
	toolHits := m.deps.Registry.MatchTools(query)
	actionHits := m.deps.Registry.MatchActions(query)
	var items []paletteItem
	ti, ai := 0, 0
	for ti < len(toolHits) || ai < len(actionHits) {
		switch {
		case ai >= len(actionHits):
			items = append(items, paletteItem{kind: itemTool, tool: toolHits[ti].Tool, pinned: toolHits[ti].Tool.Pinned})
			ti++
		case ti >= len(toolHits):
			items = append(items, paletteItem{kind: itemAction, tool: actionHits[ai].Tool, action: actionHits[ai].Action, pinned: actionHits[ai].Action.Pinned})
			ai++
		case toolHits[ti].Score >= actionHits[ai].Score:
			items = append(items, paletteItem{kind: itemTool, tool: toolHits[ti].Tool, pinned: toolHits[ti].Tool.Pinned})
			ti++
		default:
			items = append(items, paletteItem{kind: itemAction, tool: actionHits[ai].Tool, action: actionHits[ai].Action, pinned: actionHits[ai].Action.Pinned})
			ai++
		}
	}
	return items
}

// enterItem selects a palette row: tools open their action picker (or only
// action), action hits jump straight to the form, and recent runs jump to
// the confirmation screen with the recorded values.
func (m Model) enterItem(item paletteItem) (tea.Model, tea.Cmd) {
	m.tool = item.tool
	switch item.kind {
	case itemTool:
		return m.enterTool()
	case itemAction:
		m.action = item.action
		m.actionDirect = true
		return m.enterForm()
	case itemRecent:
		m.action = item.action
		m.actionDirect = true
		m.preserved[m.formKey()] = item.params
		return m.enterConfirm(item.params)
	}
	return m, nil
}

// findAction resolves an action by name, or reports it gone.
func findAction(tool config.Tool, name string) (config.Action, bool) {
	for _, a := range tool.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return config.Action{}, false
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
	m.actionDirect = false
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
	case key.Type == tea.KeyEnter || isRunes(key, "l"):
		if len(m.tool.Actions) == 0 {
			return m, nil
		}
		m.action = m.tool.Actions[m.actCursor]
		return m.enterForm()
	case isUp(key) || isRunes(key, "k"):
		if m.actCursor > 0 {
			m.actCursor--
		}
	case isDown(key) || isRunes(key, "j"):
		if m.actCursor < len(m.tool.Actions)-1 {
			m.actCursor++
		}
	case key.Type == tea.KeyEsc || key.Type == tea.KeyLeft || isRunes(key, "h"):
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
		m.backFromForm()
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
		m.backFromForm()
		return m, nil
	}
	return m, cmd
}

// backFromForm returns to wherever the form was entered from: the palette
// for direct action/recent jumps and single-action tools, else the action
// picker.
func (m *Model) backFromForm() {
	if m.actionDirect || len(m.tool.Actions) == 1 {
		m.screen = screenPalette
	} else {
		m.screen = screenAction
	}
}

func (m Model) enterConfirm(vals form.Values) (tea.Model, tea.Cmd) {
	spec, err := command.Build(m.tool, m.action, vals)
	if err != nil {
		m.notice = err.Error()
		if len(m.action.Params) == 0 {
			m.backFromForm()
			return m, nil
		}
		return m.enterForm()
	}
	m.spec = spec
	m.vals = vals
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
	case key.Type == tea.KeyEnter || isRunes(key, "l"):
		return m.startRun()
	case isRunes(key, "e"):
		clip := m.deps.Clipboard
		if clip == nil {
			m.notice = "clipboard unavailable"
			return m, nil
		}
		display := m.spec.Display
		return m, func() tea.Msg {
			return copiedMsg{err: clip.WriteString(display), ok: "command copied to clipboard"}
		}
	case key.Type == tea.KeyEsc || key.Type == tea.KeyLeft || isRunes(key, "h"):
		if len(m.action.Params) == 0 {
			m.backFromForm()
			return m, nil
		}
		return m.enterForm()
	}
	return m, nil
}

func (m Model) startRun() (tea.Model, tea.Cmd) {
	m.notice = ""
	m.rendered = -1
	if _, err := m.deps.Runner.Resolve(m.spec.Executable); err != nil {
		res := executor.Result{Err: err}
		m.result = &res
		m.buffer = executor.NewLineBuffer(m.deps.MaxOutputLines)
		m.running = false
		m.screen = screenOutput
		m.refreshViewport()
		return m, nil
	}
	// The command was confirmed and the executable resolved: remember the run.
	// History is best-effort; a failed write must not block execution.
	if h := m.deps.History; h != nil {
		_ = h.Record(m.tool.ID, m.action.Name, map[string]string(m.vals))
	}
	m.buffer = executor.NewLineBuffer(m.deps.MaxOutputLines)
	m.result = nil
	m.running = true
	m.screen = screenOutput
	m.viewport = viewport.New(m.width, max(1, m.height-6))
	m.refreshViewport()

	m.control = executor.NewRunControl()
	passthrough := m.tool.OutputFor(m.action) == config.OutputPassthrough
	m.signals.begin(m.control, passthrough)

	if passthrough {
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

func waitSignal(signals <-chan os.Signal) tea.Cmd {
	if signals == nil {
		return nil
	}
	return func() tea.Msg {
		return signalMsg{sig: <-signals}
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
	// Modal prompts own the keyboard while open; their keys never scroll.
	if m.saving {
		return m.updateSavePrompt(key)
	}
	if m.searching {
		return m.updateSearchPrompt(key)
	}
	if key.Type == tea.KeyEsc || key.Type == tea.KeyLeft || isRunes(key, "q") || isRunes(key, "h") {
		if m.running {
			// Never steal keys from a running child's stream; only Ctrl-C
			// interrupts.
			return m, nil
		}
		m.clearSearch()
		m.screen = screenPalette
		m.result = nil
		m.notice = ""
		// A run recorded while on this screen shows up in the idle palette.
		m.applyFilter()
		return m, nil
	}
	if !m.running {
		switch {
		case isRunes(key, "/"):
			m.searching = true
			m.searchInput.SetValue(m.searchQuery)
			m.searchInput.Focus()
			m.searchInput.CursorEnd()
			return m, textinput.Blink
		case isRunes(key, "n"):
			m.jumpMatch(1)
			return m, nil
		case isRunes(key, "N"):
			m.jumpMatch(-1)
			return m, nil
		case isRunes(key, "c") || isRunes(key, "y"):
			clip := m.deps.Clipboard
			if clip == nil {
				m.notice = "clipboard unavailable"
				return m, nil
			}
			content := m.content
			return m, func() tea.Msg {
				return copiedMsg{err: clip.WriteString(content), ok: "output copied to clipboard"}
			}
		case isRunes(key, "s"):
			m.saving = true
			m.saveInput.SetValue(m.defaultSaveName())
			m.saveInput.Focus()
			m.saveInput.CursorEnd()
			return m, textinput.Blink
		}
	}
	switch {
	case isRunes(key, "j"):
		m.viewport.LineDown(1)
		return m, nil
	case isRunes(key, "k"):
		m.viewport.LineUp(1)
		return m, nil
	case isRunes(key, "d") || key.Type == tea.KeyCtrlD:
		m.viewport.HalfViewDown()
		return m, nil
	case isRunes(key, "u") || key.Type == tea.KeyCtrlU:
		m.viewport.HalfViewUp()
		return m, nil
	case isRunes(key, "g"):
		m.viewport.GotoTop()
		return m, nil
	case isRunes(key, "G"):
		m.viewport.GotoBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// updateSearchPrompt handles keys while the output search prompt is focused.
// Typing live-highlights matches and jumps to the first one; Enter keeps the
// search and closes the prompt (n/N then navigate); Esc cancels the search.
func (m Model) updateSearchPrompt(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEnter:
		m.searching = false
		m.searchInput.Blur()
		m.applySearch()
		return m, nil
	case tea.KeyEsc:
		m.clearSearch()
		m.notice = ""
		return m, nil
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(key)
	m.applySearch()
	return m, cmd
}

// applySearch recomputes matches for the current query against the finished
// output, highlights them in the viewport, and jumps to the first match.
func (m *Model) applySearch() {
	q := m.searchInput.Value()
	m.searchQuery = q
	m.matchLines = nil
	m.matchPos = 0
	if q == "" {
		m.viewport.SetContent(m.content)
		m.notice = ""
		return
	}
	lines := strings.Split(m.content, "\n")
	lq := strings.ToLower(q)
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), lq) {
			m.matchLines = append(m.matchLines, i)
		}
	}
	for i, line := range lines {
		lines[i] = highlightMatches(line, lq)
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))
	if len(m.matchLines) == 0 {
		m.notice = "no matches for " + q
		return
	}
	m.viewport.SetYOffset(m.matchLines[0])
	m.notice = fmt.Sprintf("match %d/%d for %q", 1, len(m.matchLines), q)
}

// jumpMatch moves to the next/previous match, wrapping around.
func (m *Model) jumpMatch(delta int) {
	if len(m.matchLines) == 0 {
		return
	}
	m.matchPos = (m.matchPos + delta + len(m.matchLines)) % len(m.matchLines)
	m.viewport.SetYOffset(m.matchLines[m.matchPos])
	m.notice = fmt.Sprintf("match %d/%d for %q", m.matchPos+1, len(m.matchLines), m.searchQuery)
}

func (m *Model) clearSearch() {
	m.searching = false
	m.searchQuery = ""
	m.matchLines = nil
	m.matchPos = 0
	m.searchInput.SetValue("")
	m.searchInput.Blur()
	if m.buffer != nil {
		m.viewport.SetContent(m.content)
	}
}

// updateSavePrompt handles keys while the save-path prompt is focused.
func (m Model) updateSavePrompt(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEnter:
		path := strings.TrimSpace(m.saveInput.Value())
		m.saving = false
		m.saveInput.Blur()
		if path == "" {
			m.notice = "save cancelled: empty path"
			return m, nil
		}
		content := m.content
		save := m.deps.SaveOutput
		if save == nil {
			save = defaultSaveOutput
		}
		return m, func() tea.Msg {
			return savedMsg{err: save(path, content), path: path}
		}
	case tea.KeyEsc:
		m.saving = false
		m.saveInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.saveInput, cmd = m.saveInput.Update(key)
	return m, cmd
}

// defaultSaveName prefills the save prompt: control-center-<tool>-<timestamp>.log.
func (m Model) defaultSaveName() string {
	now := time.Now()
	if m.deps.Clock != nil {
		now = m.deps.Clock.Now()
	}
	name := m.tool.ID
	if name == "" {
		name = "output"
	}
	return fmt.Sprintf("control-center-%s-%s.log", name, now.Format("20060102-150405"))
}

// defaultSaveOutput writes content to path, expanding a leading "~".
func defaultSaveOutput(path, content string) error {
	expanded, err := config.ExpandPath(path)
	if err != nil {
		return err
	}
	return os.WriteFile(expanded, []byte(content), 0o644)
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
	m.content = content
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
	return k.Type == tea.KeyUp || k.Type == tea.KeyCtrlP || k.Type == tea.KeyCtrlK
}

func isDown(k tea.KeyMsg) bool {
	return k.Type == tea.KeyDown || k.Type == tea.KeyCtrlN || k.Type == tea.KeyCtrlJ
}

// isRunes matches a literal rune key such as "j" or "G".
func isRunes(k tea.KeyMsg, s string) bool {
	return k.Type == tea.KeyRunes && string(k.Runes) == s
}
