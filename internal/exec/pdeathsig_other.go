//go:build !linux

package exec

import osexec "os/exec"

func setParentDeathSignal(cmd *osexec.Cmd) {}
