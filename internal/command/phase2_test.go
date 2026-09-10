package command

import (
	"github.com/hsuanchenlin/control-center/internal/config"
	"reflect"
	"testing"
)

func TestEnvironmentAssembly(t *testing.T) {
	action := config.Action{Args: []string{"run"}, Params: []config.Param{
		{Key: "debug", Label: "Debug", Type: config.ParamText, Env: "DEBUG"},
		{Key: "enabled", Label: "Enabled", Type: config.ParamToggle, Env: "ENABLED"},
		{Key: "body", Label: "Body", Type: config.ParamText, Multiline: true, Flag: "--body"},
	}}
	value := "'; $(touch /tmp/not-executed)\nsecond line"
	spec, err := Build(config.Tool{Executable: "my-tool"}, action, map[string]string{"debug": value, "body": "one\ntwo"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec.Env, []string{"DEBUG=" + value, "ENABLED=false"}) {
		t.Fatal(spec.Env)
	}
	if !reflect.DeepEqual(spec.Args, []string{"run", "--body", "one\ntwo"}) {
		t.Fatal(spec.Args)
	}
	want := "DEBUG=" + shellQuote(value) + " ENABLED=false my-tool run --body 'one\ntwo'"
	if spec.Display != want {
		t.Fatalf("display %q, want %q", spec.Display, want)
	}
	spec, err = Build(config.Tool{Executable: "my-tool"}, action, nil)
	if err != nil || !reflect.DeepEqual(spec.Env, []string{"ENABLED=false"}) {
		t.Fatalf("empty: %+v %v", spec, err)
	}
}
