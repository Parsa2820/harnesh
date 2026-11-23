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
	if got := conv.FormatPrompt("first question"); got != "first question" {
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

	second := conv.FormatPrompt("follow up")
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
	if got := conv.FormatPrompt("fresh start"); got != "fresh start" {
		t.Fatalf("after Clear() FormatPrompt = %q, want %q", got, "fresh start")
	}
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripANSI(tc.input); got != tc.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
