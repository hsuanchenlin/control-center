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
executable = "market-monitor"
[[tool.action]]
name = "report"
args = ["report"]

[[tool]]
id = "sports"
name = "Sports Scores"
description = "game scores"
executable = "sports-scores-cli"
[[tool.action]]
name = "scores"
args = ["scores"]

[[tool]]
id = "echoforge"
name = "EchoForge"
description = "media pipeline"
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
