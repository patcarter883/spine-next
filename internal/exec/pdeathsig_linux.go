//go:build linux

package exec

import (
	osexec "os/exec"
	"syscall"
)

// setParentDeathSignal asks the kernel to SIGKILL the child if the thread that
// started it dies. It is a safety net only: Recover, not this, is what
// guarantees no orphans survive a supervisor crash (RFC 0001 §6.3).
func setParentDeathSignal(cmd *osexec.Cmd) {
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
