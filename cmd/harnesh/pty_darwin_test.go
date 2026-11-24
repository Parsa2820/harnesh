package main

import (
	"os"
	"syscall"
	"unsafe"
)

// openPTY allocates a pseudo-terminal and returns its controlling end along
// with the path of the end a program uses as its terminal.
func openPTY() (master *os.File, slavePath string, err error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	var name [128]byte
	for _, step := range []struct {
		request uintptr
		arg     unsafe.Pointer
	}{
		{syscall.TIOCPTYGRANT, nil},
		{syscall.TIOCPTYUNLK, nil},
		{syscall.TIOCPTYGNAME, unsafe.Pointer(&name)},
	} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), step.request, uintptr(step.arg)); errno != 0 {
			syscall.Close(fd)
			return nil, "", errno
		}
	}
	end := 0
	for end < len(name) && name[end] != 0 {
		end++
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), string(name[:end]), nil
}
