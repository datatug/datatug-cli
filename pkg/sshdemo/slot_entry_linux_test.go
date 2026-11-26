package sshdemo

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestEnterSlotChecksEveryStep(t *testing.T) {
	const validStatus = "Name:\tdatatug\nUid:\t21000\t21000\t21000\t21000\nGid:\t21001\t21001\t21001\t21001\n"
	const validCgroup = "0::/slot-0\n"
	for _, tc := range []struct {
		name, failAt, secondStatus, lastCgroup, want string
	}{
		{name: "success", lastCgroup: validCgroup},
		{name: "write fails", failAt: "join", want: "join slot cgroup"},
		{name: "initial cgroup unreadable", failAt: "initial cgroup", want: "read /proc/self/cgroup"},
		{name: "initial cgroup mismatch", failAt: "wrong initial cgroup", want: "does not report slot"},
		{name: "clear groups fails", failAt: "groups", want: "clear supplementary groups"},
		{name: "setgid fails", failAt: "gid", want: "set slot GID"},
		{name: "setuid fails", failAt: "uid", want: "set slot UID"},
		{name: "task list unreadable", failAt: "threads", want: "list process threads"},
		{name: "task list empty", failAt: "empty task list", want: "no process threads"},
		{name: "second thread unreadable", failAt: "102", want: "read /proc/self/task/102/status"},
		{name: "second thread has root saved UID", secondStatus: strings.Replace(validStatus, "21000\t21000\t21000", "21000\t21000\t0", 1), want: "/102/status: real, effective and saved UIDs"},
		{name: "second thread has root effective GID", secondStatus: strings.Replace(validStatus, "21001\t21001\t21001", "21001\t0\t21001", 1), want: "/102/status: real, effective and saved GIDs"},
		{name: "cannot verify truncated UIDs", secondStatus: "Uid:\t21000\t21000\nGid:\t21001\t21001\t21001\n", want: "saved UIDs"},
		{name: "cannot verify missing GIDs", secondStatus: "Uid:\t21000\t21000\t21000\n", want: "saved GIDs"},
		{name: "regained root", failAt: "regained root", want: "verify setuid(0) is denied"},
		{name: "unexpected root check error", failAt: "verify setuid", want: "verify setuid(0) is denied"},
		{name: "final cgroup unreadable", failAt: "final cgroup", want: "read /proc/self/cgroup"},
		{name: "final cgroup mismatch", lastCgroup: "0::/slot-00\n", want: "does not report slot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			slotDir := filepath.Join(root, "slot-0")
			tasksDir := filepath.Join(root, "proc", "task")
			for _, dir := range []string{slotDir, tasksDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeFixtureFile(t, slotDir, "cgroup.procs", "")
			secondStatus := tc.secondStatus
			if secondStatus == "" {
				secondStatus = validStatus
			}
			for tid, status := range map[string]string{"101": validStatus, "102": secondStatus} {
				dir := filepath.Join(tasksDir, tid)
				if tc.failAt == "empty task list" {
					break
				}
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, dir, "status", status)
			}
			original := enterSlotOps
			t.Cleanup(func() { enterSlotOps = original })
			var calls []string
			record := func(call string) error {
				calls = append(calls, call)
				if call == tc.failAt {
					return errTestIO
				}
				return nil
			}
			cgroupReads := 0
			enterSlotOps = slotEntryOps{
				write: func(path, value string) error {
					if path != filepath.Join(slotDir, "cgroup.procs") || value != "0" {
						t.Fatalf("unexpected migration: write %q to %s", value, path)
					}
					if err := record("join"); err != nil {
						return err
					}
					return original.write(path, value)
				},
				read: func(path string) ([]byte, error) {
					if path == "/proc/self/cgroup" {
						cgroupReads++
						if cgroupReads == 1 {
							data := validCgroup
							if tc.failAt == "wrong initial cgroup" {
								data = "0::/supervisor\n"
							}
							return []byte(data), record("initial cgroup")
						}
						return []byte(tc.lastCgroup), record("final cgroup")
					}
					if err := record(filepath.Base(filepath.Dir(path))); err != nil {
						return nil, err
					}
					return os.ReadFile(filepath.Join(root, "proc", strings.TrimPrefix(path, "/proc/self/")))
				},
				readDir: func(path string) ([]os.DirEntry, error) {
					if path != "/proc/self/task" {
						t.Fatalf("unexpected task directory: %s", path)
					}
					if err := record("threads"); err != nil {
						return nil, err
					}
					return os.ReadDir(tasksDir)
				},
				setgroups: func(groups []int) error {
					if len(groups) != 0 {
						t.Fatalf("unexpected groups: %v", groups)
					}
					return record("groups")
				},
				setgid: func(gid int) error {
					if gid != 21001 {
						t.Fatalf("unexpected GID: %d", gid)
					}
					return record("gid")
				},
				setuid: func(uid int) error {
					switch uid {
					case 21000:
						return record("uid")
					case 0:
						if err := record("verify setuid"); err != nil || tc.failAt == "regained root" {
							return err
						}
						return syscall.EPERM
					default:
						t.Fatalf("unexpected UID: %d", uid)
						return nil
					}
				},
			}
			err := EnterSlot(SlotEntry{CgroupDir: slotDir, UID: 21000, GID: 21001})
			if tc.want != "" {
				requireErrorContains(t, err, tc.want)
				if tc.failAt == "wrong initial cgroup" && !reflect.DeepEqual(calls, []string{"join", "initial cgroup"}) {
					t.Fatalf("continued after invalid cgroup membership: %v", calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"join", "initial cgroup", "groups", "gid", "uid", "threads", "101", "102", "verify setuid", "final cgroup"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
			if raw, err := os.ReadFile(filepath.Join(slotDir, "cgroup.procs")); err != nil || string(raw) != "0" {
				t.Fatalf("cgroup.procs = %q, %v", raw, err)
			}
		})
	}
}

func TestEnterSlotRejectsInvalidCredentials(t *testing.T) {
	reservedID := uint64(math.MaxUint32)
	for _, entry := range []SlotEntry{
		{UID: 21000, GID: 21001},
		{CgroupDir: "slot-0", UID: -1, GID: 21001},
		{CgroupDir: "slot-0", UID: 21000},
		{CgroupDir: "slot-0", UID: int(reservedID), GID: 21001},
		{CgroupDir: "slot-0", UID: 21000, GID: int(reservedID)},
	} {
		requireErrorContains(t, EnterSlot(entry), "requires a cgroup directory and non-root UID and GID")
	}
}

func TestWaitForSlotEntryRejectsUnreadableCgroupProcs(t *testing.T) {
	group := cgroup{path: t.TempDir(), fs: defaultCgroupFS()}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, err = writer.WriteString("1")
	if err := errors.Join(err, writer.Close()); err != nil {
		t.Fatal(err)
	}
	requireErrorContains(t, group.waitForSlotEntry(t.Context(), reader, 2001), "read slot cgroup.procs")
}
