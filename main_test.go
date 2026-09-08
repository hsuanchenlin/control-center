package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture swaps os.Stdout/os.Stderr while f runs.
func capture(t *testing.T, f func() int) (string, string, int) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	code := f()
	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = origOut, origErr
	var bufOut, bufErr bytes.Buffer
	io.Copy(&bufOut, rOut)
	io.Copy(&bufErr, rErr)
	return bufOut.String(), bufErr.String(), code
}

const smokeManifest = `
[[tool]]
id = "demo"
name = "Demo"
description = "demo tool"
executable = "demo-cli"

[[tool.action]]
name = "run"
description = "run demo"
args = ["run"]
`

func TestHelpSmoke(t *testing.T) {
	out, _, code := capture(t, func() int { return run([]string{"--help"}) })
	if code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestVersionSmoke(t *testing.T) {
	out, _, code := capture(t, func() int { return run([]string{"--version"}) })
	if code != 0 || !strings.Contains(out, "control-center dev") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestValidateSmoke(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "tools.toml")
	if err := os.WriteFile(good, []byte(smokeManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := capture(t, func() int { return run([]string{"--config", good, "validate"}) })
	if code != 0 || !strings.Contains(out, "OK") {
		t.Fatalf("code=%d out=%q", code, out)
	}

	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("[[tool]]\nid='x'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := capture(t, func() int { return run([]string{"validate", "--config", bad}) })
	if code != 1 || !strings.Contains(errOut, "invalid config") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}

	_, errOut, code = capture(t, func() int {
		return run([]string{"--config", filepath.Join(dir, "missing.toml"), "validate"})
	})
	if code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestUnknownCommand(t *testing.T) {
	_, _, code := capture(t, func() int { return run([]string{"frobnicate"}) })
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
}
