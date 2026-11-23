package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultSystemShell is the fallback POSIX shell used to execute !commands when
// no parent shell or SHELL environment variable can be detected.
const DefaultSystemShell = "/bin/sh"

// clearScreenSeq clears the terminal scrollback buffer, moves the cursor to
// row 1 column 1, and clears the visible screen — matching standard clear(1).
const clearScreenSeq = "\x1b[3J\x1b[H\x1b[2J"

// Config configures a Shell instance.
type Config struct {
	// Harness is the parsed CLI command invoked whenever the user submits a
	// prompt without a leading "!". Required.
	Harness *HarnessCommand

	// In is the input stream read by the REPL. Defaults to os.Stdin.
	In io.Reader

	// Out is the standard output stream for prompts, shell output, and agent
	// responses. Defaults to os.Stdout.
	Out io.Writer

	// Err is the standard error stream for diagnostics and child stderr.
	// Defaults to os.Stderr.
	Err io.Writer

	// Interactive controls whether the shell prints a startup banner and
	// prompt, probes the original shell's PS1 and color settings, and connects
	// In directly to !command child processes so interactive terminal programs
	// (e.g., !vim, !less) work as expected.
	Interactive bool

	// SystemShell is the shell executable used to evaluate !commands via
	// "<SystemShell> -c <command>" and to resolve PS1 prompt expansion.
	// Defaults to DetectOriginalShell() (parent shell, $SHELL, or /bin/sh).
	SystemShell string

	// PS1 optionally overrides the prompt template instead of reading PS1 from
	// the environment or probing the original shell.
	PS1 string

	// HistoryFile optionally specifies a file path for loading and persisting
	// interactive command history (e.g., ~/.harnesh_history). When empty,
	// command history is retained in memory for the current session only.
	HistoryFile string
}

// EvalResult describes the outcome of evaluating a single input line.
type EvalResult struct {
	// ShouldExit is true when the line requested terminating the shell
	// (e.g., via !exit).
	ShouldExit bool

	// ExitCode is the exit status produced by the evaluated line.
	ExitCode int
}

// Shell is the harnesh interactive command loop. Plain input lines are sent to
// the configured AI agent harness with retained conversation history; lines
// prefixed with "!" are executed as normal shell commands.
type Shell struct {
	cfg          Config
	prompt       *PromptRenderer
	editor       *LineEditor
	conversation Conversation
	lastExitCode int
	prevDir      string
}

// New creates a new Shell from cfg, applying sensible defaults for any omitted
// fields. Returns an error if cfg.Harness is nil.
func New(cfg Config) (*Shell, error) {
	if cfg.Harness == nil {
		return nil, errors.New("harness command is required")
	}
	if cfg.In == nil {
		cfg.In = os.Stdin
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.Err == nil {
		cfg.Err = os.Stderr
	}
	if cfg.SystemShell == "" {
		cfg.SystemShell = DetectOriginalShell()
	}

	sh := &Shell{
		cfg:     cfg,
		prompt:  NewPromptRenderer(cfg.SystemShell, cfg.PS1, cfg.Interactive),
		prevDir: os.Getenv("OLDPWD"),
	}
	if cfg.Interactive {
		sh.editor = NewLineEditor(cfg.In, cfg.Out, cfg.HistoryFile)
	}
	return sh, nil
}

// Run starts the read-eval-print loop and blocks until EOF, !exit, or ctx
// cancellation. It returns the shell's final exit code.
func (s *Shell) Run(ctx context.Context) int {
	if s.cfg.Interactive {
		return s.runInteractive(ctx)
	}
	return s.runNonInteractive(ctx)
}

// runInteractive drives the TTY REPL using LineEditor for arrow-key history
// navigation and inline cursor editing.
func (s *Shell) runInteractive(ctx context.Context) int {
	fmt.Fprintf(s.cfg.Out, "harnesh — agent harness: %s\n", s.cfg.Harness.String())
	fmt.Fprintln(s.cfg.Out, "Enter a prompt for the agent, !<command> for the shell, !clear to reset, or !exit / Ctrl-D to quit.")

	var cachedPrompt string
	promptDirty := true

	for {
		if ctx.Err() != nil {
			return s.lastExitCode
		}

		if promptDirty {
			cachedPrompt = s.Prompt(ctx)
			promptDirty = false
		}

		line, err := s.editor.ReadLine(cachedPrompt)
		if errors.Is(err, ErrInterrupted) {
			s.lastExitCode = 130
			promptDirty = true
			continue
		}
		if errors.Is(err, io.EOF) {
			return s.lastExitCode
		}
		if err != nil {
			fmt.Fprintf(s.cfg.Err, "harnesh: read error: %v\n", err)
			return 1
		}

		if strings.TrimSpace(line) != "" {
			s.editor.AddHistory(line)
			res := s.EvalLine(ctx, line)
			if res.ShouldExit {
				return res.ExitCode
			}
			promptDirty = true
		}
	}
}

// runNonInteractive drives the REPL for piped or scripted input without raw
// terminal mode or prompt output.
func (s *Shell) runNonInteractive(ctx context.Context) int {
	reader := bufio.NewReader(s.cfg.In)

	for {
		if ctx.Err() != nil {
			return s.lastExitCode
		}

		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(s.cfg.Err, "harnesh: read error: %v\n", err)
			return 1
		}

		if strings.TrimSpace(line) != "" {
			res := s.EvalLine(ctx, line)
			if res.ShouldExit {
				return res.ExitCode
			}
		}

		if errors.Is(err, io.EOF) {
			return s.lastExitCode
		}
	}
}

// Prompt renders the interactive prompt using the original shell's PS1 and
// ANSI coloring, reflecting the current working directory and last exit code.
func (s *Shell) Prompt(ctx context.Context) string {
	return s.prompt.Render(ctx, s.lastExitCode)
}

// EvalLine evaluates a single line of user input according to harnesh's routing
// rules:
//   - Blank lines are ignored.
//   - Lines starting with "!" run as shell commands (with built-in handling for
//     !cd, !clear, and !exit).
//   - All other lines are sent as a prompt to the configured agent harness
//     command, including any retained conversation history.
func (s *Shell) EvalLine(ctx context.Context, line string) EvalResult {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return EvalResult{ExitCode: s.lastExitCode}
	}

	if strings.HasPrefix(trimmed, "!") {
		cmdLine := strings.TrimSpace(trimmed[1:])
		if cmdLine == "" {
			return EvalResult{ExitCode: s.lastExitCode}
		}
		return s.runShellLine(ctx, cmdLine)
	}

	s.runAgentPrompt(ctx, trimmed)
	return EvalResult{ExitCode: s.lastExitCode}
}

// LastExitCode returns the exit code of the most recently executed command or
// agent prompt.
func (s *Shell) LastExitCode() int {
	return s.lastExitCode
}

// Conversation returns a pointer to the shell's retained agent conversation.
func (s *Shell) Conversation() *Conversation {
	return &s.conversation
}

// runShellLine handles a "!<command>" line, intercepting the built-in !cd,
// !clear, and !exit commands when they are invoked as standalone commands, and
// delegating everything else to the system shell.
func (s *Shell) runShellLine(ctx context.Context, cmdLine string) EvalResult {
	if !hasUnquotedShellOperators(cmdLine) {
		tokens, err := SplitArgs(cmdLine)
		if err == nil && len(tokens) > 0 {
			switch tokens[0] {
			case "exit":
				return s.builtinExit(tokens[1:])
			case "cd":
				s.builtinCD(tokens[1:])
				return EvalResult{ExitCode: s.lastExitCode}
			case "clear":
				s.builtinClear(ctx, cmdLine, tokens[1:])
				return EvalResult{ExitCode: s.lastExitCode}
			}
		}
	}

	s.runExternalShellCommand(ctx, cmdLine)
	return EvalResult{ExitCode: s.lastExitCode}
}

// builtinClear implements "!clear", clearing both the terminal screen and the
// retained agent conversation history.
func (s *Shell) builtinClear(ctx context.Context, cmdLine string, args []string) {
	s.conversation.Clear()
	if len(args) > 0 {
		s.runExternalShellCommand(ctx, cmdLine)
		return
	}
	fmt.Fprint(s.cfg.Out, clearScreenSeq)
	s.lastExitCode = 0
}

// builtinExit implements "!exit [code]". With no arguments, it exits using the
// last command's exit code.
func (s *Shell) builtinExit(args []string) EvalResult {
	if len(args) == 0 {
		return EvalResult{ShouldExit: true, ExitCode: s.lastExitCode}
	}
	if len(args) > 1 {
		fmt.Fprintln(s.cfg.Err, "harnesh: exit: too many arguments")
		s.lastExitCode = 1
		return EvalResult{ExitCode: 1}
	}
	code, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintf(s.cfg.Err, "harnesh: exit: numeric argument required: %s\n", args[0])
		s.lastExitCode = 2
		return EvalResult{ExitCode: 2}
	}
	return EvalResult{ShouldExit: true, ExitCode: code}
}

// builtinCD implements "!cd [dir]" in-process so that directory changes persist
// across subsequent shell commands and agent invocations. Supports bare "!cd"
// (home directory), "!cd -" (previous directory), and "~" prefix expansion.
func (s *Shell) builtinCD(args []string) {
	if len(args) > 1 {
		fmt.Fprintln(s.cfg.Err, "harnesh: cd: too many arguments")
		s.lastExitCode = 1
		return
	}

	var target string
	printTarget := false

	if len(args) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(s.cfg.Err, "harnesh: cd: cannot determine home directory: %v\n", err)
			s.lastExitCode = 1
			return
		}
		target = home
	} else {
		arg := args[0]
		switch {
		case arg == "-":
			if s.prevDir == "" {
				fmt.Fprintln(s.cfg.Err, "harnesh: cd: OLDPWD not set")
				s.lastExitCode = 1
				return
			}
			target = s.prevDir
			printTarget = true
		case arg == "~" || strings.HasPrefix(arg, "~/"):
			home, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(s.cfg.Err, "harnesh: cd: cannot determine home directory: %v\n", err)
				s.lastExitCode = 1
				return
			}
			target = filepath.Join(home, arg[1:])
		default:
			target = arg
		}
	}

	oldDir, _ := os.Getwd()
	if err := os.Chdir(target); err != nil {
		fmt.Fprintf(s.cfg.Err, "harnesh: cd: %v\n", err)
		s.lastExitCode = 1
		return
	}

	newDir, err := os.Getwd()
	if err == nil {
		_ = os.Setenv("PWD", newDir)
	}
	if oldDir != "" {
		s.prevDir = oldDir
		_ = os.Setenv("OLDPWD", oldDir)
	}
	if printTarget && newDir != "" {
		fmt.Fprintln(s.cfg.Out, newDir)
	}
	s.lastExitCode = 0
}

// runExternalShellCommand executes cmdLine using the configured system shell,
// inheriting any color variables discovered from the original shell.
func (s *Shell) runExternalShellCommand(ctx context.Context, cmdLine string) {
	cmd := exec.CommandContext(ctx, s.cfg.SystemShell, "-c", cmdLine)
	cmd.Stdout = s.cfg.Out
	cmd.Stderr = s.cfg.Err
	if s.cfg.Interactive {
		if f, ok := s.cfg.In.(*os.File); ok {
			cmd.Stdin = f
		}
	}
	if extra := s.prompt.ColorEnv(); len(extra) > 0 {
		cmd.Env = append(os.Environ(), extra...)
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			s.lastExitCode = normalizeExitCode(exitErr.ExitCode())
			return
		}
		fmt.Fprintf(s.cfg.Err, "harnesh: failed to run shell command: %v\n", err)
		s.lastExitCode = 127
		return
	}
	s.lastExitCode = 0
}

// runAgentPrompt invokes the configured CLI agent harness with prompt (wrapped
// with any retained conversation history), streams its output live to Out, and
// appends successful turns to the retained conversation.
//
// Stdin is explicitly left nil (/dev/null) so non-interactive agent CLIs never
// block waiting for EOF or consume buffered lines from piped input.
func (s *Shell) runAgentPrompt(ctx context.Context, prompt string) {
	fullPrompt := s.conversation.FormatPrompt(prompt)
	args := s.cfg.Harness.BuildArgs(fullPrompt)

	var respBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, s.cfg.Harness.Binary, args...)
	cmd.Stdin = nil
	cmd.Stdout = io.MultiWriter(s.cfg.Out, &respBuf)
	cmd.Stderr = s.cfg.Err

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := normalizeExitCode(exitErr.ExitCode())
			s.lastExitCode = code
			if exitErr.ExitCode() > 0 {
				fmt.Fprintf(s.cfg.Err, "harnesh: agent exited with status %d\n", code)
			}
			return
		}
		fmt.Fprintf(s.cfg.Err, "harnesh: failed to run agent %q: %v\n", s.cfg.Harness.Binary, err)
		s.lastExitCode = 127
		return
	}

	s.conversation.Add(prompt, respBuf.String())
	s.lastExitCode = 0
}

// normalizeExitCode converts Go's -1 (terminated by signal) into 130 (standard
// shell signal-interrupted exit status), while preserving positive exit codes.
func normalizeExitCode(code int) int {
	if code < 0 {
		return 130
	}
	return code
}

// hasUnquotedShellOperators reports whether s contains shell control or
// redirection operators outside of single/double quotes and backslash escapes.
// When true, commands like "!cd /tmp && ls" are delegated to the system shell
// rather than intercepted by an in-process built-in.
func hasUnquotedShellOperators(s string) bool {
	var inSQuot, inDQuot bool
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inSQuot:
			if ch == '\'' {
				inSQuot = false
			}
		case inDQuot:
			if ch == '\\' && i+1 < len(s) {
				i++
			} else if ch == '"' {
				inDQuot = false
			}
		default:
			switch ch {
			case '\'':
				inSQuot = true
			case '"':
				inDQuot = true
			case '\\':
				if i+1 < len(s) {
					i++
				}
			case '|', '&', ';', '>', '<', '`':
				return true
			case '$':
				if i+1 < len(s) && s[i+1] == '(' {
					return true
				}
			}
		}
	}
	return false
}
