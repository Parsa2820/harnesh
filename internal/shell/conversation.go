package shell

import "strings"

// Turn represents a single user-prompt / assistant-response exchange in a
// retained harnesh conversation.
type Turn struct {
	// Prompt is the user's input text sent to the agent.
	Prompt string
	// Response is the cleaned assistant response captured from the agent's
	// standard output.
	Response string
}

// Conversation retains the ordered sequence of Turn exchanges within a shell
// session so stateless single-prompt CLI harnesses receive prior context on
// follow-up turns.
type Conversation struct {
	turns []Turn
}

// Add records a completed prompt/response turn. Any ANSI escape sequences in
// response are stripped and surrounding whitespace is trimmed so subsequent
// prompts remain clean plain text.
func (c *Conversation) Add(prompt, response string) {
	c.turns = append(c.turns, Turn{
		Prompt:   strings.TrimSpace(prompt),
		Response: strings.TrimSpace(StripANSI(response)),
	})
}

// Clear removes all retained conversation turns.
func (c *Conversation) Clear() {
	c.turns = nil
}

// Len returns the number of retained turns in the conversation.
func (c *Conversation) Len() int {
	return len(c.turns)
}

// Turns returns a copy of all retained turns in chronological order.
func (c *Conversation) Turns() []Turn {
	if len(c.turns) == 0 {
		return nil
	}
	out := make([]Turn, len(c.turns))
	copy(out, c.turns)
	return out
}

// FormatPrompt builds the prompt string to pass to the CLI harness for
// currentPrompt. When no prior turns exist, currentPrompt is returned
// unchanged. Otherwise, prior turns are formatted ahead of currentPrompt so
// the agent has full conversation context.
func (c *Conversation) FormatPrompt(currentPrompt string) string {
	if len(c.turns) == 0 {
		return currentPrompt
	}

	var b strings.Builder
	b.WriteString("Conversation history:\n\n")
	for _, t := range c.turns {
		b.WriteString("User: ")
		b.WriteString(t.Prompt)
		b.WriteByte('\n')
		b.WriteString("Assistant: ")
		b.WriteString(t.Response)
		b.WriteString("\n\n")
	}
	b.WriteString("Current user message:\n")
	b.WriteString(currentPrompt)
	return b.String()
}

// StripANSI removes common ANSI terminal escape sequences (CSI, OSC, and
// two-byte ESC sequences) from s.
func StripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		next := s[i+1]
		switch next {
		case '[':
			// CSI sequence: ESC [ <parameter/intermediate bytes> <final byte 0x40-0x7E>
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
		case ']':
			// OSC sequence: ESC ] ... (BEL or ST "ESC \")
			i += 2
			for i < len(s) {
				if s[i] == 0x07 {
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i++
					break
				}
				i++
			}
		default:
			// Two-byte ESC sequence.
			i++
		}
	}

	return b.String()
}
