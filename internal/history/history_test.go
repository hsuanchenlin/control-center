package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", "history.json")
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries()) != 0 {
		t.Fatalf("entries = %v", s.Entries())
	}
}

func TestRecordAndLoadRoundTrip(t *testing.T) {
	path := testPath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	if err := s.Record("brew", "upgrade", map[string]string{"greedy": "true"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("gh", "pr-view", nil); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := got.Entries()
	if len(entries) != 2 {
		t.Fatalf("entries = %v", entries)
	}
	// Most recent first.
	if entries[0].ToolID != "gh" || entries[1].ToolID != "brew" {
		t.Fatalf("order = %v", entries)
	}
	if entries[1].Params["greedy"] != "true" || !entries[1].At.Equal(now) {
		t.Fatalf("entry = %+v", entries[1])
	}
}

func TestRecordDeduplicatesIdenticalRuns(t *testing.T) {
	s, err := Load(testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustRecord(t, s, "a", "run", map[string]string{"x": "1"})
	mustRecord(t, s, "b", "run", nil)
	mustRecord(t, s, "a", "run", map[string]string{"x": "2"}) // different params: kept
	mustRecord(t, s, "a", "run", map[string]string{"x": "1"}) // duplicate: moves to front

	entries := s.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries = %v", entries)
	}
	if entries[0].ToolID != "a" || entries[0].Params["x"] != "1" {
		t.Fatalf("front = %+v", entries[0])
	}
	if entries[1].Params["x"] != "2" || entries[2].ToolID != "b" {
		t.Fatalf("entries = %v", entries)
	}
}

func TestRecordBoundsToMaxEntries(t *testing.T) {
	path := testPath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxEntries+10; i++ {
		tool := string(rune('a'+i%26)) + string(rune('a'+i/26))
		mustRecord(t, s, tool, "run", nil)
	}
	if len(s.Entries()) != MaxEntries {
		t.Fatalf("len = %d", len(s.Entries()))
	}
	// The bound survives a reload.
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries()) != MaxEntries {
		t.Fatalf("reloaded len = %d", len(got.Entries()))
	}
}

func TestLoadCorruptFileRecovers(t *testing.T) {
	path := testPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries()) != 0 {
		t.Fatalf("entries = %v", s.Entries())
	}
	// The store keeps working after recovery.
	mustRecord(t, s, "a", "run", nil)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries()) != 1 {
		t.Fatalf("after recovery entries = %v", got.Entries())
	}
}

func TestLoadRejectsUnknownVersion(t *testing.T) {
	path := testPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"version": 99, "entries": [{"tool_id": "a", "action_name": "run"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries()) != 0 {
		t.Fatalf("entries = %v", s.Entries())
	}
}

func TestSavePermissionsAndNoTempLeftBehind(t *testing.T) {
	path := testPath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mustRecord(t, s, "a", "run", nil)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestDefaultPathRespectsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state-test")
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/xdg-state-test/control-center/history.json"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func mustRecord(t *testing.T, s *Store, tool, action string, params map[string]string) {
	t.Helper()
	if err := s.Record(tool, action, params); err != nil {
		t.Fatal(err)
	}
}
