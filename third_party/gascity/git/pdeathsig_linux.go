//go:build linux

package git

import "syscall"

// setParentDeathSignal is part of Spine patch 1 (UPSTREAM.md).
func setParentDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
