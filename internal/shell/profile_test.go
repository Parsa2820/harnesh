package shell

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rcFixture is the rc file a test gives a shell: an alias, a function, and a
// prompt that only exists once the prompt hook has run.
type rcFixture struct {
	shell  string // executable name
	rcFile string // rc file read by an interactive instance, relative to $HOME
	rc     string
}

var rcFixtures = []rcFixture{
	{
		shell:  "zsh",
		rcFile: ".zshrc",
		rc: "alias hi='echo hello'\n" +
			"greet() { print \"greetings $1\" }\n" +
			"precmd() { PS1=\"hook[$?]> \" }\n",
	},
	{
		shell:  "bash",
		rcFile: ".bashrc",
		rc: "alias hi='echo hello'\n" +
			"greet() { echo \"greetings $1\"; }\n" +
			"PROMPT_COMMAND='PS1=\"hook[$?]> \"'\n",
	},
}

// useFixture points the named shell at a home directory holding only the
// fixture's rc file and returns the shell's path, skipping the test if the
// shell is not installed.
func useFixture(t *testing.T, f rcFixture) string {
	t.Helper()
	shellPath, err := exec.LookPath(f.shell)
	if err != nil {
		t.Skipf("%s not installed", f.shell)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, f.rcFile), []byte(f.rc), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("HOME", home)
	for _, key := range []string{"ZDOTDIR", "BASH_ENV", "ENV", "PS1", "PROMPT_COMMAND"} {
		unsetenv(t, key)
	}
	return shellPath
}

// unsetenv removes key from the environment for the duration of the test.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers restoring the original value
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%s): %v", key, err)
	}
}

func TestProbeShellReadsWhatRCFilesDefine(t *testing.T) {
	for _, f := range rcFixtures {
		t.Run(f.shell, func(t *testing.T) {
			shellPath := useFixture(t, f)
			p := probeShell(shellPath, f.shell)

			if !p.probed {
				t.Fatal("the shell did not answer the probe")
			}
			if !p.hasHooks {
				t.Error("prompt hook not detected")
			}
			if want := "hook[0]> "; p.hookPS1 != want {
				t.Errorf("PS1 after prompt hooks = %q, want %q", p.hookPS1, want)
			}
			if p.ps1 == p.hookPS1 {
				t.Errorf("PS1 before prompt hooks = %q; it should not include the hook's work", p.ps1)
			}
			if !strings.Contains(p.aliases, "hi='echo hello'") {
				t.Errorf("alias missing from %q", p.aliases)
			}
			if !strings.Contains(p.functions, "greet") {
				t.Errorf("function missing from %q", p.functions)
			}
		})
	}
}

func TestProbeShellSkipsShellsWithoutRCFiles(t *testing.T) {
	if p := probeShell("/bin/sh", "sh"); p.probed {
		t.Fatalf("sh must not be probed, got %+v", p)
	}
}

func TestPromptBuiltByHookIsRenderedForEachExitStatus(t *testing.T) {
	for _, f := range rcFixtures {
		t.Run(f.shell, func(t *testing.T) {
			shellPath := useFixture(t, f)
			r := newPromptRenderer(shellPath, "", probeShell(shellPath, f.shell))

			for status, want := range map[int]string{0: "hook[0]> ", 3: "hook[3]> "} {
				if got := r.Render(context.Background(), status); got != want {
					t.Errorf("Render(status %d) = %q, want %q", status, got, want)
				}
			}
		})
	}
}

func TestExplicitPS1WinsOverPromptHooks(t *testing.T) {
	for _, f := range rcFixtures {
		t.Run(f.shell, func(t *testing.T) {
			shellPath := useFixture(t, f)
			r := newPromptRenderer(shellPath, "explicit> ", probeShell(shellPath, f.shell))

			if got, want := r.Render(context.Background(), 0), "explicit> "; got != want {
				t.Errorf("Render = %q, want %q", got, want)
			}
		})
	}
}

func TestPromptRendererChoosesHelperShell(t *testing.T) {
	const static = "%n@%m %1~ %# "
	tests := []struct {
		name    string
		profile shellProfile
		want    bool
	}{
		{"static prompt without hooks", shellProfile{probed: true, ps1: static, hookPS1: static}, false},
		{"static prompt with an unrelated hook", shellProfile{probed: true, ps1: static, hookPS1: static, hasHooks: true}, false},
		{"hook rewrites the prompt", shellProfile{probed: true, ps1: static, hookPS1: "branch main %# ", hasHooks: true}, true},
		{"prompt reads psvar, which a hook may set", shellProfile{probed: true, ps1: "%1v %# ", hookPS1: "%1v %# ", hasHooks: true}, true},
		{"prompt reads psvar but there is no hook", shellProfile{probed: true, ps1: "%1v %# ", hookPS1: "%1v %# "}, false},
		{"prompt substitutes a command", shellProfile{probed: true, ps1: "$(branch) %# ", hookPS1: "$(branch) %# "}, true},
	}
	unsetenv(t, "PS1")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPromptRenderer("/bin/zsh", "", tt.profile)
			if r.needsInteractive != tt.want {
				t.Errorf("needsInteractive = %v, want %v", r.needsInteractive, tt.want)
			}
		})
	}
}

// runWithDefinitions runs script the way a !command runs.
func runWithDefinitions(t *testing.T, shellPath string, d *definitions, script string) string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(shellPath, "-c", script)
	cmd.Env = append(os.Environ(), d.Env()...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s -c %q: %v\nstderr: %s", shellPath, script, err, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Errorf("%s -c %q wrote to stderr: %s", shellPath, script, stderr.String())
	}
	return string(out)
}

func TestDefinitionsMakeAliasesAndFunctionsAvailable(t *testing.T) {
	for _, f := range rcFixtures {
		t.Run(f.shell, func(t *testing.T) {
			shellPath := useFixture(t, f)
			d := newDefinitions(shellPath, f.shell, probeShell(shellPath, f.shell))
			if d == nil {
				t.Fatal("no definitions were produced")
			}
			defer d.Close()

			if got, want := runWithDefinitions(t, shellPath, d, "hi; greet you"), "hello\ngreetings you\n"; got != want {
				t.Errorf("output = %q, want %q", got, want)
			}

			// The hook is for this one shell only: nothing it starts may
			// see the variable that points at the generated files.
			const leak = `echo "${ZDOTDIR-unset} ${BASH_ENV-unset}"; ` +
				`sh -c 'echo "${ZDOTDIR-unset} ${BASH_ENV-unset}"'`
			if got, want := runWithDefinitions(t, shellPath, d, leak), "unset unset\nunset unset\n"; got != want {
				t.Errorf("environment seen by the command = %q, want %q", got, want)
			}
		})
	}
}

func TestDefinitionsKeepTheUsersOwnStartupFile(t *testing.T) {
	t.Run("zsh", func(t *testing.T) {
		f := rcFixtures[0]
		shellPath := useFixture(t, f)
		zdotdir := t.TempDir()
		for name, content := range map[string]string{".zshrc": f.rc, ".zshenv": "FROM_ZSHENV=yes\n"} {
			if err := os.WriteFile(filepath.Join(zdotdir, name), []byte(content), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
		}
		t.Setenv("ZDOTDIR", zdotdir)

		d := newDefinitions(shellPath, f.shell, probeShell(shellPath, f.shell))
		if d == nil {
			t.Fatal("no definitions were produced")
		}
		defer d.Close()

		got := runWithDefinitions(t, shellPath, d, `hi; echo "$FROM_ZSHENV $ZDOTDIR"`)
		if want := "hello\nyes " + zdotdir + "\n"; got != want {
			t.Errorf("output = %q, want %q", got, want)
		}
	})

	t.Run("bash", func(t *testing.T) {
		f := rcFixtures[1]
		shellPath := useFixture(t, f)
		bashEnv := filepath.Join(t.TempDir(), "bashenv")
		if err := os.WriteFile(bashEnv, []byte("FROM_BASH_ENV=yes\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Setenv("BASH_ENV", bashEnv)

		d := newDefinitions(shellPath, f.shell, probeShell(shellPath, f.shell))
		if d == nil {
			t.Fatal("no definitions were produced")
		}
		defer d.Close()

		got := runWithDefinitions(t, shellPath, d, `hi; echo "$FROM_BASH_ENV $BASH_ENV"`)
		if want := "hello\nyes " + bashEnv + "\n"; got != want {
			t.Errorf("output = %q, want %q", got, want)
		}
	})
}

func TestDefinitionsFallBackToAliasesWhenFunctionsDoNotLoad(t *testing.T) {
	for _, f := range rcFixtures {
		t.Run(f.shell, func(t *testing.T) {
			shellPath := useFixture(t, f)
			profile := probeShell(shellPath, f.shell)
			profile.functions = "broken () {\n" // what a shell might print but not read back

			d := newDefinitions(shellPath, f.shell, profile)
			if d == nil {
				t.Fatal("aliases should have survived the unreadable functions")
			}
			defer d.Close()

			if got, want := runWithDefinitions(t, shellPath, d, "hi"), "hello\n"; got != want {
				t.Errorf("output = %q, want %q", got, want)
			}
		})
	}
}

func TestDefinitionsAreOptional(t *testing.T) {
	if d := newDefinitions("/bin/sh", "sh", shellProfile{}); d != nil {
		t.Errorf("unprobed shell produced definitions: %+v", d)
	}
	if d := newDefinitions("/bin/zsh", "zsh", shellProfile{probed: true}); d != nil {
		t.Errorf("empty profile produced definitions: %+v", d)
	}
	// Only zsh and bash have a startup file to hook into.
	if d := newDefinitions("/bin/sh", "sh", shellProfile{probed: true, aliases: "alias hi='echo hello'"}); d != nil {
		t.Errorf("sh produced definitions: %+v", d)
	}
}

func TestDefinitionsCloseRemovesGeneratedFiles(t *testing.T) {
	f := rcFixtures[1]
	shellPath := useFixture(t, f)
	d := newDefinitions(shellPath, f.shell, probeShell(shellPath, f.shell))
	if d == nil {
		t.Fatal("no definitions were produced")
	}
	if _, err := os.Stat(d.dir); err != nil {
		t.Fatalf("generated directory missing: %v", err)
	}
	d.Close()
	if _, err := os.Stat(d.dir); !os.IsNotExist(err) {
		t.Fatalf("generated directory still present after Close (err = %v)", err)
	}
}

func TestDefinitionsSource(t *testing.T) {
	t.Run("zsh carries over language options, not session state", func(t *testing.T) {
		options := "extendedglob on\nmonitor on\nzle off\nnomatch off\nlocaloptions off\nnot an option on\n"
		got := definitionsSource("zsh", options, "greet () {\n\tprint hi\n}", "alias greet='greet -v'")
		want := "setopt extendedglob\nunsetopt nomatch\n" +
			"greet () {\n\tprint hi\n}\n" + // before the alias of the same name
			"alias greet='greet -v'\n"
		if got != want {
			t.Errorf("source =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("bash enables alias expansion and protects dash-named aliases", func(t *testing.T) {
		got := definitionsSource("bash", "shopt -s extglob", "greet () \n{ \n    echo hi\n}",
			"alias -='cd -'\nalias -- ..='cd ..'\nalias ll='ls -l'")
		want := "shopt -s extglob\n" +
			"greet () \n{ \n    echo hi\n}\n" +
			"shopt -s expand_aliases\n" +
			"alias -- -='cd -'\nalias -- ..='cd ..'\nalias ll='ls -l'\n"
		if got != want {
			t.Errorf("source =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("nothing to define", func(t *testing.T) {
		if got := definitionsSource("zsh", "", " \n", ""); got != "" {
			t.Errorf("source = %q, want empty", got)
		}
	})
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/plain/path":   `'/plain/path'`,
		"with space":    `'with space'`,
		"it's $HOME":    `'it'\''s $HOME'`,
		"":              `''`,
		"new\nline `x`": "'new\nline `x`'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestBangCommandsUseAliasesInInteractiveSessions(t *testing.T) {
	for _, f := range rcFixtures {
		t.Run(f.shell, func(t *testing.T) {
			shellPath := useFixture(t, f)
			hc, err := ParseHarnessCommand("echo")
			if err != nil {
				t.Fatalf("ParseHarnessCommand: %v", err)
			}
			var out, errOut bytes.Buffer
			sh, err := New(Config{
				Harness:     hc,
				In:          strings.NewReader(""),
				Out:         &out,
				Err:         &errOut,
				Interactive: true,
				SystemShell: shellPath,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer sh.Close()

			if res := sh.EvalLine(context.Background(), "!hi && greet you"); res.ExitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %q", res.ExitCode, errOut.String())
			}
			if got, want := out.String(), "hello\ngreetings you\n"; got != want {
				t.Errorf("output = %q, want %q", got, want)
			}
			if got, want := sh.Prompt(context.Background()), "hook[0]> "; got != want {
				t.Errorf("prompt = %q, want %q", got, want)
			}
		})
	}
}

func TestBangCommandStartingWithDashIsNotAShellOption(t *testing.T) {
	sh, _, errBuf := newTestShell(t, "echo", "")

	// "sh -c -x" would otherwise be parsed as options with no command.
	res := sh.EvalLine(context.Background(), "!-harnesh-no-such-command")
	if res.ExitCode != 127 {
		t.Fatalf("exit code = %d, want 127 (command not found); stderr = %q", res.ExitCode, errBuf.String())
	}
}
