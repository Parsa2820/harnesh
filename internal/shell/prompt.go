package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	promptRenderTimeout = 2 * time.Second
	promptStartMarker   = "__HARNESH_P_START__"
	promptEndMarker     = "__HARNESH_P_END__"
)

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

// PromptRenderer renders the interactive prompt the way the original shell
// would: from its PS1, after running its prompt hooks, with ANSI coloring.
type PromptRenderer struct {
	shellPath string
	shellKind string
	rawPS1    string
	// override is true when rawPS1 was supplied to harnesh rather than read
	// from the original shell, so it replaces whatever the shell's rc files
	// and prompt hooks set.
	override bool
	// needsInteractive is true when rendering depends on state set up by rc
	// files or prompt hooks, which takes an interactive helper shell.
	needsInteractive bool
}

// shellKindOf returns the shell's name ("zsh", "bash", ...) for shellPath.
func shellKindOf(shellPath string) string {
	return strings.TrimPrefix(filepath.Base(shellPath), "-")
}

// newPromptRenderer initializes a PromptRenderer for shellPath.
//
// The prompt template is, in order of preference: explicitPS1, the PS1 the
// original shell reported in profile (which, unlike harnesh's environment,
// reflects rc files and prompt hooks: zsh and bash do not export PS1), and
// finally $PS1.
func newPromptRenderer(shellPath, explicitPS1 string, profile shellProfile) *PromptRenderer {
	r := &PromptRenderer{
		shellPath: shellPath,
		shellKind: shellKindOf(shellPath),
	}

	hooksMatter := false
	switch {
	case explicitPS1 != "":
		r.rawPS1, r.override = explicitPS1, true
	case profile.probed && profile.hookPS1 != "":
		r.rawPS1 = profile.hookPS1
		// A hook that rewrites PS1 has to run before every prompt. So does
		// one that might feed zsh's psvar, which PS1 can refer to without
		// any "$".
		hooksMatter = profile.hookPS1 != profile.ps1 ||
			(profile.hasHooks && r.shellKind == "zsh" && zshPsvarReference.MatchString(r.rawPS1))
	case os.Getenv("PS1") != "":
		r.rawPS1, r.override = os.Getenv("PS1"), true
	}

	r.needsInteractive = hooksMatter || ps1NeedsInteractiveShell(r.rawPS1)
	return r
}

// zshPsvarReference matches the zsh prompt escapes that read the psvar array:
// %v, %2v, and the conditional %(V...) / %2(V...).
var zshPsvarReference = regexp.MustCompile(`%-?\d*(v|\(-?\d*V)`)

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
	status := strconv.Itoa(lastExitCode)

	if !r.needsInteractive {
		// Fast path (~5ms): a prompt made only of prompt escapes needs
		// neither rc files nor hooks.
		const script = `(exit "$1"); print -rPn -- "$2"`
		out, err := shellOutput(rctx, r.shellPath, "-f", "-c", script, "harnesh", status, r.rawPS1)
		if err != nil {
			return ""
		}
		return out
	}

	// Interactive path: load rc files and run the prompt hooks, so that
	// functions like $(git_branch_name), options like PROMPT_SUBST, and
	// prompts assembled in precmd work as they do in the user's own zsh.
	template := `"$PS1"`
	if r.override {
		template = `"$2"`
	}
	script := zshRunPromptHooks +
		fmt.Sprintf(`printf %%s '%s'; (exit "$1"); print -rPn -- %s; printf %%s '%s'`,
			promptStartMarker, template, promptEndMarker)

	// The exit status is irrelevant: rc files can make the shell exit
	// non-zero after it has printed the prompt.
	out, _ := interactiveShellOutput(rctx, r.shellPath, r.shellKind, script, status, r.rawPS1)
	rendered, _ := extractBetween(out, promptStartMarker, promptEndMarker)
	return rendered
}

// Markers a bash helper puts in front of its answer to say whether bash
// expanded the prompt itself or handed back the raw template.
const (
	bashPromptExpanded = "P"
	bashPromptRaw      = "R"
)

// renderBash expands a bash PS1 string. A prompt made only of backslash
// escapes is expanded by ExpandBashPS1. One that depends on rc files or
// PROMPT_COMMAND is rendered by an interactive bash, which expands it with
// ${PS1@P} when it can (Bash 4.4+) and otherwise returns the template its
// hooks produced for ExpandBashPS1 to expand (macOS ships Bash 3.2).
func (r *PromptRenderer) renderBash(ctx context.Context, lastExitCode int) string {
	if r.rawPS1 == "" {
		return ""
	}
	if !r.needsInteractive {
		return ExpandBashPS1(r.rawPS1)
	}
	rctx, cancel := context.WithTimeout(ctx, promptRenderTimeout)
	defer cancel()

	script := bashRunPromptHooks
	if r.override {
		script += `PS1=$2; `
	}
	// Bash 3.2 rejects ${PS1@P} only when it is evaluated, so the branch it
	// does not take is harmless.
	script += fmt.Sprintf(`printf %%s '%s'; `+
		`if (( BASH_VERSINFO[0] > 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] >= 4) )); then `+
		`(exit "$1"); printf '%s%%s' "${PS1@P}"; `+
		`else printf '%s%%s' "$PS1"; fi; printf %%s '%s'`,
		promptStartMarker, bashPromptExpanded, bashPromptRaw, promptEndMarker)

	out, _ := interactiveShellOutput(rctx, r.shellPath, r.shellKind, script, strconv.Itoa(lastExitCode), r.rawPS1)
	answer, _ := extractBetween(out, promptStartMarker, promptEndMarker)
	switch {
	case strings.HasPrefix(answer, bashPromptExpanded) && len(answer) > 1:
		return stripReadlineBrackets(answer[1:])
	case strings.HasPrefix(answer, bashPromptRaw) && len(answer) > 1:
		return ExpandBashPS1(answer[1:])
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
