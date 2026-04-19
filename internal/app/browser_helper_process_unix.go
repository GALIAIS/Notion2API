//go:build !windows

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func configureBrowserHelperCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	originalCancel := cmd.Cancel
	cmd.Cancel = func() error {
		var cancelErr error
		if proc := cmd.Process; proc != nil {
			if err := terminateBrowserHelperProcessTree(proc.Pid); err != nil {
				cancelErr = err
			}
		}
		if originalCancel != nil && cmd.Process != nil {
			if err := originalCancel(); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) && cancelErr == nil {
				cancelErr = err
			}
		}
		return cancelErr
	}
}

func browserHelperUsesProcessGroupKill(cmd *exec.Cmd) bool {
	return cmd != nil && cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid
}

func browserHelperProcessAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func terminateBrowserHelperProcessTree(rootPID int) error {
	var terminateErr error
	for _, childPID := range browserHelperDescendantPIDs(rootPID) {
		if err := syscall.Kill(childPID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) && terminateErr == nil {
			terminateErr = err
		}
	}
	if err := syscall.Kill(-rootPID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) && terminateErr == nil {
		terminateErr = err
	}

	time.Sleep(150 * time.Millisecond)

	for _, childPID := range browserHelperDescendantPIDs(rootPID) {
		if err := syscall.Kill(childPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && terminateErr == nil {
			terminateErr = err
		}
	}
	if err := syscall.Kill(-rootPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && terminateErr == nil {
		terminateErr = err
	}
	return terminateErr
}

func browserHelperDescendantPIDs(rootPID int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	childrenByParent := make(map[int][]int)
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil {
			continue
		}
		statusPath := fmt.Sprintf("/proc/%d/status", pid)
		status, readErr := os.ReadFile(statusPath)
		if readErr != nil {
			continue
		}
		ppid, ok := parseProcStatusParentPID(status)
		if !ok {
			continue
		}
		childrenByParent[ppid] = append(childrenByParent[ppid], pid)
	}

	var descendants []int
	queue := []int{rootPID}
	seen := map[int]bool{rootPID: true}
	for len(queue) > 0 {
		parentPID := queue[0]
		queue = queue[1:]
		for _, childPID := range childrenByParent[parentPID] {
			if seen[childPID] {
				continue
			}
			seen[childPID] = true
			descendants = append(descendants, childPID)
			queue = append(queue, childPID)
		}
	}
	return descendants
}

func parseProcStatusParentPID(status []byte) (int, bool) {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))
		ppid, err := strconv.Atoi(value)
		if err != nil {
			return 0, false
		}
		return ppid, true
	}
	return 0, false
}
