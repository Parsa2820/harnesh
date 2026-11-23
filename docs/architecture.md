# harnesh Architecture & Design

This document explains how `harnesh` is structured, why key design decisions were made, and how to extend it.

---

## Overview

`harnesh` is intentionally small and layered into two packages:

1. **`cmd/harnesh` (`cmd/harnesh/main.go`)** — Owns process lifecycle concerns: CLI argument validation, `-h`/`-v` handling, TTY detection, OS signal setup (`SIGINT` / `SIGTERM`), and process exit code propagation.
2. **`internal/shell`** — Owns all shell semantics, kept fully decoupled from process globals (`os.Stdin`/`os.Stdout`/`os.Stderr` are injected through `shell.Config`) so the REPL can be tested deterministically in-process:
   - `command.go` — Parses the single `<agent-command>` argument into a `HarnessCommand` and builds safe `argv` slices for each prompt.
   - `conversation.go` — Stores multi-turn prompt/response history (`Conversation`), strips ANSI sequences from stored responses (`StripANSI`), and formats context-enriched prompts for stateless CLI harnesses.
   - `lineeditor.go` + `rawmode_{darwin,linux,other}.go` — Interactive ANSI/UTF-8 line editor (`LineEditor`) providing `↑`/`↓` command history navigation (persisted to `~/.harnesh_history`), `←`/`→` cursor editing, word navigation, and kill-line shortcuts with raw terminal mode scoped strictly to `ReadLine`.
   - `prompt.go` — Detects the original shell (`DetectOriginalShell`), probes unexported `PS1` and color variables once at startup, and renders `PS1` dynamically with ANSI colors, working directory, and last exit status (`PromptRenderer`).
   - `shell.go` — Implements the REPL (`Shell.Run`), line router (`Shell.EvalLine`), in-process built-ins (`!cd`, `!clear`, `!exit`), external shell command runner, and agent invocation runner.

---

## Data Flow

```text
                       ┌──────────────────────────┐
  harnesh "<cmd>" ───► │ ParseHarnessCommand(cmd) │ ──► HarnessCommand{Binary, Args}
                       └──────────────────────────┘
                                    │
                                    ▼
                        ┌───────────────────────┐
                        │  PromptRenderer       │ ◄── detects parent shell, probes PS1 & colors
                        └───────────────────────┘
                                    │
                                    ▼
                        ┌───────────────────────┐
       user input ────► │     Shell.Run()       │ ◄── renders PS1; loops until EOF / !exit / SIGTERM
                        └───────────────────────┘
                                    │
                                    ▼
                        ┌───────────────────────┐
                        │   Shell.EvalLine()    │
                        └───────────────────────┘
                           │        │        │
            blank line ◄───┘        │        └───► no leading "!"
            (no-op, reuse           │              ┌─────────────────────────────────────┐
             cached PS1)            │              │ runAgentPrompt(prompt)              │
                                    │              │ full = conversation.FormatPrompt()  │
                                    │              │ exec(Binary, BuildArgs(full))       │
                                    │              │ stdout -> MultiWriter(Out, buf)     │
                                    │              │ on exit 0: conversation.Add(...)    │
                                    ▼              └─────────────────────────────────────┘
                           leading "!"
                     ┌──────────────┴──────────────┐
                     ▼                             ▼
           standalone builtin?              anything else (or shell ops)
           (!cd, !clear, !exit)             ┌──────────────────────────────┐
                     │                      │ runExternalShellCommand()    │
                     ▼                      │ exec(origShell, "-c", cmd)   │
           handled in-process               │ env += probed color vars     │
           - !cd: os.Chdir                  │ stdin = tty ? In : /dev/null │
           - !clear: clear screen + history └──────────────────────────────┘
           - !exit: terminate REPL
```

---

## Key Design Decisions

### 1. Original Shell `PS1` and Coloring Inheritance

Most shells (`zsh`, `bash`) keep `PS1` as an unexported shell variable configured inside `~/.zshrc` or `~/.bashrc`, so child processes see `os.Getenv("PS1") == ""`.

To render the user's actual prompt and colors accurately:
1. **Parent shell detection (`DetectOriginalShell`)**: Inspects the parent process command via `ps -p <ppid> -o comm=`. If the parent is a recognized shell (`zsh`, `bash`, `fish`, `sh`, `dash`, `ksh`), that exact shell is used (so launching `harnesh` from `bash` inside a login `zsh` still mirrors `bash`); otherwise it falls back to `$SHELL` and `/bin/sh`.
2. **One-time startup probe (`probeOriginalShell`)**: In interactive mode, if `PS1` is not in the environment (or color variables like `CLICOLOR`, `LSCOLORS`, `LS_COLORS`, `GREP_COLOR`, `GREP_COLORS` are unexported), `harnesh` runs a single short-timeout interactive shell probe wrapped in unique sentinel markers (`__HARNESH_PS1_START__` / `__HARNESH_PS1_END__`) so stray rc-file stdout never corrupts the captured value.
3. **Fast-path vs. interactive expansion**:
   - **zsh**: Simple `PS1` strings (no `$` / `$(...)` / backticks) expand via `zsh -f -c '(exit "$1"); print -rPn -- "$2"'` (~5ms, skipping rc files). Dynamic prompts containing functions like `$(git_branch_name)` expand via `zsh -i -c` with sentinel markers so custom prompt helpers, ANSI colors, and `%(?.green.red)` exit-status conditionals work identically to the parent zsh.
   - **bash**: Uses `${PS1@P}` on Bash 4.4+ for dynamic prompts and a zero-overhead pure-Go `ExpandBashPS1` expander (handling `\u`, `\h`, `\H`, `\w`, `\W`, `\$`, `\e`, `\033`, `\x1b`, `\[`/`\]`, timestamps, etc.) that works out of the box on macOS Bash 3.2+.
   - **fish**: Invokes `fish_prompt` to capture fish's native colored prompt.
4. **Prompt caching on blank lines**: Pressing Enter on an empty line reuses the cached rendered prompt without spawning a subprocess; any executed prompt or `!command` invalidates the cache so cwd, git status, and exit-code colors update immediately.

### 2. Harness-Agnostic Conversation Retention

Because `harnesh` works with *any* stateless single-prompt CLI harness (`claude -p`, `gemini -p`, `codex exec`, `llm`, `ollama run`, custom scripts), conversation retention is implemented at the shell layer in `Conversation`:

- **Turn 1**: Sent verbatim (`FormatPrompt` returns the raw user message unchanged).
- **Live streaming + capture**: `cmd.Stdout = io.MultiWriter(s.cfg.Out, &respBuf)` streams bytes immediately to the user's terminal while simultaneously buffering the response.
- **Clean storage**: `Conversation.Add` strips ANSI escape codes (`StripANSI`) and trims whitespace before storing the turn, preventing terminal formatting sequences from polluting future prompts.
- **Follow-up turns**: `FormatPrompt` formats prior `User:` / `Assistant:` exchanges followed by `Current user message:`.
- **Failure isolation**: Only turns that exit with code `0` are appended to `Conversation`, so typos, auth errors, or `Ctrl-C` interruptions do not corrupt history.

> **Note on prompt length**: Prompts are passed via `argv`, which is bounded by the OS `ARG_MAX` limit (~1 MB on macOS, ~2 MB+ on Linux). Running `!clear` resets the conversation history at any time.

### 3. Direct `exec.Command` for the Agent Harness (No Shell Interpolation)

User prompts (and retained multi-turn transcripts) routinely contain single quotes, double quotes, backticks, `$VAR`, pipes, and newlines. `ParseHarnessCommand` splits the harness command template once at startup using `SplitArgs`, and `HarnessCommand.BuildArgs` passes the full prompt directly as an `argv` element to `exec.CommandContext`. No intermediate shell ever interprets the prompt text.

### 4. Optional `{}` Prompt Placeholder

Most CLI harnesses accept the prompt as the final positional argument, so appending the prompt is the default. Supporting an optional `{}` placeholder (`PromptPlaceholder`) handles harnesses that expect trailing flags or `--prompt=<value>` syntax:
- `harnesh "claude -p"` → `["claude", "-p", "<prompt>"]`
- `harnesh "agent --prompt {} --json"` → `["agent", "--prompt", "<prompt>", "--json"]`
- `harnesh "agent --prompt={} --json"` → `["agent", "--prompt=<prompt>", "--json"]`

### 5. In-Process Built-ins (`!cd`, `!clear`, and `!exit`)

Three commands are intercepted in-process when invoked standalone (without unquoted shell operators like `&&`, `;`, `|`):
- **`!cd [dir]`**: Updates `harnesh`'s own working directory via `os.Chdir` (and syncs `PWD` / `OLDPWD`) so directory changes persist across subsequent `!commands`, `PS1` renders, and agent invocations.
- **`!clear`**: Clears both `Conversation` (`s.conversation.Clear()`) and the terminal screen + scrollback buffer by emitting `\x1b[3J\x1b[H\x1b[2J` (matching standard `clear(1)`). If extra flags are passed (e.g. `!clear -x`), the conversation is cleared and the flags are forwarded to the system `clear` command.
- **`!exit [code]`**: Terminates the REPL loop cleanly.

All other `!command` lines are delegated to `<original-shell> -c <command>` with inherited color environment variables.

### 6. Stdin Isolation & Signal Handling

- **Agent invocations always use `cmd.Stdin = nil` (`/dev/null`)** so non-interactive agent CLIs never hang waiting for EOF or consume buffered lines in piped runs.
- **External `!commands` inherit `stdin` only when `Interactive == true`**, allowing interactive TUIs (`!vim`, `!less`, `!git add -p`) in TTY sessions while preventing child commands from stealing lines in piped sessions.
- **`SIGINT` (`Ctrl-C`)** is ignored by `harnesh` so `Ctrl-C` interrupts the foreground child process and returns to the prompt; **`SIGTERM`** cancels `ctx` for graceful shutdown.

---

## Testing Strategy

- **`internal/shell/command_test.go`**: Unit tests for `SplitArgs` (quoting, escaping, malformed inputs) and `ParseHarnessCommand` / `BuildArgs`.
- **`internal/shell/conversation_test.go`**: Unit tests for multi-turn formatting, `Clear()`, and `StripANSI` (CSI and OSC sequences).
- **`internal/shell/lineeditor_test.go`**: Unit tests for arrow-key history navigation, draft preservation, horizontal cursor editing, Unicode backspace, kill shortcuts, `Ctrl-C`/`Ctrl-D`, and persistent history file reload.
- **`internal/shell/prompt_test.go`**: Unit tests for `ExpandBashPS1`, live `zsh` colored `PS1` + exit-status conditional rendering, and sentinel extraction.
- **`internal/shell/shell_test.go`**: Integration tests covering prompt/shell dispatch, multi-turn conversation retention, `!clear` resetting both screen and history, `!cd`, `!exit`, failed-turn isolation, and custom colored `PS1`.
- **`cmd/harnesh/main_test.go`**: End-to-end CLI argument and session tests via `run`.

---

## Potential Future Extensions

- **Reverse incremental history search (`Ctrl-R`)**: Interactive substring search through `~/.harnesh_history`.
- **Multi-line prompts**: Support trailing `\` continuation or fenced multi-line input blocks.
- **Context truncation / compaction**: Automatically trim or summarize very long conversations before approaching OS `ARG_MAX`.
- **Shell-to-agent context piping**: Optional opt-in sharing of recent `!command` outputs with the agent.
- **Native harness session flags**: Optional mapping to harnesses that support server-side session/thread IDs directly.
