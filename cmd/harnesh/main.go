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
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/Parsa2820/harnesh/internal/shell"
	"github.com/Parsa2820/harnesh/internal/term"
)

// version is set at build time via -ldflags "-X main.version=...". When unset
// (e.g. with "go install"), the module version from the build info is used.
var version string

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
  <prompt>          Send <prompt> to the agent (conversation history is retained).
                    Pasted text may span lines; Alt-Enter inserts a newline.
  !<command>        Run <command> in your original shell (inheriting its coloring)
  !cd [dir]         Change harnesh's working directory
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
	os.Exit(realMain())
}

func realMain() int {
	// Line editing needs a terminal on both ends; anything else (pipes,
	// files, /dev/null) gets the plain line-oriented mode.
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGTERM and SIGHUP always shut harnesh down. In an interactive session
	// SIGINT and SIGQUIT (Ctrl-C, Ctrl-\) are meant for the foreground child
	// (the terminal delivers them to it directly), so harnesh itself ignores
	// them like any shell; in a piped session they terminate it like any
	// other program. Handlers are installed with Notify rather than Ignore so
	// that child processes still start with the default dispositions.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	var fatalSignal atomic.Int32
	go func() {
		for sig := range signals {
			if interactive && (sig == syscall.SIGINT || sig == syscall.SIGQUIT) {
				continue
			}
			if num, ok := sig.(syscall.Signal); ok {
				fatalSignal.CompareAndSwap(0, int32(num))
			}
			cancel()
		}
	}()

	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, interactive)
	if sig := fatalSignal.Load(); sig != 0 {
		return 128 + int(sig)
	}
	return code
}

// run parses CLI arguments, initializes the Shell, and executes the REPL.
// It is separated from main so CLI behavior can be tested directly.
func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, interactive bool) int {
	if len(args) != 1 {
		fmt.Fprintf(errOut, "harnesh: expected exactly 1 argument (<agent-command>), got %d\n\n%s", len(args), usageText)
		return 2
	}

	switch arg := args[0]; {
	case arg == "-h" || arg == "--help":
		fmt.Fprint(out, usageText)
		return 0
	case arg == "-v" || arg == "--version":
		fmt.Fprintf(out, "harnesh %s\n", buildVersion())
		return 0
	case strings.HasPrefix(arg, "-"):
		fmt.Fprintf(errOut, "harnesh: unknown option %q\n\n%s", arg, usageText)
		return 2
	}

	harness, err := shell.ParseHarnessCommand(args[0])
	if err != nil {
		fmt.Fprintf(errOut, "harnesh: %v\n", err)
		return 2
	}
	// Fail now rather than on the first prompt if the harness is not installed.
	if _, err := exec.LookPath(harness.Binary); err != nil {
		fmt.Fprintf(errOut, "harnesh: agent command not found: %v\n", err)
		return 127
	}

	var historyFile string
	if interactive {
		historyFile = shell.DefaultHistoryFile()
	}

	sh, err := shell.New(shell.Config{
		Harness:     harness,
		In:          in,
		Out:         out,
		Err:         errOut,
		Interactive: interactive,
		HistoryFile: historyFile,
	})
	if err != nil {
		fmt.Fprintf(errOut, "harnesh: %v\n", err)
		return 1
	}

	return sh.Run(ctx)
}

// buildVersion returns the version stamped by the linker, falling back to the
// module version recorded by the Go toolchain.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "dev"
}
