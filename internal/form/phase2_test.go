package form

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/hsuanchenlin/control-center/internal/config"
)

func TestPathSuggestions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	for _, name := range []string{"alpha space", "álpha", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "alphabet"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := PathSuggestions("~/al"); !reflect.DeepEqual(got, []string{"~/alpha space", "~/alphabet/"}) {
		t.Fatal(got)
	}
	if got := PathSuggestions("~/."); !reflect.DeepEqual(got, []string{"~/.hidden"}) {
		t.Fatal(got)
	}
	if got := PathSuggestions("~/ál"); !reflect.DeepEqual(got, []string{"~/álpha"}) {
		t.Fatal(got)
	}
	if got := PathSuggestions(filepath.Join(dir, "missing", "x")); len(got) != 0 {
		t.Fatal(got)
	}
	if got := PathSuggestions("~"); len(got) != 3 {
		t.Fatal(got)
	}
}

// Process navigation commands but not cursor blink timers.
func applyFormCmd(f *Form, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			applyFormCmd(f, c)
		}
		return
	}
	if msg != nil && reflect.TypeOf(msg).PkgPath() == "github.com/charmbracelet/huh" {
		_, next := f.Model.Update(msg)
		applyFormCmd(f, next)
	}
}

func TestMultilineEditingAndSubmit(t *testing.T) {
	f := New(config.Action{Params: []config.Param{{Key: "body", Label: "Body", Type: config.ParamText, Multiline: true, Required: true, Flag: "--body"}}}, nil)
	f.Model.Init()
	for _, msg := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("first")}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune("second")}} {
		_, cmd := f.Model.Update(msg)
		applyFormCmd(f, cmd)
	}
	if got := f.Snapshot()["body"]; got != "first\nsecond" {
		t.Fatalf("body %q", got)
	}
	if f.Model.State == huh.StateCompleted {
		t.Fatal("Enter submitted multiline")
	}
	_, cmd := f.Model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	applyFormCmd(f, cmd)
	if f.Model.State != huh.StateCompleted {
		t.Fatal("Ctrl-D did not submit")
	}
}

func TestInteractivePathCompletion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "alpha"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	f := New(config.Action{Params: []config.Param{{Key: "path", Label: "Path", Type: config.ParamPath, Flag: "--path", MustExist: true}}}, nil)
	f.Model.Init()
	f.Model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("~/al")})
	f.Model.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if got := f.Snapshot()["path"]; got != "~/alpha" {
		t.Fatalf("completion %q", got)
	}
	if _, err := f.Values(); err != nil {
		t.Fatal(err)
	}
	*f.strings["path"] = "~/missing"
	if _, err := f.Values(); err == nil {
		t.Fatal("must_exist lost")
	}
}
