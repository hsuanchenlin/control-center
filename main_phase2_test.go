package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateManifestDiscovery(t *testing.T) {
	for _, xdg := range []bool{false, true} {
		t.Run(map[bool]string{false: "home", true: "xdg"}[xdg], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			root := filepath.Join(home, ".config")
			t.Setenv("XDG_CONFIG_HOME", "")
			if xdg {
				root = t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", root)
			}
			dir := filepath.Join(root, "control-center")
			if err := os.MkdirAll(filepath.Join(dir, "tools.d"), 0700); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(dir, "tools.toml")
			if err := os.WriteFile(main, []byte(smokeManifest), 0600); err != nil {
				t.Fatal(err)
			}
			module := filepath.Join(dir, "tools.d", "extra.toml")
			if err := os.WriteFile(module, []byte(strings.ReplaceAll(smokeManifest, "demo", "extra")), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"validate"}, {"validate", "--config", dir}, {"validate", "--config", main}} {
				_, errOut, code := capture(t, func() int { return run(args) })
				if code != 0 {
					t.Fatalf("%v: %s", args, errOut)
				}
			}
			if err := os.WriteFile(module, []byte(smokeManifest), 0600); err != nil {
				t.Fatal(err)
			}
			_, errOut, code := capture(t, func() int { return run([]string{"validate"}) })
			if code != 1 || !strings.Contains(errOut, "duplicate tool id") {
				t.Fatalf("%d: %s", code, errOut)
			}
			_, errOut, code = capture(t, func() int { return run([]string{"validate", "--config", main}) })
			if code != 0 {
				t.Fatalf("explicit file read sibling: %s", errOut)
			}
		})
	}
}
