package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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

	line, err := ed2.ReadLine(context.Background(), "> ")
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "!git status" {
		t.Fatalf("recalled line = %q, want %q", line, "!git status")
	}

	info, err := os.Stat(histFile)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("history file mode = %o, want 600 (prompts can be sensitive)", perm)
	}
}

func TestHistoryRoundTripsMultilineAndLongEntries(t *testing.T) {
	histFile := filepath.Join(t.TempDir(), "history")
	entries := []string{
		"review this:\nfunc main() {\n\tprintln(\"hi\")\n}",
		`!grep '\n' C:\path\name`,             // literal backslashes survive
		strings.Repeat("long prompt ", 20000), // far beyond bufio.Scanner's 64 KiB token limit
		"last",
	}

	h := newHistory(histFile, defaultMaxHistory)
	for _, e := range entries {
		h.add(e)
	}

	want := make([]string, len(entries))
	for i, e := range entries {
		want[i] = strings.TrimSpace(e)
	}
	if got := newHistory(histFile, defaultMaxHistory).entries; !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded %d entries, want %d; first mismatch near %.60q", len(got), len(want), got)
	}

	data, err := os.ReadFile(histFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != len(entries) {
		t.Fatalf("history file has %d lines, want one per entry (%d)", lines, len(entries))
	}
}

func TestHistoryFileIsCompacted(t *testing.T) {
	const limit = 10
	histFile := filepath.Join(t.TempDir(), "history")

	h := newHistory(histFile, limit)
	for i := 0; i < 3*limit; i++ {
		h.add(fmt.Sprintf("cmd %d", i))
	}
	if len(h.entries) != limit {
		t.Fatalf("in-memory history holds %d entries, want %d", len(h.entries), limit)
	}

	// The file is append-only within a session; the next load trims it.
	reloaded := newHistory(histFile, limit)
	var want []string
	for i := 2 * limit; i < 3*limit; i++ {
		want = append(want, fmt.Sprintf("cmd %d", i))
	}
	if !reflect.DeepEqual(reloaded.entries, want) {
		t.Fatalf("reloaded entries = %q, want %q", reloaded.entries, want)
	}

	data, err := os.ReadFile(histFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != limit {
		t.Fatalf("history file has %d lines after compaction, want %d", got, limit)
	}

	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(histFile), ".harnesh_history-*"))
	if len(leftovers) != 0 {
		t.Fatalf("compaction left temporary files behind: %v", leftovers)
	}
}

func TestHistoryMissingFileIsNotAnError(t *testing.T) {
	h := newHistory(filepath.Join(t.TempDir(), "does", "not", "exist"), defaultMaxHistory)
	h.add("still works in memory")
	if len(h.entries) != 1 {
		t.Fatalf("entries = %q, want 1 entry", h.entries)
	}
}
