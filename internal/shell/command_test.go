package shell

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace only",
			input: "   \t  ",
			want:  nil,
		},
		{
			name:  "simple command with flag",
			input: "claude -p",
			want:  []string{"claude", "-p"},
		},
		{
			name:  "double quoted argument with spaces",
			input: `gemini --system "be concise and direct" -p`,
			want:  []string{"gemini", "--system", "be concise and direct", "-p"},
		},
		{
			name:  "single quoted argument with double quotes inside",
			input: `agent --msg 'say "hello"'`,
			want:  []string{"agent", "--msg", `say "hello"`},
		},
		{
			name:  "escaped spaces and quotes outside quotes",
			input: `my\ agent --flag foo\"bar`,
			want:  []string{"my agent", "--flag", `foo"bar`},
		},
		{
			name:  "escaped characters inside double quotes",
			input: `agent "a\"b\\c\$d\ne"`,
			want:  []string{"agent", `a"b\c$d\ne`},
		},
		{
			name:  "empty quoted argument preserved",
			input: `agent --prefix "" -p`,
			want:  []string{"agent", "--prefix", "", "-p"},
		},
		{
			name:    "unterminated single quote",
			input:   `agent 'unclosed`,
			wantErr: true,
		},
		{
			name:    "unterminated double quote",
			input:   `agent "unclosed`,
			wantErr: true,
		},
		{
			name:    "trailing backslash",
			input:   `agent -p\`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitArgs(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SplitArgs(%q) expected error, got nil (tokens=%v)", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitArgs(%q) unexpected error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SplitArgs(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseHarnessCommandAndBuildArgs(t *testing.T) {
	t.Run("appends prompt when no placeholder present", func(t *testing.T) {
		hc, err := ParseHarnessCommand("claude -p")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if hc.Binary != "claude" {
			t.Fatalf("Binary = %q, want %q", hc.Binary, "claude")
		}
		got := hc.BuildArgs("explain `go.mod` & $PATH")
		want := []string{"-p", "explain `go.mod` & $PATH"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildArgs = %#v, want %#v", got, want)
		}
	})

	t.Run("substitutes standalone placeholder", func(t *testing.T) {
		hc, err := ParseHarnessCommand("my-agent --prompt {} --format text")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := hc.BuildArgs("hello world")
		want := []string{"--prompt", "hello world", "--format", "text"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildArgs = %#v, want %#v", got, want)
		}
	})

	t.Run("substitutes embedded placeholder", func(t *testing.T) {
		hc, err := ParseHarnessCommand("my-agent --prompt={} --quiet")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := hc.BuildArgs("summarize this")
		want := []string{"--prompt=summarize this", "--quiet"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildArgs = %#v, want %#v", got, want)
		}
	})

	t.Run("a prompt starting with a dash cannot become a harness option", func(t *testing.T) {
		tests := []struct {
			harness string
			prompt  string
			want    []string
		}{
			{"claude -p", "-h what does this flag do?", []string{"-p", " -h what does this flag do?"}},
			{"claude -p", "--version", []string{"-p", " --version"}},
			{"agent --prompt {} --json", "-5 degrees in F?", []string{"--prompt", " -5 degrees in F?", "--json"}},
			{"agent {}suffix", "-x", []string{" -xsuffix"}},
			// Embedded in an option value: the dash is not at the start.
			{"agent --prompt={}", "-x", []string{"--prompt=-x"}},
			// Dashes elsewhere are untouched.
			{"claude -p", "rename foo-bar - quickly", []string{"-p", "rename foo-bar - quickly"}},
		}
		for _, tc := range tests {
			hc, err := ParseHarnessCommand(tc.harness)
			if err != nil {
				t.Fatalf("ParseHarnessCommand(%q): %v", tc.harness, err)
			}
			if got := hc.BuildArgs(tc.prompt); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%q with prompt %q: BuildArgs = %#v, want %#v", tc.harness, tc.prompt, got, tc.want)
			}
		}
	})

	t.Run("rejects empty command", func(t *testing.T) {
		if _, err := ParseHarnessCommand("   "); err == nil {
			t.Fatal("expected error for empty harness command")
		}
	})

	t.Run("rejects empty quoted binary", func(t *testing.T) {
		if _, err := ParseHarnessCommand(`"" -p`); err == nil {
			t.Fatal("expected error for empty binary name")
		}
	})
}
