//go:build darwin || linux

package shell

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Parsa2820/harnesh/internal/term"
)

// suspendGrace is how long harnesh waits to be resumed after asking to be
// suspended. The kernel discards the request when there is no job-control
// shell above harnesh to resume it (an orphaned process group); the wait
// then simply times out and the job is continued.
const suspendGrace = 150 * time.Millisecond

// jobControlTTY returns the terminal to run foreground jobs on. Job control is
// only possible in an interactive session whose terminal harnesh currently
// owns; otherwise children share harnesh's process group, as they would for
// any ordinary program.
func (s *Shell) jobControlTTY() (fd int, ok bool) {
	if !s.cfg.Interactive {
		return 0, false
	}
	f, isFile := s.cfg.In.(*os.File)
	if !isFile {
		return 0, false
	}
	fd = int(f.Fd())
	foreground, err := term.ForegroundProcessGroup(fd)
	if err != nil || foreground != syscall.Getpgrp() {
		return 0, false
	}
	return fd, true
}

// runForeground runs cmd as the terminal's foreground job: in its own process
// group, which is given the terminal for as long as the command runs.
//
// This is what makes keyboard signals behave as they do in a shell. Ctrl-C
// reaches the job only, not harnesh or whatever launched harnesh, and
// shutting down can signal the job's whole process group rather than just its
// first process.
func runForeground(cmd *exec.Cmd, tty int) childResult {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: tty}
	cmd.Cancel = func() error {
		// The job leads its own process group, so its pid is the group ID.
		// A stopped job cannot act on SIGTERM until it is continued.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGCONT)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	shell := syscall.Getpgrp()
	if err := cmd.Start(); err != nil {
		// A program that cannot be executed fails in the child process,
		// which by then has taken the terminal for its now defunct group.
		allowBackgroundTerminalAccess()
		if owner, ownerErr := term.ForegroundProcessGroup(tty); ownerErr == nil && owner != shell {
			_ = term.SetForegroundProcessGroup(tty, shell)
		}
		stopOnBackgroundTerminalAccess()
		return resultOf(err)
	}
	job := cmd.Process.Pid

	// harnesh is in the background from here on, until it takes the terminal
	// back below.
	allowBackgroundTerminalAccess()
	for waitForStop(job) {
		suspendWithJob(tty, shell, job)
	}
	handTerminal(tty, job, shell)
	stopOnBackgroundTerminalAccess()

	return resultOf(cmd.Wait())
}

// suspendWithJob handles the foreground job being stopped (Ctrl-Z). harnesh
// has no job table of its own, so it suspends itself along with the job and
// lets the shell that launched it take over; "fg" there resumes both. That is
// also how Ctrl-Z behaves for any program that runs a child.
func suspendWithJob(tty, shell, job int) {
	handTerminal(tty, job, shell)
	stopOnBackgroundTerminalAccess()

	// The stop takes effect asynchronously, so wait for the matching SIGCONT
	// instead of assuming it happened by the time Kill returns.
	resumed := make(chan os.Signal, 1)
	signal.Notify(resumed, syscall.SIGCONT)
	_ = syscall.Kill(0, syscall.SIGTSTP)
	select {
	case <-resumed:
	case <-time.After(suspendGrace):
	}
	signal.Stop(resumed)

	// Resumed with "fg", harnesh owns the terminal again and passes it on.
	// Resumed with "bg", it does not, and the job continues without one.
	allowBackgroundTerminalAccess()
	handTerminal(tty, shell, job)
	_ = syscall.Kill(-job, syscall.SIGCONT)
}

// handTerminal passes the terminal from one process group to another, making
// the latter its foreground group. Nothing happens unless the terminal
// belongs to from: harnesh must not take it away from a shell that has moved
// harnesh and its job to the background.
func handTerminal(tty, from, to int) {
	if owner, err := term.ForegroundProcessGroup(tty); err == nil && owner == from {
		_ = term.SetForegroundProcessGroup(tty, to)
	}
}

// A process in the background that writes to the terminal (with "stty
// tostop") or changes its settings is sent SIGTTOU, which stops it. While a
// job runs, harnesh is in the background by design and still has to relay
// agent output and take the terminal back, so it ignores the signal. The rest
// of the time it should stop like any other program.
//
// Go cannot restore a signal's default action once it has been ignored, and
// leaving SIGTTOU ignored would pass that on to every child process. The
// signal is caught instead, which children do not inherit, and its default
// action is carried out by hand.
var terminalStop struct {
	once    sync.Once
	signals chan os.Signal
}

// stopStaleWindow is how long SIGTTOU signals keep arriving for a terminal
// operation that was already answered by stopping.
const stopStaleWindow = 50 * time.Millisecond

func allowBackgroundTerminalAccess() {
	signal.Ignore(syscall.SIGTTOU)
}

func stopOnBackgroundTerminalAccess() {
	terminalStop.once.Do(func() {
		terminalStop.signals = make(chan os.Signal, 1)
		go func() {
			for range terminalStop.signals {
				// Returns once harnesh has been stopped and continued.
				_ = syscall.Kill(syscall.Getpid(), syscall.SIGSTOP)

				// The interrupted operation is retried until the stop
				// takes effect, raising the signal each time; those
				// repeats must not stop harnesh again once it is resumed.
				stale := time.After(stopStaleWindow)
				for draining := true; draining; {
					select {
					case <-terminalStop.signals:
					case <-stale:
						draining = false
					}
				}
			}
		}()
	})
	signal.Notify(terminalStop.signals, syscall.SIGTTOU)
}

// Arguments and result codes of waitid(2); identical on Linux and macOS.
const (
	waitidPID  = 1 // P_PID: wait for the process with the given ID
	cldStopped = 5 // CLD_STOPPED: the child was stopped by a signal
)

// waitForStop blocks until the process pid stops or terminates. It reports
// true if the process stopped, and false once it has terminated (or can no
// longer be waited for). A terminated process is left unreaped so that
// exec.Cmd.Wait still collects its status.
func waitForStop(pid int) bool {
	// siginfo_t is at most 128 bytes; si_code is the third int on both
	// platforms.
	var info [16]uint64
	waitid := func(options uintptr) (code int32, ok bool) {
		for {
			info = [16]uint64{}
			_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, waitidPID, uintptr(pid),
				uintptr(unsafe.Pointer(&info)), options, 0, 0)
			if errno == syscall.EINTR {
				continue
			}
			return *(*int32)(unsafe.Add(unsafe.Pointer(&info), 8)), errno == 0
		}
	}

	code, ok := waitid(syscall.WEXITED | syscall.WSTOPPED | syscall.WNOWAIT)
	if !ok || code != cldStopped {
		return false
	}
	// WNOWAIT left the stop reportable; consume it so that the next call
	// blocks until the job changes state again. Selecting only stops here
	// cannot reap an exit.
	waitid(syscall.WSTOPPED | syscall.WNOHANG)
	return true
}
