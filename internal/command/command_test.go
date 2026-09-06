package command

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hsuanchenlin/control-center/internal/config"
)

func pos(i int) *int { return &i }

func testTool() config.Tool {
	return config.Tool{
		ID:         "t",
		Name:       "T",
		Executable: "t-cli",
		Actions: []config.Action{{
			Name: "run",
			Args: []string{"run", "--fast"},
			Params: []config.Param{
				{Key: "out", Label: "Out", Type: config.ParamText, Flag: "--out"},
				{Key: "verbose", Label: "Verbose", Type: config.ParamToggle, Flag: "--verbose"},
				{Key: "mode", Label: "Mode", Type: config.ParamSelect, Flag: "--mode", Choices: []string{"a", "b"}},
				{Key: "target", Label: "Target", Type: config.ParamText, Required: true, Positional: pos(1)},
				{Key: "src", Label: "Src", Type: config.ParamText, Required: true, Positional: pos(0)},
			},
		}},
	}
}

func TestBuildOrder(t *testing.T) {
	spec, err := Build(testTool(), testTool().Actions[0], map[string]string{
		"out":     "file.txt",
		"verbose": "true",
		"mode":    "a",
		"target":  "dest",
		"src":     "origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--fast", "--out", "file.txt", "--verbose", "--mode", "a", "origin", "dest"}
	if !reflect.DeepEqual(spec.Args, want) {
		t.Fatalf("args = %v, want %v", spec.Args, want)
	}
	if spec.Executable != "t-cli" {
		t.Fatalf("executable = %q", spec.Executable)
	}
}

func TestBuildOmitsEmptyOptionalAndFalseToggle(t *testing.T) {
	spec, err := Build(testTool(), testTool().Actions[0], map[string]string{
		"out":     "",
		"verbose": "false",
		"mode":    "",
		"target":  "dest",
		"src":     "origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--fast", "origin", "dest"}
	if !reflect.DeepEqual(spec.Args, want) {
		t.Fatalf("args = %v, want %v", spec.Args, want)
	}
}

func TestBuildDefaultsApplied(t *testing.T) {
	tool := testTool()
	tool.Actions[0].Params[2].Default = "b" // mode select
	spec, err := Build(tool, tool.Actions[0], map[string]string{
		"target": "dest", "src": "origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "--mode b") {
		t.Fatalf("default not applied: %v", spec.Args)
	}
}

func TestBuildRejectsBadValue(t *testing.T) {
	_, err := Build(testTool(), testTool().Actions[0], map[string]string{
		"mode": "nope", "target": "d", "src": "s",
	})
	if err == nil || !strings.Contains(err.Error(), "not one of") {
		t.Fatalf("err = %v", err)
	}
}

// TestShellInjectionResistance proves hostile values remain single argv
// elements and never split into extra arguments.
func TestShellInjectionResistance(t *testing.T) {
	hostile := "x; rm -rf ~ $(reboot) `id` | tee /etc/passwd"
	spec, err := Build(testTool(), testTool().Actions[0], map[string]string{
		"target": hostile, "src": "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range spec.Args {
		if a == "rm" || a == "-rf" {
			t.Fatalf("hostile value split into argv: %v", spec.Args)
		}
	}
	if spec.Args[len(spec.Args)-2] != "s" || spec.Args[len(spec.Args)-1] != hostile {
		t.Fatalf("argv = %v", spec.Args)
	}
}

func TestDisplayStringEscaping(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"git", "status"}, "git status"},
		{[]string{"cmd", "a b"}, "cmd 'a b'"},
		{[]string{"cmd", ""}, "cmd ''"},
		{[]string{"cmd", "it's"}, `cmd 'it'\''s'`},
		{[]string{"cmd", "a;rm -rf"}, "cmd 'a;rm -rf'"},
		{[]string{"cmd", "--flag=value"}, "cmd --flag=value"},
		{[]string{"cmd", "~/x"}, "cmd '~/x'"}, // literal ~ must be quoted; a shell would expand it
	}
	for _, tc := range cases {
		if got := DisplayString(tc.argv); got != tc.want {
			t.Errorf("DisplayString(%v) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

// TestDisplayIsSeparateFromExecution pins that Build's Display is only a
// rendering: Args stays unescaped.
func TestDisplayIsSeparateFromExecution(t *testing.T) {
	spec, err := Build(testTool(), testTool().Actions[0], map[string]string{
		"target": "with space", "src": "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Args[len(spec.Args)-1] != "with space" {
		t.Fatalf("args escaped: %v", spec.Args)
	}
	if !strings.Contains(spec.Display, "'with space'") {
		t.Fatalf("display not escaped: %q", spec.Display)
	}
}
