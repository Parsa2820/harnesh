package shell

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer that can be read while a child process is
// still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// preserveWorkingDir restores the process working directory and the PWD and
// OLDPWD variables when the test finishes, since !cd changes all three.
func preserveWorkingDir(t *testing.T) {
	t.Helper()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Setenv("PWD", origDir)
	t.Setenv("OLDPWD", os.Getenv("OLDPWD"))
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})
}

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
	preserveWorkingDir(t)

	dirA := t.TempDir()
	dirB := t.TempDir()

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
	want := []string{dirA, dirB, dirA, dirA}
	if len(lines) != len(want) {
		t.Fatalf("got lines %#v, want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestShellBuiltinCDTracksLogicalPath(t *testing.T) {
	preserveWorkingDir(t)

	// base/link -> base/real; entering the link must keep showing "link",
	// and "cd .." must return to base rather than real's physical parent.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	realDir := filepath.Join(base, "real", "nested")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	input := "!cd " + link + "\n!pwd\n!cd ..\n!pwd\n"
	sh, outBuf, errBuf := newTestShell(t, "echo", input)
	if code := sh.Run(context.Background()); code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}
	if got, want := outBuf.String(), link+"\n"+base+"\n"; got != want {
		t.Fatalf("pwd output = %q, want %q", got, want)
	}
	if got := os.Getenv("PWD"); got != base {
		t.Fatalf("PWD = %q, want %q", got, base)
	}
}

func TestShellBuiltinCDUsesShellExpansion(t *testing.T) {
	preserveWorkingDir(t)

	base := t.TempDir()
	target := filepath.Join(base, "project-alpha")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Setenv("HARNESH_TEST_BASE", base)

	tests := []struct {
		name string
		arg  string
	}{
		{"environment variable", "$HARNESH_TEST_BASE/project-alpha"},
		{"quoted variable", `"${HARNESH_TEST_BASE}/project-alpha"`},
		{"command substitution", "$(echo $HARNESH_TEST_BASE | cat)/project-alpha"},
		{"glob", base + "/project-al*"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := "!cd " + base + "\n!cd " + tc.arg + "\n!pwd\n"
			sh, outBuf, errBuf := newTestShell(t, "echo", input)
			if code := sh.Run(context.Background()); code != 0 {
				t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
			}
			if got, want := outBuf.String(), target+"\n"; got != want {
				t.Fatalf("pwd output = %q, want %q", got, want)
			}
		})
	}
}

func TestShellBuiltinCDErrors(t *testing.T) {
	preserveWorkingDir(t)
	start := t.TempDir()

	tests := []struct {
		name    string
		line    string
		wantErr string
	}{
		{"missing directory", "!cd /definitely/not/a/real/dir", "harnesh: cd: /definitely/not/a/real/dir: no such file or directory"},
		{"too many arguments", "!cd a b", "harnesh: cd: too many arguments"},
		{"missing directory via expansion", "!cd $HOME/definitely-not-a-real-dir-xyz", "definitely-not-a-real-dir-xyz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := "!cd " + start + "\n" + tc.line + "\n"
			sh, outBuf, errBuf := newTestShell(t, "echo", input)
			if code := sh.Run(context.Background()); code == 0 {
				t.Fatalf("exit code = 0, want failure")
			}
			if !strings.Contains(errBuf.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want it to contain %q", errBuf.String(), tc.wantErr)
			}
			if outBuf.Len() != 0 {
				t.Fatalf("unexpected stdout %q", outBuf.String())
			}
			if got := os.Getenv("PWD"); got != start {
				t.Fatalf("failed cd changed PWD to %q, want %q", got, start)
			}
		})
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

	t.Run("exit code wraps to a single byte like other shells", func(t *testing.T) {
		sh, _, _ := newTestShell(t, "echo", "!exit 300\n")
		if code := sh.Run(context.Background()); code != 44 {
			t.Fatalf("exit code = %d, want 44", code)
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

func TestShellReportsSignalExitStatus(t *testing.T) {
	// A command killed by a signal reports 128+N, as shells do.
	sh, _, errBuf := newTestShell(t, "echo", "!kill -TERM $$\n")
	if code := sh.Run(context.Background()); code != 128+15 {
		t.Fatalf("exit code = %d, want 143 (stderr=%q)", code, errBuf.String())
	}
}

func TestShellTrimsConversationThatOutgrowsPromptLimit(t *testing.T) {
	hc, err := ParseHarnessCommand(`printf "%s\n"`)
	if err != nil {
		t.Fatalf("ParseHarnessCommand: %v", err)
	}
	// Every echoed turn is larger than the previous, so the limit is hit fast.
	input := strings.Join([]string{
		"alpha " + strings.Repeat("x", 100),
		"beta",
		"gamma",
		"delta",
		"!clear",
		"epsilon",
	}, "\n") + "\n"

	var outBuf, errBuf bytes.Buffer
	sh, err := New(Config{
		Harness:        hc,
		In:             strings.NewReader(input),
		Out:            &outBuf,
		Err:            &errBuf,
		SystemShell:    "/bin/sh",
		MaxPromptBytes: 400,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if code := sh.Run(context.Background()); code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}

	if got := strings.Count(errBuf.String(), "no longer fits in one prompt"); got != 1 {
		t.Fatalf("trim notice printed %d times, want exactly once; stderr=%q", got, errBuf.String())
	}
	// The harness echoes each prompt it receives, one per line group; none
	// may exceed the limit by more than the newest message itself.
	afterClear := outBuf.String()[strings.LastIndex(outBuf.String(), clearScreenSeq)+len(clearScreenSeq):]
	if afterClear != "epsilon\n" {
		t.Fatalf("prompt after !clear = %q, want only the new message", afterClear)
	}
	if !strings.Contains(outBuf.String(), "earlier turns omitted") {
		t.Fatalf("expected a trimmed transcript to be sent, got:\n%s", outBuf.String())
	}
}

func TestShellReportsOversizedPrompt(t *testing.T) {
	// A single prompt beyond the OS argument limit cannot be delivered; the
	// failure must be explained rather than surfacing a raw exec error.
	huge := strings.Repeat("x", 4<<20)
	sh, _, errBuf := newTestShell(t, "echo", huge+"\n")
	if code := sh.Run(context.Background()); code == 0 {
		t.Fatal("exit code = 0, want failure")
	}
	if !strings.Contains(errBuf.String(), "too large to pass") {
		t.Fatalf("stderr = %.200q, want an explanation that the prompt is too large", errBuf.String())
	}
	if sh.Conversation().Len() != 0 {
		t.Fatal("undelivered prompt must not be retained")
	}
}

func TestShellRunStopsWhenContextIsCancelled(t *testing.T) {
	t.Run("while waiting for input", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pw.Close()

		hc, _ := ParseHarnessCommand("echo")
		sh, err := New(Config{Harness: hc, In: pr, Out: io.Discard, Err: io.Discard, SystemShell: "/bin/sh"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan int, 1)
		go func() { done <- sh.Run(ctx) }()

		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancellation while blocked on input")
		}
	})

	t.Run("while a child is running", func(t *testing.T) {
		// "exec" makes sleep the direct child regardless of which /bin/sh
		// is installed, so it is the process that receives SIGTERM.
		hc, _ := ParseHarnessCommand("echo")
		var out syncBuffer
		sh, err := New(Config{
			Harness:     hc,
			In:          strings.NewReader("!echo started; exec sleep 60\n!echo should-not-run\n"),
			Out:         &out,
			Err:         io.Discard,
			SystemShell: "/bin/sh",
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan int, 1)
		go func() { done <- sh.Run(ctx) }()

		for deadline := time.Now().Add(10 * time.Second); out.String() != "started\n"; {
			if time.Now().After(deadline) {
				t.Fatalf("child did not start; output %q", out.String())
			}
			time.Sleep(5 * time.Millisecond)
		}

		start := time.Now()
		cancel()
		select {
		case code := <-done:
			// The child is terminated with SIGTERM, well inside the grace period.
			if elapsed := time.Since(start); elapsed >= childShutdownGrace {
				t.Fatalf("shutdown took %v; child should exit on SIGTERM", elapsed)
			}
			if code != 128+15 {
				t.Fatalf("exit code = %d, want 143 (child terminated by SIGTERM)", code)
			}
			if got := out.String(); got != "started\n" {
				t.Fatalf("commands ran after cancellation: %q", got)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancellation while a child was running")
		}
	})
}

func TestShellInteractivePasteIsASinglePrompt(t *testing.T) {
	hc, err := ParseHarnessCommand(`printf "AI:[%s]\n"`)
	if err != nil {
		t.Fatalf("ParseHarnessCommand: %v", err)
	}
	input := "\x1b[200~line one\nline two\n\x1b[201~\r" + "\x04"

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
	if code := sh.Run(context.Background()); code != 0 {
		t.Fatalf("Run exited with %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "AI:[line one\nline two]\n") {
		t.Fatalf("pasted lines were not sent as one prompt:\n%q", outBuf.String())
	}
	if got := sh.Conversation().Len(); got != 1 {
		t.Fatalf("Conversation().Len() = %d, want 1", got)
	}
}

func TestAnalyzeCommandLine(t *testing.T) {
	tests := []struct {
		input        string
		wantCompound bool
		wantExpands  bool
	}{
		{"cd /tmp", false, false},
		{"cd ~", false, false},
		{"cd ~/src", false, false},
		{`cd "dir with & and |"`, false, false},
		{`cd 'dir;with;semicolons'`, false, false},
		{`cd 'literal $HOME and *'`, false, false},
		{`cd dir\&escaped`, false, false},
		{`cd dir\$escaped`, false, false},
		{"cd /tmp && ls", true, false},
		{"cd /tmp; pwd", true, false},
		{"cd /tmp | cat", true, false},
		{"cd /tmp > out", true, false},
		{"(cd /tmp)", true, false},
		{"cd /tmp\nls", true, false},
		{"cd $HOME", false, true},
		{`cd "$HOME/src"`, false, true},
		{"cd ${PROJECTS}/x", false, true},
		{"cd ~alice", false, true},
		{"cd proj*", false, true},
		{"cd dir?", false, true},
		{"cd {a,b}", false, true},
		// Operators inside a command substitution do not make the line compound.
		{"cd $(git rev-parse --show-toplevel | cat)", false, true},
		{"cd `dirname x; echo`", false, true},
		{`cd "$(pwd)" && ls`, true, true},
	}

	for _, tc := range tests {
		compound, expands := analyzeCommandLine(tc.input)
		if compound != tc.wantCompound || expands != tc.wantExpands {
			t.Errorf("analyzeCommandLine(%q) = (compound %v, expands %v), want (%v, %v)",
				tc.input, compound, expands, tc.wantCompound, tc.wantExpands)
		}
	}
}
