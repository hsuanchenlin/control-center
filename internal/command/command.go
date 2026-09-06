// Package command assembles argv arrays from a validated manifest and form
// values, and renders a shell-escaped display string. Execution always uses
// the argv array without a shell; the display string is presentation only.
package command

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hsuanchenlin/control-center/internal/config"
)

// Spec is a fully assembled, ready-to-execute command.
type Spec struct {
	// Executable is the bare command name resolved via PATH at launch time.
	Executable string
	// Args is the complete argument vector (excluding argv[0]).
	Args []string
	// Display is a shell-escaped rendering for the confirmation screen.
	// It is never used for execution.
	Display string
}

// Build assembles the argv for an action from raw form values keyed by param
// key. Every value is validated against its param's type and constraints.
// Assembly order: the action's fixed args, then flag params in manifest
// order, then positional params ordered by their positional index.
func Build(tool config.Tool, action config.Action, values map[string]string) (Spec, error) {
	args := make([]string, 0, len(action.Args)+len(action.Params)*2)
	args = append(args, action.Args...)

	type positional struct {
		idx int
		val string
	}
	var positionals []positional

	for _, p := range action.Params {
		raw, ok := values[p.Key]
		if !ok {
			raw = config.InitialValue(p)
		}
		v, err := config.ValidateValue(p, raw)
		if err != nil {
			return Spec{}, fmt.Errorf("%s: %w", tool.ID, err)
		}
		if p.Type == config.ParamToggle {
			if v == "true" {
				args = append(args, p.Flag)
			}
			continue
		}
		if v == "" {
			// Optional flag param left empty: omit flag and value together.
			// (Positional params are required by manifest validation, so an
			// empty positional cannot reach here after validation below.)
			if p.Positional != nil {
				return Spec{}, fmt.Errorf("%s: %s is required", tool.ID, p.Label)
			}
			continue
		}
		if p.Positional != nil {
			positionals = append(positionals, positional{idx: *p.Positional, val: v})
			continue
		}
		args = append(args, p.Flag, v)
	}

	sort.SliceStable(positionals, func(i, j int) bool { return positionals[i].idx < positionals[j].idx })
	for _, pos := range positionals {
		args = append(args, pos.val)
	}

	full := append([]string{tool.Executable}, args...)
	return Spec{
		Executable: tool.Executable,
		Args:       args,
		Display:    DisplayString(full),
	}, nil
}

// DisplayString renders an argv vector as a shell-escaped string for display.
// Each element is single-quoted when it contains characters a shell would
// interpret; embedded single quotes are escaped as '\”. This is purely
// presentational: execution never passes through a shell.
func DisplayString(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, needsQuote) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	switch r {
	case '_', '-', '.', '/', ':', ',', '+', '=', '@', '%':
		return false
	}
	return true
}
