package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// runShellLine handles a "!<command>" line. The built-ins !cd, !clear, and
// !exit are handled in-process when they are the whole command; anything
// else, including compound commands such as "cd /tmp && ls", is delegated to
// the system shell.
func (s *Shell) runShellLine(ctx context.Context, cmdLine string) EvalResult {
	compound, expands := analyzeCommandLine(cmdLine)
	if !compound {
		tokens, err := SplitArgs(cmdLine)
		if err == nil && len(tokens) > 0 {
			switch tokens[0] {
			case "exit":
				return s.builtinExit(tokens[1:])
			case "cd":
				s.lastExitCode = s.builtinCD(ctx, cmdLine, tokens[1:], expands)
				return EvalResult{ExitCode: s.lastExitCode}
			case "clear":
				s.builtinClear(ctx, cmdLine, tokens[1:])
				return EvalResult{ExitCode: s.lastExitCode}
			}
		}
	}

	s.runExternalShellCommand(ctx, cmdLine)
	return EvalResult{ExitCode: s.lastExitCode}
}

// builtinClear implements "!clear", clearing both the terminal screen and the
// retained agent conversation history.
func (s *Shell) builtinClear(ctx context.Context, cmdLine string, args []string) {
	s.conversation.Clear()
	s.trimNoticeShown = false
	if len(args) > 0 {
		s.runExternalShellCommand(ctx, cmdLine)
		return
	}
	fmt.Fprint(s.cfg.Out, clearScreenSeq)
	s.lastExitCode = 0
}

// builtinExit implements "!exit [code]". With no arguments, it exits using the
// last command's exit code.
func (s *Shell) builtinExit(args []string) EvalResult {
	if len(args) == 0 {
		return EvalResult{ShouldExit: true, ExitCode: s.lastExitCode}
	}
	if len(args) > 1 {
		fmt.Fprintln(s.cfg.Err, "harnesh: exit: too many arguments")
		s.lastExitCode = 1
		return EvalResult{ExitCode: 1}
	}
	code, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintf(s.cfg.Err, "harnesh: exit: numeric argument required: %s\n", args[0])
		s.lastExitCode = 2
		return EvalResult{ExitCode: 2}
	}
	// Exit statuses are a single byte; wrap like other shells do so the
	// reported code matches what the parent process will observe.
	return EvalResult{ShouldExit: true, ExitCode: code & 0xff}
}

// builtinCD implements "!cd [dir]" in-process so that directory changes
// persist across subsequent shell commands, prompt renders, and agent
// invocations. It returns the command's exit code.
//
// A bare "!cd" goes to the home directory, "!cd -" to the previous directory,
// and a leading "~" is expanded. When needsShell is set, the arguments use
// real shell expansion (variables, command substitution, globs, "~user") and
// are resolved by the system shell.
func (s *Shell) builtinCD(ctx context.Context, cmdLine string, args []string, needsShell bool) int {
	if needsShell {
		dir, code := s.resolveCDWithShell(ctx, cmdLine)
		if code != 0 {
			return code
		}
		return s.chdir(dir, false)
	}

	if len(args) > 1 {
		return s.cdError("too many arguments")
	}

	switch arg := strings.Join(args, ""); {
	case arg == "-":
		if s.prevDir == "" {
			return s.cdError("OLDPWD not set")
		}
		return s.chdir(s.prevDir, true)
	case arg == "" || arg == "~" || strings.HasPrefix(arg, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return s.cdError(fmt.Sprintf("cannot determine home directory: %v", err))
		}
		return s.chdir(filepath.Join(home, strings.TrimPrefix(arg, "~")), false)
	default:
		return s.chdir(arg, false)
	}
}

func (s *Shell) cdError(msg string) int {
	fmt.Fprintf(s.cfg.Err, "harnesh: cd: %s\n", msg)
	return 1
}

// resolveCDWithShell lets the system shell perform the cd (applying its own
// variable, glob, and tilde expansion) and reports the directory it ended up
// in. The shell prints its own diagnostics on failure.
func (s *Shell) resolveCDWithShell(ctx context.Context, cmdLine string) (dir string, exitCode int) {
	var stdout bytes.Buffer
	cmd := s.shellCommand(ctx, cmdLine+" >/dev/null && pwd")
	cmd.Stdout = &stdout
	cmd.Stderr = s.cfg.Err

	res := resultOf(cmd.Run())
	if res.err != nil {
		fmt.Fprintf(s.cfg.Err, "harnesh: cd: failed to run %s: %v\n", s.cfg.SystemShell, res.err)
		return "", res.code
	}
	if res.code != 0 {
		return "", res.code
	}
	dir = strings.TrimRight(stdout.String(), "\r\n")
	if dir == "" {
		return "", s.cdError("could not determine the target directory")
	}
	return dir, 0
}

// chdir changes the process working directory to target and keeps PWD and
// OLDPWD in sync. Like a shell, it tracks the logical path: entering a
// symlinked directory leaves PWD (and therefore the prompt and child
// processes) showing the path the user typed rather than the resolved one.
func (s *Shell) chdir(target string, announce bool) int {
	oldDir, _ := os.Getwd()

	logical := filepath.Clean(target)
	if !filepath.IsAbs(logical) && oldDir != "" {
		logical = filepath.Join(oldDir, target)
	}
	if err := os.Chdir(logical); err != nil {
		// Collapsing ".." lexically can name a path that does not exist
		// when the current directory was reached through a symlink; fall
		// back to resolving target physically.
		if os.Chdir(target) != nil {
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				err = pathErr.Err
			}
			return s.cdError(fmt.Sprintf("%s: %v", target, err))
		}
	}

	// os.Getwd trusts PWD only when it names the current directory, so this
	// falls back to the physical path if the logical one is not valid.
	_ = os.Setenv("PWD", logical)
	newDir, err := os.Getwd()
	if err == nil && newDir != logical {
		_ = os.Setenv("PWD", newDir)
	}

	if oldDir != "" {
		s.prevDir = oldDir
		_ = os.Setenv("OLDPWD", oldDir)
	}
	if announce && newDir != "" {
		fmt.Fprintln(s.cfg.Out, newDir)
	}
	return 0
}

// analyzeCommandLine scans a "!" command line with shell quoting rules and
// reports two properties that decide how built-ins are handled:
//
//   - compound: the line contains control or redirection operators (pipes,
//     lists, redirections, subshells, multiple lines) outside quotes, so it is
//     more than a single simple command and must be run by the system shell.
//   - expands: the line contains word expansions only a real shell can
//     resolve: parameter and command substitution, globs, brace expansion, or
//     "~user"-style tildes. A plain leading "~" or "~/" is excluded because
//     the cd built-in handles it directly.
func analyzeCommandLine(s string) (compound, expands bool) {
	var inSQuot, inDQuot bool
	wordStart := true
	for i := 0; i < len(s); i++ {
		ch := s[i]
		atWordStart := wordStart
		wordStart = false

		if inSQuot {
			inSQuot = ch != '\''
			continue
		}

		// Active both unquoted and inside double quotes.
		switch ch {
		case '\\':
			i++
			continue
		case '"':
			inDQuot = !inDQuot
			continue
		case '`':
			expands = true
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				return compound, expands
			}
			i += end + 1
			continue
		case '$':
			expands = true
			if i+1 < len(s) && s[i+1] == '(' {
				// Operators inside a command substitution belong to it.
				i = matchingParen(s, i+1)
			}
			continue
		}
		if inDQuot {
			continue
		}

		switch ch {
		case '\'':
			inSQuot = true
		case ' ', '\t':
			wordStart = true
		case '|', '&', ';', '>', '<', '(', ')', '\n':
			compound = true
		case '*', '?', '[', '{':
			expands = true
		case '~':
			if atWordStart && i+1 < len(s) && !strings.ContainsRune(" \t/", rune(s[i+1])) {
				expands = true
			}
		}
	}
	return compound, expands
}

// matchingParen returns the index of the ")" that closes the "(" at s[open],
// or the last index of s if it is never closed.
func matchingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(s) - 1
}
