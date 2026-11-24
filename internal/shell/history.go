package shell

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const defaultMaxHistory = 1000

// history is the in-memory command history, optionally mirrored to a file with
// one entry per line. Entries may span multiple lines (pasted prompts), so
// they are escaped on disk: newline is written as `\n` and backslash as `\\`.
type history struct {
	entries []string
	max     int
	path    string // empty: in-memory only
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

func newHistory(path string, max int) *history {
	h := &history{max: max, path: path}
	if path != "" {
		h.load()
	}
	return h
}

// add records line (ignoring blanks and consecutive duplicates) and appends it
// to the history file when one is configured.
func (h *history) add(line string) {
	line = strings.TrimSpace(line)
	if !h.push(line) {
		return
	}
	if len(h.entries) > h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
	}
	if h.path != "" {
		h.appendFile(line)
	}
}

func (h *history) push(line string) bool {
	if line == "" {
		return false
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == line {
		return false
	}
	h.entries = append(h.entries, line)
	return true
}

// load reads the history file into memory. The file is append-only during a
// session, so once it has grown to twice the retention limit it is compacted
// down to the most recent entries.
func (h *history) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()

	// Entries can be arbitrarily long (pasted prompts), so read with
	// ReadString rather than a fixed-buffer bufio.Scanner.
	reader := bufio.NewReader(f)
	for {
		raw, err := reader.ReadString('\n')
		h.push(strings.TrimSpace(decodeHistoryEntry(strings.TrimRight(raw, "\r\n"))))
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return
			}
			break
		}
	}

	if len(h.entries) >= 2*h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
		h.compactFile()
	} else if len(h.entries) > h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
	}
}

func (h *history) appendFile(line string) {
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(encodeHistoryEntry(line) + "\n")
}

// compactFile atomically rewrites the history file with the entries currently
// held in memory. Failures are ignored: history persistence is best-effort.
func (h *history) compactFile() {
	target := h.path
	if resolved, err := filepath.EvalSymlinks(h.path); err == nil {
		target = resolved // Keep a symlinked history file a symlink.
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".harnesh_history-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriter(tmp)
	for _, entry := range h.entries {
		_, _ = w.WriteString(encodeHistoryEntry(entry) + "\n")
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), target)
}

var (
	historyEncoder = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	historyDecoder = strings.NewReplacer(`\\`, `\`, `\n`, "\n")
)

func encodeHistoryEntry(s string) string { return historyEncoder.Replace(s) }
func decodeHistoryEntry(s string) string { return historyDecoder.Replace(s) }
