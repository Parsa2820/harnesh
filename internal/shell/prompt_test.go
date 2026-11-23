package shell

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestExpandBashPS1(t *testing.T) {
	ps1 := `\[\033[36m\]\u@\h \[\e[34m\]\w\[\x1b[0m\] \$ `
	got := ExpandBashPS1(ps1, 0)

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
