package term

import (
	"os"
	"testing"
)

func TestNonTerminalFiles(t *testing.T) {
	// /dev/null is a character device but must not be mistaken for a TTY.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	for name, f := range map[string]*os.File{"devnull": devNull, "pipe": r} {
		fd := int(f.Fd())
		if IsTerminal(fd) {
			t.Errorf("%s: IsTerminal = true, want false", name)
		}
		if _, ok := Width(fd); ok {
			t.Errorf("%s: Width reported ok for a non-terminal", name)
		}
		if restore, err := MakeRaw(fd); err == nil {
			restore()
			t.Errorf("%s: MakeRaw succeeded on a non-terminal", name)
		}
	}
}
