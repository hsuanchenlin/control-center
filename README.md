# control-center

A local, keyboard-first TUI launcher and configurator for a curated set of
command-line tools. Open one fast terminal app, fuzzy-find a tool, pick an
action, edit typed parameters, review the exact command, and run it.

control-center is **not** a shell replacement, a PATH scanner, an app
launcher, a cloud service, or a daemon. It only knows the tools you
explicitly configure, and it never touches the network itself.

## Install

```sh
# From a clone of this repository:
go install github.com/hsuanchenlin/control-center@latest   # once published

# Local build and install:
git clone git@github.com:hsuanchenlin/control-center.git
cd control-center
go build -o control-center .
sudo install -m 0755 control-center /usr/local/bin/        # or any PATH dir

# Version-stamped release build:
go build -ldflags "-X main.version=$(git describe --tags --always)" -o control-center .
```

## Usage

```sh
control-center                        # launch the TUI
control-center --config /path/tools.toml
control-center validate [--config …]  # check the manifest, exit non-zero on errors
control-center --version
control-center --help
```

## Configuration

The manifest lives at `~/.config/control-center/tools.toml`
(`$XDG_CONFIG_HOME/control-center/tools.toml` when `XDG_CONFIG_HOME` is
set). Override it with `--config <path>`. A complete, annotated example is
in [`examples/tools.toml`](examples/tools.toml).

The manifest is hand-curated: control-center never scans `PATH` or
auto-discovers tools. Run `control-center validate` after editing.

### Schema reference

```toml
[[tool]]
id = "my-tool"              # required, stable, unique
name = "My Tool"            # required, display name
description = "what it does"
executable = "my-cli"       # required; bare command name resolved via PATH
output = "capture"          # optional: "capture" (default) or "passthrough"

[[tool.action]]
name = "run"                # required, unique within the tool
description = "run it"
args = ["run", "--fast"]    # fixed argv prefix, literal (no templating)

[[tool.action.param]]
key = "target"              # required, stable, unique within the action
label = "Target"            # required, shown in the form
description = "optional help text"
type = "text"               # text | select | toggle | number | path
required = true
default = "world"           # optional; strings everywhere (toggle: "true"/"false")
choices = ["a", "b"]        # select only
min = 1                     # number only
max = 100                   # number only
must_exist = true           # path only
flag = "--target"           # exactly one of:
positional = 0              #   flag mapping or positional index
```

Rules enforced by `validate`:

- Duplicate tool ids, action names, param keys, and positional indices are
  rejected.
- Every param sets exactly one of `flag` or `positional`.
- Flags must be single tokens starting with `-` (no spaces, no `=`); values
  are always passed as a **separate argv element**. How a child parses a
  value that itself starts with `-` depends on that child's argument parser.
- Positional params must be `required` (an empty value would silently shift
  every later positional).
- `toggle` params are flag-only and are emitted only when true.
- `select` params need at least one choice; defaults must be among them.
- `number` defaults must be numeric and within `min`/`max`.
- Unknown fields are rejected with their TOML key path. Unknown type/output
  values and other validation errors identify the affected tool, action, or
  parameter.

### Parameter types

| Type     | Control      | Notes                                                        |
|----------|--------------|--------------------------------------------------------------|
| `text`   | text input   | Optional default, required validation.                       |
| `select` | choice list  | Fixed `choices`; optional selects offer `(none)`.            |
| `toggle` | yes/no       | Emits its `flag` only when true.                             |
| `number` | text input   | Numeric parsing with optional `min`/`max`.                   |
| `path`   | text input   | Leading `~` expands to your home dir; no shell globbing; `must_exist` is checked only when declared. |

There is deliberately **no secret parameter type** - do not put tokens or
passwords in the manifest; they are not persisted anywhere by
control-center, and manifests are plain text.

## Keybindings

| Screen      | Keys                                                                 |
|-------------|----------------------------------------------------------------------|
| Palette     | Type to filter · ↑/↓ or Ctrl-P/Ctrl-N move · Enter select · Esc clear filter · Ctrl-C exit. The filter always has focus, so every printable key (including `q`) is literal input. |
| Action      | ↑/↓ or Ctrl-P/Ctrl-N move · Enter select · Esc back · Ctrl-C exit    |
| Form        | Type to edit · Tab/Shift-Tab move fields · Enter submit · Esc back (edits preserved) · Ctrl-C exit |
| Confirm     | Enter run · `e` copy the command to the clipboard without running · Esc back · Ctrl-C exit |
| Output      | Capture: ↑/↓ (PgUp/PgDn) scroll · Ctrl-C interrupt the child · Ctrl-C again force-stop and exit after cleanup · `q`/Esc back once finished. Passthrough: Ctrl-C belongs to the child; control-center resumes after it exits. |

Single-letter shortcuts never fire while a text field is focused.

## Child processes, output, and privacy

- Commands run as **executable + argv array without a shell**
  (`exec.LookPath` + direct exec). Manifest and form values are never
  concatenated into `sh -c`.
- The confirmation screen shows a shell-escaped rendering of the command;
  that string is display-only and is what `e` copies.
- **capture** mode (default) streams stdout/stderr into a scrollable
  viewport bounded to the last 5,000 lines of at most 64 KB each (so
  newline-free output such as a progress bar stays bounded too), shows exit
  status and elapsed time, sanitizes invalid UTF-8, and survives empty
  output, nonzero exits, and missing executables.
- **passthrough** mode suspends the TUI and hands the terminal directly to
  the child, restoring the terminal afterwards even on error or interrupt;
  a failed restore is reported on stderr before control-center exits. The
  child owns Ctrl-C while passthrough is active;
  control-center waits for it to exit and then restores the terminal.
- In capture mode, Ctrl-C interrupts the child first (SIGINT, escalating to
  a kill if the child ignores it). A second Ctrl-C force-stops it, and
  control-center exits only after the child has been reaped.
- control-center makes no network connections of its own. Whatever a
  launched child does (e.g. a tool's own update check) is that tool's
  behavior. No telemetry, no background server, nothing persisted.

## Adding or editing a tool safely

1. Copy `examples/tools.toml` to `~/.config/control-center/tools.toml`.
2. Add a `[[tool]]` block with a bare `executable` name, then
   `[[tool.action]]` blocks with fixed `args` prefixes.
3. Add `[[tool.action.param]]` blocks; prefer `flag = "--name"` over
   positionals, and mark anything destructive or state-changing in the
   action `description` (control-center never bypasses the tool's own
   confirmations).
4. Run `control-center validate` and fix any reported errors.
5. Launch `control-center`, select the tool, and use `e` on the confirm
   screen to copy the exact command before running it.

## Troubleshooting

- **"executable … not found in PATH"** - install the tool, or fix the
  `executable` field. `control-center validate` cannot check this; the
  error appears on the output screen when you run the action.
- **Malformed TOML / validation errors** - run `control-center validate`;
  decode errors identify the source or TOML key path, while validation errors
  identify the tool, action, and parameter at fault.
- **Terminal looks broken after a passthrough command** - control-center
  always restores the terminal after passthrough children; if a child
  crashed hard, run `reset` or `stty sane`, then report a bug.
- **A tool needs interactive stdin** - set `output = "passthrough"` on that
  tool so the child inherits the terminal.

## Development

```sh
go test ./...     # hermetic tests (no network, no real tools, no real config)
go vet ./...
gofmt -l .
```

Layout: `internal/config` (schema, loading, validation), `internal/registry`
(lookup + fuzzy match), `internal/command` (argv assembly + display
escaping), `internal/form` (schema → huh controls), `internal/executor`
(process/clipboard/clock/terminal boundaries), `internal/tui` (Bubble Tea
state machine). Framework types stay out of `config`, `registry`, and
`command`.
