package shell

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// fakeScreen is a minimal terminal emulator used to assert what the line
// editor actually leaves on screen. It implements the subset of behavior the
// editor relies on: deferred line wrap, CR/LF, cursor movement (CUU/CUF/CUP),
// and erase (ED/EL). Other CSI sequences (colors, modes) are ignored.
type fakeScreen struct {
	cols     int
	lines    [][]rune // lines[row][col]; 0 marks an empty cell
	row, col int      // col == cols means a wrap is pending
}

// wideTail marks the second cell occupied by a double-width character.
const wideTail rune = -1

func newFakeScreen(cols int) *fakeScreen {
	return &fakeScreen{cols: cols}
}

func (s *fakeScreen) Write(p []byte) (int, error) {
	data := p
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		data = data[size:]

		switch {
		case r == '\r':
			s.col = 0
		case r == '\n':
			s.row++
			if s.col >= s.cols {
				s.col = s.cols - 1
			}
		case r == 0x1b:
			data = s.escape(data)
		case r < 0x20:
			// Ignore other control characters.
		default:
			s.put(r)
		}
	}
	return len(p), nil
}

func (s *fakeScreen) put(r rune) {
	w := runeWidth(r)
	if w == 0 {
		return
	}
	if s.col+w > s.cols {
		s.row++
		s.col = 0
	}
	line := s.line(s.row)
	line[s.col] = r
	for i := 1; i < w; i++ {
		line[s.col+i] = wideTail
	}
	s.col += w
}

func (s *fakeScreen) line(row int) []rune {
	for len(s.lines) <= row {
		s.lines = append(s.lines, make([]rune, s.cols))
	}
	return s.lines[row]
}

// escape consumes the escape sequence following an ESC byte and returns the
// remaining data.
func (s *fakeScreen) escape(data []byte) []byte {
	if len(data) == 0 || data[0] != '[' {
		return data
	}
	end := 1
	for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
		end++
	}
	if end == len(data) {
		return nil
	}
	params, final := string(data[1:end]), data[end]
	n, _ := strconv.Atoi(params)

	switch final {
	case 'A': // Cursor up.
		s.row = max(s.row-max(n, 1), 0)
		s.col = min(s.col, s.cols-1)
	case 'C': // Cursor forward.
		s.col = min(s.col+max(n, 1), s.cols-1)
	case 'H': // Cursor home.
		s.row, s.col = 0, 0
	case 'K': // Erase to end of line.
		s.eraseLineFrom(s.row, s.col)
	case 'J':
		switch n {
		case 0: // Erase to end of screen.
			s.eraseLineFrom(s.row, s.col)
			for row := s.row + 1; row < len(s.lines); row++ {
				s.eraseLineFrom(row, 0)
			}
		case 2, 3: // Erase the whole screen / scrollback.
			s.lines = nil
		}
	}
	return data[end+1:]
}

func (s *fakeScreen) eraseLineFrom(row, col int) {
	if row >= len(s.lines) {
		return
	}
	for i := min(col, s.cols); i < s.cols; i++ {
		s.lines[row][i] = 0
	}
}

// text returns the screen contents with trailing blank cells and rows trimmed.
func (s *fakeScreen) text() []string {
	out := make([]string, len(s.lines))
	for i, line := range s.lines {
		var b strings.Builder
		for _, r := range line {
			switch r {
			case wideTail:
			case 0:
				b.WriteByte(' ')
			default:
				b.WriteRune(r)
			}
		}
		out[i] = strings.TrimRight(b.String(), " ")
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// cursor returns the visible cursor position.
func (s *fakeScreen) cursor() cell {
	return cell{s.row, min(s.col, s.cols-1)}
}
