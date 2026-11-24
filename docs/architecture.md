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
   - `job_unix.go` — Job control for macOS and Linux: runs a child as the terminal's foreground job and handles it being stopped (`job_other.go` is the fallback without it).
   - `command.go` — Parses the single `<agent-command>` argument into a `HarnessCommand` and builds safe `argv` slices for each prompt.
   - `conversation.go` — Stores multi-turn prompt/response history (`Conversation`), strips ANSI sequences from stored responses (`StripANSI`), and formats size-bounded, context-enriched prompts for stateless CLI harnesses.
   - `lineeditor.go` — Interactive line editor (`LineEditor`): key and escape-sequence decoding, editing operations, bracketed paste, with raw terminal mode scoped strictly to `ReadLine`.
   - `render.go` — The wrap-aware screen layout model and character width tables used to redraw the edit line.
   - `history.go` — In-memory command history and its `~/.harnesh_history` persistence.
   - `input.go` — `inputReader`, a buffered reader whose blocking reads can be abandoned on context cancellation.
   - `probe.go` — Runs the original shell once at startup and collects what its rc files set up without exporting: `PS1` before and after the prompt hooks, color variables, options, functions, and aliases (`shellProfile`).
   - `prompt.go` — Detects the original shell (`DetectOriginalShell`) and renders its `PS1` with ANSI colors, working directory, and last exit status (`PromptRenderer`).
   - `definitions.go` — Makes the probed aliases and functions available to `!commands` through the shell's own startup-file mechanism.
3. **`internal/term`** — Terminal control implemented directly on `ioctl`s: `IsTerminal`, `MakeRaw`, `Width`, and the foreground process group accessors, for macOS, Linux, and the BSDs, with stubs elsewhere.

---

## Data Flow

```text
                       ┌──────────────────────────┐
  harnesh "<cmd>" ───► │ ParseHarnessCommand(cmd) │ ──► HarnessCommand{Binary, Args}
                       └──────────────────────────┘
                                    │
                                    ▼
                        ┌───────────────────────┐
                        │  probeShell()         │ ◄── detects parent shell; asks it for PS1, colors,
                        └───────────────────────┘     options, functions, and aliases (interactive only)
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
             rendered prompt)       │              │ runAgentPrompt(prompt)              │
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
           handled in-process               │ env += colors, definitions   │
           - !cd: os.Chdir (the shell       │ stdin = tty ? In : /dev/null │
             resolves $VAR, $(cmd), globs)  └──────────────────────────────┘
           - !clear: clear screen + history
           - !exit: terminate REPL
```

---

## Key Design Decisions

### 1. The Original Shell's Prompt and Colors

Most shells (`zsh`, `bash`) keep `PS1` as an unexported shell variable configured inside `~/.zshrc` or `~/.bashrc`, so child processes see `os.Getenv("PS1") == ""`. Many configurations go further and build the prompt in a hook that runs before each prompt is printed (`precmd` / `precmd_functions` in zsh, `PROMPT_COMMAND` in bash), so even the value the rc files leave behind is not the prompt the user sees.

To render the user's actual prompt:
1. **Parent shell detection (`DetectOriginalShell`)**: Inspects the parent process command via `ps -p <ppid> -o comm=`. If the parent is a recognized shell (`zsh`, `bash`, `fish`, `sh`, `dash`, `ksh`), that exact shell is used (so launching `harnesh` from `bash` inside a login `zsh` still mirrors `bash`); otherwise it falls back to `$SHELL` and `/bin/sh`.
2. **One startup probe (`probeShell`)**: In interactive mode, `harnesh` runs the original shell once as `<shell> -i -c <script>` and has it print, each between unique markers so that stray rc-file output cannot corrupt a value: `PS1`, the list of prompt hooks, `PS1` again after running those hooks, the unexported color variables (`CLICOLOR`, `LSCOLORS`, `LS_COLORS`, `GREP_COLOR`, `GREP_COLORS`), and the definitions described in [section 6](#6-aliases-and-functions-in-commands). The prompt is printed first, so it survives even if a later section runs into the timeout.
3. **Which prompt wins**: an explicit `Config.PS1`, then what the probe reported (an interactive shell's `PS1` is authoritative: rc files overwrite an inherited one), then `$PS1` for shells that are not probed.
4. **Fast path vs. interactive rendering**: rendering in an interactive helper shell costs a full rc-file load per prompt, so it is used only when needed, namely when `PS1` contains an unescaped `$` or backtick (it may call functions the rc files define), when the prompt hooks changed `PS1` during the probe, or when a zsh prompt reads `psvar` (`%v`, `%(V..)`) and hooks exist that may set it.
   - **zsh**: static prompts expand via `zsh -f -c '(exit "$1"); print -rPn -- "$2"'` (~5 ms, no rc files). Otherwise `zsh -i -c` runs the hooks with `$?` set to the last exit status and then expands `$PS1`, so `$(git_branch_name)`, `vcs_info`, and `%(?.green.red)` conditionals behave as in the user's own zsh.
   - **bash**: static prompts are expanded by the pure-Go `ExpandBashPS1` (`\u`, `\h`, `\w`, `\W`, `\$`, `\e`, `\[`/`\]`, timestamps, ...). Otherwise `bash -i -c` runs `PROMPT_COMMAND` and expands the result with `${PS1@P}` on Bash 4.4+; on older versions (macOS ships 3.2) it hands back the template the hooks produced for `ExpandBashPS1` to expand.
   - **fish**: Invokes `fish_prompt` to capture fish's native colored prompt.
5. **Rendered once per command**: Pressing Enter on an empty line reuses the rendered prompt; anything that runs invalidates it so cwd, git status, and exit-code colors update immediately.

Helper shells are run defensively, because they execute the user's rc files:

- **Bounded**: every helper runs under a timeout *and* a short `Cmd.WaitDelay`. The timeout alone is not enough: if an rc file starts a background process that inherits stdout, reading the helper's output would otherwise block until that process exits.
- **Output of hooks is discarded**: stdout is the channel back to `harnesh`. Only the hooks' effect on `PS1` and the variables it uses matters.
- **No job control (`zsh +m`)**: an interactive zsh with `MONITOR` set moves itself into a new process group and makes that the terminal's foreground group. It restores the previous owner when it exits normally, but not when it is killed by the timeout, and if `harnesh` is in the background at the time it takes the terminal away from the user's shell.
- **`SHELL_SESSIONS_DISABLE=1`**: Apple's Terminal integration in `/etc/zshrc_Apple_Terminal` treats each interactive shell as a terminal session to save and restore, and rewrites the history file when it exits.

Not reproduced: `RPROMPT`, and anything the hooks print (terminal title or working-directory escape sequences).

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

The harness's own option parser still sees that argument, and a prompt such as `--help me with this` or `-5 degrees in Fahrenheit?` would be taken for an option. `BuildArgs` therefore puts a space in front of an argument that would start with `-` because of the prompt. The conventional `--` separator is not used because not every harness accepts it, and where it goes depends on the harness's command line.

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

All other `!command` lines are delegated to `<original-shell> -c <command>`. (A command line that starts with `-` or `+` gets a leading space, or the shell would read it as more options.) The command runs in a fresh shell each time, so variables it sets and directory changes it makes do not persist; that includes a `cd` hidden inside an alias or function.

### 6. Aliases and Functions in `!commands`

Aliases and functions live in rc files, which a shell reads only when it is interactive, while `!commands` run in `<shell> -c`. Running each command in an interactive shell instead (`-i -c`) would re-run the entire rc file every time, with its start-up cost and side effects, and would attach a second interactive shell to the terminal.

Instead, the startup probe has the shell print its definitions as source code, and `definitions.go` arranges for every `!command` shell to load them:

- **What is captured**: options that differ from the defaults (zsh: found by comparing against `emulate -R zsh`, minus those describing the session such as `monitor` or `zle`; bash: the `shopt` settings that affect parsing and expansion), functions (`typeset -f` / `declare -f`, skipping `_`-prefixed completion functions and zsh's hook functions), and aliases (`alias -L` / `alias -p`). zsh functions marked for autoload print as stubs that load themselves on first use, so `fpath` is captured too.
- **Order matters**: options first (bash cannot even parse a function body using `extglob` patterns without the option), then functions, then aliases. An alias defined before a function of the same name would be expanded in the function's definition.
- **How it is loaded**: the source is written to a private temporary directory (mode 0700, removed by `Shell.Close`). A non-interactive shell reads exactly one user startup file, and that is where the hook goes: `ZDOTDIR` points zsh at a generated `.zshenv`, and `BASH_ENV` names a generated file for bash. The generated file first restores the variable to its original value (or unsets it) and sources the user's own file, then sources the definitions. Restoring first means nothing the command starts ever sees the private directory.
- **Verified before use**: shells do not always print a definition in a form they can read back. The generated source is loaded once in a pristine shell (`zsh -f`, `bash --norc`); if it produces any error, `harnesh` falls back to aliases only, and failing that to none, rather than have every command begin with an error message.
- **Scope**: zsh and bash. fish loads its configuration in every instance, so its functions already work; plain POSIX shells have no comparable mechanism. Scripted (non-interactive) sessions do not load any of this, like a shell script would not.

The definitions are a snapshot taken at startup.

### 7. Stdin Isolation, Signals & Shutdown

- **Agent invocations always use `cmd.Stdin = nil` (`/dev/null`)** so non-interactive agent CLIs never hang waiting for EOF or consume buffered lines in piped runs.
- **External `!commands` inherit `stdin` only when `Interactive == true`**, allowing interactive TUIs (`!vim`, `!less`, `!git add -p`) in TTY sessions while preventing child commands from stealing lines in piped sessions.
- **Interactive means a terminal on both stdin and stdout.** Detection uses a termios `ioctl` rather than "is a character device", which would misclassify `/dev/null`.
- **`SIGTERM` / `SIGHUP`** cancel the root context. Every blocking point honors it:
  - *Waiting for input*: `inputReader` performs the blocking read on a goroutine and selects on the context, so `ReadLine` returns immediately, restores the terminal, and the process exits. (A blocking `read(2)` on a terminal cannot be interrupted portably, so the read is abandoned; that is safe only because the process is exiting.)
  - *Running a child*: children are started with `exec.CommandContext` with `Cmd.Cancel` sending `SIGTERM` and `Cmd.WaitDelay` (3 s) as the grace period before `SIGKILL`. `WaitDelay` also bounds how long `harnesh` waits on output pipes held open by background processes a child leaves behind.
  - The process then exits with `128 + signal`.
- **Exit statuses** follow shell conventions: the child's own status, `128 + N` when it was killed by signal `N`, `126`/`127` when it could not be executed.

### 8. Job Control

A terminal sends keyboard signals to every process in its *foreground process group*. If children simply shared `harnesh`'s group, `Ctrl-C` during a command would also reach whatever launched `harnesh`, which for a non-interactive launcher (a script, `make`, an editor's task runner) means killing it; and on shutdown only the direct child could be signalled, leaving its descendants behind.

So in an interactive session on macOS and Linux, the agent and each `!command` run as a foreground *job* (`runForeground`), the way a shell runs them:

- **Own process group, own terminal**: the child is started with `Setpgid` and `Foreground`, which makes it the leader of a new group and gives that group the terminal before the program starts. `Ctrl-C` and `Ctrl-\` now reach the job and nothing else. When the job ends, or could not be executed after all, `harnesh` takes the terminal back.
- **Shutdown signals the group**: `Cmd.Cancel` sends `SIGTERM` (and `SIGCONT`, in case the job is stopped) to the whole process group.
- **`Ctrl-Z`**: `harnesh` has no job table, so it does what a program that runs a child would do without job control: it suspends together with the job and lets the shell above it take over. It waits with `waitid(WEXITED|WSTOPPED|WNOWAIT)`, which reports stops and leaves the exit status for `Cmd.Wait` to collect. On a stop it takes the terminal back and sends `SIGTSTP` to its own group; when it is continued, it returns the terminal to the job and continues it. If nothing can resume `harnesh` (its group is orphaned, as when it is a terminal's top-level program) the kernel discards that `SIGTSTP`; `harnesh` notices that it was not stopped and continues the job, so the session cannot get stuck.
- **Never steal the terminal**: the terminal is only handed over when it currently belongs to the party handing it over. If the user resumes a suspended `harnesh` with `bg`, the job continues without a terminal and `harnesh` stops, like any background program, at the point where it next needs one.
- **`SIGTTOU`**: while a job runs, `harnesh` is itself in the background and must still relay agent output (which `stty tostop` would otherwise forbid) and take the terminal back, so it ignores `SIGTTOU` for that time. An ignored signal is inherited by children, and the Go runtime cannot restore a signal's default action once it has been ignored. Once a job has run, the signal is therefore *caught* (which children do not inherit), and its default action, stopping the process, is carried out by hand.

Elsewhere (the BSDs, and piped sessions on any platform) children share `harnesh`'s process group as before. `harnesh`'s own `SIGINT`/`SIGQUIT` handlers, installed with `signal.Notify` rather than `signal.Ignore` so children do not inherit them, cover that case: in an interactive session the signals are disregarded, in a piped session they terminate `harnesh`.

### 9. Line Editor Rendering

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
- **`profile_test.go`**: against real `zsh` and `bash` (whichever are installed) with a throwaway `$HOME`: the probe, prompts built by hooks for different exit statuses, aliases and functions in `!commands`, preservation of the user's own `.zshenv` / `BASH_ENV`, the fallback when definitions do not load, and clean-up.
- **`shell_test.go`**: prompt/shell dispatch, conversation retention and trimming, `!clear`, `!cd` (logical paths, shell expansion, errors), `!exit`, signal exit statuses, oversized prompts, and shutdown on context cancellation both while idle and while a child runs.
- **`internal/term/term_test.go`**: non-terminals (pipes, `/dev/null`) are rejected.
- **`cmd/harnesh/main_test.go`**: CLI argument handling and an end-to-end session via `run`.
- **`cmd/harnesh/terminal_test.go`**: runs the real program (the test binary re-executes itself as `harnesh`) on a pseudo-terminal opened with a few `ioctl`s, and types at it. This covers what only exists on a terminal: `Ctrl-C` reaching the job but not a launching script, `Ctrl-Z` not hanging the session, shutdown terminating a job's background processes, children starting with default signal dispositions, and a session picking up a real `bash` configuration.

Rendering on a real terminal emulator (actual wrapping, bracketed paste delivery) and `Ctrl-Z` under a job-control shell (`fg`/`bg`) are not covered by the automated suite; they can be exercised by driving the binary inside `tmux` and inspecting `capture-pane` output.

---

## Potential Future Extensions

- **Reverse incremental history search (`Ctrl-R`)**: Interactive substring search through `~/.harnesh_history`.
- **Conversation compaction**: Summarize old turns instead of dropping them when the prompt budget is reached.
- **Prompt delivery via stdin**: An opt-in mode for harnesses that read the prompt from standard input, removing the argument-size limit altogether.
- **Shell-to-agent context piping**: Optional opt-in sharing of recent `!command` outputs with the agent.
- **Native harness session flags**: Optional mapping to harnesses that support server-side session/thread IDs directly.
- **Persistent shell co-process**: Keep one shell alive to render prompts and run `!commands`. That would avoid a shell start-up per prompt and let variables, directory changes, and newly defined aliases persist from one `!command` to the next.
- **Right-hand prompt (`RPROMPT`)**.
