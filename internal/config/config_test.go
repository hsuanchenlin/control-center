package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

const validManifest = `
[[tool]]
id = "alpha"
name = "Alpha"
description = "first tool"
executable = "alpha-cli"

[[tool.action]]
name = "run"
description = "run it"
args = ["run"]

[[tool.action.param]]
key = "target"
label = "Target"
type = "text"
required = true
positional = 0

[[tool.action.param]]
key = "verbose"
label = "Verbose"
type = "toggle"
flag = "--verbose"

[[tool.action]]
name = "version"
description = "print version"
args = ["version"]

[[tool]]
id = "beta"
name = "Beta"
description = "second tool"
executable = "beta-cli"
output = "passthrough"

[[tool.action]]
name = "open"
description = "open it"
args = ["open"]
`

func mustParse(t *testing.T, src string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(src), "test.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func TestParseValid(t *testing.T) {
	cfg := mustParse(t, validManifest)
	if len(cfg.Tools) != 2 {
		t.Fatalf("got %d tools", len(cfg.Tools))
	}
	if cfg.Tools[0].Output != OutputCapture {
		t.Errorf("default output = %q, want capture", cfg.Tools[0].Output)
	}
	if cfg.Tools[1].Output != OutputPassthrough {
		t.Errorf("output = %q, want passthrough", cfg.Tools[1].Output)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "control-center", "tools.toml")
	if p != want {
		t.Errorf("DefaultPath = %q, want %q", p, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	p, err = DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/xdg/control-center/tools.toml"; p != want {
		t.Errorf("DefaultPath with XDG = %q, want %q", p, want)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"no tools", ``, "no [[tool]]"},
		{"unknown field", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\nbogus=1\n[[tool.action]]\nname='r'\n", "unknown field"},
		{"dup tool id", validManifest + "\n[[tool]]\nid='alpha'\nname='A2'\nexecutable='y'\n[[tool.action]]\nname='r'\n", "duplicate tool id"},
		{"missing id", "[[tool]]\nname='A'\nexecutable='x'\n[[tool.action]]\nname='r'\n", "id is required"},
		{"missing name", "[[tool]]\nid='a'\nexecutable='x'\n[[tool.action]]\nname='r'\n", "name is required"},
		{"empty executable", "[[tool]]\nid='a'\nname='A'\nexecutable=' '\n[[tool.action]]\nname='r'\n", "executable"},
		{"path executable", "[[tool]]\nid='a'\nname='A'\nexecutable='/usr/bin/x'\n[[tool.action]]\nname='r'\n", "bare command name"},
		{"bad output mode", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\noutput='pipe'\n[[tool.action]]\nname='r'\n", "unknown output mode"},
		{"no actions", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\n", "at least one"},
		{"dup action", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\n[[tool.action]]\nname='r'\n[[tool.action]]\nname='r'\n", "duplicate action"},
		{"empty arg", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\n[[tool.action]]\nname='r'\nargs=['run','']\n", "must not be empty"},
		{"unknown param type", manifestWithParam("key='p'\nlabel='P'\ntype='magic'\nflag='--p'"), "unknown type"},
		{"missing type", manifestWithParam("key='p'\nlabel='P'\nflag='--p'"), "type is required"},
		{"dup param key", manifestWithParams("key='p'\nlabel='P'\ntype='text'\nflag='--p'", "key='p'\nlabel='P2'\ntype='text'\nflag='--q'"), "duplicate param key"},
		{"no placement", manifestWithParam("key='p'\nlabel='P'\ntype='text'"), "flag or positional is required"},
		{"both placements", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='--p'\npositional=0"), "not both"},
		{"unsafe flag space", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='--p x'"), "unsafe flag"},
		{"unsafe flag bare", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='plain'"), "unsafe flag"},
		{"unsafe flag dash", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='-'"), "unsafe flag"},
		{"unsafe flag double dash", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='--'"), "unsafe flag"},
		{"optional positional", manifestWithParam("key='p'\nlabel='P'\ntype='text'\npositional=0"), "must be required"},
		{"dup positional", manifestWithParams("key='p'\nlabel='P'\ntype='text'\nrequired=true\npositional=0", "key='q'\nlabel='Q'\ntype='text'\nrequired=true\npositional=0"), "already used"},
		{"toggle positional", manifestWithParam("key='p'\nlabel='P'\ntype='toggle'\nrequired=true\npositional=0"), "flag or env"},
		{"toggle bad default", manifestWithParam("key='p'\nlabel='P'\ntype='toggle'\nflag='--p'\ndefault='yes'"), "true\" or \"false"},
		{"toggle choices", manifestWithParam("key='p'\nlabel='P'\ntype='toggle'\nflag='--p'\nchoices=['a']"), "only to select"},
		{"select no choices", manifestWithParam("key='p'\nlabel='P'\ntype='select'\nflag='--p'"), "at least one choice"},
		{"select dup choice", manifestWithParam("key='p'\nlabel='P'\ntype='select'\nflag='--p'\nchoices=['a','a']"), "duplicate choice"},
		{"select bad default", manifestWithParam("key='p'\nlabel='P'\ntype='select'\nflag='--p'\nchoices=['a']\ndefault='b'"), "not among choices"},
		{"number bad default", manifestWithParam("key='p'\nlabel='P'\ntype='number'\nflag='--p'\ndefault='abc'"), "not numeric"},
		{"number NaN default", manifestWithParam("key='p'\nlabel='P'\ntype='number'\nflag='--p'\ndefault='NaN'"), "not numeric"},
		{"number infinite default", manifestWithParam("key='p'\nlabel='P'\ntype='number'\nflag='--p'\ndefault='+Inf'"), "not numeric"},
		{"number NaN min", manifestWithParam("key='p'\nlabel='P'\ntype='number'\nflag='--p'\nmin=nan"), "min must be a finite number"},
		{"number infinite max", manifestWithParam("key='p'\nlabel='P'\ntype='number'\nflag='--p'\nmax=+inf"), "max must be a finite number"},
		{"number min>max", manifestWithParam("key='p'\nlabel='P'\ntype='number'\nflag='--p'\nmin=5\nmax=1"), "greater than max"},
		{"number default below min", manifestWithParam("key='p'\nlabel='P'\ntype='number'\nflag='--p'\nmin=5\ndefault='2'"), "below min"},
		{"text with choices", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='--p'\nchoices=['a']"), "only to select"},
		{"text with min", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='--p'\nmin=1"), "only to number"},
		{"text must_exist", manifestWithParam("key='p'\nlabel='P'\ntype='text'\nflag='--p'\nmust_exist=true"), "only to path"},
		{"group leading space", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\ngroup=' System'\n[[tool.action]]\nname='r'\n", "whitespace"},
		{"group trailing space", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\ngroup='System '\n[[tool.action]]\nname='r'\n", "whitespace"},
		{"group with newline", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\ngroup=\"Sys\\ntem\"\n[[tool.action]]\nname='r'\n", "single line"},
		{"group with tab", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\ngroup=\"Sys\\ttem\"\n[[tool.action]]\nname='r'\n", "single line"},
		{"bad action output mode", "[[tool]]\nid='a'\nname='A'\nexecutable='x'\n[[tool.action]]\nname='r'\noutput='pipe'\n", "unknown output mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src), "test.toml")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func manifestWithParam(param string) string {
	return manifestWithParams(param)
}

func manifestWithParams(params ...string) string {
	var b strings.Builder
	b.WriteString("[[tool]]\nid='a'\nname='A'\nexecutable='x'\n[[tool.action]]\nname='r'\nargs=['r']\n")
	for _, p := range params {
		b.WriteString("[[tool.action.param]]\n" + p + "\n")
	}
	return b.String()
}

func TestValidateValue(t *testing.T) {
	min, max := f64(1), f64(10)
	cases := []struct {
		name  string
		p     Param
		raw   string
		want  string
		wantE string
	}{
		{"text ok", Param{Label: "L", Type: ParamText}, "hi", "hi", ""},
		{"text required empty", Param{Label: "L", Type: ParamText, Required: true}, "", "", "required"},
		{"text optional empty", Param{Label: "L", Type: ParamText}, "", "", ""},
		{"select ok", Param{Label: "L", Type: ParamSelect, Choices: []string{"a", "b"}}, "b", "b", ""},
		{"select bad", Param{Label: "L", Type: ParamSelect, Choices: []string{"a"}}, "z", "", "not one of"},
		{"toggle true", Param{Label: "L", Type: ParamToggle}, "true", "true", ""},
		{"toggle false", Param{Label: "L", Type: ParamToggle}, "false", "false", ""},
		{"toggle bad", Param{Label: "L", Type: ParamToggle}, "maybe", "", "true or false"},
		{"number ok", Param{Label: "L", Type: ParamNumber, Min: min, Max: max}, "5", "5", ""},
		{"number nonnumeric", Param{Label: "L", Type: ParamNumber}, "x", "", "not a number"},
		{"number NaN", Param{Label: "L", Type: ParamNumber, Min: min, Max: max}, "NaN", "", "not a number"},
		{"number positive infinity", Param{Label: "L", Type: ParamNumber, Min: min, Max: max}, "+Inf", "", "not a number"},
		{"number negative infinity", Param{Label: "L", Type: ParamNumber, Min: min, Max: max}, "-Inf", "", "not a number"},
		{"number below min", Param{Label: "L", Type: ParamNumber, Min: min}, "0", "", "below the minimum"},
		{"number above max", Param{Label: "L", Type: ParamNumber, Max: max}, "11", "", "above the maximum"},
		{"path tilde", Param{Label: "L", Type: ParamPath}, "~/x", "", ""}, // checked below
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateValue(tc.p, tc.raw)
			if tc.wantE != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantE) {
					t.Fatalf("err = %v, want %q", err, tc.wantE)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tc.name == "path tilde" {
				home, _ := os.UserHomeDir()
				if got != filepath.Join(home, "x") {
					t.Fatalf("got %q", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestValidateValuePathMustExist(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(existing, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := Param{Label: "L", Type: ParamPath, MustExist: true}
	if _, err := ValidateValue(p, existing); err != nil {
		t.Fatalf("existing path rejected: %v", err)
	}
	if _, err := ValidateValue(p, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing path accepted")
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := ExpandPath("~/docs")
	if err != nil || got != filepath.Join(home, "docs") {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = ExpandPath("/abs/path")
	if err != nil || got != "/abs/path" {
		t.Fatalf("got %q err %v", got, err)
	}
	// No globbing: patterns pass through untouched.
	got, _ = ExpandPath("~other/*.go")
	if got != "~other/*.go" {
		t.Fatalf("glob/user expansion leaked: %q", got)
	}
}

// The shipped example manifest is what README tells users to copy, so it must
// stay valid against the schema.
func TestExampleManifestIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "tools.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(data, path)
	if err != nil {
		t.Fatalf("examples/tools.toml is invalid: %v", err)
	}
	if len(cfg.Tools) == 0 {
		t.Fatal("examples/tools.toml declares no tools")
	}
	// Every example tool must be grouped and every action described, so the
	// palette stays organized and self-explanatory.
	for _, tool := range cfg.Tools {
		if tool.Group == "" {
			t.Errorf("tool %q has no group", tool.ID)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.ID)
		}
		for _, action := range tool.Actions {
			if action.Description == "" {
				t.Errorf("tool %q action %q has no description", tool.ID, action.Name)
			}
			for _, param := range action.Params {
				if param.Description == "" {
					t.Errorf("tool %q action %q param %q has no description", tool.ID, action.Name, param.Key)
				}
			}
		}
	}
}

func TestGroupParsing(t *testing.T) {
	cfg := mustParse(t, validManifest+"\n")
	if cfg.Tools[0].Group != "" {
		t.Fatalf("ungrouped tool got group %q", cfg.Tools[0].Group)
	}
	grouped := `
[[tool]]
id = "brew"
name = "Homebrew"
description = "packages"
group = "System"
executable = "brew"
[[tool.action]]
name = "update"
args = ["update"]
`
	cfg = mustParse(t, grouped)
	if cfg.Tools[0].Group != "System" {
		t.Fatalf("group = %q, want System", cfg.Tools[0].Group)
	}
}

func TestOutputFor(t *testing.T) {
	tool := Tool{Output: OutputCapture}
	if got := tool.OutputFor(Action{}); got != OutputCapture {
		t.Fatalf("tool default = %q, want capture", got)
	}
	if got := tool.OutputFor(Action{Output: OutputPassthrough}); got != OutputPassthrough {
		t.Fatalf("action override = %q, want passthrough", got)
	}
	passthroughTool := Tool{Output: OutputPassthrough}
	if got := passthroughTool.OutputFor(Action{Output: OutputCapture}); got != OutputCapture {
		t.Fatalf("action override = %q, want capture", got)
	}
	// A zero-value Tool (unvalidated) still resolves to capture.
	var zero Tool
	if got := zero.OutputFor(Action{}); got != OutputCapture {
		t.Fatalf("zero tool = %q, want capture", got)
	}
}

func TestActionOutputOverrideParses(t *testing.T) {
	cfg := mustParse(t, `
[[tool]]
id = "mix"
name = "Mix"
description = "mixed output modes"
executable = "mix-cli"
[[tool.action]]
name = "plain"
args = ["plain"]
[[tool.action]]
name = "interactive"
args = ["interactive"]
output = "passthrough"
`)
	if got := cfg.Tools[0].OutputFor(cfg.Tools[0].Actions[0]); got != OutputCapture {
		t.Fatalf("plain action = %q, want capture", got)
	}
	if got := cfg.Tools[0].OutputFor(cfg.Tools[0].Actions[1]); got != OutputPassthrough {
		t.Fatalf("interactive action = %q, want passthrough", got)
	}
}

func TestPinnedParses(t *testing.T) {
	cfg := mustParse(t, `
[[tool]]
id = "pin"
name = "Pin"
description = "pinned tool"
executable = "pin-cli"
pinned = true

[[tool.action]]
name = "run"
description = "run it"
args = ["run"]

[[tool.action]]
name = "fav"
description = "favorite action"
args = ["fav"]
pinned = true

[[tool]]
id = "plain"
name = "Plain"
description = "unpinned"
executable = "plain-cli"

[[tool.action]]
name = "run"
description = "run it"
args = ["run"]
`)
	if !cfg.Tools[0].Pinned {
		t.Fatal("tool pinned not parsed")
	}
	if cfg.Tools[0].Actions[0].Pinned {
		t.Fatal("action run should default to unpinned")
	}
	if !cfg.Tools[0].Actions[1].Pinned {
		t.Fatal("action pinned not parsed")
	}
	if cfg.Tools[1].Pinned || cfg.Tools[1].Actions[0].Pinned {
		t.Fatal("pinned must default to false")
	}
}
