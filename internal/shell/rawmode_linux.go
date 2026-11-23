//go:build linux

package shell

import (
	"syscall"
	"unsafe"
)

// enableRawMode puts the terminal file descriptor fd into raw input mode so
// individual keystrokes and ANSI arrow escape sequences can be read
// immediately. The returned restore function resets the terminal to its exact
// prior termios state.
func enableRawMode(fd int) (func(), error) {
	var orig syscall.Termios
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&orig)),
	); errno != 0 {
		return nil, errno
	}

	raw := orig
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TCSETS),
		uintptr(unsafe.Pointer(&raw)),
	); errno != 0 {
		return nil, errno
	}

	restore := func() {
		_, _, _ = syscall.Syscall(
			syscall.SYS_IOCTL,
			uintptr(fd),
			uintptr(syscall.TCSETS),
			uintptr(unsafe.Pointer(&orig)),
		)
	}
	return restore, nil
}
