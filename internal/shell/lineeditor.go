package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInterrupted is returned by LineEditor.ReadLine when the user cancels the
// current input line with Ctrl-C.
var ErrInterrupted = errors.New("interrupted")

const defaultMaxHistory = 1000

// LineEditor provides interactive line editing and arrow-key command history
// navigation while keeping raw terminal mode scoped strictly to each ReadLine
// call.
type LineEditor struct {
	reader      *bufio.Reader
	out         io.Writer
	setRaw      func() (func(), error)
	history     []string
	maxHistory  int
	historyFile string
}

// NewLineEditor creates a LineEditor reading from in and writing terminal
// redraws to out. If in is an *os.File connected to a TTY, raw mode is enabled
// automatically during ReadLine and restored immediately when ReadLine returns.
// If historyFile is non-empty, existing history is loaded from it and new
// commands are persisted to it.
func NewLineEditor(in io.Reader, out io.Writer, historyFile string) *LineEditor {
	ed := &LineEditor{
		reader:      bufio.NewReader(in),
		out:         out,
		maxHistory:  defaultMaxHistory,
		historyFile: historyFile,
	}

	if f, ok := in.(*os.File); ok {
		fd := int(f.Fd())
		ed.setRaw = func() (func(), error) {
			return enableRawMode(fd)
		}
	}

	if historyFile != "" {
		ed.loadHistoryFile(historyFile)
	}
	return ed
}

// DefaultHistoryFile returns the default persistent history file path
// (~/.harnesh_history), or an empty string if the user's home directory cannot
// be determined.
func DefaultHistoryFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".harnesh_history")
}

// AddHistory records a non-empty command in history (suppressing consecutive
// duplicates) and appends it to the persistent history file if configured.
func (e *LineEditor) AddHistory(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if len(e.history) > 0 && e.history[len(e.history)-1] == trimmed {
		return
	}
	e.history = append(e.history, trimmed)
	if len(e.history) > e.maxHistory {
		e.history = e.history[len(e.history)-e.maxHistory:]
	}
	if e.historyFile != "" {
		e.appendHistoryFile(trimmed)
	}
}

// History returns a copy of the recorded command history (oldest first).
func (e *LineEditor) History() []string {
	if len(e.history) == 0 {
		return nil
	}
	out := make([]string, len(e.history))
	copy(out, e.history)
	return out
}

// ReadLine displays prompt and reads a single interactively edited line.
//
// Supported keys:
//   - Up / Down arrows (or Ctrl-P / Ctrl-N): navigate command history
//   - Left / Right arrows (or Ctrl-B / Ctrl-F): move cursor within the line
//   - Option/Ctrl + Left / Right (or Alt-B / Alt-F): jump by word
//   - Home / End (or Ctrl-A / Ctrl-E): jump to start / end of line
//   - Backspace / Delete / Ctrl-D: delete characters
//   - Ctrl-U / Ctrl-K / Ctrl-W / Option-Backspace: kill to start, end, or word
//   - Ctrl-L: clear screen and redraw the current prompt and line
//   - Ctrl-C: cancel the current line and return ErrInterrupted
//   - Ctrl-D on an empty line: return io.EOF
func (e *LineEditor) ReadLine(prompt string) (string, error) {
	if e.setRaw != nil {
		restore, err := e.setRaw()
		if err == nil {
			defer restore()
		}
	}

	promptPrefix, promptTail := splitPromptLines(prompt)
	if promptPrefix != "" {
		fmt.Fprint(e.out, strings.ReplaceAll(promptPrefix, "\n", "\r\n"))
	}

	var (
		buf     []rune
		pos     int
		histIdx = len(e.history)
		draft   []rune
	)

	redraw := func() {
		var sb strings.Builder
		sb.WriteByte('\r')
		sb.WriteString(promptTail)
		sb.WriteString(string(buf))
		sb.WriteString("\x1b[K")
		if back := len(buf) - pos; back > 0 {
			fmt.Fprintf(&sb, "\x1b[%dD", back)
		}
		fmt.Fprint(e.out, sb.String())
	}

	redraw()

	for {
		b, err := e.reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(buf) > 0 {
					fmt.Fprint(e.out, "\r\n")
					return string(buf), nil
				}
				return "", io.EOF
			}
			return "", err
		}

		switch b {
		case '\r', '\n':
			fmt.Fprint(e.out, "\r\n")
			return string(buf), nil

		case 0x03: // Ctrl-C
			fmt.Fprint(e.out, "^C\r\n")
			return "", ErrInterrupted

		case 0x04: // Ctrl-D
			if len(buf) == 0 {
				fmt.Fprint(e.out, "\r\n")
				return "", io.EOF
			}
			if pos < len(buf) {
				buf = append(buf[:pos], buf[pos+1:]...)
				redraw()
			}

		case 0x7f, 0x08: // Backspace / Ctrl-H
			if pos > 0 {
				buf = append(buf[:pos-1], buf[pos:]...)
				pos--
				redraw()
			}

		case 0x01: // Ctrl-A (Home)
			if pos != 0 {
				pos = 0
				redraw()
			}

		case 0x05: // Ctrl-E (End)
			if pos != len(buf) {
				pos = len(buf)
				redraw()
			}

		case 0x02: // Ctrl-B (Left)
			if pos > 0 {
				pos--
				redraw()
			}

		case 0x06: // Ctrl-F (Right)
			if pos < len(buf) {
				pos++
				redraw()
			}

		case 0x10: // Ctrl-P (Up)
			if histIdx > 0 {
				if histIdx == len(e.history) {
					draft = append([]rune(nil), buf...)
				}
				histIdx--
				buf = []rune(e.history[histIdx])
				pos = len(buf)
				redraw()
			}

		case 0x0e: // Ctrl-N (Down)
			if histIdx < len(e.history) {
				histIdx++
				if histIdx == len(e.history) {
					buf = append([]rune(nil), draft...)
				} else {
					buf = []rune(e.history[histIdx])
				}
				pos = len(buf)
				redraw()
			}

		case 0x15: // Ctrl-U (kill to start of line)
			if pos > 0 {
				buf = append([]rune(nil), buf[pos:]...)
				pos = 0
				redraw()
			}

		case 0x0b: // Ctrl-K (kill to end of line)
			if pos < len(buf) {
				buf = buf[:pos]
				redraw()
			}

		case 0x17: // Ctrl-W (delete previous word)
			if newPos := prevWordPos(buf, pos); newPos < pos {
				buf = append(buf[:newPos], buf[pos:]...)
				pos = newPos
				redraw()
			}

		case 0x0c: // Ctrl-L (clear screen and redraw prompt + line)
			fmt.Fprint(e.out, clearScreenSeq)
			if promptPrefix != "" {
				fmt.Fprint(e.out, strings.ReplaceAll(promptPrefix, "\n", "\r\n"))
			}
			redraw()

		case 0x1b: // ESC sequence (arrows, Home/End, Delete, Alt+word)
			action := e.readEscapeAction()
			switch action {
			case escUp:
				if histIdx > 0 {
					if histIdx == len(e.history) {
						draft = append([]rune(nil), buf...)
					}
					histIdx--
					buf = []rune(e.history[histIdx])
					pos = len(buf)
					redraw()
				}
			case escDown:
				if histIdx < len(e.history) {
					histIdx++
					if histIdx == len(e.history) {
						buf = append([]rune(nil), draft...)
					} else {
						buf = []rune(e.history[histIdx])
					}
					pos = len(buf)
					redraw()
				}
			case escLeft:
				if pos > 0 {
					pos--
					redraw()
				}
			case escRight:
				if pos < len(buf) {
					pos++
					redraw()
				}
			case escHome:
				if pos != 0 {
					pos = 0
					redraw()
				}
			case escEnd:
				if pos != len(buf) {
					pos = len(buf)
					redraw()
				}
			case escDelete:
				if pos < len(buf) {
					buf = append(buf[:pos], buf[pos+1:]...)
					redraw()
				}
			case escWordLeft:
				if newPos := prevWordPos(buf, pos); newPos != pos {
					pos = newPos
					redraw()
				}
			case escWordRight:
				if newPos := nextWordPos(buf, pos); newPos != pos {
					pos = newPos
					redraw()
				}
			case escDeleteWord:
				if newPos := prevWordPos(buf, pos); newPos < pos {
					buf = append(buf[:newPos], buf[pos:]...)
					pos = newPos
					redraw()
				}
			}

		default:
			if b < 0x20 {
				// Ignore other unhandled control characters.
				continue
			}
			r, err := e.finishUTF8Rune(b)
			if err != nil {
				continue
			}
			buf = insertRune(buf, pos, r)
			pos++
			redraw()
		}
	}
}

type escapeAction int

const (
	escNone escapeAction = iota
	escUp
	escDown
	escLeft
	escRight
	escHome
	escEnd
	escDelete
	escWordLeft
	escWordRight
	escDeleteWord
)

// readEscapeAction parses a terminal escape sequence immediately following an
// initial ESC (0x1b) byte.
func (e *LineEditor) readEscapeAction() escapeAction {
	b1, err := e.reader.ReadByte()
	if err != nil {
		return escNone
	}

	switch b1 {
	case '[':
		var params strings.Builder
		for {
			b, err := e.reader.ReadByte()
			if err != nil {
				return escNone
			}
			if b >= 0x40 && b <= 0x7e {
				return decodeCSI(params.String(), b)
			}
			if params.Len() < 16 {
				params.WriteByte(b)
			}
		}

	case 'O':
		b2, err := e.reader.ReadByte()
		if err != nil {
			return escNone
		}
		switch b2 {
		case 'A':
			return escUp
		case 'B':
			return escDown
		case 'C':
			return escRight
		case 'D':
			return escLeft
		case 'H':
			return escHome
		case 'F':
			return escEnd
		}

	case 'b', 'B':
		return escWordLeft
	case 'f', 'F':
		return escWordRight
	case 0x7f, 0x08:
		return escDeleteWord
	}

	return escNone
}

// decodeCSI maps a CSI parameter string and final byte to an escapeAction.
func decodeCSI(params string, final byte) escapeAction {
	switch final {
	case 'A':
		return escUp
	case 'B':
		return escDown
	case 'C':
		if params == "1;5" || params == "1;3" || params == "1;9" || params == "5" {
			return escWordRight
		}
		return escRight
	case 'D':
		if params == "1;5" || params == "1;3" || params == "1;9" || params == "5" {
			return escWordLeft
		}
		return escLeft
	case 'H':
		return escHome
	case 'F':
		return escEnd
	case '~':
		switch params {
		case "1", "7":
			return escHome
		case "3":
			return escDelete
		case "4", "8":
			return escEnd
		}
	}
	return escNone
}

// finishUTF8Rune completes reading a UTF-8 rune whose first byte is first.
func (e *LineEditor) finishUTF8Rune(first byte) (rune, error) {
	if first < utf8.RuneSelf {
		return rune(first), nil
	}
	var seq [utf8.UTFMax]byte
	seq[0] = first
	n := 1
	for !utf8.FullRune(seq[:n]) && n < utf8.UTFMax {
		next, err := e.reader.ReadByte()
		if err != nil {
			return utf8.RuneError, err
		}
		seq[n] = next
		n++
	}
	r, _ := utf8.DecodeRune(seq[:n])
	return r, nil
}

// loadHistoryFile reads previously saved commands from path into memory.
func (e *LineEditor) loadHistoryFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(e.history) > 0 && e.history[len(e.history)-1] == line {
			continue
		}
		e.history = append(e.history, line)
	}
	if len(e.history) > e.maxHistory {
		e.history = e.history[len(e.history)-e.maxHistory:]
	}
}

// appendHistoryFile appends a single command line to e.historyFile.
func (e *LineEditor) appendHistoryFile(line string) {
	sanitized := strings.ReplaceAll(line, "\n", " ")
	f, err := os.OpenFile(e.historyFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, sanitized)
}

// splitPromptLines separates a potentially multiline prompt into all leading
// lines (printed once) and the final line (reused for in-place redraws).
func splitPromptLines(prompt string) (prefix, tail string) {
	idx := strings.LastIndexByte(prompt, '\n')
	if idx < 0 {
		return "", prompt
	}
	return prompt[:idx+1], prompt[idx+1:]
}

func insertRune(buf []rune, pos int, r rune) []rune {
	buf = append(buf, 0)
	copy(buf[pos+1:], buf[pos:])
	buf[pos] = r
	return buf
}

func prevWordPos(buf []rune, pos int) int {
	i := pos
	for i > 0 && unicode.IsSpace(buf[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(buf[i-1]) {
		i--
	}
	return i
}

func nextWordPos(buf []rune, pos int) int {
	i := pos
	for i < len(buf) && unicode.IsSpace(buf[i]) {
		i++
	}
	for i < len(buf) && !unicode.IsSpace(buf[i]) {
		i++
	}
	return i
}
