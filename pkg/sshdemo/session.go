//go:build linux

package sshdemo

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/ssh"
	"golang.org/x/sys/unix"
)

// ClampWindow clamps PTY dimensions to [MinPTYWidth..MaxPTYWidth] x
// [MinPTYHeight..MaxPTYHeight].
func ClampWindow(w ssh.Window) ssh.Window {
	if w.Width < MinPTYWidth {
		w.Width = MinPTYWidth
	} else if w.Width > MaxPTYWidth {
		w.Width = MaxPTYWidth
	}
	if w.Height < MinPTYHeight {
		w.Height = MinPTYHeight
	} else if w.Height > MaxPTYHeight {
		w.Height = MaxPTYHeight
	}
	return w
}

// BuildChildArgs constructs the exact argument list for `demo ssh-session`.
func BuildChildArgs(cfg Config, workspace, templateDir, sessionID string, deadline time.Time) []string {
	return []string{
		"demo", "ssh-session",
		"--workspace", workspace,
		"--template", templateDir,
		"--project-dir", cfg.ProjectDir,
		"--repo", cfg.Repo,
		"--github-client-id", cfg.GitHubClientID,
		"--save-branch", cfg.SaveBranch,
		"--session-id", sessionID,
		"--deadline", deadline.UTC().Format(time.RFC3339Nano),
		"--rlimit-as", strconv.Itoa(RlimitAS),
		"--rlimit-cpu", strconv.Itoa(RlimitCPU),
		"--rlimit-fsize", strconv.Itoa(RlimitFSize),
		"--rlimit-nofile", strconv.Itoa(RlimitNoFile),
		"--rlimit-nproc", strconv.Itoa(RlimitNProc),
	}
}

// BuildCleanupArgs constructs the argument list for `demo ssh-session --cleanup`.
func BuildCleanupArgs(workspace string) []string {
	return []string{"demo", "ssh-session", "--cleanup", workspace}
}

// BuildChildEnv returns a clean, minimal environment for the session child.
// It never inherits the supervisor's environment.
func BuildChildEnv(homeDir, term, colorTerm, egressProxy string) []string {
	tempDir := filepath.Join(filepath.Dir(homeDir), "tmp")
	term = sanitizeEnvValue(term)
	if term == "" {
		term = "xterm-256color"
	}
	colorTerm = sanitizeEnvValue(colorTerm)
	if colorTerm == "" && termSupportsTruecolor(term) {
		colorTerm = "truecolor"
	}

	env := []string{
		"HOME=" + homeDir,
		"TERM=" + term,
		"TMPDIR=" + tempDir,
		"SQLITE_TMPDIR=" + tempDir,
	}
	if colorTerm != "" {
		env = append(env, "COLORTERM="+colorTerm)
	}
	env = append(env,
		"LANG=C.UTF-8",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"DATATUG_TELEMETRY=0",
		"DO_NOT_TRACK=1",
		// A soft cap on the Go heap of one session; the UID slot cgroup enforces the hard cap.
		"GOMEMLIMIT="+ChildGoMemLimit,
	)
	if egressProxy != "" {
		env = append(env,
			"HTTPS_PROXY="+egressProxy,
			"HTTP_PROXY="+egressProxy,
			"https_proxy="+egressProxy,
			"http_proxy="+egressProxy,
		)
	}
	env = append(env, "NO_PROXY=")
	return env
}

func buildCleanupEnv(homeDir string) []string {
	return []string{
		"HOME=" + homeDir,
		"LANG=C.UTF-8",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"DATATUG_TELEMETRY=0",
		"DO_NOT_TRACK=1",
		"NO_PROXY=",
		"GOMEMLIMIT=32MiB",
		"GOMAXPROCS=2",
	}
}

func termSupportsTruecolor(term string) bool {
	lower := strings.ToLower(term)
	return strings.Contains(lower, "256color") ||
		strings.Contains(lower, "truecolor") ||
		strings.Contains(lower, "direct")
}

func sanitizeEnvValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.ContainsAny(v, "\x00\r\n") {
		return ""
	}
	return v
}

func extractClientTermAndColorTerm(ptyTerm string, environ []string) (term, colorTerm string) {
	term = ptyTerm
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "TERM":
			if term == "" {
				term = v
			}
		case "COLORTERM":
			colorTerm = v
		}
	}
	return term, colorTerm
}

var sessionRandom = rand.Reader

func generateSessionID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(sessionRandom, b[:]); err != nil {
		return "", fmt.Errorf("sshdemo: generate session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

var setWorkspaceMode = os.Chmod

func createSessionWorkspace(sessionsDir, sessionID string, slot Slot, isolation string, ops SysOps) (string, string, string, error) {
	if isolation == IsolationUID {
		sessionsDir = filepath.Join(sessionsDir, slotName(slot.Index))
	}
	dir := filepath.Join(sessionsDir, sessionID)
	homeDir := filepath.Join(dir, "home")
	repoDir := filepath.Join(dir, "repo")
	tempDir := filepath.Join(dir, "tmp")

	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", "", "", fmt.Errorf("sshdemo: mkdir %s: %w", dir, err)
	}

	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			_ = os.RemoveAll(dir)
		}
	}()

	if err := setWorkspaceMode(dir, 0o700); err != nil {
		return "", "", "", fmt.Errorf("sshdemo: chmod %s: %w", dir, err)
	}
	for _, p := range []string{homeDir, repoDir, tempDir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			return "", "", "", fmt.Errorf("sshdemo: mkdir %s: %w", p, err)
		}
		if err := setWorkspaceMode(p, 0o700); err != nil {
			return "", "", "", fmt.Errorf("sshdemo: chmod %s: %w", p, err)
		}
	}
	// Ownership goes to the slot uid innermost first: the supervisor has no
	// CAP_DAC_OVERRIDE, so once dir is the slot uid's (0700) it can no longer
	// create or chown anything inside it.
	if isolation == IsolationUID {
		for _, p := range []string{homeDir, repoDir, tempDir, dir} {
			if err := ops.Chown(p, slot.UID, slot.GID); err != nil {
				return "", "", "", fmt.Errorf("sshdemo: chown %s to %d:%d: %w", p, slot.UID, slot.GID, err)
			}
		}
	}

	cleanupOnErr = false
	return dir, homeDir, repoDir, nil
}

func appendSlotEntryArgs(args []string, path string, uid, gid int) []string {
	return append(args, "--slot-cgroup", path, "--slot-uid", strconv.Itoa(uid), "--slot-gid", strconv.Itoa(gid))
}

var ptySyscallConn = (*os.File).SyscallConn

func waitForChildReap(done <-chan error, grace time.Duration) error {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case waitErr := <-done:
		// The exit path already chose the session's exit status.
		_ = waitErr
		return nil
	case <-timer.C:
		return errors.New("timed out waiting to reap session child")
	}
}

// drainPTYMaster waits briefly for the kernel PTY master buffer to drain after
// the child process has exited (with the supervisor's slave fd already closed)
// so final output reaches the SSH channel before exit-status closes it.
func drainPTYMaster(master *os.File, maxWait time.Duration) {
	if master == nil {
		return
	}
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		var pending int
		var ioctlErr error
		raw, err := ptySyscallConn(master)
		if err != nil {
			return
		}
		ctrlErr := raw.Control(func(fd uintptr) {
			pending, ioctlErr = unix.IoctlGetInt(int(fd), unix.TIOCINQ)
		})
		if ctrlErr != nil || ioctlErr != nil || pending == 0 {
			// Give the background io.Copy goroutine one brief scheduling turn
			// to flush the last Read buffer to the SSH channel.
			time.Sleep(10 * time.Millisecond)
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func exitCodeFromWaitErr(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
