# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

- Add durable project-specific notes here as they are discovered through real work.

## control-center

Go TUI launcher for curated CLI tools (Bubble Tea / huh / Lip Gloss). Product spec, schema reference, and keybindings live in `README.md`; the annotated manifest is `examples/tools.toml` - keep both in sync with code changes.

- Build/test: `go build ./...`, `go test ./...` (hermetic: no network, no real child tools; executor tests use the `TestHelperProcess` re-exec pattern), `go vet ./...`, `gofmt -l .`.
- Module boundaries: `internal/config` (TOML schema + validation, framework-free), `internal/registry` (lookup + fuzzy match over tools and action refs), `internal/command` (argv assembly; `Display` string is presentation-only, never executed), `internal/form` (huh mapping), `internal/executor` (injected process/clipboard/clock/terminal), `internal/history` (persistent recent runs, framework-free, injected clock; JSON at `$XDG_STATE_HOME/control-center/history.json`, atomic writes, 0600), `internal/tui` (Bubble Tea state machine). Keep charmbracelet types out of `config`/`registry`/`command`.
- Safety invariants: children always exec without a shell; positional params must be `required`; single-letter shortcuts must not fire while a text field is focused; no secret param type by design.
- Version is injected at build time: `-ldflags "-X main.version=..."`.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
