package shell

import (
	"sort"
	"strings"
	"unicode"
)

const (
	// fallbackColumns is the terminal width assumed when it cannot be queried.
	fallbackColumns = 80
	// tabWidth is how many columns a pasted tab occupies in the edit buffer.
	tabWidth = 4
)

// wideRanges lists code point ranges rendered two columns wide by terminals:
// East Asian Wide/Fullwidth characters and emoji with default emoji
// presentation. It is a deliberately compact approximation of Unicode's
// EastAsianWidth data; ranges are sorted and non-overlapping.
var wideRanges = [][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo initial consonants
	{0x231A, 0x231B},   // watch, hourglass
	{0x23E9, 0x23EC},   // media control emoji
	{0x23F0, 0x23F0},   // alarm clock
	{0x23F3, 0x23F3},   // hourglass with flowing sand
	{0x25FD, 0x25FE},   // medium small squares
	{0x2614, 0x2615},   // umbrella with rain, hot beverage
	{0x2648, 0x2653},   // zodiac signs
	{0x267F, 0x267F},   // wheelchair
	{0x2693, 0x2693},   // anchor
	{0x26A1, 0x26A1},   // high voltage
	{0x26AA, 0x26AB},   // circles
	{0x26BD, 0x26BE},   // soccer ball, baseball
	{0x26C4, 0x26C5},   // snowman, sun behind cloud
	{0x26CE, 0x26CE},   // Ophiuchus
	{0x26D4, 0x26D4},   // no entry
	{0x26EA, 0x26EA},   // church
	{0x26F2, 0x26F3},   // fountain, golf flag
	{0x26F5, 0x26F5},   // sailboat
	{0x26FA, 0x26FA},   // tent
	{0x26FD, 0x26FD},   // fuel pump
	{0x2705, 0x2705},   // check mark button
	{0x270A, 0x270B},   // raised fist, raised hand
	{0x2728, 0x2728},   // sparkles
	{0x274C, 0x274C},   // cross mark
	{0x274E, 0x274E},   // cross mark button
	{0x2753, 0x2755},   // question and exclamation marks
	{0x2757, 0x2757},   // exclamation mark
	{0x2795, 0x2797},   // heavy plus, minus, division
	{0x27B0, 0x27B0},   // curly loop
	{0x27BF, 0x27BF},   // double curly loop
	{0x2B1B, 0x2B1C},   // large squares
	{0x2B50, 0x2B50},   // star
	{0x2B55, 0x2B55},   // hollow red circle
	{0x2E80, 0x303E},   // CJK radicals, Kangxi, CJK symbols and punctuation
	{0x3041, 0x33FF},   // Hiragana, Katakana, Bopomofo, CJK compatibility
	{0x3400, 0x4DBF},   // CJK Unified Ideographs Extension A
	{0x4E00, 0x9FFF},   // CJK Unified Ideographs
	{0xA000, 0xA4CF},   // Yi syllables and radicals
	{0xA960, 0xA97F},   // Hangul Jamo Extended-A
	{0xAC00, 0xD7A3},   // Hangul syllables
	{0xF900, 0xFAFF},   // CJK compatibility ideographs
	{0xFE10, 0xFE19},   // vertical forms
	{0xFE30, 0xFE6F},   // CJK compatibility forms, small form variants
	{0xFF00, 0xFF60},   // fullwidth forms
	{0xFFE0, 0xFFE6},   // fullwidth signs
	{0x1F004, 0x1F004}, // mahjong red dragon
	{0x1F0CF, 0x1F0CF}, // joker
	{0x1F18E, 0x1F18E}, // AB button
	{0x1F191, 0x1F19A}, // squared words
	{0x1F200, 0x1F251}, // enclosed ideographic supplement
	{0x1F300, 0x1F64F}, // pictographs, emoticons
	{0x1F680, 0x1F6FF}, // transport and map symbols
	{0x1F7E0, 0x1F7EB}, // colored circles and squares
	{0x1F90C, 0x1F9FF}, // supplemental symbols and pictographs
	{0x1FA70, 0x1FAFF}, // symbols and pictographs extended-A
	{0x20000, 0x3FFFD}, // CJK Unified Ideographs extensions (planes 2-3)
}

// runeWidth returns the number of terminal columns r occupies: 0 for control
// and combining characters, 2 for wide characters, and 1 otherwise.
func runeWidth(r rune) int {
	switch {
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r < 0x300:
		return 1 // Fast path: Latin text has no combining or wide characters.
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	}
	i := sort.Search(len(wideRanges), func(i int) bool { return wideRanges[i][1] >= r })
	if i < len(wideRanges) && wideRanges[i][0] <= r {
		return 2
	}
	return 1
}

// cell is a zero-based screen position relative to where a line starts.
type cell struct{ row, col int }

// layout tracks where text lands on a terminal cols columns wide, modeling the
// deferred ("pending") wrap real terminals perform: after the last column is
// written the cursor stays there (col == cols) until the next character wraps.
type layout struct {
	cols     int
	row, col int
}

// advance accounts for writing one character of width w.
func (l *layout) advance(w int) {
	if w == 0 {
		return
	}
	if l.col+w > l.cols {
		l.row++
		l.col = 0
	}
	l.col += w
}

// newline accounts for writing "\r\n".
func (l *layout) newline() {
	l.row++
	l.col = 0
}

// cellFor returns the cell in which a following character of width w would
// start; that is where the cursor should be shown when it sits before that
// character. Pass w=1 for the end of the text and w=0 for a character that
// does not wrap (a newline or combining mark).
func (l *layout) cellFor(w int) cell {
	if w > 0 && l.col+w > l.cols {
		return cell{l.row + 1, 0}
	}
	return cell{l.row, min(l.col, l.cols-1)}
}

// writeRune appends the on-screen representation of an edit-buffer rune to sb
// and advances l accordingly. Newlines start a new row, tabs become spaces,
// and stray control characters are shown as U+FFFD so they cannot corrupt the
// display.
func writeRune(sb *strings.Builder, l *layout, r rune) {
	switch {
	case r == '\n':
		sb.WriteString("\r\n")
		l.newline()
	case r == '\t':
		for i := 0; i < tabWidth; i++ {
			sb.WriteByte(' ')
			l.advance(1)
		}
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		sb.WriteRune(unicode.ReplacementChar)
		l.advance(1)
	default:
		sb.WriteRune(r)
		l.advance(runeWidth(r))
	}
}

// displayWidthOf returns the column width writeRune will use for r.
func displayWidthOf(r rune) int {
	switch {
	case r == '\n':
		return 0
	case r == '\t':
		return 1 // Tabs are written one space at a time.
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 1
	default:
		return runeWidth(r)
	}
}
