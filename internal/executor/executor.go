// Package executor owns the process, clipboard, clock, and terminal
// boundaries for control-center. All side effects are injected so tests stay
// hermetic.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	"github.com/hsuanchenlin/control-center/internal/command"
)

// Result is the outcome of a captured child process.
type Result struct {
	ExitCode int
	Elapsed  time.Duration
	// Err is non-nil only when the process could not be started or waited on;
	// nonzero exits are reported via ExitCode, not Err.
	Err error
}

// Started reports whether the child process was successfully launched.
func (r Result) Started() bool { return r.Err == nil }

// Clock supplies the current time.
type Clock interface {
	Now() time.Time
}

// SystemClock is the real clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// Clipboard copies text to the system clipboard.
type Clipboard interface {
	WriteString(s string) error
}

// SystemClipboard uses the OS clipboard via atotto/clipboard.
type SystemClipboard struct{}

// WriteString copies s to the OS clipboard.
func (SystemClipboard) WriteString(s string) error { return clipboard.WriteAll(s) }

// Terminal is the passthrough boundary: it releases the TUI's terminal
// control before a child runs interactively and restores it afterwards.
type Terminal interface {
	// Release suspends TUI rendering and returns the terminal to cooked mode.
	Release() error
	// Restore re-acquires the terminal for the TUI after the child exits.
	Restore() error
}

// Runner launches child processes without a shell.
type Runner struct {
	// LookPath resolves the executable; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// Stdin/Stdout/Stderr wire passthrough children to the terminal;
	// default to the process's own descriptors.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// StdinIsTerminal reports whether Stdin is a real terminal, required for
	// passthrough; defaults to checking os.Stdin.
	StdinIsTerminal func() bool
	// StartProcess, when set, replaces the real exec for capture mode; tests
	// inject fakes here. It receives the resolved path and argv and returns
	// a wait function yielding the exit code.
	StartProcess func(ctx context.Context, path string, args []string, stdout, stderr io.Writer) (wait func() (int, error), err error)
}

// NewRunner returns a Runner wired to the real OS.
func NewRunner() *Runner { return &Runner{} }

func (r *Runner) lookPath() func(string) (string, error) {
	if r.LookPath != nil {
		return r.LookPath
	}
	return exec.LookPath
}

// Resolve locates the executable deterministically, reporting a clear error
// when it is missing from PATH.
func (r *Runner) Resolve(executable string) (string, error) {
	path, err := r.lookPath()(executable)
	if err != nil {
		return "", fmt.Errorf("executable %q not found in PATH: install it or fix the manifest's executable field", executable)
	}
	return path, nil
}

// RunCapture starts the child, streaming stdout and stderr into the provided
// writers as data arrives, and blocks until it exits or ctx is cancelled.
// Cancellation interrupts the child (SIGINT via Cancel) before killing it.
func (r *Runner) RunCapture(ctx context.Context, spec command.Spec, stdout, stderr io.Writer) Result {
	start := time.Now()
	path, err := r.Resolve(spec.Executable)
	if err != nil {
		return Result{Err: err}
	}

	var wait func() (int, error)
	if r.StartProcess != nil {
		wait, err = r.StartProcess(ctx, path, spec.Args, stdout, stderr)
	} else {
		wait, err = r.startReal(ctx, path, spec.Args, stdout, stderr)
	}
	if err != nil {
		return Result{Err: fmt.Errorf("start %q: %w", spec.Executable, err)}
	}
	code, werr := wait()
	res := Result{ExitCode: code, Elapsed: time.Since(start)}
	if werr != nil && code == 0 {
		res.Err = werr
	}
	return res
}

func (r *Runner) startReal(ctx context.Context, path string, args []string, stdout, stderr io.Writer) (func() (int, error), error) {
	cmd := exec.CommandContext(ctx, path, args...)
	// Interrupt first on cancellation; exec kills only after Cancel returns.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() (int, error) {
		err := cmd.Wait()
		if err == nil {
			return 0, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}, nil
}

// RunPassthrough hands the terminal directly to the child: the terminal is
// released, the child runs with the real stdin/stdout/stderr, and the
// terminal is always restored afterwards, even on error.
func (r *Runner) RunPassthrough(ctx context.Context, spec command.Spec, term Terminal) Result {
	start := time.Now()
	path, err := r.Resolve(spec.Executable)
	if err != nil {
		return Result{Err: err}
	}
	isTerm := r.StdinIsTerminal
	if isTerm == nil {
		isTerm = defaultStdinIsTerminal
	}
	if !isTerm() {
		return Result{Err: errors.New("passthrough requires a terminal on stdin; run control-center in a real terminal")}
	}
	if err := term.Release(); err != nil {
		return Result{Err: fmt.Errorf("release terminal: %w", err)}
	}
	restore := func() {
		// Restoration failure is reported in the result when nothing else
		// went wrong; the TUI also re-inits on resume.
		_ = term.Restore()
	}

	stdin := r.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := r.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := r.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	cmd := exec.CommandContext(ctx, path, spec.Args...)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	restore()
	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return Result{Err: fmt.Errorf("run %q: %w", spec.Executable, runErr), Elapsed: time.Since(start)}
		}
	}
	return Result{ExitCode: code, Elapsed: time.Since(start)}
}

// LineBuffer is a bounded, scroll-safe output sink: it retains at most Max
// lines, dropping the oldest, so fast/large output cannot grow memory without
// bound. Invalid UTF-8 is replaced at render time by ToValidUTF8.
type LineBuffer struct {
	// Max is the retention limit in lines (default 5000).
	Max int

	mu      sync.Mutex
	lines   [][]byte
	head    int
	count   int
	total   int
	partial bool // last retained line lacks a trailing newline
}

// NewLineBuffer returns a LineBuffer retaining maxLines.
func NewLineBuffer(maxLines int) *LineBuffer {
	if maxLines <= 0 {
		maxLines = 5000
	}
	return &LineBuffer{Max: maxLines}
}

// Write appends data, splitting on newlines. It never fails and never grows
// beyond Max retained lines.
func (b *LineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := 0
	for i, c := range p {
		if c == '\n' {
			b.appendLine(p[start:i])
			start = i + 1
		}
	}
	if start < len(p) {
		b.appendPartial(p[start:])
	}
	return len(p), nil
}

func (b *LineBuffer) appendLine(line []byte) {
	if b.count > 0 && b.partial {
		// Continue the unterminated last line.
		last := (b.head + b.count - 1) % b.Max
		b.lines[last] = append(b.lines[last], line...)
		b.partial = false
		return
	}
	b.push(line)
}

func (b *LineBuffer) appendPartial(p []byte) {
	if b.count > 0 && b.partial {
		last := (b.head + b.count - 1) % b.Max
		b.lines[last] = append(b.lines[last], p...)
		return
	}
	b.push(p)
	b.partial = true
}

func (b *LineBuffer) push(line []byte) {
	if b.lines == nil {
		b.lines = make([][]byte, b.Max)
	}
	cp := make([]byte, len(line))
	copy(cp, line)
	if b.count < b.Max {
		b.lines[(b.head+b.count)%b.Max] = cp
		b.count++
	} else {
		b.lines[b.head] = cp
		b.head = (b.head + 1) % b.Max
	}
	b.total++
}

// Total returns the number of lines ever written, including dropped ones.
func (b *LineBuffer) Total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Len returns the number of retained lines.
func (b *LineBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// Dropped returns how many lines were discarded due to the bound.
func (b *LineBuffer) Dropped() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total - b.count
}

// Lines returns a snapshot of the retained lines, oldest first, with invalid
// UTF-8 replaced so rendering can never corrupt the terminal.
func (b *LineBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, b.count)
	for i := 0; i < b.count; i++ {
		out[i] = strings.ToValidUTF8(string(b.lines[(b.head+i)%b.Max]), "")
	}
	return out
}

// Content renders all retained lines joined with newlines.
func (b *LineBuffer) Content() string {
	return strings.Join(b.Lines(), "\n")
}

func defaultStdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
