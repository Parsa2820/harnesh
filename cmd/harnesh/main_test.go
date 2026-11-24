package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// runMainEnv, when set, makes the test binary behave as harnesh itself. The
// terminal tests use it to run the real program on a pseudo-terminal.
const runMainEnv = "HARNESH_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) != "" {
		os.Unsetenv(runMainEnv)
		os.Exit(realMain())
	}
	os.Exit(m.Run())
}

func TestCLIArguments(t *testing.T) {
	t.Run("help flag", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"--help"}, strings.NewReader(""), &out, &errOut, false)
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("expected usage text, got %q", out.String())
		}
	})

	t.Run("version flag", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &errOut, false)
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		if !strings.HasPrefix(out.String(), "harnesh ") {
			t.Fatalf("unexpected version output: %q", out.String())
		}
	})

	t.Run("missing argument returns exit code 2", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), nil, strings.NewReader(""), &out, &errOut, false)
		if code != 2 {
			t.Fatalf("code = %d, want 2", code)
		}
		if !strings.Contains(errOut.String(), "expected exactly 1 argument") {
			t.Fatalf("unexpected stderr: %q", errOut.String())
		}
	})

	t.Run("too many arguments returns exit code 2", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"claude", "-p"}, strings.NewReader(""), &out, &errOut, false)
		if code != 2 {
			t.Fatalf("code = %d, want 2", code)
		}
		if !strings.Contains(errOut.String(), "expected exactly 1 argument") {
			t.Fatalf("unexpected stderr: %q", errOut.String())
		}
	})

	t.Run("unknown option is rejected instead of being run as an agent", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"--verbose"}, strings.NewReader(""), &out, &errOut, false)
		if code != 2 {
			t.Fatalf("code = %d, want 2", code)
		}
		if !strings.Contains(errOut.String(), `unknown option "--verbose"`) {
			t.Fatalf("unexpected stderr: %q", errOut.String())
		}
	})

	t.Run("missing agent binary fails at startup", func(t *testing.T) {
		var out, errOut bytes.Buffer
		input := "this prompt must never be consumed\n"
		code := run(context.Background(), []string{"definitely-nonexistent-binary-harnesh-xyz -p"}, strings.NewReader(input), &out, &errOut, false)
		if code != 127 {
			t.Fatalf("code = %d, want 127", code)
		}
		if !strings.Contains(errOut.String(), "agent command not found") {
			t.Fatalf("unexpected stderr: %q", errOut.String())
		}
	})

	t.Run("end-to-end session via run", func(t *testing.T) {
		var out, errOut bytes.Buffer
		input := "what is 2+2?\n!echo shell-ok\n!exit 0\n"
		code := run(context.Background(), []string{`printf "AI:%s\n"`}, strings.NewReader(input), &out, &errOut, false)
		if code != 0 {
			t.Fatalf("code = %d, want 0 (stderr=%q)", code, errOut.String())
		}
		want := "AI:what is 2+2?\nshell-ok\n"
		if out.String() != want {
			t.Fatalf("stdout = %q, want %q", out.String(), want)
		}
	})
}
