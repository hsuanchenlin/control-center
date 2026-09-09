package form

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/hsuanchenlin/control-center/internal/config"
)

// PathSuggestions returns lexical prefix matches, preserving the typed path
// spelling (including ~). Directories include a trailing separator. Errors
// are nonfatal: validation remains the authority for must_exist.
func PathSuggestions(raw string) []string {
	prefix := raw
	if raw == "~" {
		prefix = "~/"
	}
	// Split before expansion: ExpandPath cleans trailing dots and slashes,
	// which are meaningful while the user is still typing a filename.
	typedDir, base := filepath.Split(prefix)
	dir := "."
	if typedDir != "" {
		var err error
		dir, err = config.ExpandPath(typedDir)
		if err != nil {
			return nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, base) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		candidate := typedDir + name
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = info.IsDir()
			}
		}
		if isDir {
			candidate += string(filepath.Separator)
		}
		out = append(out, candidate)
		if len(out) == 100 {
			break
		}
	}
	return out
}

// Refresh synchronously on the UI loop rather than having a SuggestionsFunc
// goroutine race with huh's mutable value holder.
type pathInput struct{ *huh.Input }

func (p *pathInput) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.Input.Update(msg)
	if _, ok := msg.(tea.KeyMsg); ok {
		p.Input.Suggestions(PathSuggestions(p.GetValue().(string)))
	}
	return p, cmd
}
