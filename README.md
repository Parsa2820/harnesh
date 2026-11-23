# harnesh

**harnesh** is a minimal shell where an AI agent is the first-class citizen.

Instead of prefixing every AI request with a CLI invocation, you start `harnesh` with a single argument — the non-interactive prompt command of your preferred CLI harness — and talk to the agent directly. `harnesh` adopts your original shell's `PS1` prompt and coloring, retains your conversation across turns, and lets you run traditional shell commands by prefixing them with `!`.

- **Plain text** → sent to your AI agent harness with retained conversation history
- **`!<command>`** → executed in your original shell with its coloring preserved
- **`!clear`** → clears both the terminal screen and the retained conversation

Written in pure Go with zero external dependencies.

---

## Quick Start

### Prerequisites

- Go 1.24+ (to build from source)
- Any CLI AI harness that can execute a single prompt non-interactively and print the response (e.g., `claude`, `gemini`, `codex`, `llm`, `ollama`, or a custom wrapper script)

### Build & Install

```sh
# Install directly via Go
go install github.com/Parsa2820/harnesh/cmd/harnesh@latest

# Or build ./harnesh from a local clone
make build

# Or install from a local clone into $GOPATH/bin ($GOBIN)
make install
```

You can also run directly without building:

```sh
go run ./cmd/harnesh "claude -p"
```

---

## Usage

```text
harnesh <agent-command>
harnesh -h | --help
harnesh -v | --version
```

`harnesh` takes **exactly one argument**: the command string used to run a single prompt non-interactively and print the response. Quote it so your outer shell passes it as a single argument.

### Examples with Popular CLI Harnesses

```sh
# Claude Code
harnesh "claude -p"

# Gemini CLI
harnesh "gemini -p"

# OpenAI Codex CLI
harnesh "codex exec"

# Simon Willison's llm CLI
harnesh "llm"
harnesh "llm -m gpt-4o-mini"

# Ollama
harnesh "ollama run llama3.2"

# Custom prompt placement with {} placeholder
harnesh "my-agent --prompt {} --format text"

# Quick local smoke test using printf as a mock harness
harnesh 'printf "Agent heard: %s\n"'
```

### Example Session

```text
$ ./harnesh "claude -p"
harnesh — agent harness: claude -p
Enter a prompt for the agent, !<command> for the shell, !clear to reset, or !exit / Ctrl-D to quit.
host ~/projects/app main % !git status -s
 M main.go
host ~/projects/app main % Summarize what changed in main.go
...agent response (saved to conversation history)...
host ~/projects/app main % Now write a concise commit message for those changes
...agent response with prior turn context...
host ~/projects/app main % !clear
(screen and conversation history cleared)
host ~/projects/app main % !exit
```

---

## Features & Behavior

### Original Shell `PS1` & Coloring

When started interactively, `harnesh` detects the shell that launched it (parent process if it is a recognized shell like `zsh`, `bash`, or `fish`, otherwise `$SHELL`, otherwise `/bin/sh`) and mirrors its prompt and colors:

- **`PS1` prompt rendering**:
  - Uses `PS1` from the environment if exported; otherwise probes your original shell once at startup to discover its configured `PS1` (from `~/.zshrc`, `~/.bashrc`, etc.).
  - Preserves all ANSI color sequences, working-directory expansions, dynamic prompt segments (e.g., git branch helpers), and exit-status conditionals (such as zsh's `%(?.%F{green}.%F{red})`).
  - Supports **zsh** (native `print -rPn`), **bash** (`${PS1@P}` on Bash 4.4+ plus built-in Bash escape expansion for Bash 3.2+), **fish** (`fish_prompt`), and POSIX shells.
- **Shell coloring inheritance**:
  - Inherits your terminal's color environment (`TERM`, `COLORTERM`, `CLICOLOR`, `LSCOLORS`, `LS_COLORS`, `GREP_COLOR`, `GREP_COLORS`), including unexported color variables discovered from your original shell's startup config, so `!commands` keep your expected colors.

### Conversation Retention

Because CLI harnesses invoked with a single prompt are stateless per call, `harnesh` maintains an in-memory conversation log for the session:

- **Turn 1**: Your prompt is passed directly to the harness as-is.
- **Streaming + capture**: Agent output streams live to your terminal while `harnesh` simultaneously captures an ANSI-stripped copy of the response.
- **Follow-up turns**: Subsequent prompts include the prior `User:` / `Assistant:` turns ahead of your latest message so the agent retains full context across turns.
- **Error hygiene**: Failed or interrupted agent invocations (non-zero exit status) are not added to conversation history.
- **Reset**: Run `!clear` to clear both the terminal screen and the retained conversation.

### Line Editing & Command History

In interactive sessions, `harnesh` provides terminal line editing and persistent command history (`~/.harnesh_history`) with zero external dependencies:

| Shortcut | Action |
|---|---|
| `↑` / `↓` (or `Ctrl-P` / `Ctrl-N`) | Navigate previous / next commands in history (preserving your current draft) |
| `←` / `→` (or `Ctrl-B` / `Ctrl-F`) | Move cursor left / right within the current line |
| `Option` / `Ctrl` + `←` / `→` (or `Alt-B` / `Alt-F`) | Jump backward / forward by word |
| `Home` / `End` (or `Ctrl-A` / `Ctrl-E`) | Jump to start / end of line |
| `Backspace` / `Delete` | Delete character before / at cursor |
| `Ctrl-U` / `Ctrl-K` | Delete to start / end of line |
| `Ctrl-W` / `Option-Backspace` | Delete previous word |
| `Ctrl-L` | Clear screen and redraw current prompt and input (keeps conversation) |
| `Ctrl-C` | Cancel current input line (or interrupt a running agent / `!command`) |
| `Ctrl-D` | Exit on empty line (or delete character at cursor if non-empty) |

Raw terminal mode is only active while editing a prompt and is always restored before executing any agent call or `!command`, so full-screen tools like `!vim` and `!less` work normally.

### Input Routing Reference

| Input | Behavior |
|---|---|
| `<prompt>` | Passed to `<agent-command>` (with retained conversation history on follow-up turns). |
| `!<command>` | Executed via your original shell (`<shell> -c <command>`), supporting pipes, redirects, environment variables, globbing, and interactive TUIs (`!vim`, `!less`). |
| `!cd [dir]` | Built-in: changes `harnesh`'s own working directory so directory changes persist across subsequent shell commands, `PS1` renders, and agent invocations. Supports bare `!cd` (`$HOME`), `!cd -` (`$OLDPWD`), and `~` expansion. |
| `!clear` | Built-in: clears both the terminal screen (and scrollback buffer) and the retained agent conversation history. |
| `!exit [code]` | Built-in: exits `harnesh` with `[code]` (or the last command's exit code if omitted). |
| Blank line / `!` | Ignored; re-prompts immediately. |
| `Ctrl-D` (EOF) | Exits `harnesh` cleanly with the last command's exit code. |
| `Ctrl-C` (`SIGINT`) | Interrupts a running agent or `!command` without killing `harnesh`. |

### Prompt Placement & Quoting Safety

1. `<agent-command>` is parsed once at startup into `(binary, args...)` using POSIX-like quoting rules (`'...'`, `"..."`, and `\` escapes).
2. On every prompt:
   - If any token in `args` contains `{}`, every `{}` is replaced with the prompt (supporting both standalone `{}` and embedded forms like `--prompt={}`).
   - Otherwise, the prompt is appended as the final argument.
3. Because the agent is invoked directly via `exec.Command(binary, args...)` rather than through `/bin/sh -c`, prompts containing quotes, backticks, `$variables`, or newlines are delivered verbatim without shell injection or escaping issues.

---

## Non-Interactive / Piped Usage

When `stdin` is not a TTY (for example, in scripts or pipelines), `harnesh` skips probing the interactive shell and suppresses the welcome banner and prompt string so output remains clean and machine-readable:

```sh
printf "Remember the number 42\nWhat number did I ask you to remember?\n" | ./harnesh "claude -p"
```

---

## Project Structure

```text
.
├── .github/
│   └── workflows/
│       └── ci.yml                    # GitHub Actions CI (vet, test -race, build)
├── cmd/
│   └── harnesh/
│       ├── main.go                   # CLI entry point, arg validation, signal setup
│       └── main_test.go              # CLI argument & end-to-end tests
├── internal/
│   └── shell/
│       ├── command.go                # Harness command parser, POSIX arg splitter, {} substitution
│       ├── command_test.go           # Unit tests for command parsing & prompt placement
│       ├── conversation.go           # Multi-turn conversation history & ANSI stripping
│       ├── conversation_test.go      # Unit tests for conversation formatting & ANSI stripping
│       ├── lineeditor.go             # Interactive line editor, arrow history & ~/.harnesh_history
│       ├── lineeditor_test.go        # Unit tests for arrow keys, shortcuts & history persistence
│       ├── prompt.go                 # Original shell detection, PS1 expansion & color inheritance
│       ├── prompt_test.go            # Unit tests for zsh/bash PS1 rendering & color preservation
│       ├── rawmode_darwin.go         # macOS termios raw terminal mode
│       ├── rawmode_linux.go          # Linux termios raw terminal mode
│       ├── rawmode_other.go          # Fallback stub for other platforms
│       ├── shell.go                  # Core REPL, !command dispatch, !cd/!clear/!exit built-ins
│       └── shell_test.go             # Unit & integration tests for the shell loop
├── docs/
│   └── architecture.md               # Architecture, execution model, and design notes
├── .gitignore                        # Git ignore rules for binaries, Go artifacts, OS/editor files
├── Makefile                          # Build, test, vet, install, clean targets
└── go.mod                            # Go module definition (zero external deps)
```

---

## Development

```sh
# Run unit and integration tests (with race detector)
make test

# Run go vet
make vet

# Build binary
make build

# Clean build artifacts
make clean
```

For deeper technical details on `PS1` expansion, conversation retention, stdin/TTY handling, and signal semantics, see [docs/architecture.md](docs/architecture.md).
