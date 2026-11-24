package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// DefaultSystemShell is the fallback POSIX shell used to execute !commands when
// no parent shell or SHELL environment variable can be detected.
const DefaultSystemShell = "/bin/sh"

// clearScreenSeq clears the terminal scrollback buffer, moves the cursor to
// row 1 column 1, and clears the visible screen — matching standard clear(1).
const clearScreenSeq = "\x1b[3J\x1b[H\x1b[2J"

// DefaultMaxPromptBytes is the default cap on the size of a prompt (including
// replayed conversation history) passed to the agent harness. The prompt
// travels as a single argv element, which Linux limits to 128 KiB
// (MAX_ARG_STRLEN) regardless of the overall ARG_MAX.
const DefaultMaxPromptBytes = 96 << 10

// childShutdownGrace is how long a child process gets to exit after being
// sent SIGTERM during shutdown before it is killed.
const childShutdownGrace = 3 * time.Second

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

	// MaxPromptBytes caps the size of the prompt passed to the harness. When
	// the retained conversation no longer fits, the oldest turns are left out.
	// Defaults to DefaultMaxPromptBytes.
	MaxPromptBytes int
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
	editor       *LineEditor  // interactive mode only
	input        *inputReader // non-interactive mode only
	conversation Conversation
	lastExitCode int
	prevDir      string

	// trimNoticeShown records that the user has been told the conversation
	// outgrew MaxPromptBytes, so the notice is printed once per conversation.
	trimNoticeShown bool
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
	if cfg.MaxPromptBytes <= 0 {
		cfg.MaxPromptBytes = DefaultMaxPromptBytes
	}

	sh := &Shell{
		cfg:     cfg,
		prompt:  NewPromptRenderer(cfg.SystemShell, cfg.PS1, cfg.Interactive),
		prevDir: os.Getenv("OLDPWD"),
	}
	if cfg.Interactive {
		sh.editor = NewLineEditor(cfg.In, cfg.Out, cfg.HistoryFile)
	} else {
		sh.input = newInputReader(cfg.In)
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

// runInteractive drives the TTY REPL using LineEditor for history navigation
// and inline editing.
func (s *Shell) runInteractive(ctx context.Context) int {
	fmt.Fprintf(s.cfg.Out, "harnesh — agent harness: %s\n", s.cfg.Harness.String())
	fmt.Fprintln(s.cfg.Out, "Enter a prompt for the agent, !<command> for the shell, !clear to reset, or !exit / Ctrl-D to quit.")

	// The rendered prompt is reused until something runs that could change
	// it (working directory, git state, exit status).
	var prompt string
	promptDirty := true

	for {
		if ctx.Err() != nil {
			return s.lastExitCode
		}
		if promptDirty {
			prompt = s.Prompt(ctx)
			promptDirty = false
		}

		line, err := s.editor.ReadLine(ctx, prompt)
		switch {
		case errors.Is(err, ErrInterrupted):
			s.lastExitCode = 130
			promptDirty = true
			continue
		case errors.Is(err, io.EOF), ctx.Err() != nil:
			return s.lastExitCode
		case err != nil:
			fmt.Fprintf(s.cfg.Err, "harnesh: read error: %v\n", err)
			return 1
		}

		if strings.TrimSpace(line) == "" {
			continue
		}
		s.editor.AddHistory(line)
		res := s.EvalLine(ctx, line)
		if res.ShouldExit {
			return res.ExitCode
		}
		// Output that lacks a trailing newline must not be overwritten by
		// the next prompt.
		s.editor.FreshLine()
		promptDirty = true
	}
}

// runNonInteractive drives the REPL for piped or scripted input, one line per
// prompt or command, without raw terminal mode or prompt output.
func (s *Shell) runNonInteractive(ctx context.Context) int {
	for {
		line, err := s.input.readLine(ctx)
		atEOF := errors.Is(err, io.EOF)
		switch {
		case ctx.Err() != nil:
			return s.lastExitCode
		case err != nil && !atEOF:
			fmt.Fprintf(s.cfg.Err, "harnesh: read error: %v\n", err)
			return 1
		}

		if strings.TrimSpace(line) != "" {
			res := s.EvalLine(ctx, line)
			if res.ShouldExit {
				return res.ExitCode
			}
		}
		if atEOF {
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
