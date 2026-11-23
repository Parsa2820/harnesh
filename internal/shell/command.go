// Package shell implements the interactive REPL, input routing, built-in
// commands, and agent harness invocation logic for harnesh.
package shell

import (
	"errors"
	"fmt"
	"strings"
)

// PromptPlaceholder is the optional token inside a harness command string that
// marks where the user's prompt should be inserted. If the placeholder does not
// appear in any argument, the prompt is appended as the final argument.
const PromptPlaceholder = "{}"

// HarnessCommand represents the parsed CLI command used to send a single prompt
// non-interactively to an AI agent and print its response.
type HarnessCommand struct {
	// Raw is the original, unmodified command string supplied to harnesh.
	Raw string
	// Binary is the executable name or path (first token of Raw).
	Binary string
	// Args are the parsed argument tokens following Binary.
	Args []string
}

// ParseHarnessCommand parses a single CLI harness command string (for example,
// "claude -p" or "agent --prompt {} --raw") into a HarnessCommand.
//
// It uses shell-like quoting rules via SplitArgs and returns an error if the
// input is empty or contains malformed quoting.
func ParseHarnessCommand(raw string) (*HarnessCommand, error) {
	tokens, err := SplitArgs(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid harness command: %w", err)
	}
	if len(tokens) == 0 || tokens[0] == "" {
		return nil, errors.New("invalid harness command: command cannot be empty")
	}
	return &HarnessCommand{
		Raw:    strings.TrimSpace(raw),
		Binary: tokens[0],
		Args:   tokens[1:],
	}, nil
}

// BuildArgs constructs the argument list (excluding Binary) for a single agent
// invocation with the given prompt.
//
// If any argument in h.Args contains PromptPlaceholder ("{}"), every occurrence
// of PromptPlaceholder is replaced with prompt. Otherwise, prompt is appended
// as the final argument. Because the prompt is passed directly as an argv
// element rather than interpolated into a shell string, prompts containing
// quotes, whitespace, or shell metacharacters are delivered safely without
// escaping issues.
func (h *HarnessCommand) BuildArgs(prompt string) []string {
	out := make([]string, 0, len(h.Args)+1)
	substituted := false

	for _, arg := range h.Args {
		if strings.Contains(arg, PromptPlaceholder) {
			out = append(out, strings.ReplaceAll(arg, PromptPlaceholder, prompt))
			substituted = true
		} else {
			out = append(out, arg)
		}
	}

	if !substituted {
		out = append(out, prompt)
	}
	return out
}

// String returns the original raw harness command string.
func (h *HarnessCommand) String() string {
	return h.Raw
}

// SplitArgs splits a command string into argv-style tokens using POSIX-like
// quoting rules:
//   - Spaces and tabs separate tokens outside quotes.
//   - Single quotes ('...') preserve every character literally until the closing '.
//   - Double quotes ("...") preserve whitespace and single quotes, and interpret
//     backslash escapes for ", \, $, and `.
//   - Outside quotes, a backslash escapes the following character.
//
// Returns an error if a quote is left unclosed or a backslash trails at the end
// of the string.
func SplitArgs(input string) ([]string, error) {
	var (
		tokens  []string
		current strings.Builder
		inToken bool
		inSQuot bool
		inDQuot bool
	)

	flush := func() {
		if inToken {
			tokens = append(tokens, current.String())
			current.Reset()
			inToken = false
		}
	}

	for i := 0; i < len(input); i++ {
		ch := input[i]

		switch {
		case inSQuot:
			if ch == '\'' {
				inSQuot = false
			} else {
				current.WriteByte(ch)
			}

		case inDQuot:
			if ch == '"' {
				inDQuot = false
			} else if ch == '\\' {
				if i+1 >= len(input) {
					return nil, errors.New("unterminated double quote")
				}
				next := input[i+1]
				if next == '"' || next == '\\' || next == '$' || next == '`' {
					current.WriteByte(next)
					i++
				} else {
					current.WriteByte(ch)
				}
			} else {
				current.WriteByte(ch)
			}

		default:
			switch ch {
			case ' ', '\t', '\r', '\n':
				flush()
			case '\'':
				inSQuot = true
				inToken = true
			case '"':
				inDQuot = true
				inToken = true
			case '\\':
				if i+1 >= len(input) {
					return nil, errors.New("trailing backslash escape")
				}
				i++
				current.WriteByte(input[i])
				inToken = true
			default:
				current.WriteByte(ch)
				inToken = true
			}
		}
	}

	if inSQuot {
		return nil, errors.New("unterminated single quote")
	}
	if inDQuot {
		return nil, errors.New("unterminated double quote")
	}

	flush()
	return tokens, nil
}
