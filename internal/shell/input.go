package shell

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// inputReader is a buffered byte reader whose blocking reads can be abandoned
// when a context is cancelled. This lets harnesh shut down on SIGTERM/SIGHUP
// even while it is parked waiting for the next keystroke or input line.
//
// A read abandoned by cancellation stays in flight and its result is handed to
// the next readByte call, so no input is lost. inputReader is not safe for
// concurrent use.
type inputReader struct {
	r       *bufio.Reader
	pending chan readResult // non-nil while a background read is in flight
}

type readResult struct {
	b   byte
	err error
}

func newInputReader(r io.Reader) *inputReader {
	return &inputReader{r: bufio.NewReader(r)}
}

// readByte returns the next input byte, or ctx.Err() if ctx is cancelled
// before one becomes available.
func (p *inputReader) readByte(ctx context.Context) (byte, error) {
	if p.pending == nil {
		// p.r is only touched here while no background read is in flight.
		if p.r.Buffered() > 0 {
			return p.r.ReadByte()
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		ch := make(chan readResult, 1)
		p.pending = ch
		go func() {
			b, err := p.r.ReadByte()
			ch <- readResult{b, err}
		}()
	}

	select {
	case res := <-p.pending:
		p.pending = nil
		return res.b, res.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// readLine reads up to and including the next newline and returns the line
// without its line terminator. At end of input it returns any final
// unterminated line together with io.EOF.
func (p *inputReader) readLine(ctx context.Context) (string, error) {
	var line strings.Builder
	for {
		b, err := p.readByte(ctx)
		if err != nil {
			return line.String(), err
		}
		if b == '\n' {
			return strings.TrimSuffix(line.String(), "\r"), nil
		}
		line.WriteByte(b)
	}
}
