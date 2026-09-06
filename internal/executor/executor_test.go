package executor

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
		time.Sleep(30 * time.Second)
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

type fakeTerminal struct {
	calls []string
}

func (f *fakeTerminal) Release() error {
	f.calls = append(f.calls, "release")
	return nil
}
func (f *fakeTerminal) Restore() error {
	f.calls = append(f.calls, "restore")
	return nil
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
