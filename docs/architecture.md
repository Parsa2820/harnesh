# harnesh Architecture & Design

This document explains how `harnesh` is structured, why key design decisions were made, and how to extend it.

---

## Overview

`harnesh` is intentionally small and layered into three packages:

1. **`cmd/harnesh`** — Owns process lifecycle concerns: CLI argument validation, `-h`/`-v` handling, TTY detection, OS signal policy, and process exit code propagation.
2. **`internal/shell`** — Owns all shell semantics, kept decoupled from process globals (`os.Stdin`/`os.Stdout`/`os.Stderr` are injected through `shell.Config`) so the REPL can be tested deterministically in-process:
   - `shell.go` — The `Shell` type, its configuration, and the two REPL loops (`runInteractive`, `runNonInteractive`) plus the line router (`EvalLine`).
   - `builtins.go` — The in-process built-ins (`!cd`, `!clear`, `!exit`) and `analyzeCommandLine`, which decides whether a `!` line is a simple command a built-in may handle.
   - `exec.go` — Runs the agent harness and `!commands` and maps their outcome to shell-style exit statuses.
   - `command.go` — Parses the single `<agent-command>` argument into a `HarnessCommand` and builds safe `argv` slices for each prompt.
   - `conversation.go` — Stores multi-turn prompt/response history (`Conversation`), strips ANSI sequences from stored responses (`StripANSI`), and formats size-bounded, context-enriched prompts for stateless CLI harnesses.
   - `lineeditor.go` — Interactive line editor (`LineEditor`): key and escape-sequence decoding, editing operations, bracketed paste, with raw terminal mode scoped strictly to `ReadLine`.
   - `render.go` — The wrap-aware screen layout model and character width tables used to redraw the edit line.
   - `history.go` — In-memory command history and its `~/.harnesh_history` persistence.
   - `input.go` — `inputReader`, a buffered reader whose blocking reads can be abandoned on context cancellation.
   - `prompt.go` — Detects the original shell (`DetectOriginalShell`), probes unexported `PS1` and color variables once at startup, and renders `PS1` dynamically with ANSI colors, working directory, and last exit status (`PromptRenderer`).
3. **`internal/term`** — The only platform-specific code: `IsTerminal`, `MakeRaw`, and `Width`, implemented directly on termios `ioctl`s for macOS, Linux, and the BSDs, with stubs elsewhere.

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
           - !cd: os.Chdir (the shell       │ stdin = tty ? In : /dev/null │
             resolves $VAR, $(cmd), globs)  └──────────────────────────────┘
           - !clear: clear screen + history
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

5. **Bounded helper shells**: every probe/render subprocess runs under a timeout *and* a short `Cmd.WaitDelay`. The timeout alone is not enough: if an rc file starts a background process that inherits stdout, reading the helper's output would otherwise block until that process exits.

**Known limitation**: only `PS1` itself is evaluated. `precmd` hooks / `PROMPT_COMMAND` are deliberately not run (they are written for the real session and have side effects such as history and terminal-title updates), so themes that rebuild the prompt inside such a hook are rendered from whatever `PS1` held when the rc file finished loading.

### 2. Harness-Agnostic Conversation Retention

Because `harnesh` works with *any* stateless single-prompt CLI harness (`claude -p`, `gemini -p`, `codex exec`, `llm`, `ollama run`, custom scripts), conversation retention is implemented at the shell layer in `Conversation`:

- **Turn 1**: Sent verbatim (`FormatPrompt` returns the raw user message unchanged).
- **Live streaming + capture**: `cmd.Stdout = io.MultiWriter(s.cfg.Out, &respBuf)` streams bytes immediately to the user's terminal while simultaneously buffering the response.
- **Clean storage**: `Conversation.Add` strips ANSI escape codes (`StripANSI`) and trims whitespace before storing the turn, preventing terminal formatting sequences from polluting future prompts.
- **Follow-up turns**: `FormatPrompt` formats prior `User:` / `Assistant:` exchanges followed by `Current user message:`.
- **Failure isolation**: Only turns that exit with code `0` are appended to `Conversation`, so typos, auth errors, or `Ctrl-C` interruptions do not corrupt history.
- **Size budget**: Prompts travel as a single `argv` element. macOS bounds the whole argument list plus environment at 1 MiB (`ARG_MAX`), and Linux additionally bounds each individual argument at 128 KiB (`MAX_ARG_STRLEN`), so an ever-growing transcript would eventually make every invocation fail with `E2BIG`. `FormatPrompt` therefore takes a byte budget (`Config.MaxPromptBytes`, default 96 KiB) and leaves out the oldest turns until the prompt fits; the shell prints a one-time notice when that starts. The current message is never shortened: if it alone is too large for the OS, the resulting `E2BIG` is reported as such.

### 3. Direct `exec.Command` for the Agent Harness (No Shell Interpolation)

User prompts (and retained multi-turn transcripts) routinely contain single quotes, double quotes, backticks, `$VAR`, pipes, and newlines. `ParseHarnessCommand` splits the harness command template once at startup using `SplitArgs`, and `HarnessCommand.BuildArgs` passes the full prompt directly as an `argv` element to `exec.CommandContext`. No intermediate shell ever interprets the prompt text.

### 4. Optional `{}` Prompt Placeholder

Most CLI harnesses accept the prompt as the final positional argument, so appending the prompt is the default. Supporting an optional `{}` placeholder (`PromptPlaceholder`) handles harnesses that expect trailing flags or `--prompt=<value>` syntax:
- `harnesh "claude -p"` → `["claude", "-p", "<prompt>"]`
- `harnesh "agent --prompt {} --json"` → `["agent", "--prompt", "<prompt>", "--json"]`
- `harnesh "agent --prompt={} --json"` → `["agent", "--prompt=<prompt>", "--json"]`

### 5. In-Process Built-ins (`!cd`, `!clear`, and `!exit`)

Three commands are intercepted in-process when they are the whole command. `analyzeCommandLine` scans the line with shell quoting rules and reports whether it is *compound* (contains unquoted `&&`, `;`, `|`, redirections, subshells, or newlines), in which case the line is handed to the system shell untouched, and whether it needs *shell expansion*.

- **`!cd [dir]`**: Updates `harnesh`'s own working directory via `os.Chdir` so directory changes persist across subsequent `!commands`, `PS1` renders, and agent invocations.
  - Literal arguments, `~`, `~/…`, and `-` are resolved in Go.
  - Arguments containing `$VAR`, `$(command)`, globs, braces, or `~user` are resolved by running `<shell> -c '<cd command> >/dev/null && pwd'` and changing to the directory the shell reports, so expansion behaves exactly as it does in the user's shell.
  - Like a shell, `harnesh` tracks the *logical* working directory: `PWD` keeps the path as the user typed it (through symlinks), and `cd ..` goes to the logical parent. `PWD` and `OLDPWD` are exported for children.
- **`!clear`**: Clears both `Conversation` and the terminal screen + scrollback buffer by emitting `\x1b[3J\x1b[H\x1b[2J` (matching standard `clear(1)`). If extra flags are passed (e.g. `!clear -x`), the conversation is cleared and the flags are forwarded to the system `clear` command.
- **`!exit [code]`**: Terminates the REPL loop. The code is truncated to a byte, as the OS would.

All other `!command` lines are delegated to `<original-shell> -c <command>` with inherited color environment variables. Because `-c` does not load interactive rc files, aliases and shell functions are not available.

### 6. Stdin Isolation, Signals & Shutdown

- **Agent invocations always use `cmd.Stdin = nil` (`/dev/null`)** so non-interactive agent CLIs never hang waiting for EOF or consume buffered lines in piped runs.
- **External `!commands` inherit `stdin` only when `Interactive == true`**, allowing interactive TUIs (`!vim`, `!less`, `!git add -p`) in TTY sessions while preventing child commands from stealing lines in piped sessions.
- **Interactive means a terminal on both stdin and stdout.** Detection uses a termios `ioctl` rather than "is a character device", which would misclassify `/dev/null`.
- **`SIGINT` / `SIGQUIT`** (`Ctrl-C`, `Ctrl-\`): in an interactive session the terminal delivers these to the whole foreground process group, so the running child is interrupted directly and `harnesh` ignores its own copy. Handlers are installed with `signal.Notify` rather than `signal.Ignore` because ignored dispositions are inherited across `exec` and would make children uninterruptible. In a piped session these signals terminate `harnesh`.
- **`SIGTERM` / `SIGHUP`** cancel the root context. Every blocking point honors it:
  - *Waiting for input*: `inputReader` performs the blocking read on a goroutine and selects on the context, so `ReadLine` returns immediately, restores the terminal, and the process exits. (A blocking `read(2)` on a terminal cannot be interrupted portably, so the read is abandoned; that is safe only because the process is exiting.)
  - *Running a child*: children are started with `exec.CommandContext` with `Cmd.Cancel` sending `SIGTERM` and `Cmd.WaitDelay` (3 s) as the grace period before `SIGKILL`. `WaitDelay` also bounds how long `harnesh` waits on output pipes held open by background processes a child leaves behind.
  - The process then exits with `128 + signal`.
- **Exit statuses** follow shell conventions: the child's own status, `128 + N` when it was killed by signal `N`, `126`/`127` when it could not be executed.

`harnesh` does not implement job control: children run in its own process group. Two consequences follow. On shutdown, signals are only sent to the direct child, so grandchildren that outlive it are not tracked. And a terminal-generated `Ctrl-C` reaches every process in the foreground group, which includes a non-interactive script that launched `harnesh` (an interactive parent shell is unaffected because it places `harnesh` in a separate group). Fixing either would require separate process groups and terminal ownership hand-off, which is out of scope.

### 7. Line Editor Rendering

The editor redraws the whole edit line after each keystroke, which is only correct if it knows where the text lands on screen:

- **Wrapping**: `layout` models a terminal row by row, including the *deferred wrap* real terminals perform (after writing the last column the cursor stays put until the next character). Each refresh moves up to the first row of the previous rendering, erases to the end of the screen, rewrites the prompt and buffer, and then positions the cursor from the computed cell. The terminal width is re-read from `TIOCGWINSZ` on every refresh, so resizing needs no `SIGWINCH` handling.
- **Width**: the prompt's width is measured after stripping ANSI sequences; `runeWidth` accounts for zero-width (combining, format) and double-width (CJK, emoji) characters with a compact built-in table.
- **Multi-line input**: the buffer may contain newlines (from a paste or `Alt-Enter`); they are drawn as real line breaks. Bracketed paste mode (`ESC [ ? 2004 h`) is enabled only while reading a line, so pasted newlines arrive inside paste markers instead of being indistinguishable from `Enter`. Pasted control characters are dropped.
- **Clean prompt start**: before a prompt that follows command output, the editor prints an inverse `%` followed by `columns - 1` spaces and a carriage return. If the cursor was already in the first column this is invisible; otherwise the spaces wrap and the prompt starts on a fresh row, leaving the marker to show the output had no trailing newline. This is zsh's `PROMPT_SP` technique and avoids querying the cursor position.

History entries are stored one per line with `\n` and `\\` escapes so multi-line prompts survive a round trip; the file is compacted to the newest 1000 entries when it reaches twice that.

---

## Testing Strategy

- **`command_test.go`**: `SplitArgs` (quoting, escaping, malformed inputs) and `ParseHarnessCommand` / `BuildArgs`.
- **`conversation_test.go`**: multi-turn formatting, the size budget (oldest turns dropped first, current prompt never shortened), `Clear()`, and `StripANSI`.
- **`lineeditor_test.go`** + **`screen_test.go`**: key handling (history, cursor movement, kill shortcuts, Unicode, paste, `Alt-Enter`, cancellation) and rendering. Rendering is asserted against `fakeScreen`, a small terminal emulator implementing deferred wrap, cursor movement, and erase, so tests check what ends up *on screen* (wrapped rows, cursor cell, no leftover rows) rather than byte sequences.
- **`history_test.go`**: persistence, multi-line and very long entries, compaction, file permissions.
- **`input_test.go`**: line reading and cancellation without input loss.
- **`render_test.go`**: character widths and table ordering.
- **`prompt_test.go`**: `ExpandBashPS1`, live `zsh` colored `PS1` + exit-status conditional rendering, and sentinel extraction.
- **`shell_test.go`**: prompt/shell dispatch, conversation retention and trimming, `!clear`, `!cd` (logical paths, shell expansion, errors), `!exit`, signal exit statuses, oversized prompts, and shutdown on context cancellation both while idle and while a child runs.
- **`internal/term/term_test.go`**: non-terminals (pipes, `/dev/null`) are rejected.
- **`cmd/harnesh/main_test.go`**: CLI argument handling and an end-to-end session via `run`.

Behavior that depends on a real terminal (raw mode, actual wrapping, bracketed paste delivery) is not covered by the automated suite; it can be exercised by driving the binary inside `tmux` and inspecting `capture-pane` output.

---

## Potential Future Extensions

- **Reverse incremental history search (`Ctrl-R`)**: Interactive substring search through `~/.harnesh_history`.
- **Conversation compaction**: Summarize old turns instead of dropping them when the prompt budget is reached.
- **Prompt delivery via stdin**: An opt-in mode for harnesses that read the prompt from standard input, removing the argument-size limit altogether.
- **Shell-to-agent context piping**: Optional opt-in sharing of recent `!command` outputs with the agent.
- **Native harness session flags**: Optional mapping to harnesses that support server-side session/thread IDs directly.
- **Persistent prompt co-process**: Keep one interactive shell alive to render prompts, which would allow running `precmd` hooks faithfully and avoid a shell start-up per prompt.
