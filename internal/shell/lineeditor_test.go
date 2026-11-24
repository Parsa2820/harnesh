package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLineEditorArrowHistoryAndDraftPreservation(t *testing.T) {
	// Type "draft", press Up twice to reach "first cmd", press Down once to
	// reach "second cmd", press Down again to restore "draft", press Up to
	// select "second cmd", edit it with "!", and hit Enter.
	seq := "draft" +
		"\x1b[A" + // Up -> "second cmd"
		"\x1b[A" + // Up -> "first cmd"
		"\x1b[A" + // Up at top -> stays "first cmd"
		"\x1b[B" + // Down -> "second cmd"
		"\x1b[B" + // Down -> restores "draft"
		"\x1b[A" + // Up -> "second cmd"
		"!" + // append "!" -> "second cmd!"
		"\r"

	var out bytes.Buffer
	ed := NewLineEditor(strings.NewReader(seq), &out, "")
	ed.AddHistory("first cmd")
	ed.AddHistory("second cmd")

	line, err := ed.ReadLine(context.Background(), "\x1b[36mprompt> \x1b[0m")
	if err != nil {
		t.Fatalf("ReadLine unexpected error: %v", err)
	}
	if want := "second cmd!"; line != want {
		t.Fatalf("ReadLine = %q, want %q", line, want)
	}
}

func TestLineEditorHorizontalEditingAndShortcuts(t *testing.T) {
	// Type "helo wrld", move left 3 chars, insert 'o', move to start (Home),
	// move right 3 chars, insert 'l', move to end (Ctrl-E), append "!", Enter.
	seq := "helo wrld" +
		"\x1b[D\x1b[D\x1b[D" + // before 'r'
		"o" + // "helo world"
		"\x1b[H" + // Home
		"\x1b[C\x1b[C\x1b[C" + // after "hel"
		"l" + // "hello world"
		"\x05" + // Ctrl-E (End)
		"!" +
		"\r"

	var out bytes.Buffer
	ed := NewLineEditor(strings.NewReader(seq), &out, "")

	line, err := ed.ReadLine(context.Background(), "> ")
	if err != nil {
		t.Fatalf("ReadLine unexpected error: %v", err)
	}
	if want := "hello world!"; line != want {
		t.Fatalf("ReadLine = %q, want %q", line, want)
	}
}

func TestLineEditorBackspaceDeleteAndKillKeys(t *testing.T) {
	t.Run("backspace and delete key", func(t *testing.T) {
		seq := "abXd\x1b[D\x7f\x1b[D\x1b[3~c\r"
		// "abXd" -> Left (before 'd') -> Backspace removes 'X' ("abd", pos=2)
		// -> Left (before 'b', pos=1) -> Delete removes 'b' ("ad", pos=1)
		// -> insert 'c' ("acd")
		ed := NewLineEditor(strings.NewReader(seq), &bytes.Buffer{}, "")
		got, err := ed.ReadLine(context.Background(), "> ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "acd" {
			t.Fatalf("got %q, want %q", got, "acd")
		}
	})

	t.Run("Ctrl-W deletes previous word and Ctrl-K kills to end", func(t *testing.T) {
		seq := "one two three\x17\x01\x1bf\x0b\r"
		// "one two three" -> Ctrl-W ("one two ") -> Ctrl-A (pos=0) -> Alt-F (after "one") -> Ctrl-K ("one")
		ed := NewLineEditor(strings.NewReader(seq), &bytes.Buffer{}, "")
		got, err := ed.ReadLine(context.Background(), "> ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "one" {
			t.Fatalf("got %q, want %q", got, "one")
		}
	})

	t.Run("Ctrl-U kills to start of line", func(t *testing.T) {
		seq := "discard keep\x1b[D\x1b[D\x1b[D\x1b[D\x15\r"
		ed := NewLineEditor(strings.NewReader(seq), &bytes.Buffer{}, "")
		got, err := ed.ReadLine(context.Background(), "> ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "keep" {
			t.Fatalf("got %q, want %q", got, "keep")
		}
	})

	t.Run("Unicode rune insertion and backspace", func(t *testing.T) {
		seq := "café☕\x7f!\r"
		ed := NewLineEditor(strings.NewReader(seq), &bytes.Buffer{}, "")
		got, err := ed.ReadLine(context.Background(), "> ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "café!" {
			t.Fatalf("got %q, want %q", got, "café!")
		}
	})
}

func TestLineEditorCtrlCAndCtrlD(t *testing.T) {
	t.Run("Ctrl-C returns ErrInterrupted", func(t *testing.T) {
		ed := NewLineEditor(strings.NewReader("partial\x03"), &bytes.Buffer{}, "")
		_, err := ed.ReadLine(context.Background(), "> ")
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("err = %v, want ErrInterrupted", err)
		}
	})

	t.Run("Ctrl-D on empty line returns io.EOF", func(t *testing.T) {
		ed := NewLineEditor(strings.NewReader("\x04"), &bytes.Buffer{}, "")
		_, err := ed.ReadLine(context.Background(), "> ")
		if !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want io.EOF", err)
		}
	})
}

func TestLineEditorEOFSubmitsPendingText(t *testing.T) {
	ed := NewLineEditor(strings.NewReader("unterminated"), &bytes.Buffer{}, "")
	line, err := ed.ReadLine(context.Background(), "> ")
	if err != nil || line != "unterminated" {
		t.Fatalf("ReadLine = %q, %v; want %q, nil", line, err, "unterminated")
	}
	if _, err := ed.ReadLine(context.Background(), "> "); !errors.Is(err, io.EOF) {
		t.Fatalf("second ReadLine err = %v, want io.EOF", err)
	}
}

func TestLineEditorBracketedPasteIsOneLine(t *testing.T) {
	// A multi-line paste must arrive as a single edit rather than submitting
	// each line, and typing continues after it.
	seq := "fix: " +
		"\x1b[200~line one\r\nline two\rline\tthree\x07\n\n\x1b[201~" +
		" ok" +
		"\r"
	ed := NewLineEditor(strings.NewReader(seq), &bytes.Buffer{}, "")

	line, err := ed.ReadLine(context.Background(), "> ")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if want := "fix: line one\nline two\nline\tthree ok"; line != want {
		t.Fatalf("ReadLine = %q, want %q", line, want)
	}
}

func TestLineEditorAltEnterInsertsNewline(t *testing.T) {
	ed := NewLineEditor(strings.NewReader("a\x1b\rb\r"), &bytes.Buffer{}, "")
	line, err := ed.ReadLine(context.Background(), "> ")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if want := "a\nb"; line != want {
		t.Fatalf("ReadLine = %q, want %q", line, want)
	}
}

func TestLineEditorReadLineHonorsContextCancellation(t *testing.T) {
	// Input that never delivers a byte, like an idle terminal.
	pr, pw := io.Pipe()
	defer pw.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ed := NewLineEditor(pr, &bytes.Buffer{}, "")

	done := make(chan error, 1)
	go func() {
		_, err := ed.ReadLine(ctx, "> ")
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine did not return after its context was cancelled")
	}
}

// newScreenEditor returns an editor drawing to an emulated terminal that is
// cols columns wide.
func newScreenEditor(input string, cols int) (*LineEditor, *fakeScreen) {
	screen := newFakeScreen(cols)
	ed := NewLineEditor(strings.NewReader(input), screen, "")
	ed.width = func() (int, bool) { return cols, true }
	return ed, screen
}

func TestLineEditorRendersWrappedLines(t *testing.T) {
	t.Run("typing past the right edge wraps instead of repainting", func(t *testing.T) {
		ed, screen := newScreenEditor(strings.Repeat("a", 20)+"\r", 10)
		line, err := ed.ReadLine(context.Background(), "\x1b[36m> \x1b[0m")
		if err != nil || line != strings.Repeat("a", 20) {
			t.Fatalf("ReadLine = %q, %v", line, err)
		}
		want := []string{"> aaaaaaaa", "aaaaaaaaaa", "aa"}
		if got := screen.text(); !reflect.DeepEqual(got, want) {
			t.Fatalf("screen = %q, want %q", got, want)
		}
		if got, want := screen.cursor(), (cell{3, 0}); got != want {
			t.Fatalf("cursor after Enter = %+v, want %+v", got, want)
		}
	})

	t.Run("a line that exactly fills the row does not add a blank row", func(t *testing.T) {
		ed, screen := newScreenEditor(strings.Repeat("a", 8)+"\r", 10)
		if _, err := ed.ReadLine(context.Background(), "> "); err != nil {
			t.Fatalf("ReadLine: %v", err)
		}
		if got, want := screen.cursor(), (cell{1, 0}); got != want {
			t.Fatalf("cursor after Enter = %+v, want %+v", got, want)
		}
	})

	t.Run("editing in the middle of a wrapped line", func(t *testing.T) {
		// Type 14 chars (wrapping onto row 1), move back onto row 0, insert.
		seq := "abcdefghijklmn" + strings.Repeat("\x1b[D", 10) + "XY" + "\r"
		ed, screen := newScreenEditor(seq, 10)
		line, err := ed.ReadLine(context.Background(), "> ")
		if err != nil || line != "abcdXYefghijklmn" {
			t.Fatalf("ReadLine = %q, %v", line, err)
		}
		want := []string{"> abcdXYef", "ghijklmn"}
		if got := screen.text(); !reflect.DeepEqual(got, want) {
			t.Fatalf("screen = %q, want %q", got, want)
		}
	})

	t.Run("shrinking a wrapped line erases the rows it no longer uses", func(t *testing.T) {
		seq := strings.Repeat("a", 25) + "\x15" + "ok" + "\r" // Ctrl-U, then retype.
		ed, screen := newScreenEditor(seq, 10)
		if _, err := ed.ReadLine(context.Background(), "> "); err != nil {
			t.Fatalf("ReadLine: %v", err)
		}
		want := []string{"> ok"}
		if got := screen.text(); !reflect.DeepEqual(got, want) {
			t.Fatalf("screen = %q, want %q", got, want)
		}
	})

	t.Run("multi-line prompt keeps its header lines", func(t *testing.T) {
		ed, screen := newScreenEditor("hello\r", 20)
		if _, err := ed.ReadLine(context.Background(), "~/project main\n$ "); err != nil {
			t.Fatalf("ReadLine: %v", err)
		}
		want := []string{"~/project main", "$ hello"}
		if got := screen.text(); !reflect.DeepEqual(got, want) {
			t.Fatalf("screen = %q, want %q", got, want)
		}
	})
}

func TestLineEditorCursorPlacement(t *testing.T) {
	tests := []struct {
		name string
		buf  string
		pos  int
		want cell
	}{
		{"empty line sits after the prompt", "", 0, cell{0, 2}},
		{"end of a short line", "abc", 3, cell{0, 5}},
		{"middle of the first row", "abcdefghijkl", 4, cell{0, 6}},
		{"end of a full row wraps to the next", "abcdefgh", 8, cell{1, 0}},
		{"on the wrapped row", "abcdefghijkl", 10, cell{1, 2}},
		{"after an embedded newline", "ab\ncd", 3, cell{1, 0}},
		{"before an embedded newline", "ab\ncd", 2, cell{0, 4}},
		{"wide characters take two columns", "漢字", 2, cell{0, 6}},
		{"a wide character never straddles the edge", "abcdefg漢", 7, cell{1, 0}},
		{"combining marks take no columns", "e\u0301x", 3, cell{0, 4}},
		{"tabs are drawn as spaces", "\tx", 1, cell{0, 2 + tabWidth}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ed, screen := newScreenEditor("", 10)
			st := &lineState{prompt: "> ", promptPlain: "> ", buf: []rune(tc.buf), pos: tc.pos}
			ed.refresh(st)
			if got := screen.cursor(); got != tc.want {
				t.Fatalf("cursor = %+v, want %+v (screen %q)", got, tc.want, screen.text())
			}

			// Redrawing from wherever the cursor was left must be stable.
			before := screen.text()
			ed.refresh(st)
			if got := screen.text(); !reflect.DeepEqual(got, before) {
				t.Fatalf("second refresh changed the screen: %q -> %q", before, got)
			}
			if got := screen.cursor(); got != tc.want {
				t.Fatalf("cursor after second refresh = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLineEditorFreshLine(t *testing.T) {
	t.Run("output without a trailing newline is preserved", func(t *testing.T) {
		ed, screen := newScreenEditor("x\r", 20)
		screen.Write([]byte("partial output"))
		ed.FreshLine()
		if _, err := ed.ReadLine(context.Background(), "$ "); err != nil {
			t.Fatalf("ReadLine: %v", err)
		}
		want := []string{"partial output%", "$ x"}
		if got := screen.text(); !reflect.DeepEqual(got, want) {
			t.Fatalf("screen = %q, want %q", got, want)
		}
	})

	t.Run("no marker or blank row when output ended with a newline", func(t *testing.T) {
		ed, screen := newScreenEditor("x\r", 20)
		screen.Write([]byte("complete output\r\n"))
		ed.FreshLine()
		if _, err := ed.ReadLine(context.Background(), "$ "); err != nil {
			t.Fatalf("ReadLine: %v", err)
		}
		want := []string{"complete output", "$ x"}
		if got := screen.text(); !reflect.DeepEqual(got, want) {
			t.Fatalf("screen = %q, want %q", got, want)
		}
	})

	t.Run("no-op when the output is not a terminal", func(t *testing.T) {
		var out bytes.Buffer
		NewLineEditor(strings.NewReader(""), &out, "").FreshLine()
		if out.Len() != 0 {
			t.Fatalf("FreshLine wrote %q to a non-terminal", out.String())
		}
	})
}
