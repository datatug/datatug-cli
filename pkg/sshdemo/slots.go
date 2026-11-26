//go:build linux

package sshdemo

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

var (
	// ErrGlobalLimit is returned when all session slots are occupied.
	ErrGlobalLimit = errors.New("sshdemo: global session limit reached")
	// ErrPerIPLimit is returned when a client IP reached its concurrent session cap.
	ErrPerIPLimit = errors.New("sshdemo: per-ip session limit reached")
)

// Slot identifies one reserved session slot and its Unix credentials.
type Slot struct {
	Index    int
	UID      int
	GID      int
	ClientIP string
}

type slotQuarantine uint8

const (
	slotUsable slotQuarantine = iota
	slotAwaitingMemory
	slotQuarantined
)

// SlotManager tracks concurrent session slots and per-IP caps.
type SlotManager struct {
	mu          sync.Mutex
	maxSessions int
	maxPerIP    int
	uidBase     int
	isolation   string
	inUse       []bool
	quarantined []slotQuarantine
	perIP       map[string]int
	getUID      func() int
	getGID      func() int
}

// NewSlotManager creates a SlotManager for the given limits and isolation mode.
func NewSlotManager(maxSessions, maxPerIP, uidBase int, isolation string) *SlotManager {
	return &SlotManager{
		maxSessions: maxSessions,
		maxPerIP:    maxPerIP,
		uidBase:     uidBase,
		isolation:   isolation,
		inUse:       make([]bool, maxSessions),
		quarantined: make([]slotQuarantine, maxSessions),
		perIP:       make(map[string]int),
		getUID:      os.Getuid,
		getGID:      os.Getgid,
	}
}

// Acquire reserves a free slot for clientIP or returns ErrPerIPLimit / ErrGlobalLimit.
func (m *SlotManager) Acquire(clientIP string) (Slot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.perIP[clientIP] >= m.maxPerIP {
		return Slot{}, ErrPerIPLimit
	}
	idx := -1
	for i, used := range m.inUse {
		if !used && m.quarantined[i] == slotUsable {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Slot{}, ErrGlobalLimit
	}
	m.inUse[idx] = true
	m.perIP[clientIP]++

	uid, gid := m.getUID(), m.getGID()
	if m.isolation == IsolationUID {
		uid = m.uidBase + idx
		gid = m.uidBase + idx
	}
	return Slot{
		Index:    idx,
		UID:      uid,
		GID:      gid,
		ClientIP: clientIP,
	}, nil
}

// Release frees a previously acquired slot. Calling Release on an already-free
// slot is a no-op.
func (m *SlotManager) Release(slot Slot) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if slot.Index < 0 || slot.Index >= len(m.inUse) || !m.inUse[slot.Index] {
		return
	}
	m.inUse[slot.Index] = false
	if count := m.perIP[slot.ClientIP]; count <= 1 {
		delete(m.perIP, slot.ClientIP)
	} else {
		m.perIP[slot.ClientIP] = count - 1
	}
}

// InSlotRange reports whether uid falls within [uidBase, uidBase+maxSessions).
func (m *SlotManager) InSlotRange(uid int) bool {
	return uidInRange(uid, m.uidBase, m.maxSessions)
}

// quarantine permanently removes a slot from the allocator. It returns true
// when no slot remains usable.
func (m *SlotManager) quarantine(index int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quarantined[index] = slotQuarantined
	for _, quarantined := range m.quarantined {
		if quarantined != slotQuarantined {
			return false
		}
	}
	return true
}

func (m *SlotManager) isQuarantined(index int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quarantined[index] != slotUsable
}

func (m *SlotManager) setMemoryQuarantine(index int, pending bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pending {
		m.quarantined[index] = slotAwaitingMemory
	} else if m.quarantined[index] == slotAwaitingMemory {
		m.quarantined[index] = slotUsable
	}
}

func (m *SlotManager) awaitingMemory(index int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quarantined[index] == slotAwaitingMemory
}

func (m *SlotManager) quarantinedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, state := range m.quarantined {
		if state != slotUsable {
			n++
		}
	}
	return n
}

// ActiveCount returns the number of currently occupied slots.
func (m *SlotManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, used := range m.inUse {
		if used {
			n++
		}
	}
	return n
}

// SysOps groups OS-level operations used for uid isolation so unit tests can
// verify uid-mode paths without running as root.
type SysOps struct {
	FilesystemCapacity func(string) (size, inodes uint64, err error)
	cgroups            cgroupFS
	Stat               func(string) (os.FileInfo, error) // Lstat semantics for session paths.
	StatSharedTemp     func(string) (os.FileInfo, error) // Stat follows links to shared temporary directories.
	Chown              func(string, int, int) error
	Fchown             func(*os.File, int, int) error
	Kill               func(int, syscall.Signal) error
	FileUID            func(os.FileInfo) (int, bool)
}

// DefaultSysOps returns production OS operations backed by Linux syscalls and cgroupfs.
func DefaultSysOps() SysOps {
	return SysOps{
		FilesystemCapacity: filesystemCapacity,
		cgroups:            defaultCgroupFS(),
		Stat:               os.Lstat,
		StatSharedTemp:     os.Stat,
		Chown:              os.Chown,
		Fchown:             (*os.File).Chown,
		Kill:               syscall.Kill,
		FileUID:            defaultFileUID,
	}
}

func (o SysOps) withDefaults() SysOps {
	def := DefaultSysOps()
	if o.FilesystemCapacity == nil {
		o.FilesystemCapacity = def.FilesystemCapacity
	}
	if o.cgroups.read == nil {
		o.cgroups = def.cgroups
	}
	if o.Stat == nil {
		o.Stat = def.Stat
	}
	if o.StatSharedTemp == nil {
		o.StatSharedTemp = def.StatSharedTemp
	}
	if o.Chown == nil {
		o.Chown = def.Chown
	}
	if o.Fchown == nil {
		o.Fchown = def.Fchown
	}
	if o.Kill == nil {
		o.Kill = def.Kill
	}
	if o.FileUID == nil {
		o.FileUID = def.FileUID
	}
	return o
}

func defaultFileUID(fi os.FileInfo) (int, bool) {
	if fi == nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// checkSessionsDir verifies that sessionsDir satisfies isolation requirements:
// in uid mode it must already exist, be a directory owned by uid 0, and have
// exact mode 0711; in process mode it is created with mode 0700 if absent.
func checkSessionsDir(sessionsDir, isolation string, ops SysOps) error {
	fi, err := ops.Stat(sessionsDir)
	if isolation == IsolationProcess && errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
			return fmt.Errorf("sshdemo: create sessions-dir %s: %w", sessionsDir, err)
		}
		fi, err = ops.Stat(sessionsDir)
	}
	if err != nil {
		return fmt.Errorf("sshdemo: stat sessions-dir %s: %w", sessionsDir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("sshdemo: sessions-dir %s is not a directory", sessionsDir)
	}
	if isolation == IsolationUID {
		uid, ok := ops.FileUID(fi)
		if !ok || uid != 0 {
			return fmt.Errorf("sshdemo: sessions-dir %s must be owned by root (uid 0), got uid %d", sessionsDir, uid)
		}
		if perm := fi.Mode().Perm(); perm != 0o711 {
			return fmt.Errorf("sshdemo: sessions-dir %s must have mode 0711, got %04o", sessionsDir, perm)
		}
	}
	return nil
}

func checkSharedTempDirs(stat func(string) (os.FileInfo, error)) error {
	for _, path := range []string{"/tmp", "/dev/shm"} {
		info, err := stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("sshdemo: stat shared temporary path %s: %w", path, err)
		}
		if info.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("sshdemo: shared temporary path %s is writable by others in uid isolation mode", path)
		}
	}
	return nil
}
