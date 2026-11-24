package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// shellProbeTimeout bounds the one interactive run of the original shell
	// at startup. It is generous because heavily customized rc files can
	// take a second or more to load.
	shellProbeTimeout = 5 * time.Second
	// helperPipeGrace bounds how long to wait for a helper shell's output
	// pipe after the shell itself is done. Without it, a daemon started from
	// an rc file that inherits stdout would stall harnesh indefinitely.
	helperPipeGrace = 250 * time.Millisecond
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

// shellProfile is what an interactive instance of the original shell knows
// that harnesh's own environment does not: everything the user's rc files set
// up without exporting it.
type shellProfile struct {
	// probed is true when the shell was run and answered.
	probed bool

	// ps1 is the prompt template as the rc files leave it, and hookPS1 the
	// template after the prompt hooks have run once. They differ when the
	// prompt is built by a hook.
	ps1     string
	hookPS1 string
	// hasHooks reports whether any prompt hooks (zsh precmd functions, bash
	// PROMPT_COMMAND) are installed.
	hasHooks bool

	// colorEnv holds KEY=VALUE entries for color variables that are set in
	// the shell but not exported to harnesh.
	colorEnv []string

	// options, functions, and aliases are shell source that recreates the
	// corresponding definitions in a non-interactive shell.
	options   string
	functions string
	aliases   string
}

// Scripts that run the original shell's prompt hooks the way the shell does
// before printing a prompt: with $? holding the last exit status ($1 here).
// Their output is discarded, since stdout is where the helper shell reports
// back to harnesh; what matters is their effect on PS1 and the variables it
// refers to.
const (
	zshRunPromptHooks = `for __harnesh_hook in precmd $precmd_functions; do ` +
		`(( $+functions[$__harnesh_hook] )) && { (exit "$1"); $__harnesh_hook; }; ` +
		`done >/dev/null; `
	bashRunPromptHooks = `for __harnesh_hook in "${PROMPT_COMMAND[@]}"; do ` +
		`(exit "$1"); eval "$__harnesh_hook"; ` +
		`done >/dev/null; `
)

// Scripts that print the shell's definitions as source code.
const (
	zshListPromptHooks = `for __harnesh_hook in precmd $precmd_functions; do ` +
		`(( $+functions[$__harnesh_hook] )) && print -r -- $__harnesh_hook; done; `
	bashListPromptHooks = `printf '%s\n' "${PROMPT_COMMAND[@]}"; `

	// Options that differ from zsh's defaults, as "name on|off" lines.
	// "emulate -R" resets every option except those describing the
	// interactive environment, which must not be carried over anyway.
	zshListOptions = `() { local -A __harnesh_set; __harnesh_set=("${(@kv)options}"); ` +
		`emulate -LR zsh; local __harnesh_opt; ` +
		`for __harnesh_opt in ${(ok)__harnesh_set}; do ` +
		`[[ $__harnesh_set[$__harnesh_opt] != $options[$__harnesh_opt] ]] && ` +
		`print -r -- "$__harnesh_opt $__harnesh_set[$__harnesh_opt]"; done }; `
	// The options that change how a command line is parsed or expanded.
	bashListOptions = `shopt -p extglob globstar nullglob dotglob nocaseglob nocasematch failglob xpg_echo 2>/dev/null; `

	// Functions, leaving out completion functions (by convention prefixed
	// with "_") and the hooks an interactive zsh calls on its own. Functions
	// marked for autoloading print as stubs that load themselves on first
	// use, which needs the same fpath.
	zshListFunctions = `print -r -- "fpath=(${(@q)fpath})"; ` +
		`for __harnesh_fn in ${(ok)functions}; do case $__harnesh_fn in ` +
		`(_*|TRAP*|precmd|preexec|chpwd|periodic|zshexit|zshaddhistory|zsh_directory_name) continue;; ` +
		`esac; typeset -f -- $__harnesh_fn; done; `
	bashListFunctions = `for __harnesh_fn in $(compgen -A function); do case $__harnesh_fn in ` +
		`_*) continue;; esac; declare -f -- "$__harnesh_fn"; done; `

	zshListAliases  = `alias -L; `
	bashListAliases = `alias -p; `
)

// probeShell runs the original shell once in interactive mode (with stdin
// detached) and asks it for its prompt, color settings, options, functions,
// and aliases. Only zsh and bash are probed: fish loads its configuration in
// every instance, and plain POSIX shells have no rc file to speak of.
func probeShell(shellPath, shellKind string) shellProfile {
	var listHooks, runHooks, listOptions, listFunctions, listAliases string
	switch shellKind {
	case "zsh":
		listHooks, runHooks = zshListPromptHooks, zshRunPromptHooks
		listOptions, listFunctions, listAliases = zshListOptions, zshListFunctions, zshListAliases
	case "bash":
		listHooks, runHooks = bashListPromptHooks, bashRunPromptHooks
		listOptions, listFunctions, listAliases = bashListOptions, bashListFunctions, bashListAliases
	default:
		return shellProfile{}
	}

	var script strings.Builder
	section := func(name, body string) {
		start, end := sectionMarkers(name)
		fmt.Fprintf(&script, "printf %%s '%s'; %sprintf %%s '%s'; ", start, body, end)
	}
	// The prompt comes first so that it survives even if a later, larger
	// section runs into the timeout.
	section("ps1", `printf %s "$PS1"; `)
	section("hooks", listHooks)
	script.WriteString(runHooks)
	section("hook-ps1", `printf %s "$PS1"; `)
	for _, key := range colorEnvKeys {
		// Only variables harnesh did not inherit are of interest.
		if _, exported := os.LookupEnv(key); !exported {
			section("env-"+key, fmt.Sprintf(`printf %%s "${%s:-}"; `, key))
		}
	}
	section("options", listOptions)
	section("functions", listFunctions)
	section("aliases", listAliases)

	ctx, cancel := context.WithTimeout(context.Background(), shellProbeTimeout)
	defer cancel()
	// The exit status is irrelevant: whatever was printed is used. "0" is
	// the exit status the prompt hooks see.
	output, _ := interactiveShellOutput(ctx, shellPath, shellKind, script.String(), "0")

	read := func(name string) (string, bool) {
		start, end := sectionMarkers(name)
		return extractBetween(output, start, end)
	}

	var p shellProfile
	if p.ps1, p.probed = read("ps1"); !p.probed {
		return shellProfile{}
	}
	if hooks, ok := read("hooks"); ok {
		p.hasHooks = strings.TrimSpace(hooks) != ""
	}
	var ok bool
	if p.hookPS1, ok = read("hook-ps1"); !ok {
		p.hookPS1 = p.ps1
	}
	for _, key := range colorEnvKeys {
		if value, ok := read("env-" + key); ok && value != "" {
			p.colorEnv = append(p.colorEnv, key+"="+value)
		}
	}
	p.options, _ = read("options")
	p.functions, _ = read("functions")
	p.aliases, _ = read("aliases")
	return p
}

// sectionMarkers returns the strings that delimit the named section in the
// probe's output, setting it apart from anything rc files print.
func sectionMarkers(name string) (start, end string) {
	return "__HARNESH_BEGIN_" + name + "__", "__HARNESH_END_" + name + "__"
}

// shellOutput runs a helper shell with stdin detached and returns what it
// wrote to stdout. Output is returned even alongside an error, since rc files
// can make a shell exit non-zero after it has printed what was asked for.
func shellOutput(ctx context.Context, shellPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.Env = helperShellEnv()
	cmd.WaitDelay = helperPipeGrace
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The shell succeeded; only a background process kept the pipe open.
		err = nil
	}
	return string(out), err
}

// interactiveShellOutput runs script in an interactive helper shell, so that
// the shell's rc files are loaded, with args as its positional parameters.
func interactiveShellOutput(ctx context.Context, shellPath, shellKind, script string, args ...string) (string, error) {
	flags := []string{"-i"}
	if shellKind == "zsh" {
		// An interactive zsh with job control (MONITOR) makes itself the
		// terminal's foreground process group. That would take the
		// terminal away from harnesh, or from the shell above it when
		// harnesh has been put in the background.
		flags = append(flags, "+m")
	}
	argv := append(flags, "-c", script, "harnesh")
	return shellOutput(ctx, shellPath, append(argv, args...)...)
}

// helperShellEnv returns the environment for shells harnesh runs on its own
// behalf. Apple's Terminal integration (/etc/zshrc_Apple_Terminal and its
// bash counterpart) otherwise treats every interactive helper as a new
// terminal session: it saves and restores session state and rewrites the
// shell history file each time one exits.
func helperShellEnv() []string {
	return append(os.Environ(), "SHELL_SESSIONS_DISABLE=1")
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
