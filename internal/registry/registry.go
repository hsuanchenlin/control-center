// Package registry provides immutable lookup over a validated manifest and
// the fuzzy matching used by the tool palette.
package registry

import (
	"sort"
	"strings"

	"github.com/hsuanchenlin/control-center/internal/config"
)

// Registry is an immutable, validated set of tools.
type Registry struct {
	tools []config.Tool
	byID  map[string]int
}

// New builds a Registry from a validated config.
func New(cfg *config.Config) *Registry {
	r := &Registry{byID: make(map[string]int, len(cfg.Tools))}
	for i, t := range cfg.Tools {
		r.tools = append(r.tools, cloneTool(t))
		r.byID[t.ID] = i
	}
	return r
}

// Tools returns all tools in manifest order.
func (r *Registry) Tools() []config.Tool {
	out := make([]config.Tool, len(r.tools))
	for i, tool := range r.tools {
		out[i] = cloneTool(tool)
	}
	return out
}

// Tool looks up a tool by its stable id.
func (r *Registry) Tool(id string) (config.Tool, bool) {
	i, ok := r.byID[id]
	if !ok {
		return config.Tool{}, false
	}
	return cloneTool(r.tools[i]), true
}

// Match ranks tools against a fuzzy query over id, name, and description.
// An empty query returns all tools in manifest order. Results are ordered by
// descending score, ties broken by manifest order.
func (r *Registry) Match(query string) []config.Tool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return r.Tools()
	}
	type scored struct {
		idx   int
		score int
	}
	var hits []scored
	for i, t := range r.tools {
		best := -1
		for _, field := range []string{t.ID, t.Name, t.Description} {
			if s, ok := fuzzyScore(query, strings.ToLower(field)); ok && s > best {
				best = s
			}
		}
		if best >= 0 {
			hits = append(hits, scored{idx: i, score: best})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].idx < hits[b].idx
	})
	out := make([]config.Tool, len(hits))
	for i, h := range hits {
		out[i] = cloneTool(r.tools[h.idx])
	}
	return out
}

func cloneTool(tool config.Tool) config.Tool {
	actions := make([]config.Action, len(tool.Actions))
	for i, action := range tool.Actions {
		action.Args = append([]string(nil), action.Args...)
		params := make([]config.Param, len(action.Params))
		for j, param := range action.Params {
			param.Choices = append([]string(nil), param.Choices...)
			if param.Positional != nil {
				value := *param.Positional
				param.Positional = &value
			}
			if param.Min != nil {
				value := *param.Min
				param.Min = &value
			}
			if param.Max != nil {
				value := *param.Max
				param.Max = &value
			}
			params[j] = param
		}
		action.Params = params
		actions[i] = action
	}
	tool.Actions = actions
	return tool
}

// fuzzyScore reports whether every rune of query appears in target in order
// (subsequence match), scoring contiguous runs and start-of-string matches
// higher.
func fuzzyScore(query, target string) (int, bool) {
	if query == "" {
		return 0, true
	}
	q := []rune(query)
	t := []rune(target)
	score := 0
	qi := 0
	lastMatch := -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] == q[qi] {
			score += 1
			if ti == lastMatch+1 {
				score += 3 // contiguous run
			}
			if ti == 0 {
				score += 2 // anchored at start
			}
			lastMatch = ti
			qi++
		}
	}
	if qi != len(q) {
		return -1, false
	}
	return score, true
}
