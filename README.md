# harnesh

**harnesh** is a minimal shell that makes an AI agent a first-class citizen.

When you type **`!<command>`**, harnesh executes the command in your original shell.
Anything that doesn't start with **`!`** is passed to your preferred CLI harness.

Use **`!clear`** to clear both the terminal screen and the context.

You only need to have your preferred CLI harness ready and let harnesh know its non-interactive prompt command (e.g., `claude -p`).

<img width="1134" height="888" alt="image" src="https://github.com/user-attachments/assets/0cbcdcf0-ba1d-445d-be93-fb9d67b3fd8e" />

Written using Gemini 4 Neon.

---

## Install

You need Go 1.24+ and any CLI harness that can answer a single prompt non-interactively (`claude`, `gemini`, `codex`, `llm`, `ollama`, your own script, ...).

```sh
go install github.com/Parsa2820/harnesh/cmd/harnesh@latest
```

Or, from a clone: `make build` for `./harnesh`, `make install` for `$GOBIN`.

## Usage

```text
harnesh <agent-command>
harnesh -h | --help
harnesh -v | --version
```

`harnesh` takes **exactly one argument**: the command that runs one prompt and prints the response. Quote it so your shell passes it as a single argument.

```sh
harnesh "claude -p"                 # Claude Code
harnesh "gemini -p"                 # Gemini CLI
harnesh "codex exec"                # OpenAI Codex CLI
harnesh "llm -m gpt-4o-mini"        # Simon Willison's llm
harnesh "ollama run llama3.2"       # Ollama

# The prompt is appended as the last argument; use {} to put it elsewhere.
harnesh "my-agent --prompt {} --format text"

# No harness at hand? Try it with a stand-in.
harnesh 'printf "Agent heard: %s\n"'
```

## At the prompt

| You type | What happens |
| --- | --- |
| `some question` | Sent to the agent, along with the conversation so far |
| `!<command>` | Runs in your original shell, with its aliases, functions, and colors |
| `!cd [dir]` | Changes harnesh's working directory (`!cd -`, `~`, `$VAR`, and globs work) |
| `!clear` | Clears the screen and the conversation |
| `!exit [code]`, `Ctrl-D` | Leaves harnesh |

The line editor has history (`↑`/`↓`, kept in `~/.harnesh_history`), the usual Emacs keys (`Ctrl-A`/`E`/`K`/`U`/`W`, `Alt-B`/`F`, ...), and multi-line input: pasted text keeps its line breaks, and `Alt-Enter` inserts one.

## What to expect

- **It looks like your shell.** The prompt is your shell's own `PS1`, rendered by that shell, including prompts that are built by `precmd` hooks or `PROMPT_COMMAND` and ones that show the last exit status. zsh, bash, and fish are supported; the shell is the one you start harnesh from.
- **`!commands` behave like your shell.** Aliases, functions, and shell options from your zsh or bash configuration are available, and full-screen programs (`!vim`, `!less`, `!git add -p`) work.
- **Keys behave like your shell.** `Ctrl-C` stops the running command or agent reply and nothing else. `Ctrl-Z` suspends harnesh together with the running command; `fg` in the parent shell brings both back.
- **The agent remembers.** Harnesses are stateless, so harnesh replays the conversation with each prompt. Only successful replies are kept. When the conversation outgrows what fits in a command-line argument (96 KiB), the oldest turns are left out, and harnesh tells you when that starts.
- **It can be scripted.** With input from a pipe or a file, each line is one prompt or `!command`, with no prompt, line editing, or shell configuration:

  ```sh
  printf '%s\n' 'Name three sorting algorithms' 'Which is stable?' | harnesh "claude -p"
  ```

## Limitations

- Each `!command` runs in a fresh shell, so `!export FOO=1` or `!source env.sh` do not carry over to the next line. The working directory is the exception, and only through `!cd` itself: a `cd` inside a longer command line, an alias, or a function does not persist.
- Aliases and functions are read once at startup; restart harnesh after changing your rc files.
- A right-hand prompt (`RPROMPT`) is not shown, and anything prompt hooks print is discarded.
- There is no tab completion or history search yet.
- macOS and Linux are tested. The BSDs build, without the `Ctrl-C`/`Ctrl-Z` handling described above.

## Development

```sh
make check   # gofmt, go vet, and the tests with the race detector
```

[docs/architecture.md](docs/architecture.md) explains how harnesh is put together and why.
