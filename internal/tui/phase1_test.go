package tui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hsuanchenlin/control-center/internal/config"
	"github.com/hsuanchenlin/control-center/internal/executor"
	"github.com/hsuanchenlin/control-center/internal/history"
	"github.com/hsuanchenlin/control-center/internal/registry"
)

const pinnedManifest = `
[[tool]]
id = "alpha"
name = "Alpha"
description = "plain tool"
executable = "alpha-cli"

[[tool.action]]
name = "run"
description = "run it"
args = ["run"]

[[tool]]
id = "beta"
name = "Beta"
description = "pinned tool"
executable = "beta-cli"
pinned = true

[[tool.action]]
name = "open"
description = "open it"
args = ["open"]

[[tool.action]]
name = "fav"
description = "favorite action"
args = ["fav"]
pinned = true
`

// newPhase1Model builds a model over the given manifest with optional Deps
// overrides.
func newPhase1Model(t *testing.T, manifest string, mutate func(*Deps)) Model {
	t.Helper()
	cfg, err := config.Parse([]byte(manifest), "test")
	if err != nil {
		t.Fatal(err)
	}
	runner := &executor.Runner{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		StartProcess: func(ctx context.Context, path string, args []string, stdout, stderr io.Writer) (func() (int, error), error) {
			return func() (int, error) { return 0, nil }, nil
		},
	}
	deps := Deps{
		Registry:  registry.New(cfg),
		Runner:    runner,
		Clipboard: &fakeClipboard{},
		Terminal:  nopTerminal{},
	}
	if mutate != nil {
		mutate(&deps)
	}
	m := New(deps)
	m.width, m.height = 100, 40
	return m
}

func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	tm, _ := m.Update(msg)
	return asModel(t, tm)
}

func TestActionSearchHitJumpsToForm(t *testing.T) {
	m, _ := newTestModel(t)
	m = update(t, m, runes("goto"))
	if len(m.items) == 0 || m.items[0].kind != itemAction || m.items[0].action.Name != "goto" {
		t.Fatalf("items = %v", m.items)
	}
	m = update(t, m, key(tea.KeyEnter))
	if m.screen != screenForm || m.action.Name != "goto" || m.tool.ID != "multi" {
		t.Fatalf("screen=%v action=%q tool=%q", m.screen, m.action.Name, m.tool.ID)
	}
	// Backing out of a direct jump returns to the palette, not the picker.
	m = update(t, m, key(tea.KeyEsc))
	if m.screen != screenPalette {
		t.Fatalf("esc from form = %v", m.screen)
	}
}

func TestActionSearchHitWithoutParamsJumpsToConfirm(t *testing.T) {
	m, _ := newTestModel(t)
	m = update(t, m, runes("bare"))
	if len(m.items) == 0 || m.items[0].kind != itemAction {
		t.Fatalf("items = %v", m.items)
	}
	m = update(t, m, key(tea.KeyEnter))
	if m.screen != screenConfirm || m.action.Name != "bare" {
		t.Fatalf("screen=%v action=%q", m.screen, m.action.Name)
	}
	if !strings.Contains(m.View(), "multi-cli bare") {
		t.Fatalf("confirm view missing command:\n%s", m.View())
	}
	// Esc from a direct confirm returns to the palette.
	m = update(t, m, key(tea.KeyEsc))
	if m.screen != screenPalette {
		t.Fatalf("esc from confirm = %v", m.screen)
	}
}

func TestToolLevelSelectionStillOpensPicker(t *testing.T) {
	m, _ := newTestModel(t)
	m = update(t, m, runes("multi"))
	if len(m.items) == 0 || m.items[0].kind != itemTool {
		t.Fatalf("items = %v", m.items)
	}
	m = update(t, m, key(tea.KeyEnter))
	if m.screen != screenAction {
		t.Fatalf("screen = %v", m.screen)
	}
}

func TestIdlePaletteRanksPinnedFirst(t *testing.T) {
	m := newPhase1Model(t, pinnedManifest, nil)
	// Pinned action, pinned tool, then the remaining tool.
	if len(m.items) != 3 {
		t.Fatalf("items = %v", m.items)
	}
	if m.items[0].kind != itemAction || m.items[0].action.Name != "fav" || !m.items[0].pinned {
		t.Fatalf("item0 = %+v", m.items[0])
	}
	if m.items[1].kind != itemTool || m.items[1].tool.ID != "beta" || !m.items[1].pinned {
		t.Fatalf("item1 = %+v", m.items[1])
	}
	if m.items[2].kind != itemTool || m.items[2].tool.ID != "alpha" {
		t.Fatalf("item2 = %+v", m.items[2])
	}
	view := m.View()
	if !strings.Contains(view, "[Pinned]") {
		t.Fatalf("view missing pinned tag:\n%s", view)
	}
}

func TestRunIsRecordedAndSurfacedAsRecent(t *testing.T) {
	store, err := history.Load(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	m := newPhase1Model(t, modelManifest, func(d *Deps) { d.History = store })

	// Run the plain tool's no-param action.
	m = update(t, m, runes("status"))
	m = update(t, m, key(tea.KeyEnter)) // action hit -> confirm
	m = update(t, m, key(tea.KeyEnter)) // run
	if m.screen != screenOutput {
		t.Fatalf("screen = %v", m.screen)
	}
	entries := store.Entries()
	if len(entries) != 1 || entries[0].ToolID != "plain" || entries[0].ActionName != "status" {
		t.Fatalf("entries = %+v", entries)
	}
	if !entries[0].At.Equal(now) {
		t.Fatalf("timestamp = %v", entries[0].At)
	}

	// Back at the palette with an empty filter, the run shows as [Recent].
	m = update(t, m, childDoneMsg(executor.Result{ExitCode: 0}))
	m = update(t, m, runes("q"))
	m = update(t, m, key(tea.KeyEsc)) // clear the leftover filter text
	if len(m.items) == 0 || m.items[0].kind != itemRecent {
		t.Fatalf("items = %v", m.items)
	}
	if !strings.Contains(m.View(), "[Recent]") {
		t.Fatalf("view missing recent tag:\n%s", m.View())
	}
}

func TestRecentRowShowsRelativeAge(t *testing.T) {
	store, err := history.Load(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	runAt := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return runAt }
	if err := store.Record("plain", "status", nil); err != nil {
		t.Fatal(err)
	}
	m := newPhase1Model(t, modelManifest, func(d *Deps) {
		d.History = store
		d.Clock = &fakeClock{now: runAt.Add(2 * time.Hour)}
	})
	if len(m.items) == 0 || m.items[0].kind != itemRecent {
		t.Fatalf("items = %v", m.items)
	}
	view := m.View()
	if !strings.Contains(view, "[Recent]") || !strings.Contains(view, "2h ago") {
		t.Fatalf("view missing recent age:\n%s", view)
	}
}

func TestRelativeAge(t *testing.T) {
	at := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		now  time.Time
		want string
	}{
		{"future clamps to just now", at.Add(-time.Hour), "just now"},
		{"seconds", at.Add(30 * time.Second), "just now"},
		{"minutes", at.Add(5 * time.Minute), "5m ago"},
		{"hours", at.Add(2 * time.Hour), "2h ago"},
		{"days", at.Add(3 * 24 * time.Hour), "3d ago"},
		{"older than a month shows the date", at.Add(60 * 24 * time.Hour), "2026-09-09"},
	}
	for _, tc := range cases {
		if got := relativeAge(tc.now, at); got != tc.want {
			t.Errorf("%s: relativeAge = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRecentRunReRunsWithPrepopulatedParams(t *testing.T) {
	store, err := history.Load(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record("multi", "go", map[string]string{"dest": "mars"}); err != nil {
		t.Fatal(err)
	}
	m := newPhase1Model(t, modelManifest, func(d *Deps) { d.History = store })

	if len(m.items) == 0 || m.items[0].kind != itemRecent {
		t.Fatalf("items = %v", m.items)
	}
	m = update(t, m, key(tea.KeyEnter)) // recent -> confirm directly
	if m.screen != screenConfirm {
		t.Fatalf("screen = %v", m.screen)
	}
	if !strings.Contains(m.spec.Display, "mars") {
		t.Fatalf("spec not pre-populated: %q", m.spec.Display)
	}
	// The values are also preserved for the form when navigating back.
	m = update(t, m, key(tea.KeyEsc))
	if m.screen != screenForm {
		t.Fatalf("esc from confirm = %v", m.screen)
	}
	if got := m.form.Snapshot()["dest"]; got != "mars" {
		t.Fatalf("form dest = %q", got)
	}
}

func TestRecentRunsSkipMissingActions(t *testing.T) {
	store, err := history.Load(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustHist := func(tool, action string) {
		if err := store.Record(tool, action, nil); err != nil {
			t.Fatal(err)
		}
	}
	mustHist("ghost", "run") // tool left the manifest
	mustHist("multi", "gone")
	mustHist("plain", "status") // action left the manifest
	m := newPhase1Model(t, modelManifest, func(d *Deps) { d.History = store })

	var recent []paletteItem
	for _, item := range m.items {
		if item.kind == itemRecent {
			recent = append(recent, item)
		}
	}
	if len(recent) != 1 || recent[0].tool.ID != "plain" {
		t.Fatalf("recent = %v", recent)
	}
}

// finishedOutput drives a no-param run to completion with the given output.
func finishedOutput(t *testing.T, m Model, output string) Model {
	t.Helper()
	m = update(t, m, runes("status"))
	m = update(t, m, key(tea.KeyEnter)) // confirm
	m = update(t, m, key(tea.KeyEnter)) // run
	if m.screen != screenOutput || !m.running {
		t.Fatalf("screen=%v running=%v", m.screen, m.running)
	}
	if _, err := m.buffer.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	m = update(t, m, childDoneMsg(executor.Result{ExitCode: 0}))
	if m.running {
		t.Fatal("still running")
	}
	return m
}

func TestOutputCopyKeys(t *testing.T) {
	m, clip := newTestModel(t)
	m = finishedOutput(t, m, "hello\nworld\n")
	for _, k := range []string{"c", "y"} {
		clip.last = ""
		tm, cmd := m.Update(runes(k))
		m = asModel(t, tm)
		if cmd == nil {
			t.Fatalf("%s produced no command", k)
		}
		m = update(t, m, cmd())
		if clip.last != "hello\nworld" {
			t.Fatalf("%s copied %q", k, clip.last)
		}
		if m.notice != "output copied to clipboard" {
			t.Fatalf("notice = %q", m.notice)
		}
	}
}

func TestOutputSearchAndMatchNavigation(t *testing.T) {
	m, _ := newTestModel(t)
	m = finishedOutput(t, m, "l0\nbeta one\nl2\nl3\nl4\nl5\nl6\nl7\nbeta two\nl9\n")
	// A small viewport makes match jumps observable (offsets clamp to the
	// scrollable range).
	m.viewport = viewport.New(40, 3)
	m.refreshViewport()

	m = update(t, m, runes("/"))
	if !m.searching {
		t.Fatal("/ did not open the search prompt")
	}
	m = update(t, m, runes("beta"))
	if len(m.matchLines) != 2 || m.matchLines[0] != 1 || m.matchLines[1] != 8 {
		t.Fatalf("matchLines = %v", m.matchLines)
	}
	if m.viewport.YOffset != 1 {
		t.Fatalf("offset = %d, want first match line 1", m.viewport.YOffset)
	}
	if !strings.Contains(m.View(), "match 1/2") {
		t.Fatalf("view missing match counter:\n%s", m.View())
	}

	// Enter closes the prompt but keeps the search; n/N navigate.
	m = update(t, m, key(tea.KeyEnter))
	if m.searching {
		t.Fatal("enter did not close the prompt")
	}
	m = update(t, m, runes("n"))
	if m.matchPos != 1 || m.viewport.YOffset != 7 { // line 8 clamps to maxYOffset 7
		t.Fatalf("n: pos=%d offset=%d", m.matchPos, m.viewport.YOffset)
	}
	m = update(t, m, runes("n")) // wraps
	if m.matchPos != 0 || m.viewport.YOffset != 1 {
		t.Fatalf("n wrap: pos=%d offset=%d", m.matchPos, m.viewport.YOffset)
	}
	m = update(t, m, runes("N"))
	if m.matchPos != 1 {
		t.Fatalf("N: pos=%d", m.matchPos)
	}
}

func TestOutputSearchEscClears(t *testing.T) {
	m, _ := newTestModel(t)
	m = finishedOutput(t, m, "alpha\nbeta\n")
	m = update(t, m, runes("/"))
	m = update(t, m, runes("beta"))
	m = update(t, m, key(tea.KeyEsc))
	if m.searching || m.searchQuery != "" || len(m.matchLines) != 0 {
		t.Fatalf("esc did not clear search: searching=%v query=%q matches=%v",
			m.searching, m.searchQuery, m.matchLines)
	}
}

func TestOutputSearchNoMatches(t *testing.T) {
	m, _ := newTestModel(t)
	m = finishedOutput(t, m, "alpha\n")
	m = update(t, m, runes("/"))
	m = update(t, m, runes("zzz"))
	if len(m.matchLines) != 0 || !strings.Contains(m.View(), "no matches") {
		t.Fatalf("matches=%v view:\n%s", m.matchLines, m.View())
	}
}

func TestOutputKeysBlockedWhileRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = update(t, m, runes("status"))
	m = update(t, m, key(tea.KeyEnter))
	m = update(t, m, key(tea.KeyEnter))
	if !m.running {
		t.Fatal("not running")
	}
	for _, k := range []string{"/", "c", "y", "s"} {
		m = update(t, m, runes(k))
		if m.searching || m.saving {
			t.Fatalf("%s opened a prompt while running", k)
		}
	}
}

func TestOutputSaveFlow(t *testing.T) {
	var savedPath, savedContent string
	m := newPhase1Model(t, modelManifest, func(d *Deps) {
		d.SaveOutput = func(path, content string) error {
			savedPath, savedContent = path, content
			return nil
		}
	})
	m = finishedOutput(t, m, "hello\n")

	m = update(t, m, runes("s"))
	if !m.saving {
		t.Fatal("s did not open the save prompt")
	}
	if !strings.HasPrefix(m.saveInput.Value(), "control-center-plain-") {
		t.Fatalf("prefill = %q", m.saveInput.Value())
	}
	tm, cmd := m.Update(key(tea.KeyEnter))
	m = asModel(t, tm)
	if cmd == nil {
		t.Fatal("enter produced no save command")
	}
	m = update(t, m, cmd())
	if m.saving {
		t.Fatal("still saving")
	}
	if savedPath == "" || savedContent != "hello" {
		t.Fatalf("saved path=%q content=%q", savedPath, savedContent)
	}
	if !strings.Contains(m.notice, "output saved to") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestOutputSaveEscCancels(t *testing.T) {
	called := false
	m := newPhase1Model(t, modelManifest, func(d *Deps) {
		d.SaveOutput = func(path, content string) error {
			called = true
			return nil
		}
	})
	m = finishedOutput(t, m, "hello\n")
	m = update(t, m, runes("s"))
	m = update(t, m, key(tea.KeyEsc))
	if m.saving || called {
		t.Fatalf("esc did not cancel: saving=%v called=%v", m.saving, called)
	}
}

func TestDefaultSaveOutputWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	if err := defaultSaveOutput(path, "data"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Fatalf("got %q", got)
	}
}
