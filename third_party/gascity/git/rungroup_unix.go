//go:build !windows

package git

import (
	"errors"
	"os/exec"
	"syscall"
)

// killGroupOnCancel is a Spine patch (UPSTREAM.md, patch 1). It starts cmd in
// its own process group, makes context cancellation SIGKILL the group, and on
// Linux asks the kernel to kill git if its parent dies.
func killGroupOnCancel(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	setParentDeathSignal(cmd.SysProcAttr)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}
