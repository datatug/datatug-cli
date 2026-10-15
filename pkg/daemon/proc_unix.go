//go:build !windows

package daemon

import (
	"errors"
	"syscall"
)

func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminateProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
