//go:build !(darwin || linux)

package shell

import "os/exec"

// jobControlTTY reports that job control is unavailable on this platform, so
// children always share harnesh's process group.
func (s *Shell) jobControlTTY() (fd int, ok bool) { return 0, false }

func runForeground(cmd *exec.Cmd, _ int) childResult { return resultOf(cmd.Run()) }
