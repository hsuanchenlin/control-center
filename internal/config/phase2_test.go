package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, path, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := "[[tool]]\nid='" + id + "'\nname='Tool'\nexecutable='echo'\n[[tool.action]]\nname='run'\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDirectory(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, filepath.Join(dir, "tools.toml"), "main")
	writeManifest(t, filepath.Join(dir, "tools.d", "z.toml"), "last")
	writeManifest(t, filepath.Join(dir, "tools.d", "a.toml"), "first")
	writeManifest(t, filepath.Join(dir, "tools.d", "ignored.txt"), "ignored")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tool := range cfg.Tools {
		ids = append(ids, tool.ID)
	}
	if !reflect.DeepEqual(ids, []string{"main", "first", "last"}) {
		t.Fatal(ids)
	}
	single, err := Load(filepath.Join(dir, "tools.toml"))
	if err != nil || len(single.Tools) != 1 {
		t.Fatalf("single: %v %v", single, err)
	}
	writeManifest(t, filepath.Join(dir, "tools.d", "dup.toml"), "main")
	_, err = Load(dir)
	if err == nil || !strings.Contains(err.Error(), "tools.toml") || !strings.Contains(err.Error(), "dup.toml") {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestDirectoryOptionalFilesAndErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("empty accepted")
	}
	writeManifest(t, filepath.Join(dir, "tools.d", "only.toml"), "only")
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bad1.toml", "bad2.toml"} {
		if err := os.WriteFile(filepath.Join(dir, "tools.d", name), []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "bad1.toml") || !strings.Contains(err.Error(), "bad2.toml") {
		t.Fatal(err)
	}
}

func TestEnvAndMultilineSchema(t *testing.T) {
	cfg, err := Parse([]byte(manifestWithParam("key='p'\nlabel='P'\ntype='text'\nenv='DEBUG'\nmultiline=true")), "test")
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Tools[0].Actions[0].Params[0]
	if p.Env != "DEBUG" || !p.Multiline {
		t.Fatal(p)
	}
	for _, extra := range []string{"env='1BAD'", "env='A=B'", "env='DEBUG'\nflag='--debug'", "env='DEBUG'\npositional=0\nrequired=true", "env='DEBUG'\nmultiline=true\ntype='path'"} {
		typ := "type='text'\n"
		if strings.Contains(extra, "type=") {
			typ = ""
		}
		if _, err := Parse([]byte(manifestWithParam("key='p'\nlabel='P'\n"+typ+extra)), "test"); err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
	if _, err := ValidateValue(p, "x\x00y"); err == nil {
		t.Fatal("NUL accepted")
	}
}
