//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package term

import (
	"syscall"
	"unsafe"
)

// winsize mirrors struct winsize from <sys/ioctl.h>.
type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// IsTerminal reports whether fd refers to a terminal. Unlike checking for a
// character device, this correctly rejects /dev/null.
func IsTerminal(fd int) bool {
	var t syscall.Termios
	return ioctl(fd, ioctlGetTermios, unsafe.Pointer(&t)) == nil
}

// MakeRaw switches the terminal on fd to raw input mode: keystrokes are
// delivered immediately, unechoed, and without signal generation. Output
// post-processing is left enabled. The returned function restores the exact
// prior terminal state.
func MakeRaw(fd int) (restore func(), err error) {
	var orig syscall.Termios
	if err := ioctl(fd, ioctlGetTermios, unsafe.Pointer(&orig)); err != nil {
		return nil, err
	}

	raw := orig
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if err := ioctl(fd, ioctlSetTermios, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return func() {
		_ = ioctl(fd, ioctlSetTermios, unsafe.Pointer(&orig))
	}, nil
}

// Width returns the number of columns of the terminal on fd, or ok=false if
// fd is not a terminal or its size is unknown.
func Width(fd int) (cols int, ok bool) {
	var ws winsize
	if err := ioctl(fd, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil || ws.Col == 0 {
		return 0, false
	}
	return int(ws.Col), true
}
