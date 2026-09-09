package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hsuanchenlin/control-center/internal/command"
)

// TestHelperProcess runs as a child: modes echo, err, sleep, deaf, big,
// badutf8.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch args[0] {
	case "env":
		fmt.Print(os.Getenv("CC_OVERRIDE") + "|" + os.Getenv("CC_INHERITED"))
	case "echo":
		fmt.Println(strings.Join(args[1:], " "))
	case "err":
		fmt.Fprintln(os.Stderr, "to stderr")
		fmt.Println("to stdout")
		os.Exit(3)
	case "sleep":
		// A real timer keeps the child alive until the parent's
		// interrupt/cancel arrives; select{} would trip the deadlock
		// detector and exit on its own.
		time.Sleep(30 * time.Second)
	case "deaf":
		// Ignores SIGINT, so only the kill after WaitDelay ends it.
		signal.Notify(make(chan os.Signal, 1), os.Interrupt)
		fmt.Println("ready")
		time.Sleep(30 * time.Second)
	case "catchint":
		// Traps SIGINT and exits successfully, simulating a graceful child.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		fmt.Println("caught interrupt")
	case "big":
		for i := 0; i < 20000; i++ {
			fmt.Printf("line %d\n", i)
		}
	case "badutf8":
		os.Stdout.Write([]byte{'a', 0xff, 0xfe, 'b', '\n'})
	}
	os.Exit(0)
}

func helperRunner(t *testing.T) *Runner {
	t.Helper()
	t.Setenv("GO_WANT_HELPER", "1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{
		LookPath: func(string) (string, error) { return self, nil },
	}
}

func helperSpec(mode string, extra ...string) command.Spec {
	args := append([]string{"-test.run=TestHelperProcess", "--", mode}, extra...)
	return command.Spec{Executable: "helper", Args: args, Display: "helper " + mode}
}

func TestRunCaptureStdout(t *testing.T) {
	r := helperRunner(t)
	var out, errBuf strings.Builder
	res := r.RunCapture(context.Background(), helperSpec("echo", "hello", "world"), &out, &errBuf)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("res = %+v", res)
	}
	if strings.TrimSpace(out.String()) != "hello world" {
		t.Fatalf("out = %q", out.String())
	}
	if res.Elapsed <= 0 {
		t.Fatal("elapsed not recorded")
	}
}

func TestRunCaptureStderrAndNonzeroExit(t *testing.T) {
	r := helperRunner(t)
	var out, errBuf strings.Builder
	res := r.RunCapture(context.Background(), helperSpec("err"), &out, &errBuf)
	if res.Err != nil {
		t.Fatalf("nonzero exit surfaced as Err: %v", res.Err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if !strings.Contains(errBuf.String(), "to stderr") || !strings.Contains(out.String(), "to stdout") {
		t.Fatalf("out=%q err=%q", out.String(), errBuf.String())
	}
}

func TestRunCaptureMissingExecutable(t *testing.T) {
	r := helperRunner(t)
	r.LookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	res := r.RunCapture(context.Background(), helperSpec("echo"), &strings.Builder{}, &strings.Builder{})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "not found in PATH") {
		t.Fatalf("err = %v", res.Err)
	}
}

func TestRunCaptureCancellation(t *testing.T) {
	r := helperRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res := r.RunCapture(ctx, helperSpec("sleep"), &strings.Builder{}, &strings.Builder{})
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancel did not interrupt the child promptly")
	}
	_ = res // exit status after SIGINT is platform-specific; promptness is the contract
}

// stepClock advances by a fixed step on every reading, so elapsed time is
// deterministic.
type stepClock struct {
	step  time.Duration
	calls int
}

func (c *stepClock) Now() time.Time {
	c.calls++
	return time.Unix(0, 0).Add(time.Duration(c.calls) * c.step)
}

func TestRunCaptureUsesInjectedClock(t *testing.T) {
	r := helperRunner(t)
	r.Clock = &stepClock{step: 500 * time.Millisecond}
	res := r.RunCapture(context.Background(), helperSpec("echo", "hi"), &strings.Builder{}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("res = %+v", res)
	}
	if res.Elapsed != 500*time.Millisecond {
		t.Fatalf("elapsed = %v, want the injected clock's step", res.Elapsed)
	}
}

func TestRunCaptureCancellationReportsInterrupted(t *testing.T) {
	r := helperRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	res := r.RunCapture(ctx, helperSpec("sleep"), &strings.Builder{}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("cancellation surfaced as a failure: %v", res.Err)
	}
	if !res.Interrupted {
		t.Fatalf("cancelled run not reported as interrupted: %+v", res)
	}
}

func TestRunCaptureKillsChildThatIgnoresInterrupt(t *testing.T) {
	r := helperRunner(t)
	r.KillDelay = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res := r.RunCapture(ctx, helperSpec("deaf"), &strings.Builder{}, &strings.Builder{})
	if time.Since(start) > 5*time.Second {
		t.Fatal("wait blocked on a child that ignores SIGINT: no kill escalation")
	}
	if !res.Interrupted {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunControlForceStopsAndReapsChild(t *testing.T) {
	r := helperRunner(t)
	r.KillDelay = 30 * time.Second
	control := NewRunControl()
	ready := &notifyWriter{ready: make(chan struct{})}
	done := make(chan Result, 1)
	go func() {
		done <- r.RunCaptureControlled(control, helperSpec("deaf"), ready, &strings.Builder{})
	}()
	select {
	case <-ready.ready:
	case res := <-done:
		t.Fatalf("child exited before ready: %+v", res)
	case <-time.After(3 * time.Second):
		t.Fatal("child did not become ready")
	}
	control.Interrupt()
	control.ForceStop()
	select {
	case res := <-done:
		if !res.Interrupted {
			t.Fatalf("force-stopped run not interrupted: %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("force-stop did not reap child")
	}
}

func TestRunControlForceStopRestoresPassthroughTerminal(t *testing.T) {
	r := helperRunner(t)
	r.KillDelay = 30 * time.Second
	r.StdinIsTerminal = func() bool { return true }
	r.Stdin = strings.NewReader("")
	ready := &notifyWriter{ready: make(chan struct{})}
	r.Stdout = ready
	r.Stderr = &strings.Builder{}
	control := NewRunControl()
	term := &fakeTerminal{}
	done := make(chan Result, 1)
	go func() {
		done <- r.RunPassthroughControlled(control, helperSpec("deaf"), term)
	}()
	select {
	case <-ready.ready:
	case res := <-done:
		t.Fatalf("passthrough child exited before ready: %+v", res)
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough child did not become ready")
	}
	control.Interrupt()
	control.ForceStop()
	select {
	case <-done:
		if len(term.calls) != 2 || term.calls[1] != "restore" {
			t.Fatalf("terminal not restored after force-stop: %v", term.calls)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough force-stop did not finish cleanup")
	}
}

type notifyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

func TestPreCancelledRunsDoNotStartOrReleaseTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := false
	r := helperRunner(t)
	r.StartProcess = func(ctx context.Context, path string, args []string, env []string, stdout, stderr io.Writer) (func() (int, error), error) {
		started = true
		return func() (int, error) { return 0, nil }, nil
	}
	if res := r.RunCapture(ctx, helperSpec("echo"), io.Discard, io.Discard); !res.Interrupted {
		t.Fatalf("pre-cancelled capture result = %+v", res)
	}
	term := &fakeTerminal{}
	if res := r.RunPassthrough(ctx, helperSpec("echo"), term); !res.Interrupted {
		t.Fatalf("pre-cancelled passthrough result = %+v", res)
	}
	if started || len(term.calls) != 0 {
		t.Fatalf("pre-cancelled run started=%v terminal calls=%v", started, term.calls)
	}
}

type fakeTerminal struct {
	calls      []string
	restoreErr error
}

func (f *fakeTerminal) Release() error {
	f.calls = append(f.calls, "release")
	return nil
}
func (f *fakeTerminal) Restore() error {
	f.calls = append(f.calls, "restore")
	return f.restoreErr
}

func TestRunPassthroughRestoresTerminal(t *testing.T) {
	r := helperRunner(t)
	r.StdinIsTerminal = func() bool { return true }
	r.Stdin = strings.NewReader("")
	var out strings.Builder
	r.Stdout = &out
	r.Stderr = &out
	term := &fakeTerminal{}
	res := r.RunPassthrough(context.Background(), helperSpec("echo", "hi"), term)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("res = %+v", res)
	}
	if len(term.calls) != 2 || term.calls[0] != "release" || term.calls[1] != "restore" {
		t.Fatalf("terminal calls = %v", term.calls)
	}
	if strings.TrimSpace(out.String()) != "hi" {
		t.Fatalf("child did not inherit stdout: %q", out.String())
	}
}

func TestRunPassthroughRestoresOnError(t *testing.T) {
	r := helperRunner(t)
	r.StdinIsTerminal = func() bool { return true }
	r.Stdin = strings.NewReader("")
	r.Stdout = &strings.Builder{}
	r.Stderr = &strings.Builder{}
	term := &fakeTerminal{}
	res := r.RunPassthrough(context.Background(), helperSpec("err"), term)
	if res.ExitCode != 3 {
		t.Fatalf("res = %+v", res)
	}
	if len(term.calls) != 2 || term.calls[1] != "restore" {
		t.Fatalf("terminal not restored after nonzero exit: %v", term.calls)
	}
}

func TestRunPassthroughRequiresTerminal(t *testing.T) {
	r := helperRunner(t)
	r.StdinIsTerminal = func() bool { return false }
	term := &fakeTerminal{}
	res := r.RunPassthrough(context.Background(), helperSpec("echo"), term)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "requires a terminal") {
		t.Fatalf("err = %v", res.Err)
	}
	if len(term.calls) != 0 {
		t.Fatalf("terminal touched despite refusal: %v", term.calls)
	}
}

func TestRunPassthroughRequiresTerminalBoundary(t *testing.T) {
	r := helperRunner(t)
	r.StdinIsTerminal = func() bool { return true }
	res := r.RunPassthrough(context.Background(), helperSpec("echo"), nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "terminal boundary") {
		t.Fatalf("err = %v", res.Err)
	}
}

func TestLineBufferBounds(t *testing.T) {
	b := NewLineBuffer(100)
	for i := 0; i < 500; i++ {
		fmt.Fprintf(b, "line %d\n", i)
	}
	if b.Len() != 100 || b.Total() != 500 || b.Dropped() != 400 {
		t.Fatalf("len=%d total=%d dropped=%d", b.Len(), b.Total(), b.Dropped())
	}
	lines := b.Lines()
	if lines[0] != "line 400" || lines[99] != "line 499" {
		t.Fatalf("window wrong: %q ... %q", lines[0], lines[99])
	}
}

func TestLineBufferPartialLines(t *testing.T) {
	b := NewLineBuffer(10)
	b.Write([]byte("hel"))
	b.Write([]byte("lo\nwor"))
	b.Write([]byte("ld\n"))
	lines := b.Lines()
	if len(lines) != 2 || lines[0] != "hello" || lines[1] != "world" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestLineBufferCapsNewlineFreeOutput(t *testing.T) {
	b := NewLineBuffer(4)
	chunk := make([]byte, 32<<10)
	for i := range chunk {
		chunk[i] = 'x'
	}
	// 8 MB with no newline at all.
	for i := 0; i < 256; i++ {
		b.Write(chunk)
	}
	retained := 0
	for _, l := range b.Lines() {
		if len(l) > maxLineBytes {
			t.Fatalf("line of %d bytes exceeds the %d-byte cap", len(l), maxLineBytes)
		}
		retained += len(l)
	}
	if retained > 4*maxLineBytes {
		t.Fatalf("retained %d bytes, want at most %d", retained, 4*maxLineBytes)
	}
	if b.Dropped() == 0 {
		t.Fatal("over-long output was not split into droppable lines")
	}
}

func TestLineBufferVersionChangesWithinOneLine(t *testing.T) {
	b := NewLineBuffer(10)
	b.Write([]byte("progress 10%"))
	v := b.Version()
	b.Write([]byte("\rprogress 20%"))
	if b.Version() == v {
		t.Fatal("version did not change for output appended to the current line")
	}
	if b.Total() != 1 {
		t.Fatalf("partial-line writes should not add lines: total = %d", b.Total())
	}
}

func TestLineBufferInvalidUTF8(t *testing.T) {
	b := NewLineBuffer(10)
	b.Write([]byte{'a', 0xff, 0xfe, 'b', '\n'})
	for _, l := range b.Lines() {
		if !strings.Contains(l, "ab") {
			t.Fatalf("invalid bytes not sanitized: %q", l)
		}
	}
}

func TestLineBufferConcurrentUse(t *testing.T) {
	b := NewLineBuffer(50)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			fmt.Fprintf(b, "w%d\n", i)
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		_ = b.Lines()
		_ = b.Content()
	}
	<-done
}

func TestSystemClipboardImplementsBoundary(t *testing.T) {
	var _ Clipboard = SystemClipboard{}
	var _ Clock = SystemClock{}
}

// TestInterruptExitZeroStillInterrupted pins that an explicit interrupt is
// reported as Interrupted even when the child traps SIGINT and exits 0.
func TestInterruptExitZeroStillInterrupted(t *testing.T) {
	r := helperRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	res := r.RunCapture(ctx, helperSpec("catchint"), &strings.Builder{}, &strings.Builder{})
	if !res.Interrupted {
		t.Fatalf("graceful interrupt not classified: %+v", res)
	}
	if res.Err != nil {
		t.Fatalf("interrupt must not surface as a start failure: %v", res.Err)
	}
}

func TestInterruptExitZeroStillInterruptedPassthrough(t *testing.T) {
	r := helperRunner(t)
	r.StdinIsTerminal = func() bool { return true }
	r.Stdin = strings.NewReader("")
	r.Stdout = &strings.Builder{}
	r.Stderr = &strings.Builder{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	res := r.RunPassthrough(ctx, helperSpec("catchint"), &fakeTerminal{})
	if !res.Interrupted {
		t.Fatalf("graceful passthrough interrupt not classified: %+v", res)
	}
}

// TestRestoreErrorReported pins that terminal restoration failures are
// surfaced whether or not the child succeeded.
func TestRestoreErrorReported(t *testing.T) {
	newRunner := func(t *testing.T) *Runner {
		r := helperRunner(t)
		r.StdinIsTerminal = func() bool { return true }
		r.Stdin = strings.NewReader("")
		r.Stdout = &strings.Builder{}
		r.Stderr = &strings.Builder{}
		return r
	}

	// Child succeeds, restore fails.
	term := &fakeTerminal{restoreErr: errors.New("restore blew up")}
	res := newRunner(t).RunPassthrough(context.Background(), helperSpec("echo", "hi"), term)
	if res.RestoreErr == nil || !strings.Contains(res.RestoreErr.Error(), "restore blew up") {
		t.Fatalf("restore error dropped on success: %+v", res)
	}
	if res.Err != nil {
		t.Fatalf("restore error must not masquerade as start failure: %v", res.Err)
	}

	// Child fails AND restore fails: both must be visible.
	term = &fakeTerminal{restoreErr: errors.New("restore blew up")}
	res = newRunner(t).RunPassthrough(context.Background(), helperSpec("err"), term)
	if res.ExitCode != 3 {
		t.Fatalf("child exit lost: %+v", res)
	}
	if res.RestoreErr == nil {
		t.Fatalf("restore error dropped alongside child failure: %+v", res)
	}
}

// TestPassthroughUsesInjectedBoundary pins that passthrough launches go
// through the injected process boundary, so hermetic tests never exec.
func TestPassthroughUsesInjectedBoundary(t *testing.T) {
	r := helperRunner(t)
	r.StdinIsTerminal = func() bool { return true }
	spawned := false
	r.StartPassthrough = func(ctx context.Context, path string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) (func() (int, error), error) {
		spawned = true
		if !strings.HasSuffix(args[len(args)-1], "fake-mode") {
			t.Errorf("argv not passed through: %v", args)
		}
		return func() (int, error) { return 7, nil }, nil
	}
	term := &fakeTerminal{}
	res := r.RunPassthrough(context.Background(), helperSpec("fake-mode"), term)
	if !spawned {
		t.Fatal("injected boundary not used")
	}
	if res.ExitCode != 7 || res.Err != nil {
		t.Fatalf("res = %+v", res)
	}
	if len(term.calls) != 2 || term.calls[0] != "release" || term.calls[1] != "restore" {
		t.Fatalf("terminal calls = %v", term.calls)
	}
}

// TestInjectedCaptureInterruptApproximation documents the ctx-based
// interrupt classification for the injected capture path.
func TestInjectedCaptureInterruptApproximation(t *testing.T) {
	r := helperRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.StartProcess = func(ctx context.Context, path string, args []string, env []string, stdout, stderr io.Writer) (func() (int, error), error) {
		return func() (int, error) {
			<-ctx.Done() // simulate a graceful child exiting 0 on interrupt
			return 0, nil
		}, nil
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	res := r.RunCapture(ctx, helperSpec("echo"), &strings.Builder{}, &strings.Builder{})
	if !res.Interrupted {
		t.Fatalf("injected graceful interrupt not classified: %+v", res)
	}
}
