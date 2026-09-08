// Package form maps validated action schemas onto huh controls and back into
// typed, validated string values for command assembly.
package form

import (
	"github.com/charmbracelet/huh"
	"github.com/hsuanchenlin/control-center/internal/config"
)

// Values maps param keys to the raw strings shown in the form. Toggles are
// "true"/"false". These are the values preserved when navigating backward.
type Values map[string]string

// Form wraps a huh.Form for one action plus the backing value holders.
type Form struct {
	action config.Action
	Model  *huh.Form

	strings map[string]*string
	toggles map[string]*bool
}

// New builds the form for an action. initial carries values from a previous
// visit within the same run so navigating back never loses edits; missing
// keys fall back to the manifest defaults.
func New(action config.Action, initial Values) *Form {
	f := &Form{
		action:  action,
		strings: map[string]*string{},
		toggles: map[string]*bool{},
	}

	fields := make([]huh.Field, 0, len(action.Params))
	for _, p := range action.Params {
		start := config.InitialValue(p)
		if v, ok := initial[p.Key]; ok {
			start = v
		}
		title := p.Label
		if p.Required {
			title += " *"
		}

		switch p.Type {
		case config.ParamToggle:
			v := start == "true"
			holder := &v
			f.toggles[p.Key] = holder
			fields = append(fields, huh.NewConfirm().
				Key(p.Key).
				Title(title).
				Description(p.Description).
				Value(holder))
		case config.ParamSelect:
			holder := new(string)
			*holder = start
			f.strings[p.Key] = holder
			choices := p.Choices
			if !p.Required && p.Default == "" {
				// Allow explicitly choosing "no value" for optional selects.
				choices = append([]string{""}, choices...)
			}
			opts := make([]huh.Option[string], len(choices))
			for i, c := range choices {
				label := c
				if c == "" {
					label = "(none)"
				}
				opts[i] = huh.NewOption(label, c)
			}
			fields = append(fields, huh.NewSelect[string]().
				Key(p.Key).
				Title(title).
				Description(p.Description).
				Options(opts...).
				Value(holder))
		default: // text, number, path
			holder := new(string)
			*holder = start
			f.strings[p.Key] = holder
			fields = append(fields, huh.NewInput().
				Key(p.Key).
				Title(title).
				Description(p.Description).
				Value(holder).
				Validate(validator(p)))
		}
	}

	f.Model = huh.NewForm(huh.NewGroup(fields...))
	return f
}

func validator(p config.Param) func(string) error {
	return func(s string) error {
		_, err := config.ValidateValue(p, s)
		return err
	}
}

// Snapshot returns the current raw field values without final validation, for
// preserving edits when the user navigates backward.
func (f *Form) Snapshot() Values {
	out := Values{}
	for k, p := range f.strings {
		out[k] = *p
	}
	for k, p := range f.toggles {
		if *p {
			out[k] = "true"
		} else {
			out[k] = "false"
		}
	}
	return out
}

// Values validates every field and returns the raw values ready for
// command.Build.
func (f *Form) Values() (Values, error) {
	out := f.Snapshot()
	for _, p := range f.action.Params {
		if _, err := config.ValidateValue(p, out[p.Key]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
