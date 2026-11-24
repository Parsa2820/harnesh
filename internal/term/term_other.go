//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package term

// IsTerminal always reports false on unsupported platforms, so harnesh runs
// in its line-oriented non-interactive mode.
func IsTerminal(int) bool { return false }

// MakeRaw is unsupported on this platform.
func MakeRaw(int) (func(), error) { return nil, ErrUnsupported }

// Width is unknown on this platform.
func Width(int) (int, bool) { return 0, false }

// ForegroundProcessGroup is unsupported on this platform.
func ForegroundProcessGroup(int) (int, error) { return 0, ErrUnsupported }

// SetForegroundProcessGroup is unsupported on this platform.
func SetForegroundProcessGroup(int, int) error { return ErrUnsupported }
