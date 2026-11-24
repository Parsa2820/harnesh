package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// childResult is the outcome of running a child process.
type childResult struct {
	// code is the shell-style exit status: the process's own status, 128+N
	// when it was terminated by signal N, or 126/127 when it could not be
	// started.
	code int
	// signaled is true when the process was terminated by a signal.
	signaled bool
	// err is non-nil only when the process could not be started or waited on.
	err error
}

// command builds a child process tied to ctx. On cancellation the child is
// asked to terminate with SIGTERM and only killed if it has not exited after
// childShutdownGrace. The same grace period bounds how long harnesh waits on
// output pipes still held open by background processes the child left behind.
func (s *Shell) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = childShutdownGrace
	return cmd
}

// shellCommand builds a "<shell> -c <script>" child with the environment
// !commands run in: the process environment, the original shell's color
// variables, and the hook that loads the user's aliases and functions.
func (s *Shell) shellCommand(ctx context.Context, script string) *exec.Cmd {
	// A script starting with "-" or "+" would be read as more shell options.
	if strings.HasPrefix(script, "-") || strings.HasPrefix(script, "+") {
		script = " " + script
	}
	cmd := s.command(ctx, s.cfg.SystemShell, "-c", script)
	if len(s.colorEnv) > 0 || s.definitions != nil {
		cmd.Env = append(os.Environ(), s.colorEnv...)
		if s.definitions != nil {
			cmd.Env = append(cmd.Env, s.definitions.Env()...)
		}
	}
	return cmd
}

// runJob runs cmd as the user's current foreground job and waits for it. In
// an interactive session the job gets the terminal to itself (see
// runForeground); otherwise it simply runs as a child.
func (s *Shell) runJob(cmd *exec.Cmd) childResult {
	if tty, ok := s.jobControlTTY(); ok {
		return runForeground(cmd, tty)
	}
	return resultOf(cmd.Run())
}

// resultOf translates the error from starting or waiting for a child into a
// shell-style exit status.
func resultOf(err error) childResult {
	var exitErr *exec.ExitError
	switch {
	case err == nil, errors.Is(err, exec.ErrWaitDelay):
		// ErrWaitDelay: the process itself exited successfully, but a
		// descendant kept an output pipe open past the grace period.
		return childResult{}
	case errors.As(err, &exitErr):
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return childResult{code: 128 + int(ws.Signal()), signaled: true}
		}
		if code := exitErr.ExitCode(); code >= 0 {
			return childResult{code: code}
		}
		return childResult{code: 1}
	case errors.Is(err, os.ErrPermission):
		return childResult{code: 126, err: err}
	default:
		return childResult{code: 127, err: err}
	}
}

// runExternalShellCommand executes cmdLine using the configured system shell,
// with the color variables, aliases, and functions discovered from the
// original shell.
func (s *Shell) runExternalShellCommand(ctx context.Context, cmdLine string) {
	cmd := s.shellCommand(ctx, cmdLine)
	cmd.Stdout = s.cfg.Out
	cmd.Stderr = s.cfg.Err
	// Only hand the terminal to the child in interactive sessions; in piped
	// sessions stdin carries the remaining script and must not be consumed.
	if s.cfg.Interactive {
		if f, ok := s.cfg.In.(*os.File); ok {
			cmd.Stdin = f
		}
	}

	res := s.runJob(cmd)
	if res.err != nil {
		fmt.Fprintf(s.cfg.Err, "harnesh: failed to run %s: %v\n", s.cfg.SystemShell, res.err)
	}
	s.lastExitCode = res.code
}

// runAgentPrompt invokes the configured CLI agent harness with prompt (wrapped
// with any retained conversation history), streams its output live to Out, and
// appends successful turns to the retained conversation.
//
// Stdin is left nil (/dev/null) so non-interactive agent CLIs never block
// waiting for EOF or consume buffered lines from piped input.
func (s *Shell) runAgentPrompt(ctx context.Context, prompt string) {
	fullPrompt, omitted := s.conversation.FormatPrompt(prompt, s.cfg.MaxPromptBytes)
	if omitted > 0 && !s.trimNoticeShown {
		s.trimNoticeShown = true
		fmt.Fprintf(s.cfg.Err,
			"harnesh: the conversation no longer fits in one prompt (limit %d KiB); "+
				"the oldest turns are now left out. Run !clear to start fresh.\n",
			s.cfg.MaxPromptBytes>>10)
	}

	var response bytes.Buffer
	cmd := s.command(ctx, s.cfg.Harness.Binary, s.cfg.Harness.BuildArgs(fullPrompt)...)
	cmd.Stdout = io.MultiWriter(s.cfg.Out, &response)
	cmd.Stderr = s.cfg.Err

	res := s.runJob(cmd)
	s.lastExitCode = res.code
	switch {
	case errors.Is(res.err, syscall.E2BIG):
		fmt.Fprintf(s.cfg.Err,
			"harnesh: the prompt is too large to pass to %q as a command-line argument; shorten it and try again\n",
			s.cfg.Harness.Binary)
	case res.err != nil:
		fmt.Fprintf(s.cfg.Err, "harnesh: failed to run agent %q: %v\n", s.cfg.Harness.Binary, res.err)
	case res.signaled:
		// Interrupted (typically Ctrl-C): nothing to report or retain.
	case res.code != 0:
		fmt.Fprintf(s.cfg.Err, "harnesh: agent exited with status %d\n", res.code)
	default:
		// Only successful turns are retained, so failures and interruptions
		// never pollute the context of later prompts.
		s.conversation.Add(prompt, response.String())
	}
}
