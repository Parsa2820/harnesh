package shell

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestShell(t *testing.T, harnessCmd string, input string) (*Shell, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	hc, err := ParseHarnessCommand(harnessCmd)
	if err != nil {
		t.Fatalf("ParseHarnessCommand(%q): %v", harnessCmd, err)
	}
	var outBuf, errBuf bytes.Buffer
	sh, err := New(Config{
		Harness:     hc,
		In:          strings.NewReader(input),
		Out:         &outBuf,
		Err:         &errBuf,
		Interactive: false,
		SystemShell: "/bin/sh",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return sh, &outBuf, &errBuf
}

func TestShellDispatchesAgentAndShellCommands(t *testing.T) {
	input := strings.Join([]string{
		"",
		"   ",
		"hello agent world",
		"!echo from-shell",
		"!echo pipe-test | tr a-z A-Z",
		"!",
		"",
	}, "\n")

	// Use printf as a deterministic fake CLI harness: prints "AGENT:<prompt>\n".
	sh, outBuf, errBuf := newTestShell(t, `printf "AGENT:%s\n"`, input)

	code := sh.Run(context.Background())
	if code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}
	if errBuf.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errBuf.String())
	}

	want := strings.Join([]string{
		"AGENT:hello agent world",
		"from-shell",
		"PIPE-TEST",
	}, "\n") + "\n"

	if outBuf.String() != want {
		t.Fatalf("stdout mismatch:\ngot:\n%q\nwant:\n%q", outBuf.String(), want)
	}
}

func TestShellRetainsConversationAcrossTurns(t *testing.T) {
	input := strings.Join([]string{
		"my name is Alice",
		"prompt with 'quotes' and $HOME",
	}, "\n") + "\n"

	sh, outBuf, errBuf := newTestShell(t, `printf "REPLY:%s\n"`, input)
	code := sh.Run(context.Background())
	if code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}

	if sh.Conversation().Len() != 2 {
		t.Fatalf("Conversation().Len() = %d, want 2", sh.Conversation().Len())
	}

	out := outBuf.String()
	for _, wantSubstr := range []string{
		"REPLY:my name is Alice\n",
		"Conversation history:\n\nUser: my name is Alice\nAssistant: REPLY:my name is Alice",
		"Current user message:\nprompt with 'quotes' and $HOME",
	} {
		if !strings.Contains(out, wantSubstr) {
			t.Fatalf("expected output to contain %q, got:\n%s", wantSubstr, out)
		}
	}
}

func TestShellBuiltinClearResetsScreenAndConversation(t *testing.T) {
	input := strings.Join([]string{
		"remember the secret 12345",
		"!clear",
		"what is the secret?",
	}, "\n") + "\n"

	sh, outBuf, errBuf := newTestShell(t, `printf "ECHO:%s\n"`, input)
	code := sh.Run(context.Background())
	if code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}

	out := outBuf.String()
	if !strings.Contains(out, clearScreenSeq) {
		t.Fatalf("expected screen clear escape sequence %q in output %q", clearScreenSeq, out)
	}

	// After !clear, the next prompt should NOT contain the previous conversation.
	parts := strings.Split(out, clearScreenSeq)
	if len(parts) != 2 {
		t.Fatalf("expected output split around clearScreenSeq, got %q", out)
	}
	afterClear := parts[1]
	if strings.Contains(afterClear, "12345") || strings.Contains(afterClear, "Conversation history:") {
		t.Fatalf("conversation was not cleared by !clear; output after clear:\n%s", afterClear)
	}
	if afterClear != "ECHO:what is the secret?\n" {
		t.Fatalf("output after clear = %q, want %q", afterClear, "ECHO:what is the secret?\n")
	}

	if sh.Conversation().Len() != 1 {
		t.Fatalf("Conversation().Len() = %d, want 1 (only post-clear turn)", sh.Conversation().Len())
	}
}

func TestShellPlaceholderHarness(t *testing.T) {
	sh, outBuf, errBuf := newTestShell(t, `printf "[%s]\n" {}`, "custom placement\n")

	code := sh.Run(context.Background())
	if code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}
	if got, want := outBuf.String(), "[custom placement]\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestShellBuiltinCD(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	dirA := t.TempDir()
	dirB := t.TempDir()

	// Resolve symlinks (macOS /var -> /private/var) so comparisons match pwd.
	realA, err := filepath.EvalSymlinks(dirA)
	if err != nil {
		t.Fatalf("EvalSymlinks(dirA): %v", err)
	}
	realB, err := filepath.EvalSymlinks(dirB)
	if err != nil {
		t.Fatalf("EvalSymlinks(dirB): %v", err)
	}

	input := strings.Join([]string{
		"!cd " + dirA,
		"!pwd",
		"!cd " + dirB,
		"!pwd",
		"!cd -",
		"!pwd",
	}, "\n") + "\n"

	sh, outBuf, errBuf := newTestShell(t, "echo", input)
	code := sh.Run(context.Background())
	if code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}

	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	want := []string{realA, realB, realA, realA}
	if len(lines) != len(want) {
		t.Fatalf("got lines %#v, want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestShellBuiltinExit(t *testing.T) {
	t.Run("exit with explicit code stops loop immediately", func(t *testing.T) {
		input := "!exit 42\n!echo should-not-run\n"
		sh, outBuf, errBuf := newTestShell(t, "echo", input)
		code := sh.Run(context.Background())
		if code != 42 {
			t.Fatalf("exit code = %d, want 42", code)
		}
		if outBuf.Len() != 0 || errBuf.Len() != 0 {
			t.Fatalf("unexpected output: stdout=%q stderr=%q", outBuf.String(), errBuf.String())
		}
	})

	t.Run("exit with no arguments preserves last exit code", func(t *testing.T) {
		input := "!false\n!exit\n"
		sh, _, _ := newTestShell(t, "echo", input)
		code := sh.Run(context.Background())
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})

	t.Run("invalid exit argument reports error and continues", func(t *testing.T) {
		input := "!exit notanumber\n!echo still-running\n"
		sh, outBuf, errBuf := newTestShell(t, "echo", input)
		code := sh.Run(context.Background())
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(errBuf.String(), "numeric argument required") {
			t.Fatalf("expected numeric argument error, got stderr=%q", errBuf.String())
		}
		if strings.TrimSpace(outBuf.String()) != "still-running" {
			t.Fatalf("stdout = %q, want %q", outBuf.String(), "still-running")
		}
	})
}

func TestShellAgentErrors(t *testing.T) {
	t.Run("non-zero agent exit reports status and does not pollute history", func(t *testing.T) {
		sh, _, errBuf := newTestShell(t, "/bin/sh -c 'exit 3'", "hello\n")
		code := sh.Run(context.Background())
		if code != 3 {
			t.Fatalf("exit code = %d, want 3", code)
		}
		if !strings.Contains(errBuf.String(), "agent exited with status 3") {
			t.Fatalf("expected exit status message, got stderr=%q", errBuf.String())
		}
		if sh.Conversation().Len() != 0 {
			t.Fatalf("failed agent turn should not be retained, got Len=%d", sh.Conversation().Len())
		}
	})

	t.Run("missing agent binary reports launch error", func(t *testing.T) {
		sh, _, errBuf := newTestShell(t, "definitely-nonexistent-binary-harnesh-xyz", "hello\n")
		code := sh.Run(context.Background())
		if code != 127 {
			t.Fatalf("exit code = %d, want 127", code)
		}
		if !strings.Contains(errBuf.String(), "failed to run agent") {
			t.Fatalf("expected launch error, got stderr=%q", errBuf.String())
		}
	})
}

func TestShellUsesConfiguredPS1WithColors(t *testing.T) {
	hc, err := ParseHarnessCommand("echo")
	if err != nil {
		t.Fatalf("ParseHarnessCommand: %v", err)
	}
	sh, err := New(Config{
		Harness:     hc,
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Err:         &bytes.Buffer{},
		Interactive: false,
		SystemShell: "/bin/bash",
		PS1:         `\[\033[35m\]custom:\W\[\033[0m\] \$ `,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := sh.Prompt(context.Background())
	if !strings.Contains(got, "\x1b[35mcustom:") || !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("expected colored PS1 prompt, got %q", got)
	}
}

func TestShellInteractiveArrowHistoryRecall(t *testing.T) {
	hc, err := ParseHarnessCommand(`printf "AI:%s\n"`)
	if err != nil {
		t.Fatalf("ParseHarnessCommand: %v", err)
	}

	// Run "!echo first", then recall it with Up arrow, edit to "!echo first2",
	// and exit with Ctrl-D.
	input := "!echo first\r" + "\x1b[A2\r" + "\x04"

	var outBuf, errBuf bytes.Buffer
	sh, err := New(Config{
		Harness:     hc,
		In:          strings.NewReader(input),
		Out:         &outBuf,
		Err:         &errBuf,
		Interactive: true,
		SystemShell: "/bin/sh",
		PS1:         "$ ",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	code := sh.Run(context.Background())
	if code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}

	out := outBuf.String()
	if !strings.Contains(out, "first\n") || !strings.Contains(out, "first2\n") {
		t.Fatalf("expected recalled & edited command output in:\n%q", out)
	}
}

func TestHasUnquotedShellOperators(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"cd /tmp", false},
		{`cd "dir with & and |"`, false},
		{`cd 'dir;with;semicolons'`, false},
		{`cd dir\&escaped`, false},
		{"cd /tmp && ls", true},
		{"cd /tmp; pwd", true},
		{"cd /tmp | cat", true},
		{"cd $(pwd)", true},
	}

	for _, tc := range tests {
		if got := hasUnquotedShellOperators(tc.input); got != tc.want {
			t.Errorf("hasUnquotedShellOperators(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
