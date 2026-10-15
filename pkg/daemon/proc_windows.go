//go:build windows

package daemon

import (
	"os"
	"syscall"
)

const detachedProcess = 0x00000008 // DETACHED_PROCESS

func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}

func terminateProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// processOwns cannot read another process's command line here, so the recorded
// port answering as a datatug API is the only ownership check.
func processOwns(_, port int) bool {
	_, up := probeAgent(port)
	return up
}
