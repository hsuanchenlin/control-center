// Command control-center is a keyboard-first TUI launcher and configurator
// for a curated set of command-line tools.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hsuanchenlin/control-center/internal/config"
	"github.com/hsuanchenlin/control-center/internal/executor"
	"github.com/hsuanchenlin/control-center/internal/history"
	"github.com/hsuanchenlin/control-center/internal/registry"
	"github.com/hsuanchenlin/control-center/internal/tui"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `control-center - a keyboard-first launcher for your curated CLI tools

Usage:
  control-center [--config <path>]     launch the TUI
  control-center validate [--config <path>]   check the manifest and exit
  control-center --version             print version
  control-center --help                show this help

Configuration lives at ~/.config/control-center/tools.toml (or
$XDG_CONFIG_HOME/control-center/tools.toml), plus tools.d/*.toml alongside it.
An explicit --config file loads only that file; a directory loads its tools.toml
and tools.d/*.toml. See the README for the schema.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// Allow the subcommand first: `control-center validate --config x`.
	validate := false
	if len(args) > 0 && args[0] == "validate" {
		validate = true
		args = args[1:]
	}

	fs := flag.NewFlagSet("control-center", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "manifest file or directory (default: ~/.config/control-center)")
	showVersion := fs.Bool("version", false, "print version and exit")
	showHelp := fs.Bool("help", false, "show help and exit")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	switch {
	case *showHelp:
		fmt.Fprint(os.Stdout, usage)
		return 0
	case *showVersion:
		fmt.Println("control-center " + version)
		return 0
	}

	path := *configPath
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		path = filepath.Dir(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "control-center:", err)
			return 1
		}
	}

	rest := fs.Args()
	if !validate && len(rest) > 0 && rest[0] == "validate" {
		validate = true
		rest = rest[1:]
	}
	if len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "control-center: unknown command %q\n", rest[0])
		fs.Usage()
		return 2
	}
	if validate {
		return runValidate(path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "control-center:", err)
		return 1
	}
	return runTUI(cfg)
}

func runValidate(path string) int {
	_, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "control-center: invalid config:", err)
		return 1
	}
	fmt.Printf("%s: OK\n", path)
	return 0
}

// loadHistory opens the persistent run-history store. History is
// best-effort: when the state path cannot be resolved or read, the TUI runs
// without it rather than failing to launch.
func loadHistory() *history.Store {
	path, err := history.DefaultPath()
	if err != nil {
		return nil
	}
	store, err := history.Load(path)
	if err != nil {
		return nil
	}
	return store
}

// programTerminal adapts the live Bubble Tea program to the executor's
// Terminal boundary for passthrough children. The program pointer is wired
// after construction because the program is built from the model.
type programTerminal struct {
	p *tea.Program
}

func (t *programTerminal) Release() error { return t.p.ReleaseTerminal() }
func (t *programTerminal) Restore() error { return t.p.RestoreTerminal() }

func runTUI(cfg *config.Config) int {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	term := &programTerminal{}
	model := tui.New(tui.Deps{
		Registry:  registry.New(cfg),
		Runner:    executor.NewRunner(),
		Clipboard: executor.SystemClipboard{},
		Terminal:  term,
		Signals:   signals,
		Clock:     executor.SystemClock{},
		History:   loadHistory(),
	})
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithoutSignalHandler())
	term.p = p
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "control-center:", err)
		return 1
	}
	if final, ok := finalModel.(tui.Model); ok && final.FatalError() != nil {
		fmt.Fprintln(os.Stderr, "control-center:", final.FatalError())
		return 1
	}
	return 0
}
