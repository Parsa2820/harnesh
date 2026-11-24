//go:build darwin || linux

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests run harnesh on a pseudo-terminal, since what they cover only
// exists there: which processes the terminal's keyboard signals reach.

const (
	terminalTimeout = 15 * time.Second
	testPrompt      = "ready> "

	ctrlC = "\x03"
	ctrlZ = "\x1a"
)

// terminalSession is a program running on its own pseudo-terminal, as the
// leader of a new session, the way a terminal emulator starts a shell.
type terminalSession struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File
	exited chan struct{}

	mu   sync.Mutex
	out  bytes.Buffer
	seen int // offset in out up to which output has been matched
}

// startTerminal runs argv on a new pseudo-terminal with env added to a
// scrubbed copy of the environment.
func startTerminal(t *testing.T, env []string, argv ...string) *terminalSession {
	t.Helper()
	master, slavePath, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Skipf("cannot open %s: %v", slavePath, err)
	}
	defer slave.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Dir = t.TempDir()
	cmd.Env = append(scrubbedEnv(), "HOME="+t.TempDir(), "TERM=xterm", "SHELL=/bin/sh", "PS1="+testPrompt)
	cmd.Env = append(cmd.Env, env...)
	// Ctty is the descriptor number in the child: its standard input.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		t.Fatalf("start %v: %v", argv, err)
	}

	s := &terminalSession{t: t, cmd: cmd, master: master, exited: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-s.exited
		master.Close()
	})
	return s
}

// scrubbedEnv returns the environment without the variables that would let
// the developer's own shell setup leak into the session.
func scrubbedEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME", "TERM", "SHELL", "PS1", "PROMPT_COMMAND", "ZDOTDIR", "BASH_ENV", "ENV":
		default:
			env = append(env, entry)
		}
	}
	return env
}

// harneshArgv returns the command line that runs harnesh (this test binary)
// with the given agent command.
func harneshArgv(agent string) []string {
	return []string{os.Args[0], agent}
}

var harneshEnv = []string{runMainEnv + "=1"}

// send types keys on the terminal.
func (s *terminalSession) send(keys string) {
	s.t.Helper()
	if _, err := s.master.WriteString(keys); err != nil {
		s.t.Fatalf("typing %q: %v", keys, err)
	}
}

// expect waits for output matching pattern that follows everything matched
// so far, and returns the match with its submatches.
func (s *terminalSession) expect(pattern string) []string {
	s.t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.Now().Add(terminalTimeout)
	for {
		s.mu.Lock()
		pending := s.out.Bytes()[s.seen:]
		if loc := re.FindSubmatchIndex(pending); loc != nil {
			match := make([]string, 0, len(loc)/2)
			for i := 0; i < len(loc); i += 2 {
				match = append(match, string(pending[loc[i]:loc[i+1]]))
			}
			s.seen += loc[1]
			s.mu.Unlock()
			return match
		}
		transcript := s.out.String()
		s.mu.Unlock()

		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %q; terminal output so far:\n%q", pattern, transcript)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expectText is expect for a literal string.
func (s *terminalSession) expectText(text string) {
	s.t.Helper()
	s.expect(regexp.QuoteMeta(text))
}

// run types a line at the prompt.
func (s *terminalSession) run(line string) {
	s.t.Helper()
	s.expectText(testPrompt)
	s.send(line + "\r")
}

// output returns everything written to the terminal so far.
func (s *terminalSession) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// exitCode waits for the program to exit and returns its exit status.
func (s *terminalSession) exitCode() int {
	s.t.Helper()
	select {
	case <-s.exited:
	case <-time.After(terminalTimeout):
		s.t.Fatalf("timed out waiting for exit; terminal output so far:\n%q", s.output())
	}
	return s.cmd.ProcessState.ExitCode()
}

// runHarnesh is the command that starts harnesh in a script passed to
// scriptArgv. (PS1 is set here because a non-interactive bash, which is what
// /bin/sh may be, drops it from the environment.)
const runHarnesh = `PS1='` + testPrompt + `' "$0" "$1"`

// scriptArgv returns the command line for a shell script that runs harnesh.
// harnesh started this way is not a process group leader: it shares the
// script's group, as it would under any launcher that is not an interactive
// shell.
func scriptArgv(script, agent string) []string {
	return append([]string{"/bin/sh", "-c", script}, harneshArgv(agent)...)
}

func TestInterruptReachesOnlyTheForegroundJob(t *testing.T) {
	// Ctrl-C during a !command must end that command and nothing else:
	// neither harnesh nor the script waiting for it should even see it.
	script := `trap 'echo "script was interrupted"' INT; ` + runHarnesh + `; echo "harnesh exited with $?"`
	s := startTerminal(t, harneshEnv, scriptArgv(script, "echo")...)

	// The quotes keep the typed line from matching the expected output.
	s.run(`!echo st""arted; sleep 60`)
	s.expectText("started")
	s.send(ctrlC)

	s.run("!exit") // exits with the status of the interrupted command
	s.expectText("harnesh exited with 130")
	if code := s.exitCode(); code != 0 {
		t.Fatalf("script exited with %d, want 0", code)
	}
	if out := s.output(); strings.Contains(out, "script was interrupted") {
		t.Fatalf("Ctrl-C reached the script that launched harnesh:\n%q", out)
	}
}

func TestInterruptStopsTheAgent(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\necho thinking\nsleep 60\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s := startTerminal(t, harneshEnv, harneshArgv(agent)...)

	s.run("a question")
	s.expectText("thinking")
	s.send(ctrlC)

	s.run(`!echo still""-here`)
	s.expectText("still-here")
	s.run("!exit 4")
	if code := s.exitCode(); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
}

func TestCommandThatCannotStartLeavesTheTerminalUsable(t *testing.T) {
	// The file is executable but not a program, so starting it fails in the
	// child process, after the child has been given the terminal.
	agent := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(agent, []byte{0, 1, 2, 3}, 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s := startTerminal(t, harneshEnv, harneshArgv(agent)...)

	s.run("a question")
	s.expectText("failed to run agent")

	s.run(`!echo still""-here`)
	s.expectText("still-here")
	s.run("!exit 4")
	if code := s.exitCode(); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
}

func TestSuspendedJobDoesNotHangTheSession(t *testing.T) {
	// Nothing above this harnesh can resume it (its process group is
	// orphaned), so Ctrl-Z cannot hand control to another shell. The job
	// must then carry on rather than stay stopped forever.
	s := startTerminal(t, harneshEnv, harneshArgv("echo")...)

	s.run(`!echo st""arted; sleep 1; echo fin""ished`)
	s.expectText("started")
	s.send(ctrlZ)
	s.expectText("finished")

	s.run("!exit 4")
	if code := s.exitCode(); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
}

func TestShutdownTerminatesTheWholeJob(t *testing.T) {
	// The script outlives harnesh, so that the session (and with it the
	// hangup the kernel sends when a session ends) does not do the cleanup
	// that is harnesh's to do.
	script := runHarnesh + `; echo "harnesh exited with $?"; sleep 60`
	s := startTerminal(t, harneshEnv, scriptArgv(script, "echo")...)

	s.run(`!sleep 300 & echo "pids"": $PPID $!"; sleep 300`)
	pids := s.expect(`pids: (\d+) (\d+)`)
	harnesh, _ := strconv.Atoi(pids[1])
	background, _ := strconv.Atoi(pids[2])

	if err := syscall.Kill(harnesh, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	s.expectText("harnesh exited with 143")

	// The background process is not harnesh's child, only a member of the
	// job's process group.
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(background, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(background, syscall.SIGKILL)
			t.Fatalf("background process %d of the job survived the shutdown", background)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChildrenStartWithDefaultTerminalSignals(t *testing.T) {
	// harnesh ignores SIGTTOU while a job runs. Ignored signals are
	// inherited, so a later job must not start with it still ignored: it
	// could then never be stopped for background terminal output.
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("needs /proc to inspect signal dispositions")
	}
	s := startTerminal(t, harneshEnv, harneshArgv("echo")...)

	s.run("!true")
	s.run(`!echo "ignored""-mask=$(grep SigIgn /proc/self/status | tr -d '[:space:]')"`)
	mask, err := strconv.ParseUint(strings.TrimPrefix(s.expect(`ignored-mask=SigIgn:([0-9a-f]+)`)[1], "0x"), 16, 64)
	if err != nil {
		t.Fatalf("parsing mask: %v", err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGTTOU, syscall.SIGTTIN, syscall.SIGTSTP, syscall.SIGINT, syscall.SIGQUIT} {
		if mask&(1<<(uint(sig)-1)) != 0 {
			t.Errorf("signal %d (%v) is ignored in !commands", int(sig), sig)
		}
	}

	s.run("!exit")
	s.exitCode()
}

func TestSessionUsesTheOriginalShellsConfiguration(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	home := t.TempDir()
	rc := "alias hi='echo hello'\n" +
		"PROMPT_COMMAND='PS1=\"status[$?]> \"'\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// PS1 in the environment must lose to the prompt the shell builds.
	env := append([]string{"HOME=" + home, "SHELL=" + bash}, harneshEnv...)
	s := startTerminal(t, env, harneshArgv("echo")...)

	s.expectText("status[0]> ")
	s.send("!hi\r")
	s.expectText("hello")
	s.expectText("status[0]> ")
	s.send("!false\r")
	s.expectText("status[1]> ")
	s.send("!exit 4\r")
	if code := s.exitCode(); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
}
