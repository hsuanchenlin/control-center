package executor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hsuanchenlin/control-center/internal/command"
)

// TestHelperProcess runs as a child: modes echo, err, sleep, big, badutf8.
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
		select {} // killed by interrupt/cancel in the parent
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
