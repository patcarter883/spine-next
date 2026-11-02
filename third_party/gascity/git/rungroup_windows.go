//go:build windows

package git

import "os/exec"

func killGroupOnCancel(cmd *exec.Cmd) {}
