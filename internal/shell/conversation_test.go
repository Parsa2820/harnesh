package shell

import (
	"strings"
	"testing"
)

func TestConversationLifecycle(t *testing.T) {
	var conv Conversation

	if conv.Len() != 0 {
		t.Fatalf("initial Len() = %d, want 0", conv.Len())
	}
	if got, _ := conv.FormatPrompt("first question", 0); got != "first question" {
		t.Fatalf("first turn FormatPrompt = %q, want unchanged prompt", got)
	}

	conv.Add("first question", "\x1b[32mfirst answer\x1b[0m\n")
	if conv.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", conv.Len())
	}

	turns := conv.Turns()
	if turns[0].Prompt != "first question" || turns[0].Response != "first answer" {
		t.Fatalf("unexpected stored turn: %#v", turns[0])
	}

	second, omitted := conv.FormatPrompt("follow up", 0)
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0", omitted)
	}
	for _, wantSubstr := range []string{
		"Conversation history:",
		"User: first question",
		"Assistant: first answer",
		"Current user message:\nfollow up",
	} {
		if !strings.Contains(second, wantSubstr) {
			t.Fatalf("FormatPrompt missing %q in:\n%s", wantSubstr, second)
		}
	}

	conv.Clear()
	if conv.Len() != 0 {
		t.Fatalf("after Clear() Len() = %d, want 0", conv.Len())
	}
	if got, _ := conv.FormatPrompt("fresh start", 0); got != "fresh start" {
		t.Fatalf("after Clear() FormatPrompt = %q, want %q", got, "fresh start")
	}
}

func TestFormatPromptDropsOldestTurnsToFitBudget(t *testing.T) {
	var conv Conversation
	for _, name := range []string{"one", "two", "three", "four"} {
		conv.Add("question "+name, strings.Repeat(name+" ", 50))
	}

	full, omitted := conv.FormatPrompt("latest", 0)
	if omitted != 0 {
		t.Fatalf("unlimited budget omitted %d turns", omitted)
	}

	t.Run("budget that fits everything changes nothing", func(t *testing.T) {
		got, omitted := conv.FormatPrompt("latest", len(full)+64)
		if omitted != 0 || got != full {
			t.Fatalf("omitted = %d, prompt changed = %v", omitted, got != full)
		}
	})

	t.Run("oldest turns are dropped first", func(t *testing.T) {
		budget := len(full) - 100
		got, omitted := conv.FormatPrompt("latest", budget)
		if omitted == 0 || omitted >= conv.Len() {
			t.Fatalf("omitted = %d, want some but not all of %d turns", omitted, conv.Len())
		}
		if len(got) > budget {
			t.Fatalf("prompt is %d bytes, over the %d byte budget", len(got), budget)
		}
		if strings.Contains(got, "question one") {
			t.Fatalf("oldest turn should have been dropped:\n%s", got)
		}
		for _, want := range []string{"earlier turns omitted", "question four", "Current user message:\nlatest"} {
			if !strings.Contains(got, want) {
				t.Fatalf("prompt missing %q:\n%s", want, got)
			}
		}
		if conv.Len() != 4 {
			t.Fatalf("FormatPrompt must not modify the conversation; Len() = %d", conv.Len())
		}
	})

	t.Run("the current prompt is never shortened", func(t *testing.T) {
		got, omitted := conv.FormatPrompt("latest", 1)
		if got != "latest" || omitted != conv.Len() {
			t.Fatalf("got %q with %d omitted, want the bare prompt with all %d omitted", got, omitted, conv.Len())
		}
	})
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "CSI color sequences",
			input: "\x1b[1;36mTitle\x1b[0m: \x1b[32mok\x1b[39m",
			want:  "Title: ok",
		},
		{
			name:  "OSC hyperlink with BEL terminator",
			input: "\x1b]8;;https://example.com\x07link\x1b]8;;\x07",
			want:  "link",
		},
		{
			name:  "OSC sequence with ST terminator",
			input: "\x1b]0;window title\x1b\\body",
			want:  "body",
		},
		{
			name:  "charset selection and other short escapes",
			input: "\x1b(Bplain\x1b=text\x1b",
			want:  "plaintext",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripANSI(tc.input); got != tc.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
