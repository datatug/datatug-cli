package sshdemo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		// A soft cap on the Go heap of one session; the cgroup is the hard one.
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

func generateSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("sshdemo: generate session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func createSessionWorkspace(sessionsDir, sessionID string, slot Slot, isolation string, ops SysOps) (string, string, string, error) {
	dir := filepath.Join(sessionsDir, sessionID)
	homeDir := filepath.Join(dir, "home")
	repoDir := filepath.Join(dir, "repo")
	tempDir := filepath.Join(dir, "tmp")

	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			_ = os.RemoveAll(dir)
		}
	}()

	for _, p := range []string{dir, homeDir, repoDir, tempDir} {
		if err := os.Mkdir(p, 0o700); err != nil {
			return "", "", "", fmt.Errorf("sshdemo: mkdir %s: %w", p, err)
		}
		if err := os.Chmod(p, 0o700); err != nil {
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

// killAndDrainUIDProcs kills any process running under uid and waits briefly
// for /proc to reflect that none remain.
func killAndDrainUIDProcs(uid int, ops SysOps) bool {
	for attempt := 0; attempt < 20; attempt++ {
		pids, err := ops.ListUIDProcs(uid)
		if err != nil {
			return false
		}
		if len(pids) == 0 {
			return true
		}
		for _, pid := range pids {
			_ = ops.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(15 * time.Millisecond)
	}
	pids, err := ops.ListUIDProcs(uid)
	return err == nil && len(pids) == 0
}

// runCleanupCommand invokes `demo ssh-session --cleanup <dir>` as (uid, gid),
// removes the empty directory, and verifies that the directory is gone and (in
// uid mode) no processes of that uid remain.
func runCleanupCommand(
	ctx context.Context,
	execPath string,
	execPrefix []string,
	dir string,
	uid, gid int,
	isolation string,
	ops SysOps,
	logger *slog.Logger,
) bool {
	// A process that left the session's process group (setsid) survived the
	// group kill: kill everything of the slot uid first, so nothing writes into
	// the workspace while it is being removed.
	if isolation == IsolationUID {
		_ = killAndDrainUIDProcs(uid, ops)
	}
	args := append(append([]string(nil), execPrefix...), BuildCleanupArgs(dir)...)
	cmd := exec.CommandContext(ctx, execPath, args...)
	cmd.Env = buildCleanupEnv(filepath.Join(dir, "home"))
	if isolation == IsolationUID {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: ops.CredentialFor(uid, gid),
		}
	}
	if err := cmd.Run(); err != nil {
		logger.Warn("session cleanup command failed",
			"dir", dir,
			"uid", uid,
			"err", err,
		)
	}

	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		logger.Error("session workspace remove failed",
			"dir", dir,
			"uid", uid,
			"err", err,
		)
	}

	_, statErr := os.Lstat(dir)
	dirGone := os.IsNotExist(statErr)

	procsGone := true
	if isolation == IsolationUID {
		procsGone = killAndDrainUIDProcs(uid, ops)
		if !procsGone {
			logger.Error("leftover processes remain for slot uid", "uid", uid, "dir", dir)
		}
	}
	return dirGone && procsGone
}

// cleanStaleSessions cleans any leftover directories under cfg.SessionsDir from
// a previous crash. In uid mode, only directories owned by a UID within the
// configured slot range are cleaned; others are logged and left untouched.
func (s *Server) cleanStaleSessions(ctx context.Context) error {
	entries, err := os.ReadDir(s.cfg.SessionsDir)
	if err != nil {
		return fmt.Errorf("sshdemo: read sessions-dir for stale cleanup: %w", err)
	}
	allQuarantined := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.cfg.SessionsDir, entry.Name())
		uid, gid := os.Getuid(), os.Getgid()
		if s.cfg.Isolation == IsolationUID {
			fi, statErr := s.ops.Stat(dir)
			if statErr != nil {
				return fmt.Errorf("sshdemo: stat stale session dir %s: %w", dir, statErr)
			}
			ownerUID, ok := s.ops.FileUID(fi)
			if !ok {
				return fmt.Errorf("sshdemo: cannot determine owner of stale session dir %s", dir)
			}
			if !s.slots.InSlotRange(ownerUID) {
				s.logger.Warn("ignoring stale session dir outside slot uid range",
					"dir", dir,
					"owner_uid", ownerUID,
				)
				continue
			}
			uid = ownerUID
			gid = ownerUID
		}

		cleanCtx, cancel := context.WithTimeout(ctx, s.cfg.CleanupTimeout)
		ok := runCleanupCommand(cleanCtx, s.executable, s.cfg.ExecPrefixArgs, dir, uid, gid, s.cfg.Isolation, s.ops, s.logger)
		cancel()
		if ok {
			s.logger.Info("cleaned stale session dir", "dir", dir, "uid", uid)
			continue
		}
		s.logger.Error("quarantining uid after unverified stale session cleanup", "dir", dir, "uid", uid)
		if s.cfg.Isolation == IsolationProcess {
			return fmt.Errorf("sshdemo: could not verify cleanup of workspace %s for shared uid %d", dir, uid)
		}
		allQuarantined = s.slots.quarantineUID(uid)
	}
	if allQuarantined {
		return errors.New("sshdemo: all session slots quarantined after stale cleanup")
	}
	return nil
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
		raw, err := master.SyscallConn()
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
