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
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		switch errno {
		case 0:
			return nil
		case syscall.EINTR:
			// Interrupted by a signal, e.g. after being stopped for
			// changing terminal settings from the background.
			continue
		default:
			return errno
		}
	}
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

// ForegroundProcessGroup returns the ID of the process group that currently
// owns the terminal on fd, i.e. the one that receives keyboard signals.
func ForegroundProcessGroup(fd int) (int, error) {
	var pgid int32
	if err := ioctl(fd, syscall.TIOCGPGRP, unsafe.Pointer(&pgid)); err != nil {
		return 0, err
	}
	return int(pgid), nil
}

// SetForegroundProcessGroup makes pgid the foreground process group of the
// terminal on fd. A caller that is itself in the background is sent SIGTTOU
// instead, unless it ignores that signal.
func SetForegroundProcessGroup(fd, pgid int) error {
	id := int32(pgid)
	return ioctl(fd, syscall.TIOCSPGRP, unsafe.Pointer(&id))
}
