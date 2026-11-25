//go:build unix

package pyne

import (
	"os/exec"
	"syscall"
)

// ownGroup runs cmd in its own process group and makes cancellation kill
// the whole group, so helpers PYNE spawns (compile mode) die with it.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
