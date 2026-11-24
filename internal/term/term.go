// Package term provides the small amount of terminal control harnesh needs
// (TTY detection, raw input mode, and window width) without external
// dependencies.
package term

import "errors"

// ErrUnsupported is returned by MakeRaw on platforms without termios support.
var ErrUnsupported = errors.New("raw terminal mode is not supported on this platform")
