package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	promptProbeTimeout  = 2 * time.Second
	promptRenderTimeout = 2 * time.Second
	// promptPipeGrace bounds how long to wait for a helper shell's output
	// pipe after the shell itself is done. Without it, a daemon started from
	// an rc file that inherits stdout would stall the prompt indefinitely.
	promptPipeGrace   = 250 * time.Millisecond
	promptStartMarker = "__HARNESH_P_START__"
	promptEndMarker   = "__HARNESH_P_END__"
	ps1StartMarker    = "__HARNESH_PS1_START__"
	ps1EndMarker      = "__HARNESH_PS1_END__"
)

// colorEnvKeys lists shell color configuration variables inherited from the
// original shell when not already exported into the process environment.
var colorEnvKeys = []string{
	"CLICOLOR",
	"CLICOLOR_FORCE",
	"LSCOLORS",
	"LS_COLORS",
	"GREP_COLOR",
	"GREP_COLORS",
}

// knownShells enumerates shell basenames recognized when inspecting the parent
// process via DetectOriginalShell.
var knownShells = map[string]bool{
	"zsh":  true,
	"bash": true,
	"fish": true,
	"sh":   true,
	"dash": true,
	"ksh":  true,
}

// DetectOriginalShell determines the shell that launched harnesh. It first
// inspects the parent process name; if the parent is a recognized interactive
// shell (such as zsh, bash, or fish), that shell is used. Otherwise it falls
// back to $SHELL, and finally DefaultSystemShell (/bin/sh).
func DetectOriginalShell() string {
	if parent := detectParentShell(); parent != "" {
		return parent
	}
	if envShell := os.Getenv("SHELL"); envShell != "" {
		return envShell
	}
	return DefaultSystemShell
}

// detectParentShell queries ps for the parent process command name and resolves
// it if it matches a known shell.
func detectParentShell() string {
	ppid := os.Getppid()
	if ppid <= 1 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(ppid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	comm := strings.TrimSpace(string(out))
	comm = strings.TrimPrefix(comm, "-") // Login shells appear as "-zsh" / "-bash".
	if comm == "" {
		return ""
	}

	base := filepath.Base(comm)
	if !knownShells[base] {
		return ""
	}
	if filepath.IsAbs(comm) {
		return comm
	}
	if resolved, err := exec.LookPath(base); err == nil {
		return resolved
	}
	return ""
}

// PromptRenderer renders the interactive prompt using the original shell's PS1
// and ANSI coloring, and collects any shell color environment variables for
// child commands.
type PromptRenderer struct {
	shellPath        string
	shellKind        string
	rawPS1           string
	fromEnv          bool
	needsInteractive bool
	colorEnv         []string
}

// NewPromptRenderer initializes a PromptRenderer for shellPath.
//
// If explicitPS1 is non-empty, it is used directly. Otherwise, os.Getenv("PS1")
// is checked. When interactive is true and neither is set (the common case,
// since zsh and bash do not export PS1 by default), the original shell is
// probed once to discover its configured PS1 and color settings.
func NewPromptRenderer(shellPath, explicitPS1 string, interactive bool) *PromptRenderer {
	if shellPath == "" {
		shellPath = DetectOriginalShell()
	}
	kind := strings.TrimPrefix(filepath.Base(shellPath), "-")

	r := &PromptRenderer{
		shellPath: shellPath,
		shellKind: kind,
	}

	switch {
	case explicitPS1 != "":
		r.rawPS1 = explicitPS1
		r.fromEnv = true
	case os.Getenv("PS1") != "":
		r.rawPS1 = os.Getenv("PS1")
		r.fromEnv = true
	}

	if interactive {
		probedPS1, probedColors := probeOriginalShell(shellPath, kind, r.rawPS1 == "")
		if r.rawPS1 == "" && probedPS1 != "" {
			r.rawPS1 = probedPS1
		}
		r.colorEnv = probedColors
	}

	r.needsInteractive = ps1NeedsInteractiveShell(r.rawPS1)
	return r
}

// ColorEnv returns KEY=VALUE entries for color variables discovered in the
// original shell that are not already present in the current process
// environment.
func (r *PromptRenderer) ColorEnv() []string {
	if len(r.colorEnv) == 0 {
		return nil
	}
	out := make([]string, len(r.colorEnv))
	copy(out, r.colorEnv)
	return out
}

// Render expands the original shell's PS1 (including ANSI colors, working
// directory, and exit-status conditionals) for the current directory and
// lastExitCode. Falls back to DefaultPrompt if expansion fails or no PS1 is
// available.
func (r *PromptRenderer) Render(ctx context.Context, lastExitCode int) string {
	switch r.shellKind {
	case "zsh":
		if rendered := r.renderZsh(ctx, lastExitCode); rendered != "" {
			return rendered
		}
	case "bash":
		if rendered := r.renderBash(ctx, lastExitCode); rendered != "" {
			return rendered
		}
	case "fish":
		if rendered := r.renderFish(ctx, lastExitCode); rendered != "" {
			return rendered
		}
	default:
		if r.rawPS1 != "" {
			return ExpandBashPS1(r.rawPS1)
		}
	}
	return DefaultPrompt()
}

// DefaultPrompt formats the fallback prompt when the original shell's PS1
// cannot be determined.
func DefaultPrompt() string {
	return fmt.Sprintf("harnesh:%s> ", shortWorkingDir())
}

// renderZsh expands PS1 using zsh's native prompt expansion ("print -rPn"),
// preserving ANSI color sequences and exit-status conditionals such as
// %(?.%F{green}.%F{red}).
func (r *PromptRenderer) renderZsh(ctx context.Context, lastExitCode int) string {
	if r.rawPS1 == "" {
		return ""
	}
	rctx, cancel := context.WithTimeout(ctx, promptRenderTimeout)
	defer cancel()

	if !r.needsInteractive {
		// Fast path (~5ms): skip rc files when PS1 has no command/var substitutions.
		const script = `(exit "$1"); print -rPn -- "$2"`
		out, err := shellOutput(rctx, r.shellPath, "-f", "-c", script, "_", strconv.Itoa(lastExitCode), r.rawPS1)
		if err != nil {
			return ""
		}
		return out
	}

	// Interactive path: load rc files so functions like $(git_branch_name) and
	// PROMPT_SUBST options work identically to the user's original zsh.
	var script string
	var args []string
	if r.fromEnv {
		script = fmt.Sprintf(`printf %q; (exit "$1"); print -rPn -- "$2"; printf %q`, promptStartMarker, promptEndMarker)
		args = []string{"-i", "-c", script, "_", strconv.Itoa(lastExitCode), r.rawPS1}
	} else {
		script = fmt.Sprintf(`printf %q; (exit "$1"); print -rPn -- "$PS1"; printf %q`, promptStartMarker, promptEndMarker)
		args = []string{"-i", "-c", script, "_", strconv.Itoa(lastExitCode)}
	}

	out, err := shellOutput(rctx, r.shellPath, args...)
	if err != nil {
		return ""
	}
	if extracted, ok := extractBetween(out, promptStartMarker, promptEndMarker); ok && extracted != "" {
		return extracted
	}
	return ""
}

// renderBash expands a bash PS1 string. When PS1 contains dynamic command
// substitutions and Bash 4.4+ is available, it uses ${PS1@P}; otherwise it
// falls back to ExpandBashPS1 (which also supports macOS Bash 3.2).
func (r *PromptRenderer) renderBash(ctx context.Context, lastExitCode int) string {
	if r.rawPS1 == "" {
		return ""
	}
	if r.needsInteractive {
		rctx, cancel := context.WithTimeout(ctx, promptRenderTimeout)
		defer cancel()

		var script string
		var args []string
		if r.fromEnv {
			script = fmt.Sprintf(`PS1="$2"; printf %q; (exit "$1"); printf '%%s' "${PS1@P}"; printf %q`, promptStartMarker, promptEndMarker)
			args = []string{"-i", "-c", script, "_", strconv.Itoa(lastExitCode), r.rawPS1}
		} else {
			script = fmt.Sprintf(`printf %q; (exit "$1"); printf '%%s' "${PS1@P}"; printf %q`, promptStartMarker, promptEndMarker)
			args = []string{"-i", "-c", script, "_", strconv.Itoa(lastExitCode)}
		}
		if out, err := shellOutput(rctx, r.shellPath, args...); err == nil {
			if extracted, ok := extractBetween(out, promptStartMarker, promptEndMarker); ok && extracted != "" {
				return stripReadlineBrackets(extracted)
			}
		}
	}
	return ExpandBashPS1(r.rawPS1)
}

// renderFish invokes fish's fish_prompt function to capture its colored prompt.
func (r *PromptRenderer) renderFish(ctx context.Context, lastExitCode int) string {
	rctx, cancel := context.WithTimeout(ctx, promptRenderTimeout)
	defer cancel()

	script := fmt.Sprintf(`printf %q; sh -c "exit %d"; fish_prompt; printf %q`, promptStartMarker, lastExitCode, promptEndMarker)
	out, err := shellOutput(rctx, r.shellPath, "-c", script)
	if err != nil {
		return ""
	}
	if extracted, ok := extractBetween(out, promptStartMarker, promptEndMarker); ok && extracted != "" {
		return extracted
	}
	return ""
}

// shellOutput runs a helper shell with stdin detached and returns what it
// wrote to stdout. Output is returned even alongside an error, since rc files
// can make a shell exit non-zero after it has printed what was asked for.
func shellOutput(ctx context.Context, shellPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.WaitDelay = promptPipeGrace
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The shell succeeded; only a background process kept the pipe open.
		err = nil
	}
	return string(out), err
}

// probeOriginalShell launches the original shell once in interactive mode (with
// stdin detached) to read its unexported PS1 and any color environment
// variables defined in rc files.
func probeOriginalShell(shellPath, shellKind string, needPS1 bool) (string, []string) {
	if shellKind != "zsh" && shellKind != "bash" {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), promptProbeTimeout)
	defer cancel()

	var script strings.Builder
	if needPS1 {
		fmt.Fprintf(&script, `printf %q; printf '%%s' "$PS1"; printf %q; `, ps1StartMarker, ps1EndMarker)
	}
	for _, key := range colorEnvKeys {
		// Only inspect keys not already exported into our environment.
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		startM := "__HARNESH_ENV_S_" + key + "__"
		endM := "__HARNESH_ENV_E_" + key + "__"
		fmt.Fprintf(&script, `[ -n "${%s:-}" ] && { printf %q; printf '%%s' "$%s"; printf %q; }; `, key, startM, key, endM)
	}
	if script.Len() == 0 {
		return "", nil
	}

	// The exit status is irrelevant here: whatever was printed between the
	// markers is used.
	output, _ := shellOutput(ctx, shellPath, "-i", "-c", script.String())
	var ps1 string
	if needPS1 {
		if v, ok := extractBetween(output, ps1StartMarker, ps1EndMarker); ok {
			ps1 = v
		}
	}

	var extraEnv []string
	for _, key := range colorEnvKeys {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		startM := "__HARNESH_ENV_S_" + key + "__"
		endM := "__HARNESH_ENV_E_" + key + "__"
		if val, ok := extractBetween(output, startM, endM); ok && val != "" {
			extraEnv = append(extraEnv, key+"="+val)
		}
	}

	return ps1, extraEnv
}

// ps1NeedsInteractiveShell reports whether rawPS1 contains unescaped command or
// variable substitutions that may depend on functions or options defined in
// shell rc files. Escaped "\$" (Bash's root/user prompt sigil) does not require
// an interactive subshell.
func ps1NeedsInteractiveShell(rawPS1 string) bool {
	for i := 0; i < len(rawPS1); i++ {
		switch rawPS1[i] {
		case '\\':
			i++ // Skip escaped character (e.g., \$ or \`).
		case '$', '`':
			return true
		}
	}
	return false
}

// extractBetween returns the substring between the last occurrence of start and
// the matching end marker that follows it.
func extractBetween(s, start, end string) (string, bool) {
	si := strings.LastIndex(s, start)
	if si < 0 {
		return "", false
	}
	rest := s[si+len(start):]
	ei := strings.Index(rest, end)
	if ei < 0 {
		return "", false
	}
	return rest[:ei], true
}

// ExpandBashPS1 expands standard Bash/POSIX PS1 prompt escape sequences
// (including ANSI color escapes such as \e, \033, and \x1b) while stripping
// readline non-printing markers (\[ and \]). It performs no parameter or
// command substitution, so it cannot reflect the last exit status.
func ExpandBashPS1(ps1 string) string {
	var b strings.Builder
	b.Grow(len(ps1) + 16)

	now := time.Now()
	hostname, _ := os.Hostname()
	shortHost := hostname
	if idx := strings.IndexByte(hostname, '.'); idx >= 0 {
		shortHost = hostname[:idx]
	}

	username := os.Getenv("USER")
	if username == "" {
		if u, err := user.Current(); err == nil {
			username = u.Username
		}
	}

	cwdShort := shortWorkingDir()
	cwdBase := filepath.Base(cwdShort)
	if cwdShort == "~" || cwdShort == "/" {
		cwdBase = cwdShort
	}

	for i := 0; i < len(ps1); i++ {
		if ps1[i] != '\\' || i+1 >= len(ps1) {
			b.WriteByte(ps1[i])
			continue
		}
		i++
		switch ps1[i] {
		case '[':
			// Strip readline non-printing start marker.
		case ']':
			// Strip readline non-printing end marker.
		case 'e':
			b.WriteByte(0x1b)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		case 'u':
			b.WriteString(username)
		case 'h':
			b.WriteString(shortHost)
		case 'H':
			b.WriteString(hostname)
		case 'w':
			b.WriteString(cwdShort)
		case 'W':
			b.WriteString(cwdBase)
		case '$':
			if os.Geteuid() == 0 {
				b.WriteByte('#')
			} else {
				b.WriteByte('$')
			}
		case 's':
			b.WriteString("harnesh")
		case 't':
			b.WriteString(now.Format("15:04:05"))
		case 'T':
			b.WriteString(now.Format("03:04:05"))
		case '@':
			b.WriteString(now.Format("03:04 PM"))
		case 'A':
			b.WriteString(now.Format("15:04"))
		case 'd':
			b.WriteString(now.Format("Mon Jan 02"))
		case '0', '1', '2', '3':
			// Octal character escape (e.g. \033 for ESC).
			end := i + 1
			for end < len(ps1) && end < i+3 && ps1[end] >= '0' && ps1[end] <= '7' {
				end++
			}
			if val, err := strconv.ParseUint(ps1[i:end], 8, 8); err == nil {
				b.WriteByte(byte(val))
				i = end - 1
			} else {
				b.WriteByte('\\')
				b.WriteByte(ps1[i])
			}
		case 'x':
			// Hex character escape (e.g. \x1b for ESC).
			if i+2 < len(ps1) {
				if val, err := strconv.ParseUint(ps1[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(val))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		default:
			b.WriteByte('\\')
			b.WriteByte(ps1[i])
		}
	}

	return stripReadlineBrackets(b.String())
}

// stripReadlineBrackets removes literal \[ and \] wrappers as well as Bash
// ${PS1@P} readline non-printing markers (\x01 and \x02).
func stripReadlineBrackets(s string) string {
	s = strings.ReplaceAll(s, `\[`, "")
	s = strings.ReplaceAll(s, `\]`, "")
	s = strings.ReplaceAll(s, "\x01", "")
	s = strings.ReplaceAll(s, "\x02", "")
	return s
}

// shortWorkingDir returns the current working directory with the user's home
// directory abbreviated to "~".
func shortWorkingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if wd == home {
			return "~"
		}
		if strings.HasPrefix(wd, home+string(os.PathSeparator)) {
			return "~" + wd[len(home):]
		}
	}
	return wd
}
