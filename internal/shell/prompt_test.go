package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExpandBashPS1(t *testing.T) {
	ps1 := `\[\033[36m\]\u@\h \[\e[34m\]\w\[\x1b[0m\] \$ `
	got := ExpandBashPS1(ps1)

	if strings.Contains(got, `\[`) || strings.Contains(got, `\]`) {
		t.Fatalf("readline brackets not stripped: %q", got)
	}
	if !strings.Contains(got, "\x1b[36m") {
		t.Fatalf("expected octal ESC color sequence in %q", got)
	}
	if !strings.Contains(got, "\x1b[34m") {
		t.Fatalf("expected \\e color sequence in %q", got)
	}
	if !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("expected \\x1b reset sequence in %q", got)
	}
	if !strings.HasSuffix(got, " $ ") && !strings.HasSuffix(got, " # ") {
		t.Fatalf("expected trailing shell sigil in %q", got)
	}
}

func TestRenderZshPS1WithColorAndExitCode(t *testing.T) {
	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}

	ps1 := `%F{cyan}%~ %(?.%F{green}.%F{red})%#%f `
	r := NewPromptRenderer(zshPath, ps1, false)

	okPrompt := r.Render(context.Background(), 0)
	if !strings.Contains(okPrompt, "\x1b[36m") || !strings.Contains(okPrompt, "\x1b[32m") {
		t.Fatalf("expected cyan + green ANSI codes on exit 0, got %q", okPrompt)
	}

	errPrompt := r.Render(context.Background(), 1)
	if !strings.Contains(errPrompt, "\x1b[36m") || !strings.Contains(errPrompt, "\x1b[31m") {
		t.Fatalf("expected cyan + red ANSI codes on exit 1, got %q", errPrompt)
	}
}

func TestExtractBetween(t *testing.T) {
	raw := "rc noise\n__HARNESH_P_START__\x1b[36mhello\x1b[0m __HARNESH_P_END__\ntrailing"
	got, ok := extractBetween(raw, promptStartMarker, promptEndMarker)
	if !ok {
		t.Fatal("expected match")
	}
	if want := "\x1b[36mhello\x1b[0m "; got != want {
		t.Fatalf("extractBetween = %q, want %q", got, want)
	}
}

func TestStripReadlineBracketsAndNeedsInteractive(t *testing.T) {
	// Bash 4.4+ ${PS1@P} converts \[ and \] to \x01 and \x02.
	in := "\x01\x1b[35m\x02custom:shell\x01\x1b[0m\x02 $ "
	want := "\x1b[35mcustom:shell\x1b[0m $ "
	if got := stripReadlineBrackets(in); got != want {
		t.Fatalf("stripReadlineBrackets = %q, want %q", got, want)
	}

	if ps1NeedsInteractiveShell(`\[\033[35m\]custom:\W\[\033[0m\] \$ `) {
		t.Fatal("escaped \\$ should not require an interactive subshell")
	}
	if !ps1NeedsInteractiveShell(`%F{magenta}$(git_branch_name)%f %# `) {
		t.Fatal("$(...) command substitution should require an interactive subshell")
	}
}

func TestRenderDoesNotWaitForBackgroundProcesses(t *testing.T) {
	// A stand-in "zsh" that, like an rc file starting a daemon, leaves a
	// background process holding its stdout open after printing the prompt.
	fakeZsh := filepath.Join(t.TempDir(), "zsh")
	script := "#!/bin/sh\nsleep 5 &\nprintf 'PROMPT> '\n"
	if err := os.WriteFile(fakeZsh, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := NewPromptRenderer(fakeZsh, "ignored> ", false)
	start := time.Now()
	got := r.Render(context.Background(), 0)
	elapsed := time.Since(start)

	if got != "PROMPT> " {
		t.Fatalf("Render = %q, want %q", got, "PROMPT> ")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Render took %v; it must not wait for the background process to exit", elapsed)
	}
}
