# harnesh

**harnesh** is a minimal shell that makes an AI agent a first-class citizen.

When you type **`!<command>`**, harnesh executes the command in your original shell.
Anything that doesn't start with **`!`** is passed to your preferred CLI harness.

Use **`!clear`** to clear both the terminal screen and the context.

You only need to have your preferred CLI harness ready and let harnesh know its non-interactive prompt command (e.g., `claude -p`).

<img width="1134" height="888" alt="image" src="https://github.com/user-attachments/assets/0cbcdcf0-ba1d-445d-be93-fb9d67b3fd8e" />

Written using Gemini 4 Neon.

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
