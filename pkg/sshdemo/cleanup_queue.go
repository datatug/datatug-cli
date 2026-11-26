//go:build linux

package sshdemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const cleanupRetryInterval = 30 * time.Second

type cleanupReport struct {
	progress, invariant bool
}

func (r *cleanupReport) Write(p []byte) (int, error) {
	r.progress = r.progress || bytes.Contains(p, []byte(CleanupProgress))
	r.invariant = r.invariant || bytes.Contains(p, []byte(CleanupInvariant))
	return len(p), nil
}

// cleanWorkspace serializes helpers and starts their budget after queue admission.
func (s *Server) cleanWorkspace(ctx context.Context, dir string, uid, gid int) (cleanupReport, error) {
	var report cleanupReport
	select {
	case s.cleanupQueue <- struct{}{}:
		defer func() { <-s.cleanupQueue }()
	case <-ctx.Done():
		return report, ctx.Err()
	}
	args := append(append([]string(nil), s.cfg.ExecPrefixArgs...), BuildCleanupArgs(dir)...)
	if s.cfg.Isolation == IsolationUID {
		args = appendSlotEntryArgs(args, s.cleanupGroup.path, uid, gid)
	}
	cleanCtx, cancel := context.WithTimeout(ctx, s.cfg.CleanupTimeout)
	defer cancel()
	cmd := exec.CommandContext(cleanCtx, s.executable, args...)
	cmd.Env = buildCleanupEnv(filepath.Join(dir, "home"))
	cmd.Stdout = &report
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		err = os.Remove(dir)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return report, nil
		}
	}
	return report, fmt.Errorf("could not verify cleanup of workspace %s: %w", dir, errors.Join(err, cleanCtx.Err()))
}

type cleanupJob struct {
	slot       Slot
	dirs       []string
	noProgress int
	retryAfter time.Time
}

func (s *Server) runCleanupJob(ctx context.Context, job *cleanupJob) {
	active := job.slot.Index < s.cfg.MaxSessions
	for len(job.dirs) != 0 {
		dir := job.dirs[0]
		report, err := s.cleanWorkspace(ctx, dir, job.slot.UID, job.slot.GID)
		if err != nil {
			s.deferCleanup(job, active, report, err)
			return
		}
		job.dirs = job.dirs[1:]
		job.noProgress = 0
		s.logger.Info("cleaned session workspace", "dir", dir, "uid", job.slot.UID)
	}
	if active && s.cfg.Isolation == IsolationUID {
		s.checkSlotReuse(job.slot.Index, false)
	} else if active {
		s.slots.setSlotState(job.slot.Index, slotUsable)
	}
}

func (s *Server) deferCleanup(job *cleanupJob, active bool, report cleanupReport, err error) {
	if report.progress {
		job.noProgress = 0
	} else {
		job.noProgress++
	}
	if report.invariant {
		err = fmt.Errorf("cleanup helper reported a slot-entry or traversal invariant: %w", err)
	}
	if report.invariant || job.noProgress >= 3 {
		if active {
			s.quarantineSlot(job.slot.Index, err)
		} else {
			s.logger.Error("quarantining retired session workspace", "uid", job.slot.UID, "err", err)
		}
		return
	}
	job.retryAfter = time.Now().Add(cleanupRetryInterval)
	s.queueCleanup(job)
	s.logger.Warn("session workspace awaiting cleanup", "uid", job.slot.UID, "err", err,
		"quarantined_slots", s.slots.quarantinedCount(), "max_sessions", s.cfg.MaxSessions)
}

func (s *Server) queueCleanup(job *cleanupJob) {
	if job.slot.Index < s.cfg.MaxSessions {
		s.slots.setSlotState(job.slot.Index, slotAwaitingCleanup)
	}
	s.cleanupMu.Lock()
	if s.pendingCleanup == nil {
		s.pendingCleanup = make(map[int]*cleanupJob)
	}
	s.pendingCleanup[job.slot.Index] = job
	s.cleanupMu.Unlock()
}

func (s *Server) retryCleanupJobs(ctx context.Context) {
	s.cleanupMu.Lock()
	var due []*cleanupJob
	now := time.Now()
	for index, job := range s.pendingCleanup {
		if !now.Before(job.retryAfter) {
			due = append(due, job)
			delete(s.pendingCleanup, index)
		}
	}
	s.cleanupMu.Unlock()
	for _, job := range due {
		if ctx.Err() != nil {
			s.queueCleanup(job)
			continue
		}
		s.runCleanupJob(ctx, job)
	}
}

func (s *Server) recheckCleanup(ctx context.Context, interval time.Duration) {
	s.retryCleanupJobs(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.retryCleanupJobs(ctx)
		}
	}
}

// cleanStaleSessions runs after startup drains active and retired cgroups.
func (s *Server) cleanStaleSessions(ctx context.Context) error {
	entries, err := os.ReadDir(s.cfg.SessionsDir)
	if err != nil {
		return fmt.Errorf("sshdemo: read sessions-dir for stale cleanup: %w", err)
	}
	if s.cfg.Isolation == IsolationProcess {
		for _, entry := range entries {
			if entry.IsDir() {
				dir := filepath.Join(s.cfg.SessionsDir, entry.Name())
				if _, err := s.cleanWorkspace(ctx, dir, os.Getuid(), os.Getgid()); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for _, entry := range entries {
		name := entry.Name()
		index, err := strconv.Atoi(strings.TrimPrefix(name, "slot-"))
		if err != nil || index < 0 || name != slotName(index) || uint64(s.cfg.UIDBase)+uint64(index) > maxSlotUID {
			return fmt.Errorf("sshdemo: unexpected entry %q in UID-mode sessions-dir; see the SSH demo runbook for the required slot layout", name)
		}
		if err := s.cleanStaleSlot(index); err != nil {
			return err
		}
	}
	for index := range s.cfg.MaxSessions {
		if err := s.ensureSlotParent(index); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) cleanStaleSlot(index int) error {
	parent := filepath.Join(s.cfg.SessionsDir, slotName(index))
	if err := checkSlotParent(parent, s.cfg.UIDBase+index, s.ops); err != nil {
		return err
	}
	if index < s.cfg.MaxSessions && s.slots.isQuarantined(index) {
		return nil
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("sshdemo: read stale slot directory %s: %w", parent, err)
	}
	uid := s.cfg.UIDBase + index
	job := &cleanupJob{slot: Slot{Index: index, UID: uid, GID: uid}}
	for _, entry := range entries {
		dir := filepath.Join(parent, entry.Name())
		fi, err := s.ops.Stat(dir)
		if err != nil {
			return fmt.Errorf("sshdemo: stat stale workspace %s: %w", dir, err)
		}
		owner, ok := s.ops.FileUID(fi)
		if !fi.IsDir() || !ok || owner != uid {
			return fmt.Errorf("sshdemo: stale workspace %s must be a directory owned by slot UID %d", dir, uid)
		}
		job.dirs = append(job.dirs, dir)
	}
	if len(job.dirs) != 0 {
		s.queueCleanup(job)
	}
	return nil
}

func (s *Server) ensureSlotParent(index int) error {
	parent := filepath.Join(s.cfg.SessionsDir, slotName(index))
	gid := s.cfg.UIDBase + index
	err := os.Mkdir(parent, 0o700)
	if err == nil {
		if err := initializeSlotParent(parent, gid, s.ops); err != nil {
			_ = os.Remove(parent)
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("sshdemo: create slot parent %s: %w", parent, err)
	}
	return checkSlotParent(parent, gid, s.ops)
}

func initializeSlotParent(parent string, gid int, ops SysOps) error {
	if err := ops.Chown(parent, 0, gid); err != nil {
		return fmt.Errorf("sshdemo: set slot parent ownership %s: %w", parent, err)
	}
	if err := setWorkspaceMode(parent, 0o710); err != nil {
		return fmt.Errorf("sshdemo: set slot parent permissions %s: %w", parent, err)
	}
	return nil
}

func checkSlotParent(parent string, gid int, ops SysOps) error {
	fi, err := ops.Stat(parent)
	if err != nil {
		return fmt.Errorf("sshdemo: stat slot parent %s: %w", parent, err)
	}
	owner, ownerOK := ops.FileUID(fi)
	group, groupOK := ops.FileGID(fi)
	if fi.Mode() != os.ModeDir|0o710 || !ownerOK || owner != 0 || !groupOK || group != gid {
		return fmt.Errorf("sshdemo: slot parent %s must be a root-owned directory with GID %d and mode 0710", parent, gid)
	}
	return nil
}
