package form

import (
	"testing"

	"github.com/hsuanchenlin/control-center/internal/config"
)

func testAction() config.Action {
	zero := 0
	return config.Action{
		Name: "run",
		Args: []string{"run"},
		Params: []config.Param{
			{Key: "name", Label: "Name", Type: config.ParamText, Required: true, Default: "world", Flag: "--name"},
			{Key: "count", Label: "Count", Type: config.ParamNumber, Flag: "--count", Min: configFloat(1), Max: configFloat(5)},
			{Key: "mode", Label: "Mode", Type: config.ParamSelect, Flag: "--mode", Choices: []string{"a", "b"}, Default: "a"},
			{Key: "loud", Label: "Loud", Type: config.ParamToggle, Flag: "--loud"},
			{Key: "target", Label: "Target", Type: config.ParamText, Required: true, Positional: &zero},
		},
	}
}

func configFloat(v float64) *float64 { return &v }

func TestDefaultsPopulateSnapshot(t *testing.T) {
	f := New(testAction(), nil)
	snap := f.Snapshot()
	if snap["name"] != "world" {
		t.Errorf("name = %q", snap["name"])
	}
	if snap["mode"] != "a" {
		t.Errorf("mode = %q", snap["mode"])
	}
	if snap["loud"] != "false" {
		t.Errorf("loud = %q", snap["loud"])
	}
}

func TestInitialValuesPreserved(t *testing.T) {
	f := New(testAction(), Values{"name": "mars", "loud": "true"})
	snap := f.Snapshot()
	if snap["name"] != "mars" {
		t.Errorf("name = %q, want preserved value", snap["name"])
	}
	if snap["loud"] != "true" {
		t.Errorf("loud = %q, want preserved value", snap["loud"])
	}
	// Unspecified keys still get defaults.
	if snap["mode"] != "a" {
		t.Errorf("mode = %q", snap["mode"])
	}
}

func TestValuesValidates(t *testing.T) {
	f := New(testAction(), Values{"target": "dest"})
	*f.strings["count"] = "99"
	if _, err := f.Values(); err == nil {
		t.Fatal("out-of-range number accepted")
	}
	*f.strings["count"] = "3"
	vals, err := f.Values()
	if err != nil {
		t.Fatal(err)
	}
	if vals["count"] != "3" || vals["target"] != "dest" {
		t.Fatalf("vals = %v", vals)
	}
}

func TestValuesRequireMissing(t *testing.T) {
	f := New(testAction(), nil) // target missing
	if _, err := f.Values(); err == nil {
		t.Fatal("missing required positional accepted")
	}
}
