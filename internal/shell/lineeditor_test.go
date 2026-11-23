package shell

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

	line, err := ed.ReadLine("\x1b[36mprompt> \x1b[0m")
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

	line, err := ed.ReadLine("> ")
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
		got, err := ed.ReadLine("> ")
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
		got, err := ed.ReadLine("> ")
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
		got, err := ed.ReadLine("> ")
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
		got, err := ed.ReadLine("> ")
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
		_, err := ed.ReadLine("> ")
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("err = %v, want ErrInterrupted", err)
		}
	})

	t.Run("Ctrl-D on empty line returns io.EOF", func(t *testing.T) {
		ed := NewLineEditor(strings.NewReader("\x04"), &bytes.Buffer{}, "")
		_, err := ed.ReadLine("> ")
		if !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want io.EOF", err)
		}
	})
}

func TestLineEditorPersistentHistory(t *testing.T) {
	histFile := filepath.Join(t.TempDir(), "harnesh_history")

	ed1 := NewLineEditor(strings.NewReader(""), &bytes.Buffer{}, histFile)
	ed1.AddHistory("!git status")
	ed1.AddHistory("!git status") // consecutive duplicate ignored
	ed1.AddHistory("explain main.go")
	ed1.AddHistory("   ") // blank ignored

	ed2 := NewLineEditor(strings.NewReader("\x1b[A\x1b[A\r"), &bytes.Buffer{}, histFile)
	wantHist := []string{"!git status", "explain main.go"}
	if got := ed2.History(); !reflect.DeepEqual(got, wantHist) {
		t.Fatalf("loaded history = %#v, want %#v", got, wantHist)
	}

	line, err := ed2.ReadLine("> ")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "!git status" {
		t.Fatalf("recalled line = %q, want %q", line, "!git status")
	}
}
