package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentExecution(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "capture", true: "passthrough"}[passthrough], func(t *testing.T) {
			r := helperRunner(t)
			t.Setenv("CC_OVERRIDE", "old")
			t.Setenv("CC_INHERITED", "inherited")
			marker := filepath.Join(t.TempDir(), "must-not-exist")
			value := "$(touch " + marker + "); 'quoted'\nnext"
			spec := helperSpec("env")
			spec.Env = []string{"CC_OVERRIDE=" + value}
			var out strings.Builder
			var res Result
			if passthrough {
				r.StdinIsTerminal = func() bool { return true }
				r.Stdin = strings.NewReader("")
				r.Stdout, r.Stderr = &out, &out
				res = r.RunPassthrough(context.Background(), spec, &fakeTerminal{})
			} else {
				res = r.RunCapture(context.Background(), spec, &out, &strings.Builder{})
			}
			if res.Err != nil || res.ExitCode != 0 || out.String() != value+"|inherited" {
				t.Fatalf("%+v output %q", res, out.String())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("shell evaluated value: %v", err)
			}
			if os.Getenv("CC_OVERRIDE") != "old" {
				t.Fatal("mutated parent environment")
			}
		})
	}
}
