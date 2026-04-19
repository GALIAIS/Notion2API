//go:build windows

package app

import "os/exec"

func configureBrowserHelperCommand(cmd *exec.Cmd) {
	_ = cmd
}

func browserHelperUsesProcessGroupKill(cmd *exec.Cmd) bool {
	return false
}

func browserHelperProcessAlive(pid int) (bool, error) {
	_ = pid
	return false, nil
}
