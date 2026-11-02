//go:build !linux && !windows

package git

import "syscall"

func setParentDeathSignal(a *syscall.SysProcAttr) {}
