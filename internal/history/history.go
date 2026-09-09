// Package history persists recent runs so the palette can offer one-key
// repeats. It is framework-free: the clock is injected and the state path is
// explicit, so tests stay hermetic.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"
)

// MaxEntries bounds how many runs are retained; older ones are dropped.
const MaxEntries = 50

// Entry is one confirmed execution: which action ran, with which parameter
// values, and when.
type Entry struct {
	ToolID     string            `json:"tool_id"`
	ActionName string            `json:"action_name"`
	Params     map[string]string `json:"params,omitempty"`
	At         time.Time         `json:"at"`
}

// file is the on-disk schema; Version allows future migration.
type file struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

const fileVersion = 1

// Store is a run-history repository backed by one JSON file.
type Store struct {
	path string
	// Now supplies timestamps; defaults to time.Now.
	Now     func() time.Time
	entries []Entry
}

// DefaultPath returns the history location:
// $XDG_STATE_HOME/control-center/history.json, falling back to
// ~/.local/state/control-center/history.json.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "control-center", "history.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "control-center", "history.json"), nil
}

// Load reads the store at path. A missing file yields an empty store; a
// corrupt or incompatible file is recovered by starting empty rather than
// failing the launch.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read history %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil || f.Version != fileVersion {
		// Corrupt or from an incompatible version: start fresh.
		return s, nil
	}
	for _, e := range f.Entries {
		if e.ToolID == "" || e.ActionName == "" {
			continue
		}
		s.entries = append(s.entries, e)
	}
	if len(s.entries) > MaxEntries {
		s.entries = s.entries[:MaxEntries]
	}
	return s, nil
}

// Entries returns the retained runs, most recent first.
func (s *Store) Entries() []Entry {
	out := make([]Entry, len(s.entries))
	for i, e := range s.entries {
		e.Params = maps.Clone(e.Params)
		out[i] = e
	}
	return out
}

// Record prepends a run and persists the store. A run identical to an
// existing entry (same tool, action, and parameter values) replaces it, so
// entries stay unique; the store is then bounded to MaxEntries.
func (s *Store) Record(toolID, actionName string, params map[string]string) error {
	e := Entry{ToolID: toolID, ActionName: actionName, Params: maps.Clone(params), At: s.now()}
	kept := s.entries[:0]
	for _, old := range s.entries {
		if !sameRun(old, e) {
			kept = append(kept, old)
		}
	}
	s.entries = append([]Entry{e}, kept...)
	if len(s.entries) > MaxEntries {
		s.entries = s.entries[:MaxEntries]
	}
	return s.Save()
}

func sameRun(a, b Entry) bool {
	return a.ToolID == b.ToolID && a.ActionName == b.ActionName && maps.Equal(a.Params, b.Params)
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Save writes the store atomically (temp file plus rename) with 0600
// permissions: parameter values may be sensitive to the operator.
func (s *Store) Save() error {
	data, err := json.MarshalIndent(file{Version: fileVersion, Entries: s.entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode history: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write history: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace history: %w", err)
	}
	return nil
}
