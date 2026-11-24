package main

import (
	"fmt"
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
	var unlock int32
	var number uint32
	for _, step := range []struct {
		request uintptr
		arg     unsafe.Pointer
	}{
		{syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)},
		{syscall.TIOCGPTN, unsafe.Pointer(&number)},
	} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), step.request, uintptr(step.arg)); errno != 0 {
			syscall.Close(fd)
			return nil, "", errno
		}
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), fmt.Sprintf("/dev/pts/%d", number), nil
}
