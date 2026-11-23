// Command harnesh is a shell where an AI agent is a first-class citizen.
//
// Pass a single argument specifying the CLI harness command that runs one
// prompt non-interactively and prints the response, for example:
//
//	harnesh "claude -p"
//	harnesh "gemini -p"
//	harnesh "my-agent --prompt {} --raw"
//
// Once started, plain lines are sent directly to the agent, while lines
// prefixed with "!" execute as normal shell commands.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Parsa2820/harnesh/internal/shell"
)

// version can be overridden at build time via -ldflags "-X main.version=...".
var version = "0.1.0"

const usageText = `harnesh — a shell with an AI agent as a first-class citizen

Usage:
  harnesh <agent-command>
  harnesh -h | --help
  harnesh -v | --version

Arguments:
  <agent-command>   Command that runs a single prompt non-interactively and
                    prints the response. By default the user's prompt is
                    appended as the final argument. Include "{}" anywhere in
                    the command to place the prompt at a specific position.

Interactive Commands:
  <prompt>          Send <prompt> to the agent (conversation history is retained)
  !<command>        Run <command> in your original shell (inheriting its coloring)
  !cd [dir]         Change the shell's working directory (supports ~ and -)
  !clear            Clear both the terminal screen and the retained conversation
  !exit [code]      Exit harnesh (or press Ctrl-D)

Examples:
  harnesh "claude -p"
  harnesh "gemini -p"
  harnesh "codex exec"
  harnesh "llm -m gpt-4o-mini"
  harnesh "my-agent --prompt {} --raw"
`

func main() {
	// Ignore SIGINT in the shell process itself so Ctrl-C interrupts a running
	// child agent or !command without terminating harnesh.
	sigIntCh := make(chan os.Signal, 1)
	signal.Notify(sigIntCh, os.Interrupt)
	defer signal.Stop(sigIntCh)

	ctx, stopTerm := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stopTerm()

	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, isTerminal(os.Stdin))
	os.Exit(code)
}

// run parses CLI arguments, initializes the Shell, and executes the REPL.
// It is separated from main so CLI behavior can be tested directly.
func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, interactive bool) int {
	if len(args) == 1 {
		switch args[0] {
		case "-h", "--help":
			fmt.Fprint(out, usageText)
			return 0
		case "-v", "--version":
			fmt.Fprintf(out, "harnesh %s\n", version)
			return 0
		}
	}

	if len(args) != 1 {
		fmt.Fprintf(errOut, "harnesh: expected exactly 1 argument (<agent-command>), got %d\n\n%s", len(args), usageText)
		return 2
	}

	harness, err := shell.ParseHarnessCommand(args[0])
	if err != nil {
		fmt.Fprintf(errOut, "harnesh: %v\n", err)
		return 2
	}

	sh, err := shell.New(shell.Config{
		Harness:     harness,
		In:          in,
		Out:         out,
		Err:         errOut,
		Interactive: interactive,
	})
	if err != nil {
		fmt.Fprintf(errOut, "harnesh: %v\n", err)
		return 1
	}

	return sh.Run(ctx)
}

// isTerminal reports whether f is connected to a character device (TTY).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
