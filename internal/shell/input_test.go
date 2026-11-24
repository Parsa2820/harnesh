package shell

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestInputReaderReadLine(t *testing.T) {
	in := newInputReader(strings.NewReader("first\r\nsecond\n\nunterminated"))
	ctx := context.Background()

	for _, want := range []string{"first", "second", ""} {
		got, err := in.readLine(ctx)
		if err != nil || got != want {
			t.Fatalf("ReadLine = %q, %v; want %q, nil", got, err, want)
		}
	}
	got, err := in.readLine(ctx)
	if !errors.Is(err, io.EOF) || got != "unterminated" {
		t.Fatalf("final ReadLine = %q, %v; want %q, io.EOF", got, err, "unterminated")
	}
}

func TestInputReaderCancellationLosesNoInput(t *testing.T) {
	pr, pw := io.Pipe()
	in := newInputReader(pr)

	// A read abandoned by cancellation...
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := in.readByte(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadByte did not return after cancellation")
	}

	// ...must still deliver its byte to the next reader.
	go func() {
		_, _ = pw.Write([]byte("ok\n"))
		pw.Close()
	}()
	line, err := in.readLine(context.Background())
	if err != nil || line != "ok" {
		t.Fatalf("ReadLine after cancellation = %q, %v; want %q, nil", line, err, "ok")
	}
}
