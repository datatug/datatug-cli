//go:build unix

package commands

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Seams over unix.Getrlimit and unix.Setrlimit for deterministic unit tests.
var (
	demoSSHGetrlimit = unix.Getrlimit
	demoSSHSetrlimit = unix.Setrlimit
)

func applyDemoSSHRlimits(limits demoSSHRlimits) error {
	specs := []struct {
		name      string
		resource  int
		want      uint64
		allowZero bool
	}{
		{"RLIMIT_CORE", unix.RLIMIT_CORE, 0, true},
		{"RLIMIT_AS", unix.RLIMIT_AS, limits.AS, false},
		{"RLIMIT_CPU", unix.RLIMIT_CPU, limits.CPU, false},
		{"RLIMIT_FSIZE", unix.RLIMIT_FSIZE, limits.FSize, false},
		{"RLIMIT_NOFILE", unix.RLIMIT_NOFILE, limits.NoFile, false},
		{"RLIMIT_NPROC", unix.RLIMIT_NPROC, limits.NProc, false},
	}
	for _, s := range specs {
		if !s.allowZero && s.want == 0 {
			return fmt.Errorf("%s must be > 0", s.name)
		}
		if err := lowerRlimit(s.resource, s.want); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

func lowerRlimit(resource int, want uint64) error {
	var cur unix.Rlimit
	if err := demoSSHGetrlimit(resource, &cur); err != nil {
		return fmt.Errorf("getrlimit: %w", err)
	}
	next := computeLoweredRlimit(cur, want)
	if err := demoSSHSetrlimit(resource, &next); err != nil {
		return fmt.Errorf("setrlimit: %w", err)
	}
	return nil
}

func computeLoweredRlimit(cur unix.Rlimit, want uint64) unix.Rlimit {
	next := cur
	if cur.Max == unix.RLIM_INFINITY || want < cur.Max {
		next.Max = want
	}
	if cur.Cur == unix.RLIM_INFINITY || want < cur.Cur {
		next.Cur = want
	}
	if next.Cur > next.Max {
		next.Cur = next.Max
	}
	return next
}

// raiseDemoSSHOOMScore makes this session child a preferred OOM-killer victim.
func raiseDemoSSHOOMScore() error {
	return os.WriteFile(demoSSHOOMScoreAdjPath, []byte("900"), 0)
}

var demoSSHOOMScoreAdjPath = "/proc/self/oom_score_adj"

func fileOwnedByUID(info os.FileInfo, uid int) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == uid
}
