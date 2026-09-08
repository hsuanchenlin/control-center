package registry

import (
	"testing"

	"github.com/hsuanchenlin/control-center/internal/config"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	cfg, err := config.Parse([]byte(`
[[tool]]
id = "market-monitor"
name = "Market Monitor"
description = "daily market reports"
group = "Personal"
executable = "market-monitor"
[[tool.action]]
name = "report"
args = ["report"]

[[tool]]
id = "sports"
name = "Sports Scores"
description = "game scores"
group = "Personal"
executable = "sports-scores-cli"
[[tool.action]]
name = "scores"
args = ["scores"]

[[tool]]
id = "echoforge"
name = "EchoForge"
description = "media pipeline"
group = "System"
executable = "echoforge"
[[tool.action]]
name = "start"
args = ["start"]
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg)
}

func TestLookup(t *testing.T) {
	r := testRegistry(t)
	tool, ok := r.Tool("sports")
	if !ok || tool.Name != "Sports Scores" {
		t.Fatalf("lookup failed: %v %v", tool, ok)
	}
	if _, ok := r.Tool("nope"); ok {
		t.Fatal("unknown id resolved")
	}
}

func TestMatchEmptyQuery(t *testing.T) {
	r := testRegistry(t)
	all := r.Match("")
	if len(all) != 3 {
		t.Fatalf("got %d", len(all))
	}
	if all[0].ID != "market-monitor" || all[2].ID != "echoforge" {
		t.Fatalf("manifest order not preserved: %v", all)
	}
}

func TestMatchFuzzy(t *testing.T) {
	r := testRegistry(t)
	hits := r.Match("mm")
	if len(hits) == 0 || hits[0].ID != "market-monitor" {
		t.Fatalf("got %v", hits)
	}
	hits = r.Match("echo")
	if len(hits) != 1 || hits[0].ID != "echoforge" {
		t.Fatalf("got %v", hits)
	}
	if hits := r.Match("zzzz"); len(hits) != 0 {
		t.Fatalf("unexpected hits: %v", hits)
	}
}

func TestMatchDescription(t *testing.T) {
	r := testRegistry(t)
	hits := r.Match("scores")
	found := false
	for _, h := range hits {
		if h.ID == "sports" {
			found = true
		}
	}
	if !found {
		t.Fatalf("description not searched: %v", hits)
	}
}

func TestMatchGroup(t *testing.T) {
	r := testRegistry(t)
	hits := r.Match("sys")
	if len(hits) != 1 || hits[0].ID != "echoforge" {
		t.Fatalf("group not searched: %v", hits)
	}
	hits = r.Match("personal")
	if len(hits) != 2 {
		t.Fatalf("group match returned %v, want the two Personal tools", hits)
	}
}

func TestGroupSurvivesCloning(t *testing.T) {
	r := testRegistry(t)
	tool, ok := r.Tool("echoforge")
	if !ok || tool.Group != "System" {
		t.Fatalf("group lost in lookup: %+v", tool)
	}
	if got := r.Tools()[2].Group; got != "System" {
		t.Fatalf("group lost in Tools(): %q", got)
	}
	if got := r.Match("echo")[0].Group; got != "System" {
		t.Fatalf("group lost in Match(): %q", got)
	}
}

func TestRegistryDoesNotAliasConfigOrResults(t *testing.T) {
	position := 0
	minimum := 1.0
	maximum := 10.0
	cfg := &config.Config{Tools: []config.Tool{{
		ID: "tool",
		Actions: []config.Action{{
			Args: []string{"run"},
			Params: []config.Param{{
				Key: "value", Choices: []string{"one"}, Positional: &position,
				Min: &minimum, Max: &maximum,
			}},
		}},
	}}}
	r := New(cfg)

	cfg.Tools[0].Actions[0].Args[0] = "changed"
	cfg.Tools[0].Actions[0].Params[0].Choices[0] = "changed"
	position, minimum, maximum = 2, 3, 4

	tool, ok := r.Tool("tool")
	if !ok {
		t.Fatal("tool not found")
	}
	assertNestedValues(t, tool)

	tool.Actions[0].Args[0] = "returned"
	tool.Actions[0].Params[0].Choices[0] = "returned"
	*tool.Actions[0].Params[0].Positional = 5
	*tool.Actions[0].Params[0].Min = 5
	*tool.Actions[0].Params[0].Max = 5
	assertNestedValues(t, r.Tools()[0])

	matched := r.Match("tool")
	matched[0].Actions[0].Args[0] = "matched"
	assertNestedValues(t, r.Match("tool")[0])
}

func assertNestedValues(t *testing.T, tool config.Tool) {
	t.Helper()
	action := tool.Actions[0]
	param := action.Params[0]
	if action.Args[0] != "run" || param.Choices[0] != "one" ||
		*param.Positional != 0 || *param.Min != 1 || *param.Max != 10 {
		t.Fatalf("registry value was mutated: %+v", tool)
	}
}
