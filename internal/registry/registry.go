// Package registry provides immutable lookup over a validated manifest and
// the fuzzy matching over tools and individual actions used by the palette.
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

// ToolMatch is a tool ranked against a fuzzy query.
type ToolMatch struct {
	Tool  config.Tool
	Score int
}

// ActionRef pairs a tool with one of its actions, so individual actions can
// be indexed and searched as palette entities.
type ActionRef struct {
	Tool   config.Tool
	Action config.Action
}

// ActionMatch is an action ranked against a fuzzy query.
type ActionMatch struct {
	ActionRef
	Score int
}

// Actions returns every action of every tool in manifest order.
func (r *Registry) Actions() []ActionRef {
	var out []ActionRef
	for _, t := range r.tools {
		tool := cloneTool(t)
		for _, a := range tool.Actions {
			out = append(out, ActionRef{Tool: tool, Action: a})
		}
	}
	return out
}

// Match ranks tools against a fuzzy query over id, name, description, and
// group. An empty query returns all tools in manifest order. Results are
// ordered by descending score, ties broken by manifest order.
func (r *Registry) Match(query string) []config.Tool {
	scored := r.MatchTools(query)
	out := make([]config.Tool, len(scored))
	for i, sm := range scored {
		out[i] = sm.Tool
	}
	return out
}

// MatchTools is Match with scores preserved, so callers can merge tool and
// action results into one ranked list.
func (r *Registry) MatchTools(query string) []ToolMatch {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		out := make([]ToolMatch, 0, len(r.tools))
		for _, t := range r.tools {
			out = append(out, ToolMatch{Tool: cloneTool(t)})
		}
		return out
	}
	type scored struct {
		idx   int
		score int
	}
	var hits []scored
	for i, t := range r.tools {
		best := -1
		for _, field := range []string{t.ID, t.Name, t.Description, t.Group} {
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
	out := make([]ToolMatch, len(hits))
	for i, h := range hits {
		out[i] = ToolMatch{Tool: cloneTool(r.tools[h.idx]), Score: h.score}
	}
	return out
}

// MatchActions ranks individual actions against a fuzzy query over the action
// name, its description, and the combined "tool name + action name" (so
// "brew upgrade" matches Homebrew's upgrade action). An empty query returns
// all actions in manifest order; results are ordered by descending score,
// ties broken by manifest order.
func (r *Registry) MatchActions(query string) []ActionMatch {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		refs := r.Actions()
		out := make([]ActionMatch, len(refs))
		for i, ref := range refs {
			out[i] = ActionMatch{ActionRef: ref}
		}
		return out
	}
	type scored struct {
		idx   int
		score int
		ref   ActionRef
	}
	var hits []scored
	idx := 0
	for _, t := range r.tools {
		tool := cloneTool(t)
		for _, a := range tool.Actions {
			combined := t.Name + " " + a.Name
			best := -1
			for _, field := range []string{a.Name, a.Description, combined} {
				if s, ok := fuzzyScore(query, strings.ToLower(field)); ok && s > best {
					best = s
				}
			}
			if best >= 0 {
				hits = append(hits, scored{idx: idx, score: best, ref: ActionRef{Tool: tool, Action: a}})
			}
			idx++
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].idx < hits[b].idx
	})
	out := make([]ActionMatch, len(hits))
	for i, h := range hits {
		out[i] = ActionMatch{ActionRef: h.ref, Score: h.score}
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
