package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Parsa2820/harnesh/internal/term"
)

// ErrInterrupted is returned by LineEditor.ReadLine when the user cancels the
// current input line with Ctrl-C.
var ErrInterrupted = errors.New("interrupted")

const (
	// Bracketed paste makes the terminal wrap pasted text in pasteStart/pasteEnd
	// markers, so a multi-line paste arrives as one edit instead of one
	// submitted line per newline.
	bracketedPasteOn  = "\x1b[?2004h"
	bracketedPasteOff = "\x1b[?2004l"
	pasteEnd          = "\x1b[201~"

	// minColumns is the narrowest terminal the wrap-aware renderer supports;
	// it must fit a double-width character.
	minColumns = 2
)

// LineEditor provides interactive line editing with command history. Raw
// terminal mode is scoped strictly to each ReadLine call, so child processes
// always see the terminal in its original state.
type LineEditor struct {
	in      *inputReader
	out     io.Writer
	makeRaw func() (restore func(), err error) // nil unless input is a file
	width   func() (cols int, ok bool)         // nil unless output is a file
	hist    *history
}

// NewLineEditor creates a LineEditor reading keystrokes from in and drawing to
// out. When in is a terminal, raw mode is enabled for the duration of each
// ReadLine call. If historyFile is non-empty, existing history is loaded from
// it and new entries are appended to it.
func NewLineEditor(in io.Reader, out io.Writer, historyFile string) *LineEditor {
	e := &LineEditor{
		in:   newInputReader(in),
		out:  out,
		hist: newHistory(historyFile, defaultMaxHistory),
	}
	if f, ok := in.(*os.File); ok {
		fd := int(f.Fd())
		e.makeRaw = func() (func(), error) { return term.MakeRaw(fd) }
	}
	if f, ok := out.(*os.File); ok {
		fd := int(f.Fd())
		e.width = func() (int, bool) { return term.Width(fd) }
	}
	return e
}

// AddHistory records a non-empty line in history (suppressing consecutive
// duplicates) and appends it to the persistent history file if configured.
func (e *LineEditor) AddHistory(line string) {
	e.hist.add(line)
}

// History returns a copy of the recorded history, oldest entry first.
func (e *LineEditor) History() []string {
	if len(e.hist.entries) == 0 {
		return nil
	}
	return append([]string(nil), e.hist.entries...)
}

// FreshLine makes sure the cursor is at the start of an empty row before a
// prompt is drawn. If the previous command's output did not end in a newline,
// an inverse-video "%" is left at its end and the prompt starts on the next
// row, instead of the prompt overwriting that output.
//
// It uses the same technique as zsh's PROMPT_SP: print a marker followed by
// enough spaces to wrap exactly when the cursor was not in the first column.
// It is a no-op when the output is not a terminal.
func (e *LineEditor) FreshLine() {
	if e.width == nil {
		return
	}
	cols, ok := e.width()
	if !ok || cols < minColumns {
		return
	}
	e.write("\x1b[7m%\x1b[27m" + strings.Repeat(" ", cols-1) + "\r\x1b[K")
}

// ReadLine displays prompt and reads one interactively edited line. The line
// may contain newlines when text is pasted or Alt-Enter is used.
//
// Supported keys:
//   - Up / Down (or Ctrl-P / Ctrl-N): navigate history, preserving the draft
//   - Left / Right (or Ctrl-B / Ctrl-F): move the cursor
//   - Option/Ctrl + Left / Right (or Alt-B / Alt-F): move by word
//   - Home / End (or Ctrl-A / Ctrl-E): jump to the start / end
//   - Backspace / Delete / Ctrl-D: delete characters
//   - Ctrl-U / Ctrl-K / Ctrl-W / Option-Backspace: kill to start, end, or word
//   - Alt-Enter: insert a newline
//   - Ctrl-L: clear the screen and redraw
//   - Enter: submit the line
//
// It returns ErrInterrupted on Ctrl-C, io.EOF on Ctrl-D with an empty line or
// when the input ends, and ctx.Err() if ctx is cancelled while waiting.
func (e *LineEditor) ReadLine(ctx context.Context, prompt string) (string, error) {
	if e.makeRaw != nil {
		if restore, err := e.makeRaw(); err == nil {
			e.write(bracketedPasteOn)
			defer func() {
				e.write(bracketedPasteOff)
				restore()
			}()
		}
	}

	// Only the final prompt line is redrawn in place; earlier lines of a
	// multi-line prompt are printed once.
	header, promptLine := splitPromptLines(prompt)
	header = strings.ReplaceAll(header, "\n", "\r\n")

	st := &lineState{
		prompt:      promptLine,
		promptPlain: StripANSI(promptLine),
		histIdx:     len(e.hist.entries),
	}
	e.write(header)
	e.refresh(st)

	for {
		ev, err := e.readKey(ctx)
		if err != nil {
			e.endLine(st, "")
			if errors.Is(err, io.EOF) && len(st.buf) > 0 {
				return string(st.buf), nil
			}
			return "", err
		}

		switch ev.code {
		case keyNone:
			continue
		case keyEnter:
			e.endLine(st, "")
			return string(st.buf), nil
		case keyInterrupt:
			e.endLine(st, "^C")
			return "", ErrInterrupted
		case keyEOF:
			if len(st.buf) == 0 {
				e.endLine(st, "")
				return "", io.EOF
			}
			st.deleteForward()
		case keyClearScreen:
			e.write(clearScreenSeq + header)
			st.cursorRow = 0
		case keyRune:
			st.insert([]rune{ev.r})
		case keyPaste:
			st.insert([]rune(ev.text))
		case keyNewline:
			st.insert([]rune{'\n'})
		case keyBackspace:
			st.backspace()
		case keyDelete:
			st.deleteForward()
		case keyLeft:
			st.pos = max(st.pos-1, 0)
		case keyRight:
			st.pos = min(st.pos+1, len(st.buf))
		case keyHome:
			st.pos = 0
		case keyEnd:
			st.pos = len(st.buf)
		case keyWordLeft:
			st.pos = prevWordPos(st.buf, st.pos)
		case keyWordRight:
			st.pos = nextWordPos(st.buf, st.pos)
		case keyKillToStart:
			st.deleteRange(0, st.pos)
		case keyKillToEnd:
			st.deleteRange(st.pos, len(st.buf))
		case keyKillWord:
			st.deleteRange(prevWordPos(st.buf, st.pos), st.pos)
		case keyUp:
			st.historyPrev(e.hist.entries)
		case keyDown:
			st.historyNext(e.hist.entries)
		}
		e.refresh(st)
	}
}

// lineState is the state of the line being edited by one ReadLine call.
type lineState struct {
	prompt      string // final prompt line, possibly containing ANSI sequences
	promptPlain string // prompt with ANSI sequences removed, for width math
	buf         []rune
	pos         int // cursor index into buf

	histIdx int    // index into history; len(history) means the draft
	draft   []rune // in-progress line saved while browsing history

	cursorRow  int  // screen row of the cursor relative to the prompt line
	wrappedEnd bool // the last refresh left the cursor on a fresh row
}

func (st *lineState) insert(rs []rune) {
	buf := make([]rune, 0, len(st.buf)+len(rs))
	buf = append(buf, st.buf[:st.pos]...)
	buf = append(buf, rs...)
	buf = append(buf, st.buf[st.pos:]...)
	st.buf = buf
	st.pos += len(rs)
}

// deleteRange removes buf[from:to] and leaves the cursor at from.
func (st *lineState) deleteRange(from, to int) {
	st.buf = append(st.buf[:from], st.buf[to:]...)
	st.pos = from
}

func (st *lineState) backspace() {
	if st.pos > 0 {
		st.deleteRange(st.pos-1, st.pos)
	}
}

func (st *lineState) deleteForward() {
	if st.pos < len(st.buf) {
		st.deleteRange(st.pos, st.pos+1)
	}
}

func (st *lineState) historyPrev(entries []string) {
	if st.histIdx == 0 {
		return
	}
	if st.histIdx == len(entries) {
		st.draft = append([]rune(nil), st.buf...)
	}
	st.histIdx--
	st.setLine([]rune(entries[st.histIdx]))
}

func (st *lineState) historyNext(entries []string) {
	if st.histIdx >= len(entries) {
		return
	}
	st.histIdx++
	if st.histIdx == len(entries) {
		st.setLine(append([]rune(nil), st.draft...))
	} else {
		st.setLine([]rune(entries[st.histIdx]))
	}
}

func (st *lineState) setLine(buf []rune) {
	st.buf = buf
	st.pos = len(buf)
}

// refresh redraws the prompt line and edit buffer and places the terminal
// cursor at st.pos. It accounts for lines that wrap across several terminal
// rows, embedded newlines, and double-width characters.
func (e *LineEditor) refresh(st *lineState) {
	cols := e.columns()

	var sb strings.Builder
	// Return to the first row of the previous rendering and erase it.
	if st.cursorRow > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", st.cursorRow)
	}
	sb.WriteString("\r\x1b[J")

	sb.WriteString(st.prompt)
	l := layout{cols: cols}
	for _, r := range st.promptPlain {
		l.advance(runeWidth(r))
	}

	cursor := l.cellFor(1)
	for i, r := range st.buf {
		if i == st.pos {
			cursor = l.cellFor(displayWidthOf(r))
		}
		writeRune(&sb, &l, r)
	}
	if st.pos >= len(st.buf) {
		cursor = l.cellFor(1)
	}

	// A row filled to its last column leaves the terminal cursor parked on
	// that column; wrap explicitly so it moves to the row the layout expects.
	endRow := l.row
	st.wrappedEnd = l.col >= cols
	if st.wrappedEnd {
		sb.WriteString("\r\n")
		endRow++
	}

	if up := endRow - cursor.row; up > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", up)
	}
	sb.WriteByte('\r')
	if cursor.col > 0 {
		fmt.Fprintf(&sb, "\x1b[%dC", cursor.col)
	}
	st.cursorRow = cursor.row

	e.write(sb.String())
}

// endLine finishes editing: it moves the cursor past the end of the text,
// prints suffix (if any), and starts a new row.
func (e *LineEditor) endLine(st *lineState, suffix string) {
	st.pos = len(st.buf)
	e.refresh(st)
	if suffix != "" || !st.wrappedEnd {
		e.write(suffix + "\r\n")
	}
}

func (e *LineEditor) write(s string) {
	if s != "" {
		_, _ = io.WriteString(e.out, s)
	}
}

func (e *LineEditor) columns() int {
	if e.width != nil {
		if cols, ok := e.width(); ok && cols >= minColumns {
			return cols
		}
	}
	return fallbackColumns
}

type keyCode int

const (
	keyNone keyCode = iota
	keyRune
	keyPaste
	keyEnter
	keyNewline
	keyInterrupt
	keyEOF
	keyClearScreen
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyUp
	keyDown
	keyHome
	keyEnd
	keyWordLeft
	keyWordRight
	keyKillToStart
	keyKillToEnd
	keyKillWord
)

// keyEvent is one decoded keystroke (or paste) from the terminal.
type keyEvent struct {
	code keyCode
	r    rune   // keyRune: the typed character
	text string // keyPaste: the sanitized pasted text
}

// controlKeys maps single control bytes to editing actions.
var controlKeys = map[byte]keyCode{
	'\r': keyEnter,
	'\n': keyEnter,
	0x01: keyHome,        // Ctrl-A
	0x02: keyLeft,        // Ctrl-B
	0x03: keyInterrupt,   // Ctrl-C
	0x04: keyEOF,         // Ctrl-D
	0x05: keyEnd,         // Ctrl-E
	0x06: keyRight,       // Ctrl-F
	0x08: keyBackspace,   // Ctrl-H
	0x0b: keyKillToEnd,   // Ctrl-K
	0x0c: keyClearScreen, // Ctrl-L
	0x0e: keyDown,        // Ctrl-N
	0x10: keyUp,          // Ctrl-P
	0x15: keyKillToStart, // Ctrl-U
	0x17: keyKillWord,    // Ctrl-W
	0x7f: keyBackspace,   // Backspace
}

// readKey reads and decodes the next keystroke.
func (e *LineEditor) readKey(ctx context.Context) (keyEvent, error) {
	b, err := e.in.readByte(ctx)
	if err != nil {
		return keyEvent{}, err
	}
	if b == 0x1b {
		return e.readEscape(ctx)
	}
	if code, ok := controlKeys[b]; ok {
		return keyEvent{code: code}, nil
	}
	if b < 0x20 {
		return keyEvent{}, nil // Unhandled control character.
	}
	r, err := e.readRune(ctx, b)
	if err != nil {
		return keyEvent{}, err
	}
	if r == utf8.RuneError {
		return keyEvent{}, nil
	}
	return keyEvent{code: keyRune, r: r}, nil
}

// readEscape decodes the escape sequence following an ESC byte.
func (e *LineEditor) readEscape(ctx context.Context) (keyEvent, error) {
	b, err := e.in.readByte(ctx)
	if err != nil {
		return keyEvent{}, err
	}

	switch b {
	case '[': // CSI: parameter bytes followed by a final byte in 0x40-0x7E.
		var params []byte
		for {
			b, err := e.in.readByte(ctx)
			if err != nil {
				return keyEvent{}, err
			}
			if b < 0x40 || b > 0x7e {
				if len(params) < 16 {
					params = append(params, b)
				}
				continue
			}
			code := decodeCSI(string(params), b)
			if code != keyPaste {
				return keyEvent{code: code}, nil
			}
			text, err := e.readPaste(ctx)
			if err != nil {
				return keyEvent{}, err
			}
			return keyEvent{code: keyPaste, text: text}, nil
		}

	case 'O': // SS3: arrow and Home/End keys in application cursor mode.
		b, err := e.in.readByte(ctx)
		if err != nil {
			return keyEvent{}, err
		}
		return keyEvent{code: decodeCSI("", b)}, nil

	case 'b', 'B':
		return keyEvent{code: keyWordLeft}, nil
	case 'f', 'F':
		return keyEvent{code: keyWordRight}, nil
	case 0x7f, 0x08:
		return keyEvent{code: keyKillWord}, nil
	case '\r', '\n':
		return keyEvent{code: keyNewline}, nil
	}
	return keyEvent{}, nil
}

// decodeCSI maps a CSI (or SS3) parameter string and final byte to a key.
func decodeCSI(params string, final byte) keyCode {
	// Ctrl (5), Alt (3), and iTerm2 Option (9) modifiers move by word.
	wordModifier := params == "1;5" || params == "1;3" || params == "1;9" || params == "5"

	switch final {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		if wordModifier {
			return keyWordRight
		}
		return keyRight
	case 'D':
		if wordModifier {
			return keyWordLeft
		}
		return keyLeft
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	case '~':
		switch params {
		case "1", "7":
			return keyHome
		case "3":
			return keyDelete
		case "4", "8":
			return keyEnd
		case "200":
			return keyPaste
		}
	}
	return keyNone
}

// readPaste reads bracketed-paste content up to the closing marker.
func (e *LineEditor) readPaste(ctx context.Context) (string, error) {
	var data []byte
	for {
		b, err := e.in.readByte(ctx)
		if err != nil {
			return "", err
		}
		data = append(data, b)
		if b == '~' && bytes.HasSuffix(data, []byte(pasteEnd)) {
			return sanitizePaste(string(data[:len(data)-len(pasteEnd)])), nil
		}
	}
}

// sanitizePaste normalizes line endings to "\n" and drops control characters
// other than newline and tab. Trailing newlines are removed so that pasting a
// copied line does not leave the cursor on an empty row.
func sanitizePaste(s string) string {
	s = strings.ToValidUTF8(s, string(unicode.ReplacementChar))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimRight(s, "\n")
}

// readRune completes the UTF-8 sequence that starts with the byte first.
func (e *LineEditor) readRune(ctx context.Context, first byte) (rune, error) {
	if first < utf8.RuneSelf {
		return rune(first), nil
	}
	seq := []byte{first}
	for !utf8.FullRune(seq) && len(seq) < utf8.UTFMax {
		b, err := e.in.readByte(ctx)
		if err != nil {
			return utf8.RuneError, err
		}
		seq = append(seq, b)
	}
	r, _ := utf8.DecodeRune(seq)
	return r, nil
}

// splitPromptLines separates a possibly multi-line prompt into its leading
// lines (printed once) and its final line (redrawn in place while editing).
func splitPromptLines(prompt string) (header, last string) {
	idx := strings.LastIndexByte(prompt, '\n')
	if idx < 0 {
		return "", prompt
	}
	return prompt[:idx+1], prompt[idx+1:]
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
