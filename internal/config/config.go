// Package config loads, validates, and resolves paths for control-center
// manifests. It contains no framework-specific types: the schema here is the
// single source of truth for tools, actions, and parameters.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// OutputMode declares how a launched child process is wired to the terminal.
type OutputMode string

const (
	// OutputCapture streams stdout/stderr into a bounded, scrollable viewport.
	OutputCapture OutputMode = "capture"
	// OutputPassthrough suspends the TUI and hands the terminal to the child.
	OutputPassthrough OutputMode = "passthrough"
)

// ParamType enumerates the supported parameter control kinds.
type ParamType string

const (
	ParamText   ParamType = "text"
	ParamSelect ParamType = "select"
	ParamToggle ParamType = "toggle"
	ParamNumber ParamType = "number"
	ParamPath   ParamType = "path"
)

// Config is a decoded and validated manifest.
type Config struct {
	Tools []Tool `toml:"tool"`
}

// Tool is a registered command-line tool.
type Tool struct {
	ID          string `toml:"id"`
	Name        string `toml:"name"`
	Description string `toml:"description"`
	// Group is an optional category label (e.g. "System", "Research") shown in
	// the palette and searched by fuzzy matching.
	Group      string     `toml:"group"`
	Executable string     `toml:"executable"`
	Output     OutputMode `toml:"output"`
	// Pinned sorts the tool to the top of the palette when the filter is
	// empty.
	Pinned  bool     `toml:"pinned"`
	Actions []Action `toml:"action"`
}

// Action is one invocable operation of a tool.
type Action struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	// Args is the fixed argv prefix for the action (e.g. ["start"]). Values
	// are literal; no templating is performed.
	Args []string `toml:"args"`
	// Output optionally overrides the tool's output mode for this action
	// (e.g. one interactive action on an otherwise captured tool).
	Output OutputMode `toml:"output"`
	// Pinned surfaces the action as its own palette row at the top when the
	// filter is empty.
	Pinned bool    `toml:"pinned"`
	Params []Param `toml:"param"`
}

// OutputFor returns the action's effective output mode: the action's own
// override when set, otherwise the tool's mode (which Validate has already
// defaulted to capture).
func (t Tool) OutputFor(a Action) OutputMode {
	if a.Output != "" {
		return a.Output
	}
	if t.Output != "" {
		return t.Output
	}
	return OutputCapture
}

// Param is a typed parameter of an action.
type Param struct {
	Key         string    `toml:"key"`
	Label       string    `toml:"label"`
	Description string    `toml:"description"`
	Type        ParamType `toml:"type"`
	Required    bool      `toml:"required"`
	// Default is the initial value, as a string for every type; toggles use
	// "true"/"false" and numbers use a decimal literal.
	Default string `toml:"default"`
	// Choices constrains select params.
	Choices []string `toml:"choices"`
	// Flag, when set, maps the value to "--flag value" as separate argv
	// elements. Toggles emit the flag alone when true.
	Flag string `toml:"flag"`
	// Env places a value in the child's environment instead of argv.
	Env string `toml:"env"`
	// Multiline uses a text editor; valid only for text parameters.
	Multiline bool `toml:"multiline"`
	// Positional, when non-nil, places the value positionally; positional
	// params are ordered by this index after all flag params.
	Positional *int `toml:"positional"`
	// Min/Max optionally bound number params.
	Min *float64 `toml:"min"`
	Max *float64 `toml:"max"`
	// MustExist, for path params, requires the expanded path to exist.
	MustExist bool `toml:"must_exist"`
}

// DefaultPath returns the legacy default manifest file path:
// $XDG_CONFIG_HOME/control-center/tools.toml, falling back to
// ~/.config/control-center/tools.toml (macOS and Linux alike).
// Callers using the modern directory loader should use filepath.Dir on this result.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "control-center", "tools.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "control-center", "tools.toml"), nil
}

// Load reads and validates the manifest at path. If path is a directory, it
// delegates to LoadDirectory. A missing file yields a descriptive error naming
// the expected location.
func Load(path string) (*Config, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return LoadDirectory(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config not found at %s (create it or pass --config; see the README for the schema)", path)
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(data, path)
}

// Parse decodes and validates manifest bytes; source names the origin used in
// error messages.
func Parse(data []byte, source string) (*Config, error) {
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("%s: unknown field(s): %s", source, strings.Join(keys, ", "))
	}
	if err := cfg.Validate(source); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks the manifest and returns an actionable error naming the
// offending tool/action/parameter.
func (c *Config) Validate(source string) error {
	if len(c.Tools) == 0 {
		return fmt.Errorf("%s: manifest defines no [[tool]] entries", source)
	}
	seenTools := map[string]bool{}
	for ti := range c.Tools {
		t := &c.Tools[ti]
		where := fmt.Sprintf("%s: tool %q", source, toolLabel(t))
		if t.ID == "" {
			return fmt.Errorf("%s: id is required and must be stable", where)
		}
		if seenTools[t.ID] {
			return fmt.Errorf("%s: duplicate tool id %q", where, t.ID)
		}
		seenTools[t.ID] = true
		if t.Name == "" {
			return fmt.Errorf("%s: name is required", where)
		}
		if t.Group != "" {
			if strings.TrimSpace(t.Group) != t.Group {
				return fmt.Errorf("%s: group %q must not have leading or trailing whitespace", where, t.Group)
			}
			if strings.IndexFunc(t.Group, unicode.IsControl) >= 0 {
				return fmt.Errorf("%s: group %q must be a single line without control characters", where, t.Group)
			}
		}
		if strings.TrimSpace(t.Executable) == "" {
			return fmt.Errorf("%s: executable is required and must not be empty", where)
		}
		if strings.ContainsAny(t.Executable, "/\\") {
			return fmt.Errorf("%s: executable %q must be a bare command name resolved via PATH, not a path", where, t.Executable)
		}
		switch t.Output {
		case "":
			t.Output = OutputCapture
		case OutputCapture, OutputPassthrough:
		default:
			return fmt.Errorf("%s: unknown output mode %q (want %q or %q)", where, t.Output, OutputCapture, OutputPassthrough)
		}
		if len(t.Actions) == 0 {
			return fmt.Errorf("%s: at least one [[tool.action]] is required", where)
		}
		seenActions := map[string]bool{}
		for ai := range t.Actions {
			a := &t.Actions[ai]
			awhere := fmt.Sprintf("%s action %q", where, actionLabel(a, ai))
			if a.Name == "" {
				return fmt.Errorf("%s: name is required", awhere)
			}
			if seenActions[a.Name] {
				return fmt.Errorf("%s: duplicate action name %q", awhere, a.Name)
			}
			seenActions[a.Name] = true
			switch a.Output {
			case "", OutputCapture, OutputPassthrough:
			default:
				return fmt.Errorf("%s: unknown output mode %q (want %q or %q)", awhere, a.Output, OutputCapture, OutputPassthrough)
			}
			for i, arg := range a.Args {
				if arg == "" {
					return fmt.Errorf("%s: args[%d] must not be empty", awhere, i)
				}
			}
			if err := validateParams(awhere, a.Params); err != nil {
				return err
			}
		}
	}
	return nil
}

func toolLabel(t *Tool) string {
	if t.ID != "" {
		return t.ID
	}
	if t.Name != "" {
		return t.Name
	}
	return "(unnamed)"
}

func actionLabel(a *Action, idx int) string {
	if a.Name != "" {
		return a.Name
	}
	return fmt.Sprintf("#%d", idx+1)
}

func validateParams(where string, params []Param) error {
	seenKeys := map[string]bool{}
	seenPositional := map[int]string{}
	seenEnv := map[string]bool{}
	for i := range params {
		p := &params[i]
		pwhere := fmt.Sprintf("%s param %q", where, paramLabel(p, i))
		if p.Key == "" {
			return fmt.Errorf("%s: key is required and must be stable", pwhere)
		}
		if seenKeys[p.Key] {
			return fmt.Errorf("%s: duplicate param key %q", pwhere, p.Key)
		}
		seenKeys[p.Key] = true
		if p.Label == "" {
			return fmt.Errorf("%s: label is required", pwhere)
		}
		switch p.Type {
		case ParamText, ParamSelect, ParamToggle, ParamNumber, ParamPath:
		case "":
			return fmt.Errorf("%s: type is required (text, select, toggle, number, path)", pwhere)
		default:
			return fmt.Errorf("%s: unknown type %q (want text, select, toggle, number, or path)", pwhere, p.Type)
		}

		if p.Multiline && p.Type != ParamText {
			return fmt.Errorf("%s: multiline applies only to text params", pwhere)
		}
		if p.Env != "" {
			if !validEnvName(p.Env) {
				return fmt.Errorf("%s: invalid environment name %q", pwhere, p.Env)
			}
			if seenEnv[p.Env] {
				return fmt.Errorf("%s: duplicate environment name %q", pwhere, p.Env)
			}
			seenEnv[p.Env] = true
			if p.Flag != "" || p.Positional != nil {
				return fmt.Errorf("%s: env is mutually exclusive with flag and positional", pwhere)
			}
		}
		hasFlag := p.Flag != ""
		hasPositional := p.Positional != nil
		switch {
		case hasFlag && hasPositional:
			return fmt.Errorf("%s: set either flag or positional, not both", pwhere)
		case !hasFlag && !hasPositional && p.Env == "":
			return fmt.Errorf("%s: one of flag or positional is required (or env for environment placement)", pwhere)
		}
		if hasFlag {
			if !strings.HasPrefix(p.Flag, "-") || len(p.Flag) < 2 || p.Flag == "--" {
				return fmt.Errorf("%s: unsafe flag %q: must look like -x or --long", pwhere, p.Flag)
			}
			if strings.ContainsAny(p.Flag, " \t\n=") {
				return fmt.Errorf("%s: unsafe flag %q: must be a single token without spaces or '='", pwhere, p.Flag)
			}
		}
		if hasPositional {
			if *p.Positional < 0 {
				return fmt.Errorf("%s: positional index must be >= 0", pwhere)
			}
			if prev, dup := seenPositional[*p.Positional]; dup {
				return fmt.Errorf("%s: positional index %d already used by param %q", pwhere, *p.Positional, prev)
			}
			seenPositional[*p.Positional] = p.Key
			// An omitted positional would shift every later positional, so
			// empty values are never safe here.
			if !p.Required {
				return fmt.Errorf("%s: positional params must be required (an empty value would shift later positionals)", pwhere)
			}
		}

		switch p.Type {
		case ParamToggle:
			if !hasFlag && p.Env == "" {
				return fmt.Errorf("%s: toggle params require a flag or env; positional placement is not meaningful", pwhere)
			}
			if p.Default != "" && p.Default != "true" && p.Default != "false" {
				return fmt.Errorf("%s: toggle default %q must be \"true\" or \"false\"", pwhere, p.Default)
			}
			if len(p.Choices) > 0 {
				return fmt.Errorf("%s: choices apply only to select params", pwhere)
			}
		case ParamSelect:
			if len(p.Choices) == 0 {
				return fmt.Errorf("%s: select params require at least one choice", pwhere)
			}
			seen := map[string]bool{}
			for _, ch := range p.Choices {
				if ch == "" {
					return fmt.Errorf("%s: choices must not contain empty values", pwhere)
				}
				if seen[ch] {
					return fmt.Errorf("%s: duplicate choice %q", pwhere, ch)
				}
				seen[ch] = true
			}
			if p.Default != "" && !seen[p.Default] {
				return fmt.Errorf("%s: default %q is not among choices %v", pwhere, p.Default, p.Choices)
			}
		case ParamNumber:
			if len(p.Choices) > 0 {
				return fmt.Errorf("%s: choices apply only to select params", pwhere)
			}
			if p.Min != nil && (math.IsNaN(*p.Min) || math.IsInf(*p.Min, 0)) {
				return fmt.Errorf("%s: min must be a finite number", pwhere)
			}
			if p.Max != nil && (math.IsNaN(*p.Max) || math.IsInf(*p.Max, 0)) {
				return fmt.Errorf("%s: max must be a finite number", pwhere)
			}
			if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
				return fmt.Errorf("%s: min (%v) is greater than max (%v)", pwhere, *p.Min, *p.Max)
			}
			if p.Default != "" {
				v, err := strconv.ParseFloat(p.Default, 64)
				if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
					return fmt.Errorf("%s: number default %q is not numeric", pwhere, p.Default)
				}
				if p.Min != nil && v < *p.Min {
					return fmt.Errorf("%s: default %v is below min %v", pwhere, v, *p.Min)
				}
				if p.Max != nil && v > *p.Max {
					return fmt.Errorf("%s: default %v is above max %v", pwhere, v, *p.Max)
				}
			}
		case ParamText, ParamPath:
			if len(p.Choices) > 0 {
				return fmt.Errorf("%s: choices apply only to select params", pwhere)
			}
			if p.Min != nil || p.Max != nil {
				return fmt.Errorf("%s: min/max apply only to number params", pwhere)
			}
		}
		if p.Type != ParamPath && p.MustExist {
			return fmt.Errorf("%s: must_exist applies only to path params", pwhere)
		}
	}
	return nil
}

func validEnvName(s string) bool {
	for i, r := range s {
		if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return s != ""
}

func paramLabel(p *Param, idx int) string {
	if p.Key != "" {
		return p.Key
	}
	return fmt.Sprintf("#%d", idx+1)
}

// ExpandPath expands a leading "~" in p to the user's home directory. It
// performs no globbing and no shell evaluation.
func ExpandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		return filepath.Join(home, p[1:]), nil
	}
	return p, nil
}

// InitialValue returns the form's starting value for a param: its default, or
// the type's zero value.
func InitialValue(p Param) string {
	if p.Default != "" {
		return p.Default
	}
	if p.Type == ParamToggle {
		return "false"
	}
	return ""
}

// ValidateValue checks a raw form value against the param's type and
// constraints and returns the normalized string to place in argv. An empty
// value for a non-required, non-positional param yields ("", nil) and is
// omitted from argv by the caller.
func ValidateValue(p Param, raw string) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("%s: value must not contain NUL", p.Label)
	}
	if p.Type == ParamToggle {
		switch raw {
		case "true":
			return "true", nil
		case "false", "":
			return "false", nil
		default:
			return "", fmt.Errorf("%s: toggle value %q must be true or false", p.Label, raw)
		}
	}
	if raw == "" {
		if p.Required {
			return "", fmt.Errorf("%s is required", p.Label)
		}
		return "", nil
	}
	switch p.Type {
	case ParamSelect:
		for _, ch := range p.Choices {
			if raw == ch {
				return raw, nil
			}
		}
		return "", fmt.Errorf("%s: %q is not one of %s", p.Label, raw, strings.Join(p.Choices, ", "))
	case ParamNumber:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("%s: %q is not a number", p.Label, raw)
		}
		if p.Min != nil && v < *p.Min {
			return "", fmt.Errorf("%s: %v is below the minimum %v", p.Label, v, *p.Min)
		}
		if p.Max != nil && v > *p.Max {
			return "", fmt.Errorf("%s: %v is above the maximum %v", p.Label, v, *p.Max)
		}
		return raw, nil
	case ParamPath:
		expanded, err := ExpandPath(raw)
		if err != nil {
			return "", err
		}
		if p.MustExist {
			if _, err := os.Stat(expanded); err != nil {
				return "", fmt.Errorf("%s: %s", p.Label, err)
			}
		}
		return expanded, nil
	default: // ParamText
		return raw, nil
	}
}
