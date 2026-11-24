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

const (
	historyHeader        = "Conversation history:\n\n"
	historyHeaderTrimmed = "Conversation history (earlier turns omitted):\n\n"
	currentMessageHeader = "Current user message:\n"
)

// FormatPrompt builds the prompt string to pass to the CLI harness for
// currentPrompt. When no prior turns exist, currentPrompt is returned
// unchanged. Otherwise, prior turns are formatted ahead of currentPrompt so
// the agent has the conversation context.
//
// If maxBytes is positive and the full transcript would exceed it, the oldest
// turns are left out until the prompt fits; omitted reports how many. The
// current prompt itself is never shortened.
func (c *Conversation) FormatPrompt(currentPrompt string, maxBytes int) (prompt string, omitted int) {
	// Walk backwards from the newest turn to find how many fit the budget.
	first := 0
	if maxBytes > 0 {
		size := len(historyHeaderTrimmed) + len(currentMessageHeader) + len(currentPrompt)
		first = len(c.turns)
		for first > 0 {
			size += c.turns[first-1].formattedLen()
			if size > maxBytes {
				break
			}
			first--
		}
	}
	omitted = first
	turns := c.turns[first:]
	if len(turns) == 0 {
		return currentPrompt, omitted
	}

	var b strings.Builder
	if omitted > 0 {
		b.WriteString(historyHeaderTrimmed)
	} else {
		b.WriteString(historyHeader)
	}
	for _, t := range turns {
		b.WriteString("User: ")
		b.WriteString(t.Prompt)
		b.WriteString("\nAssistant: ")
		b.WriteString(t.Response)
		b.WriteString("\n\n")
	}
	b.WriteString(currentMessageHeader)
	b.WriteString(currentPrompt)
	return b.String(), omitted
}

// formattedLen is the number of bytes t occupies in a formatted prompt.
func (t Turn) formattedLen() int {
	return len("User: ") + len(t.Prompt) + len("\nAssistant: ") + len(t.Response) + len("\n\n")
}

// StripANSI removes ANSI terminal escape sequences (CSI, OSC, and shorter ESC
// sequences) from s.
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
			// Other escapes: optional intermediate bytes (0x20-0x2F) and a
			// final byte, e.g. "ESC c" or the charset selection "ESC ( B".
			i++
			for i < len(s)-1 && s[i] >= 0x20 && s[i] <= 0x2f {
				i++
			}
		}
	}

	return b.String()
}
