//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminateProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// processOwns reads the command line of pid with ps and checks that it is a
// `serve` on port, so a reused pid is never mistaken for the daemon.
func processOwns(pid, port int) bool {
	out, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "command=").Output()
	line := string(out)
	return err == nil && strings.Contains(line, " serve ") && strings.Contains(line, fmt.Sprintf("--port %d", port))
}
