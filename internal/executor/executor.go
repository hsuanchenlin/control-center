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
	"sync/atomic"
	"time"

	"github.com/atotto/clipboard"
	"github.com/hsuanchenlin/control-center/internal/command"
)

// Result is the outcome of a captured child process.
type Result struct {
	ExitCode int
	Elapsed  time.Duration
	// Interrupted reports that the run ended because its context was
	// cancelled; the exit code of an interrupted child is not meaningful.
	Interrupted bool
	// Err is non-nil only when the process could not be started or waited on;
	// nonzero exits are reported via ExitCode, not Err.
	Err error
	// RestoreErr is non-nil when the terminal could not be restored after a
	// passthrough run. It is reported independently of Err and ExitCode so a
	// failed restore is never silently discarded, including when the child
	// also failed.
	RestoreErr error
}

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

// defaultKillDelay bounds how long a child may ignore SIGINT after
// cancellation before os/exec escalates to Kill.
const defaultKillDelay = 5 * time.Second

// Runner launches child processes without a shell.
type Runner struct {
	// LookPath resolves the executable; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// Clock measures start and elapsed time; defaults to SystemClock.
	Clock Clock
	// KillDelay bounds the wait between the interrupt sent on cancellation
	// and the kill that follows it, so Wait can never block forever;
	// defaults to defaultKillDelay.
	KillDelay time.Duration
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
	// StartPassthrough, when set, replaces the real exec for passthrough
	// mode; tests inject fakes here so hermetic tests never spawn real
	// tools. It receives the resolved path, argv, and terminal streams and
	// returns a wait function yielding the exit code. Cancellation of ctx
	// must interrupt the child the same way the real implementation does.
	StartPassthrough func(ctx context.Context, path string, args []string, stdin io.Reader, stdout, stderr io.Writer) (wait func() (int, error), err error)
}

// NewRunner returns a Runner wired to the real OS.
func NewRunner() *Runner { return &Runner{Clock: SystemClock{}} }

func (r *Runner) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

func (r *Runner) killDelay() time.Duration {
	if r.KillDelay > 0 {
		return r.KillDelay
	}
	return defaultKillDelay
}

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
// Cancellation interrupts the child (SIGINT via Cancel) and kills it once
// KillDelay passes; the run is then reported as Interrupted, never as a
// success or a start failure, even when the child handles SIGINT and exits 0.
func (r *Runner) RunCapture(ctx context.Context, spec command.Spec, stdout, stderr io.Writer) Result {
	start := r.now()
	path, err := r.Resolve(spec.Executable)
	if err != nil {
		return Result{Err: err}
	}

	var wait func() (int, error)
	var interrupted *atomic.Bool
	if r.StartProcess != nil {
		wait, err = r.StartProcess(ctx, path, spec.Args, stdout, stderr)
	} else {
		wait, interrupted, err = r.startReal(ctx, path, spec.Args, nil, stdout, stderr)
	}
	if err != nil {
		return Result{Err: fmt.Errorf("start %q: %w", spec.Executable, err)}
	}
	code, werr := wait()
	res := Result{ExitCode: code, Elapsed: r.now().Sub(start)}
	switch {
	case wasInterrupted(ctx, interrupted):
		// An explicit interrupt is never a success, regardless of exit code.
		res.Interrupted = true
	case werr != nil && code == 0:
		res.Err = werr
	}
	return res
}

// wasInterrupted reports whether the run ended due to cancellation. The
// Cancel-closure flag is authoritative for real processes (os/exec invokes
// Cancel only when ctx is cancelled while the child is still running); for
// injected fakes, which have no Cancel closure, a post-wait context check is
// the documented approximation.
func wasInterrupted(ctx context.Context, flag *atomic.Bool) bool {
	if flag != nil {
		return flag.Load()
	}
	return ctx.Err() != nil
}

// startReal starts a real child process. The returned flag reports whether
// the Cancel closure fired, i.e. the child was interrupted by cancellation;
// stdin may be nil for capture mode.
func (r *Runner) startReal(ctx context.Context, path string, args []string, stdin io.Reader, stdout, stderr io.Writer) (func() (int, error), *atomic.Bool, error) {
	interrupted := &atomic.Bool{}
	cmd := exec.CommandContext(ctx, path, args...)
	// Interrupt first on cancellation; exec kills only after Cancel returns.
	cmd.Cancel = func() error {
		interrupted.Store(true)
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = r.killDelay()
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
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
		if cmd.ProcessState != nil {
			// The child ran to completion; its exit code is authoritative
			// even when Wait reports a late I/O or cancellation error.
			return cmd.ProcessState.ExitCode(), nil
		}
		return 0, err
	}, interrupted, nil
}

// RunPassthrough hands the terminal directly to the child: the terminal is
// released, the child runs with the real stdin/stdout/stderr, and the
// terminal is always restored afterwards. Restoration errors are reported in
// Result.RestoreErr, never discarded, including when the child also fails.
func (r *Runner) RunPassthrough(ctx context.Context, spec command.Spec, term Terminal) Result {
	if term == nil {
		return Result{Err: errors.New("passthrough requires a terminal boundary; this tool cannot run without one")}
	}
	start := r.now()
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

	var wait func() (int, error)
	var interrupted *atomic.Bool
	if r.StartPassthrough != nil {
		wait, err = r.StartPassthrough(ctx, path, spec.Args, stdin, stdout, stderr)
	} else {
		wait, interrupted, err = r.startReal(ctx, path, spec.Args, stdin, stdout, stderr)
	}
	if err != nil {
		res := Result{Err: fmt.Errorf("start %q: %w", spec.Executable, err), Elapsed: r.now().Sub(start)}
		res.RestoreErr = restoreError(term.Restore())
		return res
	}
	code, werr := wait()
	restoreErr := restoreError(term.Restore())
	res := Result{ExitCode: code, Elapsed: r.now().Sub(start), RestoreErr: restoreErr}
	switch {
	case wasInterrupted(ctx, interrupted):
		res.Interrupted = true
	case werr != nil:
		res.Err = fmt.Errorf("run %q: %w", spec.Executable, werr)
	}
	return res
}

// restoreError wraps a terminal restoration failure for reporting; nil in,
// nil out.
func restoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("restore terminal: %w", err)
}

// maxLineBytes caps a single retained line, so newline-free output (progress
// bars, binary data) is split instead of growing one line without bound.
const maxLineBytes = 64 << 10

// LineBuffer is a bounded, scroll-safe output sink: it retains at most Max
// lines of at most maxLineBytes each, dropping the oldest, so fast/large
// output cannot grow memory without bound. Invalid UTF-8 is replaced at
// render time by ToValidUTF8.
type LineBuffer struct {
	// Max is the retention limit in lines (default 5000).
	Max int

	mu      sync.Mutex
	lines   [][]byte
	head    int
	count   int
	total   int
	version int
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
// beyond Max retained lines of maxLineBytes each.
func (b *LineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := 0
	for i, c := range p {
		if c == '\n' {
			b.append(p[start:i], true)
			start = i + 1
		}
	}
	if start < len(p) {
		b.append(p[start:], false)
	}
	if len(p) > 0 {
		b.version++
	}
	return len(p), nil
}

// append adds chunk to the retained lines, continuing the current
// unterminated line when there is one and starting a fresh line whenever the
// current one reaches maxLineBytes.
func (b *LineBuffer) append(chunk []byte, terminated bool) {
	for {
		if b.count > 0 && b.partial {
			last := (b.head + b.count - 1) % b.Max
			n := min(maxLineBytes-len(b.lines[last]), len(chunk))
			b.lines[last] = append(b.lines[last], chunk[:n]...)
			chunk = chunk[n:]
		} else {
			n := min(maxLineBytes, len(chunk))
			b.push(chunk[:n])
			chunk = chunk[n:]
			b.partial = true
		}
		if len(chunk) == 0 {
			b.partial = !terminated
			return
		}
		b.partial = false
	}
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

// Version returns a counter that changes whenever data is appended; callers
// use it to skip re-rendering unchanged output.
func (b *LineBuffer) Version() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
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
