// Package term provides the small amount of terminal control harnesh needs
// (TTY detection, raw input mode, window width, and foreground process group
// hand-off) without external dependencies.
package term

import "errors"

// ErrUnsupported is returned on platforms without termios support.
var ErrUnsupported = errors.New("terminal control is not supported on this platform")
