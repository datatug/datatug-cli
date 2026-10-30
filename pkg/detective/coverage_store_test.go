package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failAudit struct{}

func (failAudit) Append(_ context.Context, _ AuditEvent) error {
	return errors.New("audit append failed")
}

type mockLocker struct {
	ok  bool
	err error
}

func (m mockLocker) TryLockContext(_ context.Context, _ time.Duration) (bool, error) {
	return m.ok, m.err
}

func (mockLocker) Unlock() error { return nil }

type mockTempFile struct {
	name     string
	chmodErr error
	writeErr error
	syncErr  error
	closeErr error
}

func (m mockTempFile) Chmod(os.FileMode) error { return m.chmodErr }
func (m mockTempFile) Write([]byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return 10, nil
}
func (m mockTempFile) Sync() error  { return m.syncErr }
func (m mockTempFile) Close() error { return m.closeErr }
func (m mockTempFile) Name() string { return m.name }

type mockAuditFile struct {
	writeErr error
	syncErr  error
	closeErr error
}

func (m mockAuditFile) Write([]byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return 10, nil
}
func (m mockAuditFile) Sync() error  { return m.syncErr }
func (m mockAuditFile) Close() error { return m.closeErr }

// Not parallel: tests filesystem modifications and permissions.
func TestCoverage100_Store_InitAndHelpers(t *testing.T) {
	ctx := context.Background()
	temp := t.TempDir()
	proj := filepath.Join(temp, "p")
	priv := filepath.Join(temp, "priv")
	_ = os.Mkdir(proj, 0700)
	_ = os.Mkdir(priv, 0700)

	// 1. NewLocalStore argument validation
	if _, err := NewLocalStore("", proj, priv); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty projectID: %v", err)
	}
	if _, err := NewLocalStore("p\n", proj, priv); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for newline in projectID: %v", err)
	}
	if _, err := NewLocalStore(strings.Repeat("a", 513), proj, priv); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for long projectID: %v", err)
	}
	if _, err := NewLocalStore("p", "", priv); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty projectDir: %v", err)
	}
	if _, err := NewLocalStore("p", proj, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty privateBase: %v", err)
	}

	// within checks
	subPriv := filepath.Join(proj, "subpriv")
	if _, err := NewLocalStore("p", proj, subPriv); err == nil || !strings.Contains(err.Error(), "outside project") {
		t.Fatalf("expected outside project error: %v", err)
	}
	subProj := filepath.Join(priv, "subproj")
	_ = os.Mkdir(subProj, 0700)
	if _, err := NewLocalStore("p", subProj, priv); err == nil || !strings.Contains(err.Error(), "outside project") {
		t.Fatalf("expected outside project error: %v", err)
	}

	// container/private failures: make container a regular file
	badBase := filepath.Join(temp, "badbase")
	_ = os.Mkdir(badBase, 0700)
	_ = os.WriteFile(filepath.Join(badBase, "detective"), []byte("file"), 0600)
	if _, err := NewLocalStore("p", proj, badBase); err == nil {
		t.Fatalf("expected error when detective container is a regular file")
	}

	// lockPath is a directory
	badLockBase := filepath.Join(temp, "badlockbase")
	_ = os.Mkdir(badLockBase, 0700)
	projEval, _ := filepath.EvalSymlinks(proj)
	sum := sha256.Sum256([]byte(projEval))
	targetPriv := filepath.Join(badLockBase, "detective", hex.EncodeToString(sum[:16]))
	_ = os.MkdirAll(targetPriv, 0700)
	_ = os.Mkdir(filepath.Join(targetPriv, "store.lock"), 0700)
	if _, err := NewLocalStore("p", proj, badLockBase); err == nil {
		t.Fatalf("expected error when store.lock is a directory")
	}
	_ = os.Remove(filepath.Join(targetPriv, "store.lock"))

	// auditPath is a directory
	_ = os.Mkdir(filepath.Join(targetPriv, "audit.jsonl"), 0700)
	if _, err := NewLocalStore("p", proj, badLockBase); err == nil {
		t.Fatalf("expected error when audit.jsonl is a directory")
	}
	_ = os.Remove(filepath.Join(targetPriv, "audit.jsonl"))

	// scope conflict or corrupt
	_ = os.WriteFile(filepath.Join(targetPriv, "scope.json"), []byte(`{"projectId":"other"}`), 0600)
	if _, err := NewLocalStore("p", proj, badLockBase); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for scope project mismatch: %v", err)
	}
	_ = os.WriteFile(filepath.Join(targetPriv, "scope.json"), []byte(`corrupt`), 0600)
	if _, err := NewLocalStore("p", proj, badLockBase); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for corrupt scope: %v", err)
	}
	// scope is a directory -> safeReadFile error
	_ = os.Remove(filepath.Join(targetPriv, "scope.json"))
	_ = os.Mkdir(filepath.Join(targetPriv, "scope.json"), 0700)
	if _, err := NewLocalStore("p", proj, badLockBase); err == nil {
		t.Fatalf("expected error when scope.json is a directory")
	}
	_ = os.Remove(filepath.Join(targetPriv, "scope.json"))

	// Successful NewLocalStore and NewLocalStoreWithAudit
	s, err := NewLocalStore("p", proj, priv)
	if err != nil {
		t.Fatalf("NewLocalStore failed: %v", err)
	}
	// Re-opening existing store with same projectID
	sReopen, err := NewLocalStore("p", proj, priv)
	if err != nil || sReopen == nil {
		t.Fatalf("reopening store failed: %v", err)
	}

	// NewLocalStoreWithAudit with nil audit and custom audit
	sNilAudit, err := NewLocalStoreWithAudit("p", proj, priv, nil)
	if err != nil || sNilAudit == nil {
		t.Fatalf("NewLocalStoreWithAudit with nil audit failed: %v", err)
	}
	sCustomAudit, err := NewLocalStoreWithAudit("p", proj, priv, failAudit{})
	if err != nil || sCustomAudit == nil {
		t.Fatalf("NewLocalStoreWithAudit with custom audit failed: %v", err)
	}

	// 2. ensureNoSymlink
	missingPath := filepath.Join(temp, "non-existent")
	if err := ensureNoSymlink(missingPath); err == nil {
		t.Fatalf("expected error for missing path in ensureNoSymlink")
	}
	regFile := filepath.Join(temp, "reg.txt")
	_ = os.WriteFile(regFile, []byte("ok"), 0600)
	if err := ensureNoSymlink(regFile); err != nil {
		t.Fatalf("expected nil for regular file: %v", err)
	}
	symFile := filepath.Join(temp, "sym.txt")
	_ = os.Symlink(regFile, symFile)
	if err := ensureNoSymlink(symFile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for symlink in ensureNoSymlink: %v", err)
	}

	// 3. ensureDir
	subDir := filepath.Join(temp, "subdir")
	if err := ensureDir(subDir); err != nil {
		t.Fatalf("ensureDir failed: %v", err)
	}
	// already exists as dir
	if err := ensureDir(subDir); err != nil {
		t.Fatalf("ensureDir on existing dir failed: %v", err)
	}
	// exists as regular file
	if err := ensureDir(regFile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when ensureDir on regular file: %v", err)
	}
	// exists as symlink to dir
	symDir := filepath.Join(temp, "symdir")
	_ = os.Symlink(subDir, symDir)
	if err := ensureDir(symDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when ensureDir on symlink dir: %v", err)
	}

	// 4. ensureAbsentOrRegular
	if err := ensureAbsentOrRegular(filepath.Join(temp, "absent")); err != nil {
		t.Fatalf("expected nil for absent: %v", err)
	}
	if err := ensureAbsentOrRegular(regFile); err != nil {
		t.Fatalf("expected nil for regular file: %v", err)
	}
	if err := ensureAbsentOrRegular(subDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for directory in ensureAbsentOrRegular: %v", err)
	}

	// 5. safeReadFile
	if _, err := safeReadFile(subDir, "bad slug!"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slug: %v", err)
	}
	// dir is a file
	if _, err := safeReadFile(regFile, "foo"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when dir is a file: %v", err)
	}
	// file > 1MB
	bigFile := filepath.Join(subDir, "big.json")
	_ = os.WriteFile(bigFile, make([]byte, 1<<20+10), 0600)
	if _, err := safeReadFile(subDir, "big"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for file > 1MB: %v", err)
	}
	_ = os.Remove(bigFile)

	// 6. ensureDirIfExists
	if err := ensureDirIfExists(filepath.Join(temp, "absent")); err != nil {
		t.Fatalf("expected nil for absent dir in ensureDirIfExists: %v", err)
	}
	if err := ensureDirIfExists(subDir); err != nil {
		t.Fatalf("expected nil for valid dir in ensureDirIfExists: %v", err)
	}
	if err := ensureDirIfExists(regFile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for regular file in ensureDirIfExists: %v", err)
	}

	// 7. withLock timeout and error
	origLock := s.lock
	// a) TryLockContext returns error
	s.lock = mockLocker{ok: false, err: errors.New("lock error")}
	if err := s.withLock(ctx, func() error { return nil }); err == nil || err.Error() != "lock error" {
		t.Fatalf("expected lock error: %v", err)
	}
	// b) TryLockContext returns !ok -> context.DeadlineExceeded
	s.lock = mockLocker{ok: false, err: nil}
	if err := s.withLock(ctx, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded: %v", err)
	}
	s.lock = origLock
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Store_ReadWriteItems(t *testing.T) {
	temp := t.TempDir()
	itemsDir := filepath.Join(temp, "items")
	_ = os.Mkdir(itemsDir, 0700)

	// 1. readItems
	// a) ensureDirIfExists error (dir is a regular file)
	dummyFile := filepath.Join(temp, "dummy.txt")
	_ = os.WriteFile(dummyFile, []byte("x"), 0600)
	if _, err := readItems[Check](dummyFile); err == nil {
		t.Fatalf("expected error when dir is a regular file in readItems")
	}

	// b) absent dir -> empty slice, nil
	absentDir := filepath.Join(temp, "non-existent-dir")
	items, err := readItems[Check](absentDir)
	if err != nil || len(items) != 0 {
		t.Fatalf("expected empty slice for absent dir: %v, %v", err, items)
	}

	// c) dir read error (chmod 0000)
	unreadableDir := filepath.Join(temp, "unreadable")
	_ = os.Mkdir(unreadableDir, 0700)
	_ = os.Chmod(unreadableDir, 0000)
	if _, err := readItems[Check](unreadableDir); err == nil {
		t.Fatalf("expected readDir error on unreadable dir")
	}
	_ = os.Chmod(unreadableDir, 0700)

	// d) dir containing subdirectories and non-.json files -> skipped
	_ = os.Mkdir(filepath.Join(itemsDir, "subdir"), 0700)
	_ = os.WriteFile(filepath.Join(itemsDir, "notes.txt"), []byte("txt"), 0600)
	items, err = readItems[Check](itemsDir)
	if err != nil || len(items) != 0 {
		t.Fatalf("expected 0 items with subdirs and txt files: %v, %v", err, items)
	}

	// e) invalid slug file name
	badSlugFile := filepath.Join(itemsDir, "bad..slug.json")
	_ = os.WriteFile(badSlugFile, []byte(`{}`), 0600)
	if _, err := readItems[Check](itemsDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slug in readItems: %v", err)
	}
	_ = os.Remove(badSlugFile)

	// f) item is a symlink
	validFile := filepath.Join(temp, "valid.json")
	_ = os.WriteFile(validFile, []byte(`{"id":"chk-1"}`), 0600)
	symItem := filepath.Join(itemsDir, "chk-1.json")
	_ = os.Symlink(validFile, symItem)
	if _, err := readItems[Check](itemsDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for symlink in readItems: %v", err)
	}
	_ = os.Remove(symItem)

	// g) item > 1MB
	bigItem := filepath.Join(itemsDir, "big-item.json")
	_ = os.WriteFile(bigItem, make([]byte, 1<<20+10), 0600)
	if _, err := readItems[Check](itemsDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for item > 1MB: %v", err)
	}
	_ = os.Remove(bigItem)

	// h) invalid JSON in item
	corruptItem := filepath.Join(itemsDir, "corrupt.json")
	_ = os.WriteFile(corruptItem, []byte(`{not json`), 0600)
	if _, err := readItems[Check](itemsDir); err == nil {
		t.Fatalf("expected unmarshal error on corrupt item in readItems")
	}
	_ = os.Remove(corruptItem)

	// 2. writeItem
	// a) invalid slug
	if err := writeItem(itemsDir, "bad slug!", Check{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slug in writeItem: %v", err)
	}

	// b) ensureDir error (parent is a file)
	if err := writeItem(dummyFile, "chk-1", Check{}); err == nil {
		t.Fatalf("expected ensureDir error when parent is a file in writeItem")
	}

	// c) dir is symlink
	symItemsDir := filepath.Join(temp, "symitems")
	_ = os.Symlink(itemsDir, symItemsDir)
	if err := writeItem(symItemsDir, "chk-1", Check{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for symlink dir in writeItem: %v", err)
	}

	// d) target path already exists as symlink
	targetSym := filepath.Join(itemsDir, "sym-target.json")
	_ = os.Symlink(dummyFile, targetSym)
	if err := writeItem(itemsDir, "sym-target", Check{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when target path is a symlink: %v", err)
	}
	_ = os.Remove(targetSym)

	// e) marshal error or data > 1MB
	hugeItem := Check{ID: "huge", Title: strings.Repeat("x", 1<<20)}
	if err := writeItem(itemsDir, "huge", hugeItem); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when data > 1MB: %v", err)
	}

	// f) CreateTemp error (dir is read-only chmod 0500)
	_ = os.Chmod(itemsDir, 0500)
	if err := writeItem(itemsDir, "chk-ro", Check{ID: "chk-ro"}); err == nil {
		t.Fatalf("expected CreateTemp error on read-only dir")
	}
	_ = os.Chmod(itemsDir, 0700)

	// g) successful write and read
	goodCheck := Check{ID: "chk-good", Title: "Good Check", Version: 1}
	if err := writeItem(itemsDir, "chk-good", goodCheck); err != nil {
		t.Fatalf("expected successful writeItem: %v", err)
	}
	loaded, err := readItems[Check](itemsDir)
	if err != nil || len(loaded) != 1 || loaded[0].ID != "chk-good" {
		t.Fatalf("expected loaded check: %v, %v", err, loaded)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Store_StateAndRunPages(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)

	// 1. State calls StatePage
	st, err := store.State(ctx)
	if err != nil {
		t.Fatalf("store.State failed: %v", err)
	}
	if !st.Capabilities.CanRead {
		t.Fatalf("expected CanRead: %+v", st)
	}

	// 2. StatePage read errors for various catalog dirs
	for _, dirName := range []string{"checks", "validators", "query-validators", "scenarios"} {
		dir := store.definitionDir(dirName)
		_ = os.RemoveAll(dir)
		_ = os.WriteFile(dir, []byte("file"), 0600)
		if _, err := store.StatePage(ctx, ""); err == nil {
			t.Fatalf("expected error in StatePage when %s is a file", dirName)
		}
		_ = os.Remove(dir)
		_ = os.Mkdir(dir, 0700)
	}

	// cases read error
	casesDir := store.privateItemDir("cases")
	_ = os.RemoveAll(casesDir)
	_ = os.WriteFile(casesDir, []byte("file"), 0600)
	if _, err := store.StatePage(ctx, ""); err == nil {
		t.Fatalf("expected error in StatePage when cases dir is a file")
	}
	_ = os.Remove(casesDir)
	_ = os.Mkdir(casesDir, 0700)

	// case with timeline > 50 -> trimmed to 50
	bigCase := Case{
		ID:       "case-big-tl",
		Title:    "Big TL",
		Status:   "active",
		Timeline: make([]CaseEvent, 55),
	}
	for i := range bigCase.Timeline {
		bigCase.Timeline[i] = CaseEvent{ID: fmt.Sprintf("ev-%d", i), Type: "note"}
	}
	_ = writeItem(casesDir, "case-big-tl", bigCase)
	stWithCase, err := store.StatePage(ctx, "")
	if err != nil {
		t.Fatalf("StatePage failed: %v", err)
	}
	var foundCase *Case
	for i := range stWithCase.Cases {
		if stWithCase.Cases[i].ID == "case-big-tl" {
			foundCase = &stWithCase.Cases[i]
			break
		}
	}
	if foundCase == nil || len(foundCase.Timeline) != 50 || foundCase.TimelineOffset != 5 {
		t.Fatalf("expected case timeline trimmed to 50 and offset 5: %+v", foundCase)
	}
	_ = os.Remove(filepath.Join(casesDir, "case-big-tl.json"))

	// 3. readRunPage
	runsDir := store.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)

	// a) cursor validation errors
	if _, _, _, err := readRunPage(runsDir, strings.Repeat("x", 181)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for cursor > 180: %v", err)
	}
	if _, _, _, err := readRunPage(runsDir, "cur/sor"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for cursor with slash: %v", err)
	}
	if _, _, _, err := readRunPage(runsDir, "cursor-no-run-marker"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for cursor missing :run-: %v", err)
	}

	// b) absent dir
	absentRuns := filepath.Join(store.privateDir, "absent-runs")
	runs, hasMore, nextCur, err := readRunPage(absentRuns, "")
	if err != nil || len(runs) != 0 || hasMore || nextCur != "" {
		t.Fatalf("expected empty for absent runs dir: %v, %v, %v, %v", err, runs, hasMore, nextCur)
	}

	// c) invalid slug in run file
	badRunFile := filepath.Join(runsDir, "bad..run.json")
	_ = os.WriteFile(badRunFile, []byte(`{}`), 0600)
	if _, _, _, err := readRunPage(runsDir, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad run slug: %v", err)
	}
	_ = os.Remove(badRunFile)

	// d) symlink run file
	targetRun := filepath.Join(store.privateDir, "real-run.json")
	_ = os.WriteFile(targetRun, []byte(`{"id":"run-sym"}`), 0600)
	symRunFile := filepath.Join(runsDir, "run-sym.json")
	_ = os.Symlink(targetRun, symRunFile)
	if _, _, _, err := readRunPage(runsDir, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for symlink in readRunPage: %v", err)
	}
	_ = os.Remove(symRunFile)
	_ = os.Remove(targetRun)

	// e) run file > 1MB
	bigRunFile := filepath.Join(runsDir, "run-big.json")
	_ = os.WriteFile(bigRunFile, make([]byte, 1<<20+10), 0600)
	if _, _, _, err := readRunPage(runsDir, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for run > 1MB: %v", err)
	}
	_ = os.Remove(bigRunFile)

	// f) corrupt run JSON
	corruptRun := filepath.Join(runsDir, "run-corrupt.json")
	_ = os.WriteFile(corruptRun, []byte(`{bad json`), 0600)
	if _, _, _, err := readRunPage(runsDir, ""); err == nil {
		t.Fatalf("expected unmarshal error on corrupt run")
	}
	_ = os.Remove(corruptRun)

	// g) pagination with 42 runs
	for i := 0; i < 42; i++ {
		rID := fmt.Sprintf("run-%03d", i)
		_ = writeItem(runsDir, rID, CheckRun{ID: rID})
	}
	page1, hasMore1, next1, err := readRunPage(runsDir, "")
	if err != nil || len(page1) != 40 || !hasMore1 || next1 == "" {
		t.Fatalf("expected 40 runs with hasMore true: %v, len=%d, hasMore=%v, next=%s", err, len(page1), hasMore1, next1)
	}
	page2, hasMore2, _, err := readRunPage(runsDir, next1)
	if err != nil || len(page2) != 2 || hasMore2 {
		t.Fatalf("expected 2 runs on second page: %v, len=%d, hasMore=%v", err, len(page2), hasMore2)
	}
	for i := 0; i < 42; i++ {
		_ = os.Remove(filepath.Join(runsDir, fmt.Sprintf("run-%03d.json", i)))
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Store_ChecksAndCases(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	actor := "alice"

	// 1. getCheck / GetCheck
	if _, err := store.getCheck("bad..check"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad check slug: %v", err)
	}
	if _, err := store.GetCheck(ctx, "non-existent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing check: %v", err)
	}
	chkDir := store.definitionDir("checks")
	_ = os.MkdirAll(chkDir, 0700)
	_ = os.WriteFile(filepath.Join(chkDir, "corrupt.json"), []byte(`{bad json`), 0600)
	if _, err := store.GetCheck(ctx, "corrupt"); err == nil {
		t.Fatalf("expected unmarshal error for corrupt check")
	}
	_ = os.Remove(filepath.Join(chkDir, "corrupt.json"))

	// 2. SaveCheck
	// a) validateCheck error
	if _, err := store.SaveCheck(ctx, Check{ID: ""}, actor); err == nil {
		t.Fatalf("expected validateCheck error for empty ID")
	}

	// b) step.Kind == "query" with resolveQueryRef error
	chkBadQuery := Check{
		ID:      "chk-bad-q",
		Title:   "Bad Q",
		Version: 0,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "non-existent-query"}},
	}
	if _, err := store.SaveCheck(ctx, chkBadQuery, actor); err == nil {
		t.Fatalf("expected resolveQueryRef error for non-existent query")
	}

	// c) Version conflict: existing check with mismatched version
	validCheckSteps := []Step{{ID: "s1", Kind: "query", QueryID: "q"}}
	_ = writeItem(chkDir, "chk-v1", Check{ID: "chk-v1", Title: "V1", Version: 1, Steps: validCheckSteps})
	chkV2Bad := Check{ID: "chk-v1", Title: "V1", Version: 2, Steps: validCheckSteps} // expected version 1
	if _, err := store.SaveCheck(ctx, chkV2Bad, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for version mismatch: %v", err)
	}
	// new check with version != 0 -> ErrConflict
	chkNewBadVer := Check{ID: "chk-new-v", Title: "New", Version: 2, Steps: validCheckSteps}
	if _, err := store.SaveCheck(ctx, chkNewBadVer, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new check with version!=0: %v", err)
	}

	// d) Capacity limit: len(all) >= maxItems
	existingChecks, _ := os.ReadDir(chkDir)
	neededChecks := maxItems - len(existingChecks)
	var chkCreated []string
	for i := 0; i < neededChecks; i++ {
		name := fmt.Sprintf("chk-cap-%04d", i)
		_ = writeItem(chkDir, name, Check{ID: name, Title: name, Version: 1, Steps: validCheckSteps})
		chkCreated = append(chkCreated, name)
	}
	chkOverflow := Check{ID: "chk-overflow", Title: "Overflow", Version: 0, Steps: validCheckSteps}
	if _, err := store.SaveCheck(ctx, chkOverflow, actor); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity for SaveCheck: %v", err)
	}
	for _, name := range chkCreated {
		_ = os.Remove(filepath.Join(chkDir, name+".json"))
	}
	_ = os.Remove(filepath.Join(chkDir, "chk-v1.json"))

	// e) readItems[PublishedValidator] error
	valDir := store.definitionDir("validators")
	_ = os.RemoveAll(valDir)
	_ = os.WriteFile(valDir, []byte("file"), 0600)
	chkVldErr := Check{ID: "chk-vld-err", Title: "Vld", Version: 0, Steps: validCheckSteps}
	if _, err := store.SaveCheck(ctx, chkVldErr, actor); err == nil {
		t.Fatalf("expected validators read error in SaveCheck")
	}
	_ = os.Remove(valDir)
	_ = os.Mkdir(valDir, 0700)

	// f) readItems[QueryValidators] error
	qvDir := store.definitionDir("query-validators")
	_ = os.RemoveAll(qvDir)
	_ = os.WriteFile(qvDir, []byte("file"), 0600)
	if _, err := store.SaveCheck(ctx, chkVldErr, actor); err == nil {
		t.Fatalf("expected query-validators read error in SaveCheck")
	}
	_ = os.Remove(qvDir)
	_ = os.Mkdir(qvDir, 0700)

	// g) validateGraph error: check step references missing check
	chkMissingDep := Check{
		ID:      "chk-dep-missing",
		Title:   "Dep Missing",
		Version: 0,
		Steps:   []Step{{ID: "s1", Kind: "check", CheckID: "non-existent-child"}},
	}
	if _, err := store.SaveCheck(ctx, chkMissingDep, actor); err == nil {
		t.Fatalf("expected validateGraph error for missing child check")
	}

	// h) writeItem error: checks dir chmod 0500
	_ = os.Chmod(chkDir, 0500)
	chkGood := Check{ID: "chk-good-save", Title: "Good Save", Version: 0, Steps: validCheckSteps}
	if _, err := store.SaveCheck(ctx, chkGood, actor); err == nil {
		t.Fatalf("expected writeItem error on read-only checks dir")
	}
	_ = os.Chmod(chkDir, 0700)

	// i) auditMutation error with failAudit
	storeWithFailAudit := *store
	storeWithFailAudit.audit = failAudit{}
	if _, err := storeWithFailAudit.SaveCheck(ctx, chkGood, actor); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from audit mutation failure: %v", err)
	}
	_ = os.Remove(filepath.Join(chkDir, chkGood.ID+".json"))

	// j) successful SaveCheck (create and update)
	savedChk, err := store.SaveCheck(ctx, chkGood, actor)
	if err != nil || savedChk.Version != 1 {
		t.Fatalf("expected successful SaveCheck create: %v, %+v", err, savedChk)
	}
	savedChk.Title = "Updated Title"
	updChk, err := store.SaveCheck(ctx, savedChk, actor)
	if err != nil || updChk.Version != 2 {
		t.Fatalf("expected successful SaveCheck update: %v, %+v", err, updChk)
	}

	// 3. getCase
	if _, err := store.getCase("bad..case"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad case slug: %v", err)
	}
	if _, err := store.getCase("missing-case"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}
	casesDir := store.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)
	_ = os.WriteFile(filepath.Join(casesDir, "corrupt.json"), []byte(`{bad json`), 0600)
	if _, err := store.getCase("corrupt"); err == nil {
		t.Fatalf("expected unmarshal error for corrupt case")
	}
	_ = os.Remove(filepath.Join(casesDir, "corrupt.json"))

	// 4. CaseEventsPage
	// a) invalid slug or negative before
	if _, err := store.CaseEventsPage(ctx, "bad..case", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slug in CaseEventsPage: %v", err)
	}
	if _, err := store.CaseEventsPage(ctx, "c-1", -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for negative before: %v", err)
	}

	// b) missing case
	if _, err := store.CaseEventsPage(ctx, "c-missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}

	// c) before > total -> ErrInvalid
	cBase := Case{
		ID:             "c-page-test",
		Title:          "Page Test",
		Status:         "active",
		Version:        1,
		TimelineOffset: 10,
		Timeline:       []CaseEvent{{ID: "ev-10", Type: "note"}, {ID: "ev-11", Type: "note"}},
	}
	_ = writeItem(casesDir, "c-page-test", cBase)
	if _, err := store.CaseEventsPage(ctx, "c-page-test", 20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for before > total: %v", err)
	}

	// d) chunk retrieval: dir is a file
	tlDir := store.privateItemDir("case-timeline")
	_ = os.RemoveAll(tlDir)
	_ = os.WriteFile(tlDir, []byte("file"), 0600)
	if _, err := store.CaseEventsPage(ctx, "c-page-test", 5); err == nil {
		t.Fatalf("expected error when case-timeline dir is a file")
	}
	_ = os.Remove(tlDir)
	_ = os.Mkdir(tlDir, 0700)

	// e) chunk read error on unreadable tlDir
	_ = os.Chmod(tlDir, 0000)
	if _, err := store.CaseEventsPage(ctx, "c-page-test", 5); err == nil {
		t.Fatalf("expected error on unreadable case-timeline dir")
	}
	_ = os.Chmod(tlDir, 0700)

	// f) chunk file parsing: non-matching prefix, corrupt json, mismatched CaseID or Start
	prefix := caseTimelinePrefix("c-page-test")
	// non-matching prefix
	_ = os.WriteFile(filepath.Join(tlDir, "other-prefix-000000000.json"), []byte(`{}`), 0600)
	// corrupt JSON
	corruptChunkName := fmt.Sprintf("%s000000000.json", prefix)
	_ = os.WriteFile(filepath.Join(tlDir, corruptChunkName), []byte(`{bad json`), 0600)
	if _, err := store.CaseEventsPage(ctx, "c-page-test", 5); err == nil {
		t.Fatalf("expected error on corrupt chunk JSON")
	}
	// mismatched CaseID
	badChunk := caseEventChunk{CaseID: "other-case", Start: 0, Events: []CaseEvent{{ID: "e0", Type: "note"}}}
	_ = writeItem(tlDir, strings.TrimSuffix(corruptChunkName, ".json"), badChunk)
	if _, err := store.CaseEventsPage(ctx, "c-page-test", 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for mismatched CaseID in chunk: %v", err)
	}
	// mismatched Start
	badChunk.CaseID = "c-page-test"
	badChunk.Start = 999
	_ = writeItem(tlDir, strings.TrimSuffix(corruptChunkName, ".json"), badChunk)
	if _, err := store.CaseEventsPage(ctx, "c-page-test", 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for mismatched Start in chunk: %v", err)
	}

	// g) missing events (gap in timeline -> filled has false)
	gapChunk := caseEventChunk{
		CaseID: "c-page-test",
		Start:  0,
		Events: []CaseEvent{{ID: "e0", Type: "note"}}, // only 1 event, but offset is 10!
	}
	_ = writeItem(tlDir, strings.TrimSuffix(corruptChunkName, ".json"), gapChunk)
	if _, err := store.CaseEventsPage(ctx, "c-page-test", 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for gap in timeline: %v", err)
	}

	// h) valid full chunk
	events10 := make([]CaseEvent, 10)
	for i := range events10 {
		events10[i] = CaseEvent{ID: fmt.Sprintf("ev-%d", i), Type: "note"}
	}
	goodChunk := caseEventChunk{CaseID: "c-page-test", Start: 0, Events: events10}
	_ = writeItem(tlDir, strings.TrimSuffix(corruptChunkName, ".json"), goodChunk)
	pg, err := store.CaseEventsPage(ctx, "c-page-test", 0) // before == 0 defaults to total (12)
	if err != nil || len(pg.Events) != 12 {
		t.Fatalf("expected 12 events across chunk and case timeline: %v, len=%d", err, len(pg.Events))
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Store_MutationsAndAudit(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	actor := "alice"

	// 1. SaveRun
	// a) invalid slug
	if err := store.SaveRun(ctx, CheckRun{ID: "bad..run"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad run slug: %v", err)
	}
	// b) conflict (already exists)
	runsDir := store.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)
	_ = writeItem(runsDir, "run-existing", CheckRun{ID: "run-existing"})
	if err := store.SaveRun(ctx, CheckRun{ID: "run-existing"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for existing run: %v", err)
	}
	// c) write error (chmod 0500)
	_ = os.Chmod(runsDir, 0500)
	if err := store.SaveRun(ctx, CheckRun{ID: "run-ro"}); err == nil {
		t.Fatalf("expected write error on read-only runs dir")
	}
	_ = os.Chmod(runsDir, 0700)
	// d) audit error with failAudit
	storeWithFailAudit := *store
	storeWithFailAudit.audit = failAudit{}
	if err := storeWithFailAudit.SaveRun(ctx, CheckRun{ID: "run-audit-fail"}); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from audit failure: %v", err)
	}
	// e) successful SaveRun
	if err := store.SaveRun(ctx, CheckRun{ID: "run-ok", Kind: "check", Actor: "alice", Outcome: "passed", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("expected successful SaveRun: %v", err)
	}

	// 2. SaveScenario
	// a) ValidateScenario error
	if _, err := store.SaveScenario(ctx, Scenario{ID: ""}, actor); err == nil {
		t.Fatalf("expected ValidateScenario error for empty ID")
	}
	// b) step query ref error
	scenBadQuery := Scenario{
		ID:      "scen-bad-q",
		Title:   "Bad Q",
		Version: 0,
		Steps:   []ScenarioStep{{ID: "s1", Kind: "query", Ref: "non-existent-query"}},
	}
	if _, err := store.SaveScenario(ctx, scenBadQuery, actor); err == nil {
		t.Fatalf("expected query ref error for missing query")
	}
	// c) step check ref error
	scenBadCheck := Scenario{
		ID:      "scen-bad-chk",
		Title:   "Bad Chk",
		Version: 0,
		Steps:   []ScenarioStep{{ID: "s1", Kind: "check", Ref: "non-existent-check"}},
	}
	if _, err := store.SaveScenario(ctx, scenBadCheck, actor); err == nil {
		t.Fatalf("expected getCheck error for missing check ref")
	}
	// d) version conflict
	scenDir := store.definitionDir("scenarios")
	_ = os.MkdirAll(scenDir, 0700)
	validScenSteps := []ScenarioStep{{ID: "s1", Kind: "query", Ref: "q"}}
	_ = writeItem(scenDir, "scen-v1", Scenario{ID: "scen-v1", Title: "V1", Version: 1, Steps: validScenSteps})
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-v1", Title: "V1", Version: 2, Steps: validScenSteps}, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for scenario version mismatch")
	}
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-new", Title: "New", Version: 2, Steps: validScenSteps}, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new scenario with version!=0")
	}
	// e) corrupt old scenario
	_ = os.WriteFile(filepath.Join(scenDir, "scen-corrupt.json"), []byte(`{bad json`), 0600)
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-corrupt", Title: "Corrupt", Version: 1, Steps: validScenSteps}, actor); err == nil {
		t.Fatalf("expected unmarshal error on corrupt old scenario")
	}
	_ = os.Remove(filepath.Join(scenDir, "scen-corrupt.json"))
	// f) capacity limit
	existingScens, _ := os.ReadDir(scenDir)
	neededScens := maxItems - len(existingScens)
	var scenCreated []string
	for i := 0; i < neededScens; i++ {
		name := fmt.Sprintf("scen-cap-%04d", i)
		_ = writeItem(scenDir, name, Scenario{ID: name, Title: name, Version: 1, Steps: validScenSteps})
		scenCreated = append(scenCreated, name)
	}
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-overflow", Title: "Overflow", Version: 0, Steps: validScenSteps}, actor); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity for SaveScenario: %v", err)
	}
	for _, name := range scenCreated {
		_ = os.Remove(filepath.Join(scenDir, name+".json"))
	}
	_ = os.Remove(filepath.Join(scenDir, "scen-v1.json"))
	// g) audit failure
	scenGood := Scenario{ID: "scen-good", Title: "Good", Version: 0, Steps: validScenSteps}
	if _, err := storeWithFailAudit.SaveScenario(ctx, scenGood, actor); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from audit failure: %v", err)
	}
	_ = os.Remove(filepath.Join(scenDir, scenGood.ID+".json"))
	// h) successful SaveScenario create & update
	scenSaved, err := store.SaveScenario(ctx, scenGood, actor)
	if err != nil || scenSaved.Version != 1 {
		t.Fatalf("expected successful SaveScenario create: %v, %+v", err, scenSaved)
	}
	scenSaved.Title = "Updated"
	scenUpd, err := store.SaveScenario(ctx, scenSaved, actor)
	if err != nil || scenUpd.Version != 2 {
		t.Fatalf("expected successful SaveScenario update: %v, %+v", err, scenUpd)
	}

	// 3. SaveValidator
	// a) ValidatePublishedValidator error
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: ""}, actor); err == nil {
		t.Fatalf("expected ValidatePublishedValidator error for empty ID")
	}
	valDir := store.definitionDir("validators")
	_ = os.MkdirAll(valDir, 0700)
	valPayload := Validator{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}
	// b) version conflict
	_ = writeItem(valDir, "val-v1", PublishedValidator{ID: "val-v1", Title: "V1", Version: 1, Validator: valPayload})
	valMismatch := PublishedValidator{ID: "val-v1", Title: "V1", Version: 2, Validator: valPayload}
	if _, err := store.SaveValidator(ctx, valMismatch, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for validator version mismatch: %v", err)
	}
	valNewMismatch := PublishedValidator{ID: "val-new", Title: "New", Version: 2, Validator: valPayload}
	if _, err := store.SaveValidator(ctx, valNewMismatch, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new validator with version!=0: %v", err)
	}
	// c) corrupt old validator
	_ = os.WriteFile(filepath.Join(valDir, "val-corrupt.json"), []byte(`{bad json`), 0600)
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: "val-corrupt", Title: "Corrupt", Version: 1, Validator: valPayload}, actor); err == nil {
		t.Fatalf("expected unmarshal error on corrupt old validator")
	}
	_ = os.Remove(filepath.Join(valDir, "val-corrupt.json"))
	// d) capacity limit
	existingVals, _ := os.ReadDir(valDir)
	neededVals := maxItems - len(existingVals)
	var valCreated []string
	for i := 0; i < neededVals; i++ {
		name := fmt.Sprintf("val-cap-%04d", i)
		_ = writeItem(valDir, name, PublishedValidator{ID: name, Title: name, Version: 1, Validator: valPayload})
		valCreated = append(valCreated, name)
	}
	valOverflow := PublishedValidator{ID: "val-overflow", Title: "Overflow", Version: 0, Validator: valPayload}
	if _, err := store.SaveValidator(ctx, valOverflow, actor); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity for SaveValidator: %v", err)
	}
	for _, name := range valCreated {
		_ = os.Remove(filepath.Join(valDir, name+".json"))
	}
	_ = os.Remove(filepath.Join(valDir, "val-v1.json"))
	// e) readItems[Check] error
	chkDir := store.definitionDir("checks")
	_ = os.RemoveAll(chkDir)
	_ = os.WriteFile(chkDir, []byte("file"), 0600)
	valGood := PublishedValidator{ID: "val-good", Title: "Good", Version: 0, Validator: valPayload}
	if _, err := store.SaveValidator(ctx, valGood, actor); err == nil {
		t.Fatalf("expected error when checks dir is a file in SaveValidator")
	}
	_ = os.Remove(chkDir)
	_ = os.Mkdir(chkDir, 0700)
	// f) audit failure
	if _, err := storeWithFailAudit.SaveValidator(ctx, valGood, actor); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from audit failure: %v", err)
	}
	_ = os.Remove(filepath.Join(valDir, valGood.ID+".json"))
	// g) successful SaveValidator create & update
	valSaved, err := store.SaveValidator(ctx, valGood, actor)
	if err != nil || valSaved.Version != 1 {
		t.Fatalf("expected successful SaveValidator create: %v, %+v", err, valSaved)
	}
	valSaved.Title = "Updated"
	valUpd, err := store.SaveValidator(ctx, valSaved, actor)
	if err != nil || valUpd.Version != 2 {
		t.Fatalf("expected successful SaveValidator update: %v, %+v", err, valUpd)
	}

	// 4. resolveQueryRef & ensureQueryRefs
	// a) invalid query ref
	if _, err := store.resolveQueryRef("../escape"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad query ref: %v", err)
	}
	// b) missing query
	if _, err := store.resolveQueryRef("missing-query"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing query: %v", err)
	}
	// c) queriesDir is a file
	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.RemoveAll(queriesDir)
	_ = os.WriteFile(queriesDir, []byte("file"), 0600)
	if _, err := store.resolveQueryRef("invoice-list"); err == nil {
		t.Fatalf("expected error when queriesDir is a file")
	}
	_ = os.Remove(queriesDir)
	_ = os.Mkdir(queriesDir, 0700)
	// re-stub queries
	for _, id := range []string{"q", "child-q", "detail", "list", "invoice-list"} {
		stubQuery(t, store.projectDir, id)
	}
	// d) ensureAbsentOrRegular error for query file (query file is a directory)
	_ = os.Remove(filepath.Join(queriesDir, "q.query.json"))
	_ = os.Mkdir(filepath.Join(queriesDir, "q.query.json"), 0700)
	if _, err := store.resolveQueryRef("q"); err == nil {
		t.Fatalf("expected error when query file is a directory")
	}
	_ = os.Remove(filepath.Join(queriesDir, "q.query.json"))
	stubQuery(t, store.projectDir, "q")

	// e) ensureQueryRefs: check step with query not found
	checksWithMissingQ := map[string]Check{
		"c1": {ID: "c1", Steps: []Step{{Kind: "query", QueryID: "missing-q"}}},
	}
	if err := store.ensureQueryRefs(checksWithMissingQ, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing query in ensureQueryRefs: %v", err)
	}
	// local validator query not found
	localsWithMissingQ := map[string]QueryValidators{
		"missing-q": {QueryID: "missing-q"},
	}
	if err := store.ensureQueryRefs(nil, localsWithMissingQ); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing local query in ensureQueryRefs: %v", err)
	}

	// 5. SaveQueryValidators
	// a) ValidateQueryValidators error
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{}, actor); err == nil {
		t.Fatalf("expected ValidateQueryValidators error for empty struct")
	}
	// b) resolveQueryRef error
	qvMissingQ := QueryValidators{
		QueryID:    "missing-q",
		Validators: []Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}},
	}
	if _, err := store.SaveQueryValidators(ctx, qvMissingQ, actor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing query in SaveQueryValidators: %v", err)
	}
	qvDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvDir, 0700)
	validVals := []Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}}
	// c) version conflict
	fid := queryValidatorFileID("invoice-list")
	_ = writeItem(qvDir, fid, QueryValidators{QueryID: "invoice-list", Version: 1, Validators: validVals})
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Version: 2, Validators: validVals}, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for version mismatch in SaveQueryValidators")
	}
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "q", Version: 2, Validators: validVals}, actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new QueryValidators with version!=0")
	}
	// d) corrupt old QueryValidators
	_ = os.WriteFile(filepath.Join(qvDir, fid+".json"), []byte(`{bad json`), 0600)
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Version: 1, Validators: validVals}, actor); err == nil {
		t.Fatalf("expected unmarshal error on corrupt old QueryValidators")
	}
	_ = os.Remove(filepath.Join(qvDir, fid+".json"))
	// e) capacity limit
	existingQVs, _ := os.ReadDir(qvDir)
	neededQVs := maxItems - len(existingQVs)
	var qvCreated []string
	for i := 0; i < neededQVs; i++ {
		name := fmt.Sprintf("qv-cap-%04d", i)
		_ = writeItem(qvDir, name, QueryValidators{QueryID: name, Version: 1, Validators: validVals})
		qvCreated = append(qvCreated, name)
	}
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Version: 0, Validators: validVals}, actor); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity for SaveQueryValidators: %v", err)
	}
	for _, name := range qvCreated {
		_ = os.Remove(filepath.Join(qvDir, name+".json"))
	}
	// f) audit failure
	qvGood := QueryValidators{QueryID: "invoice-list", Version: 0, Validators: validVals}
	if _, err := storeWithFailAudit.SaveQueryValidators(ctx, qvGood, actor); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from audit failure: %v", err)
	}
	_ = os.Remove(filepath.Join(qvDir, queryValidatorFileID(qvGood.QueryID)+".json"))
	// g) successful SaveQueryValidators create & update
	qvSaved, err := store.SaveQueryValidators(ctx, qvGood, actor)
	if err != nil || qvSaved.Version != 1 {
		t.Fatalf("expected successful SaveQueryValidators create: %v, %+v", err, qvSaved)
	}
	qvSaved.Validators = append(qvSaved.Validators, Validator{ID: "v2", Scope: "row", Kind: "non-null", Severity: "error", Field: "y"})
	qvUpd, err := store.SaveQueryValidators(ctx, qvSaved, actor)
	if err != nil || qvUpd.Version != 2 {
		t.Fatalf("expected successful SaveQueryValidators update: %v, %+v", err, qvUpd)
	}

	// 6. Append: LocalStore.Append and localJSONLAudit.Append
	auditEv := AuditEvent{ID: "aud-1", At: time.Now().UTC(), Actor: "alice", Action: "test-audit"}
	if err := store.Append(ctx, auditEv); err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}
	// localJSONLAudit.Append failure when path is a directory
	auditFile := filepath.Join(store.privateDir, "audit.jsonl")
	_ = os.Remove(auditFile)
	_ = os.Mkdir(auditFile, 0700)
	if err := store.Append(ctx, auditEv); err == nil {
		t.Fatalf("expected error when audit.jsonl is a directory")
	}
	_ = os.Remove(auditFile)
}

// Not parallel: tests filesystem modifications on store dirs and global seams.
func TestCoverage100_Store_DeepBranches(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	actor := "alice"

	// 1. NewLocalStoreWithAudit error branch
	if _, err := NewLocalStoreWithAudit("", store.projectDir, store.privateDir, nil); err == nil {
		t.Fatalf("expected error from NewLocalStoreWithAudit with empty projectID")
	}

	// 2. storeAbs and storeEvalSymlinks errors in NewLocalStore
	origAbs := storeAbs
	origEval := storeEvalSymlinks
	defer func() {
		storeAbs = origAbs
		storeEvalSymlinks = origEval
	}()

	storeAbs = func(p string) (string, error) {
		if strings.Contains(p, "fail-proj") {
			return "", errors.New("abs proj fail")
		}
		if strings.Contains(p, "fail-base") {
			return "", errors.New("abs base fail")
		}
		return origAbs(p)
	}
	if _, err := NewLocalStore("p", "fail-proj", store.privateDir); err == nil || err.Error() != "abs proj fail" {
		t.Fatalf("expected abs proj fail: %v", err)
	}
	if _, err := NewLocalStore("p", store.projectDir, "fail-base"); err == nil || err.Error() != "abs base fail" {
		t.Fatalf("expected abs base fail: %v", err)
	}

	storeAbs = origAbs
	storeEvalSymlinks = func(p string) (string, error) {
		if strings.Contains(p, "eval-proj") {
			return "", errors.New("eval proj fail")
		}
		if strings.Contains(p, "eval-base") {
			return "", errors.New("eval base fail")
		}
		return origEval(p)
	}
	projDirEval := filepath.Join(t.TempDir(), "eval-proj")
	_ = os.Mkdir(projDirEval, 0700)
	if _, err := NewLocalStore("p", projDirEval, store.privateDir); err == nil || err.Error() != "eval proj fail" {
		t.Fatalf("expected eval proj fail: %v", err)
	}
	baseDirEval := filepath.Join(t.TempDir(), "eval-base")
	if _, err := NewLocalStore("p", store.projectDir, baseDirEval); err == nil || err.Error() != "eval base fail" {
		t.Fatalf("expected eval base fail: %v", err)
	}
	storeEvalSymlinks = origEval

	// 3. writeItem marshal error and createTemp error
	temp := t.TempDir()
	itemsDir := filepath.Join(temp, "items")
	_ = os.Mkdir(itemsDir, 0700)

	// a) json.MarshalIndent error (unsupported type: channel)
	if err := writeItem(itemsDir, "chan-item", make(chan int)); err == nil {
		t.Fatalf("expected json marshal error on channel item")
	}

	// b) writeItemCreateTemp error
	origCreateTemp := writeItemCreateTemp
	defer func() { writeItemCreateTemp = origCreateTemp }()
	writeItemCreateTemp = func(string, string) (tempFile, error) {
		return nil, errors.New("create temp fail")
	}
	if err := writeItem(itemsDir, "tmp-fail", Check{ID: "tmp-fail"}); err == nil || err.Error() != "create temp fail" {
		t.Fatalf("expected create temp fail: %v", err)
	}

	// c) writeItemCreateTemp returns read-only file -> tmp.Write error
	roPath := filepath.Join(temp, "ro.tmp")
	_ = os.WriteFile(roPath, []byte("x"), 0600)
	roFile, err := os.Open(roPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = roFile.Close() }()
	writeItemCreateTemp = func(string, string) (tempFile, error) {
		return roFile, nil
	}
	if err := writeItem(itemsDir, "write-fail", Check{ID: "write-fail"}); err == nil {
		t.Fatalf("expected write error on read-only temp file")
	}
	writeItemCreateTemp = origCreateTemp

	// 4. readItems unreadable file error (chmod 0000)
	unreadableItem := filepath.Join(itemsDir, "unreadable.json")
	_ = os.WriteFile(unreadableItem, []byte(`{"id":"unreadable"}`), 0000)
	if _, err := readItems[Check](itemsDir); err == nil {
		t.Fatalf("expected read error for chmod 0000 file in readItems")
	}
	_ = os.Remove(unreadableItem)

	// 5. statePageLocked marshal error & limit reached
	origStateMarshal := stateMarshal
	defer func() { stateMarshal = origStateMarshal }()
	stateMarshal = func(any) ([]byte, error) {
		return nil, errors.New("state marshal fail")
	}
	if _, err := store.StatePage(ctx, ""); err == nil || err.Error() != "state marshal fail" {
		t.Fatalf("expected state marshal fail: %v", err)
	}
	stateMarshal = func(any) ([]byte, error) {
		return make([]byte, 4<<20+10), nil
	}
	if _, err := store.StatePage(ctx, ""); err == nil || !strings.Contains(err.Error(), "detective state response limit reached") {
		t.Fatalf("expected state response limit reached: %v", err)
	}
	stateMarshal = origStateMarshal

	// 6. readRunPage: directory with non-json file or dir, unreadable dir, and unreadable file
	runsDir := store.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)
	_ = os.Mkdir(filepath.Join(runsDir, "subdir"), 0700)
	_ = os.WriteFile(filepath.Join(runsDir, "notes.txt"), []byte("txt"), 0600)
	// unreadable run file (chmod 0000)
	unreadableRun := filepath.Join(runsDir, "run-unreadable.json")
	_ = os.WriteFile(unreadableRun, []byte(`{"id":"run-unreadable"}`), 0000)
	if _, _, _, err := readRunPage(runsDir, ""); err == nil {
		t.Fatalf("expected read error on chmod 0000 run file")
	}
	_ = os.Remove(unreadableRun)
	_ = os.Remove(filepath.Join(runsDir, "notes.txt"))
	_ = os.Remove(filepath.Join(runsDir, "subdir"))

	// unreadable runsDir
	unreadableRunsDir := filepath.Join(t.TempDir(), "unreadable-runs")
	_ = os.Mkdir(unreadableRunsDir, 0700)
	_ = os.Chmod(unreadableRunsDir, 0000)
	if _, _, _, err := readRunPage(unreadableRunsDir, ""); err == nil {
		t.Fatalf("expected error on unreadable runsDir")
	}
	_ = os.Chmod(unreadableRunsDir, 0700)

	// 7. getCheck unreadable file (chmod 0000) -> safeReadFile error
	chkDir := store.definitionDir("checks")
	_ = os.MkdirAll(chkDir, 0700)
	unreadableChk := filepath.Join(chkDir, "chk-unreadable.json")
	_ = os.WriteFile(unreadableChk, []byte(`{"id":"chk-unreadable"}`), 0000)
	if _, err := store.getCheck("chk-unreadable"); err == nil {
		t.Fatalf("expected error reading unreadable check file")
	}

	// 8. SaveCheck: old check exists but is unreadable (chmod 0000)
	validCheckSteps := []Step{{ID: "s1", Kind: "query", QueryID: "q"}}
	if _, err := store.SaveCheck(ctx, Check{ID: "chk-unreadable", Title: "Unreadable", Version: 1, Steps: validCheckSteps}, actor); err == nil {
		t.Fatalf("expected error in SaveCheck when existing check is unreadable")
	}
	_ = os.Remove(unreadableChk)

	// 9. SaveScenario: existing scenario file is unreadable (chmod 0000)
	scenDir := store.definitionDir("scenarios")
	_ = os.MkdirAll(scenDir, 0700)
	unreadableScen := filepath.Join(scenDir, "scen-unreadable.json")
	_ = os.WriteFile(unreadableScen, []byte(`{"id":"scen-unreadable"}`), 0000)
	validScenSteps := []ScenarioStep{{ID: "s1", Kind: "query", Ref: "q"}}
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-unreadable", Title: "Unreadable", Version: 1, Steps: validScenSteps}, actor); err == nil {
		t.Fatalf("expected error in SaveScenario when existing scenario is unreadable")
	}
	_ = os.Remove(unreadableScen)

	// 10. SaveValidator: existing validator file is unreadable (chmod 0000)
	valDir := store.definitionDir("validators")
	_ = os.MkdirAll(valDir, 0700)
	unreadableVal := filepath.Join(valDir, "val-unreadable.json")
	_ = os.WriteFile(unreadableVal, []byte(`{"id":"val-unreadable"}`), 0000)
	valPayload := Validator{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: "val-unreadable", Title: "Unreadable", Version: 1, Validator: valPayload}, actor); err == nil {
		t.Fatalf("expected error in SaveValidator when existing validator is unreadable")
	}
	_ = os.Remove(unreadableVal)

	// 11. SaveQueryValidators: existing query-validators file is unreadable (chmod 0000)
	qvDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvDir, 0700)
	fidUnreadable := queryValidatorFileID("invoice-list")
	unreadableQV := filepath.Join(qvDir, fidUnreadable+".json")
	_ = os.WriteFile(unreadableQV, []byte(`{}`), 0000)
	validVals := []Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}}
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Version: 1, Validators: validVals}, actor); err == nil {
		t.Fatalf("expected error in SaveQueryValidators when existing item is unreadable")
	}
	_ = os.Remove(unreadableQV)

	// 12. resolveQueryRef:
	// a) storeResolveQueryID returns invalid canonical
	origResolveQuery := storeResolveQueryID
	defer func() { storeResolveQueryID = origResolveQuery }()
	storeResolveQueryID = func(string, string) (string, error) {
		return "../invalid/canonical", nil
	}
	if _, err := store.resolveQueryRef("invoice-list"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when canonical is invalid query ref: %v", err)
	}
	storeResolveQueryID = origResolveQuery

	// b) multi-part query ref (folder/subfolder/q)
	_ = os.MkdirAll(filepath.Join(store.projectDir, "queries", "nested"), 0700)
	stubQuery(t, store.projectDir, "nested/query")
	canonical, err := store.resolveQueryRef("nested/query")
	if err != nil || canonical != "nested/query" {
		t.Fatalf("expected successful resolveQueryRef for nested/query: %v, %s", err, canonical)
	}
	// intermediate folder is a file
	_ = os.RemoveAll(filepath.Join(store.projectDir, "queries", "nested"))
	_ = os.WriteFile(filepath.Join(store.projectDir, "queries", "nested"), []byte("file"), 0600)
	if _, err := store.resolveQueryRef("nested/query"); err == nil {
		t.Fatalf("expected error when intermediate folder is a file")
	}
	_ = os.Remove(filepath.Join(store.projectDir, "queries", "nested"))
	_ = os.MkdirAll(filepath.Join(store.projectDir, "queries", "nested"), 0700)
	stubQuery(t, store.projectDir, "nested/query")

	// 13. audit mutations and audit Append errors
	origAuditMarshal := auditMarshal
	origAuditOpen := auditOpenFile
	defer func() {
		auditMarshal = origAuditMarshal
		auditOpenFile = origAuditOpen
	}()

	// a) auditMarshal error
	auditMarshal = func(any) ([]byte, error) {
		return nil, errors.New("audit marshal fail")
	}
	auditEv := AuditEvent{ID: "aud-deep", At: time.Now().UTC(), Actor: "alice", Action: "test"}
	if err := store.Append(ctx, auditEv); err == nil || !strings.Contains(err.Error(), "audit marshal fail") {
		t.Fatalf("expected audit marshal fail: %v", err)
	}

	// b) auditOpenFile error
	auditMarshal = origAuditMarshal
	auditOpenFile = func(string, int, os.FileMode) (syncCloserWriter, error) {
		return nil, errors.New("audit open fail")
	}
	if err := store.Append(ctx, auditEv); err == nil || !strings.Contains(err.Error(), "audit open fail") {
		t.Fatalf("expected audit open fail: %v", err)
	}

	// c) auditOpenFile returning read-only file -> f.Write error
	auditOpenFile = func(string, int, os.FileMode) (syncCloserWriter, error) {
		return roFile, nil
	}
	if err := store.Append(ctx, auditEv); err == nil {
		t.Fatalf("expected write error from audit Append")
	}
	auditOpenFile = origAuditOpen

	// 14. CaseEventsPage: chunk safeReadFile error (unreadable chunk)
	cTimeline := Case{
		ID:             "c-chunk-read-err",
		Title:          "Chunk Read Err",
		Status:         "active",
		Version:        1,
		TimelineOffset: 5,
		Timeline:       []CaseEvent{{ID: "ev-5", Type: "note"}},
	}
	casesDir := store.privateItemDir("cases")
	_ = writeItem(casesDir, "c-chunk-read-err", cTimeline)
	tlDir := store.privateItemDir("case-timeline")
	_ = os.MkdirAll(tlDir, 0700)
	prefix := caseTimelinePrefix("c-chunk-read-err")
	chunkFile := filepath.Join(tlDir, fmt.Sprintf("%s%09d.json", prefix, 0))
	_ = os.WriteFile(chunkFile, []byte(`{}`), 0000)
	if _, err := store.CaseEventsPage(ctx, "c-chunk-read-err", 5); err == nil {
		t.Fatalf("expected safeReadFile error on unreadable chunk file in CaseEventsPage")
	}
	_ = os.Remove(chunkFile)
	_ = os.Remove(filepath.Join(casesDir, "c-chunk-read-err.json"))
}

// Not parallel: tests filesystem modifications on store dirs and global seams.
func TestCoverage100_Store_FinalBranches(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	actor := "alice"

	// 1. NewLocalStore:
	// a) privateBase cannot be created (os.MkdirAll error) -> line 81
	if _, err := NewLocalStore("p", store.projectDir, "/dev/null/cannot_mkdir"); err == nil {
		t.Fatalf("expected error from os.MkdirAll failure in NewLocalStore")
	}

	// b) private dir already exists as a regular file -> line 97
	tempBase := t.TempDir()
	projEval, _ := filepath.EvalSymlinks(store.projectDir)
	sum := sha256.Sum256([]byte(projEval))
	targetPrivFile := filepath.Join(tempBase, "detective", hex.EncodeToString(sum[:16]))
	_ = os.MkdirAll(filepath.Dir(targetPrivFile), 0700)
	_ = os.WriteFile(targetPrivFile, []byte("regular file"), 0600)
	if _, err := NewLocalStore("p", store.projectDir, tempBase); err == nil {
		t.Fatalf("expected error when private dir is a file in NewLocalStore")
	}

	// 2. ensureDir, ensureAbsentOrRegular, ensureDirIfExists, writeItem with storeLstat
	origLstat := storeLstat
	defer func() { storeLstat = origLstat }()

	// a) ensureDir: os.Mkdir error (line 189)
	if err := ensureDir("/nonexistent_parent/dir"); err == nil {
		t.Fatalf("expected error from ensureDir with nonexistent parent")
	}

	// b) ensureDir: storeLstat error (line 194)
	storeLstat = func(p string) (os.FileInfo, error) {
		if strings.Contains(p, "lstat-fail") {
			return nil, errors.New("lstat fail")
		}
		return origLstat(p)
	}
	lstatDir := filepath.Join(t.TempDir(), "lstat-fail")
	if err := ensureDir(lstatDir); err == nil || err.Error() != "lstat fail" {
		t.Fatalf("expected lstat fail from ensureDir: %v", err)
	}

	// c) ensureAbsentOrRegular: storeLstat error (line 208)
	if err := ensureAbsentOrRegular(lstatDir); err == nil || err.Error() != "lstat fail" {
		t.Fatalf("expected lstat fail from ensureAbsentOrRegular: %v", err)
	}

	// d) ensureDirIfExists: storeLstat error (line 243)
	if err := ensureDirIfExists(lstatDir); err == nil || err.Error() != "lstat fail" {
		t.Fatalf("expected lstat fail from ensureDirIfExists: %v", err)
	}

	// e) writeItem: storeLstat error other than os.ErrNotExist (line 324)
	if err := writeItem(store.projectDir, "lstat-fail", Check{ID: "lstat-fail"}); err == nil || err.Error() != "lstat fail" {
		t.Fatalf("expected lstat fail from writeItem: %v", err)
	}
	storeLstat = origLstat

	// 3. writeItem tempFile methods and storeRename
	origCreateTemp := writeItemCreateTemp
	origRename := storeRename
	defer func() {
		writeItemCreateTemp = origCreateTemp
		storeRename = origRename
	}()

	itemsDir := filepath.Join(t.TempDir(), "items")
	_ = os.Mkdir(itemsDir, 0700)

	// a) Chmod error -> line 339
	writeItemCreateTemp = func(dir, pattern string) (tempFile, error) {
		return mockTempFile{name: filepath.Join(dir, "mock.tmp"), chmodErr: errors.New("chmod fail")}, nil
	}
	if err := writeItem(itemsDir, "chk-chmod-fail", Check{ID: "chk-chmod-fail"}); err == nil || err.Error() != "chmod fail" {
		t.Fatalf("expected chmod fail in writeItem: %v", err)
	}

	// b) Sync error -> line 347
	writeItemCreateTemp = func(dir, pattern string) (tempFile, error) {
		return mockTempFile{name: filepath.Join(dir, "mock.tmp"), syncErr: errors.New("sync fail")}, nil
	}
	if err := writeItem(itemsDir, "chk-sync-fail", Check{ID: "chk-sync-fail"}); err == nil || err.Error() != "sync fail" {
		t.Fatalf("expected sync fail in writeItem: %v", err)
	}

	// c) Close error -> line 351
	writeItemCreateTemp = func(dir, pattern string) (tempFile, error) {
		return mockTempFile{name: filepath.Join(dir, "mock.tmp"), closeErr: errors.New("close fail")}, nil
	}
	if err := writeItem(itemsDir, "chk-close-fail", Check{ID: "chk-close-fail"}); err == nil || err.Error() != "close fail" {
		t.Fatalf("expected close fail in writeItem: %v", err)
	}
	writeItemCreateTemp = origCreateTemp

	// d) storeRename error -> line 353
	storeRename = func(oldpath, newpath string) error {
		return errors.New("rename fail")
	}
	if err := writeItem(itemsDir, "chk-rename-fail", Check{ID: "chk-rename-fail"}); err == nil || err.Error() != "rename fail" {
		t.Fatalf("expected rename fail in writeItem: %v", err)
	}
	storeRename = origRename

	// 4. statePageLocked: readRunPage error -> line 376
	runsDir := store.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)
	badRunFile := filepath.Join(runsDir, "bad..run.json")
	_ = os.WriteFile(badRunFile, []byte(`{}`), 0600)
	if _, err := store.StatePage(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from statePageLocked with bad run file: %v", err)
	}
	_ = os.Remove(badRunFile)

	// 5. readRunPage:
	// a) ensureDirIfExists error (dir is a regular file) -> line 402
	dummyFile := filepath.Join(t.TempDir(), "dummy-run-file")
	_ = os.WriteFile(dummyFile, []byte("x"), 0600)
	if _, _, _, err := readRunPage(dummyFile, ""); err == nil {
		t.Fatalf("expected error from readRunPage when dir is a file")
	}

	// b) runEntryInfo error -> line 453
	origEntryInfo := runEntryInfo
	defer func() { runEntryInfo = origEntryInfo }()
	runEntryInfo = func(e os.DirEntry) (os.FileInfo, error) {
		return nil, errors.New("info fail")
	}
	_ = writeItem(runsDir, "run-info-test", CheckRun{ID: "run-info-test"})
	if _, _, _, err := readRunPage(runsDir, ""); err == nil || err.Error() != "info fail" {
		t.Fatalf("expected info fail from readRunPage: %v", err)
	}
	_ = os.Remove(filepath.Join(runsDir, "run-info-test.json"))
	runEntryInfo = origEntryInfo

	// 6. SaveRun: storeStat error other than ErrNotExist -> line 724
	origStat := storeStat
	defer func() { storeStat = origStat }()
	storeStat = func(name string) (os.FileInfo, error) {
		return nil, errors.New("stat fail")
	}
	if err := store.SaveRun(ctx, CheckRun{ID: "run-stat-fail"}); err == nil || err.Error() != "stat fail" {
		t.Fatalf("expected stat fail in SaveRun: %v", err)
	}
	storeStat = origStat

	// 7. SaveCheck:
	// a) reconcilePendingActions error -> line 502
	opsDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(opsDir, 0700)
	badJSON := filepath.Join(opsDir, "bad.json")
	_ = os.WriteFile(badJSON, []byte("{"), 0600)
	validCheckSteps := []Step{{ID: "s1", Kind: "query", QueryID: "q"}}
	if _, err := store.SaveCheck(ctx, Check{ID: "chk-pend-fail", Title: "Title", Steps: validCheckSteps}, actor); err == nil {
		t.Fatalf("expected reconcilePendingActions error in SaveCheck")
	}
	_ = os.Remove(badJSON)

	// b) readItems[Check] error -> line 520
	chkDir := store.definitionDir("checks")
	_ = os.MkdirAll(chkDir, 0700)
	unreadableChk := filepath.Join(chkDir, "unreadable.json")
	_ = os.WriteFile(unreadableChk, []byte(`{}`), 0000)
	if _, err := store.SaveCheck(ctx, Check{ID: "chk-read-fail", Title: "Title", Steps: validCheckSteps}, actor); err == nil {
		t.Fatalf("expected readItems[Check] error in SaveCheck")
	}
	_ = os.Remove(unreadableChk)

	// c) ensureQueryRefs error in SaveCheck -> line 550
	qvDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvDir, 0700)
	fidMissing := queryValidatorFileID("absent-q")
	_ = writeItem(qvDir, fidMissing, QueryValidators{QueryID: "absent-q", Version: 1, Validators: []Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}}})
	if _, err := store.SaveCheck(ctx, Check{ID: "chk-ensure-fail", Title: "Title", Steps: validCheckSteps}, actor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from ensureQueryRefs in SaveCheck: %v", err)
	}
	_ = os.Remove(filepath.Join(qvDir, fidMissing+".json"))

	// 8. SaveScenario:
	// a) reconcilePendingActions error -> line 720
	_ = os.WriteFile(badJSON, []byte("{"), 0600)
	validScenSteps := []ScenarioStep{{ID: "s1", Kind: "query", Ref: "q"}}
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-pend-fail", Title: "Title", Steps: validScenSteps}, actor); err == nil {
		t.Fatalf("expected reconcilePendingActions error in SaveScenario")
	}
	_ = os.Remove(badJSON)

	// b) readItems[Scenario] error -> line 748
	scenDir := store.definitionDir("scenarios")
	_ = os.MkdirAll(scenDir, 0700)
	unreadableScen := filepath.Join(scenDir, "unreadable.json")
	_ = os.WriteFile(unreadableScen, []byte(`{}`), 0000)
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-read-fail", Title: "Title", Steps: validScenSteps}, actor); err == nil {
		t.Fatalf("expected readItems[Scenario] error in SaveScenario")
	}
	_ = os.Remove(unreadableScen)

	// c) writeItem error in SaveScenario -> line 758
	storeRename = func(oldpath, newpath string) error {
		return errors.New("rename fail")
	}
	if _, err := store.SaveScenario(ctx, Scenario{ID: "scen-write-fail", Title: "Title", Steps: validScenSteps}, actor); err == nil {
		t.Fatalf("expected writeItem error in SaveScenario")
	}
	storeRename = origRename

	// 9. SaveValidator:
	// a) reconcilePendingActions error -> line 771
	_ = os.WriteFile(badJSON, []byte("{"), 0600)
	valPayload := Validator{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: "val-pend-fail", Title: "Title", Validator: valPayload}, actor); err == nil {
		t.Fatalf("expected reconcilePendingActions error in SaveValidator")
	}
	_ = os.Remove(badJSON)

	// b) readItems[PublishedValidator] error -> line 797
	valDir := store.definitionDir("validators")
	_ = os.MkdirAll(valDir, 0700)
	unreadableVal := filepath.Join(valDir, "unreadable.json")
	_ = os.WriteFile(unreadableVal, []byte(`{}`), 0000)
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: "val-read-fail", Title: "Title", Validator: valPayload}, actor); err == nil {
		t.Fatalf("expected readItems[PublishedValidator] error in SaveValidator")
	}
	_ = os.Remove(unreadableVal)

	// c) readItems[QueryValidators] error -> line 804
	unreadableQV := filepath.Join(qvDir, "unreadable.json")
	_ = os.WriteFile(unreadableQV, []byte(`{}`), 0000)
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: "val-qv-fail", Title: "Title", Validator: valPayload}, actor); err == nil {
		t.Fatalf("expected readItems[QueryValidators] error in SaveValidator")
	}
	_ = os.Remove(unreadableQV)

	// d) validateGraph error in SaveValidator -> line 823
	// Validator referencing non-existent check
	valBadRef := PublishedValidator{ID: "val-bad-ref", Title: "Bad Ref", Validator: Validator{ID: "v1", Scope: "row", Kind: "check", CheckID: "non-existent-chk"}}
	if _, err := store.SaveValidator(ctx, valBadRef, actor); err == nil {
		t.Fatalf("expected validateGraph error in SaveValidator")
	}

	// e) ensureQueryRefs error in SaveValidator -> line 826
	_ = writeItem(qvDir, fidMissing, QueryValidators{QueryID: "absent-q", Version: 1, Validators: []Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}}})
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: "val-ensure-fail", Title: "Title", Validator: valPayload}, actor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from ensureQueryRefs in SaveValidator: %v", err)
	}
	_ = os.Remove(filepath.Join(qvDir, fidMissing+".json"))

	// f) writeItem error in SaveValidator -> line 848
	storeRename = func(oldpath, newpath string) error {
		return errors.New("rename fail")
	}
	if _, err := store.SaveValidator(ctx, PublishedValidator{ID: "val-wr-fail", Title: "Title", Validator: valPayload}, actor); err == nil {
		t.Fatalf("expected writeItem error in SaveValidator")
	}
	storeRename = origRename

	// 10. SaveQueryValidators:
	// a) reconcilePendingActions error -> line 907
	_ = os.WriteFile(badJSON, []byte("{"), 0600)
	validVals := []Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}}
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: validVals}, actor); err == nil {
		t.Fatalf("expected reconcilePendingActions error in SaveQueryValidators")
	}
	_ = os.Remove(badJSON)

	// b) readItems[Check] error -> line 929
	_ = os.WriteFile(unreadableChk, []byte(`{}`), 0000)
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: validVals}, actor); err == nil {
		t.Fatalf("expected readItems[Check] error in SaveQueryValidators")
	}
	_ = os.Remove(unreadableChk)

	// c) readItems[PublishedValidator] error -> line 933
	_ = os.WriteFile(unreadableVal, []byte(`{}`), 0000)
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: validVals}, actor); err == nil {
		t.Fatalf("expected readItems[PublishedValidator] error in SaveQueryValidators")
	}
	_ = os.Remove(unreadableVal)

	// d) readItems[QueryValidators] error -> line 937
	_ = os.WriteFile(unreadableQV, []byte(`{}`), 0000)
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: validVals}, actor); err == nil {
		t.Fatalf("expected readItems[QueryValidators] error in SaveQueryValidators")
	}
	_ = os.Remove(unreadableQV)

	// e) validateGraph error in SaveQueryValidators -> line 978
	_ = writeItem(chkDir, "chk-cyc-1", Check{ID: "chk-cyc-1", Title: "C1", Version: 1, Steps: []Step{{ID: "s1", Kind: "check", CheckID: "chk-cyc-2"}}})
	_ = writeItem(chkDir, "chk-cyc-2", Check{ID: "chk-cyc-2", Title: "C2", Version: 1, Steps: []Step{{ID: "s1", Kind: "check", CheckID: "chk-cyc-1"}}})
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: validVals}, actor); err == nil {
		t.Fatalf("expected validateGraph error in SaveQueryValidators")
	}
	_ = os.Remove(filepath.Join(chkDir, "chk-cyc-1.json"))
	_ = os.Remove(filepath.Join(chkDir, "chk-cyc-2.json"))

	// f) ensureQueryRefs error in SaveQueryValidators -> line 980
	_ = writeItem(qvDir, fidMissing, QueryValidators{QueryID: "absent-q", Version: 1, Validators: []Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error", Field: "x"}}})
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: validVals}, actor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from ensureQueryRefs in SaveQueryValidators: %v", err)
	}
	_ = os.Remove(filepath.Join(qvDir, fidMissing+".json"))

	// g) writeItem error in SaveQueryValidators -> line 983
	storeRename = func(oldpath, newpath string) error {
		return errors.New("rename fail")
	}
	if _, err := store.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: validVals}, actor); err == nil {
		t.Fatalf("expected writeItem error in SaveQueryValidators")
	}
	storeRename = origRename

	// 11. audit Sync and Close errors: lines 995, 999
	origAuditOpen := auditOpenFile
	defer func() { auditOpenFile = origAuditOpen }()

	// a) Sync error -> line 995
	auditOpenFile = func(path string, flag int, perm os.FileMode) (syncCloserWriter, error) {
		return mockAuditFile{syncErr: errors.New("audit sync fail")}, nil
	}
	auditEv := AuditEvent{ID: "aud-sync", At: time.Now().UTC(), Actor: "alice", Action: "test"}
	if err := store.Append(ctx, auditEv); err == nil || !strings.Contains(err.Error(), "audit sync fail") {
		t.Fatalf("expected audit sync fail: %v", err)
	}

	// b) Close error -> line 999
	auditOpenFile = func(path string, flag int, perm os.FileMode) (syncCloserWriter, error) {
		return mockAuditFile{closeErr: errors.New("audit close fail")}, nil
	}
	if err := store.Append(ctx, auditEv); err == nil || !strings.Contains(err.Error(), "audit close fail") {
		t.Fatalf("expected audit close fail: %v", err)
	}
	auditOpenFile = origAuditOpen

	// 12. writeItem ensureNoSymlink(dir) error (line 316-317)
	testTmp := t.TempDir()
	origLstat = storeLstat
	callCount := 0
	storeLstat = func(name string) (os.FileInfo, error) {
		callCount++
		if callCount == 2 {
			return nil, errors.New("lstat ensureNoSymlink error")
		}
		return origLstat(name)
	}
	if err := writeItem(testTmp, "item-sym", Check{ID: "item-sym"}); err == nil || err.Error() != "lstat ensureNoSymlink error" {
		t.Fatalf("expected ensureNoSymlink error in writeItem: %v", err)
	}
	storeLstat = origLstat

	// 13. resolveQueryRef branches (lines 870-871, 877-878, 881-882):
	// a) queriesDir is not a directory -> line 870
	rqrStore := testStore(t)
	queriesFile := filepath.Join(rqrStore.projectDir, "queries")
	_ = os.RemoveAll(queriesFile)
	_ = os.WriteFile(queriesFile, []byte("x"), 0600)
	origResolveQID := storeResolveQueryID
	defer func() { storeResolveQueryID = origResolveQID }()
	storeResolveQueryID = func(projDir, queryID string) (string, error) {
		return "invoice-list", nil
	}
	if _, err := rqrStore.resolveQueryRef("invoice-list"); err == nil {
		t.Fatalf("expected ensureDirIfExists error when queries is regular file")
	}
	_ = os.Remove(queriesFile)
	_ = os.Mkdir(queriesFile, 0700)

	// b) subfolder in query ref is not a directory -> line 877
	subFile := filepath.Join(queriesFile, "sub")
	_ = os.WriteFile(subFile, []byte("x"), 0600)
	storeResolveQueryID = func(projDir, queryID string) (string, error) {
		return "sub/nested-q", nil
	}
	if _, err := rqrStore.resolveQueryRef("invoice-list"); err == nil {
		t.Fatalf("expected ensureDirIfExists error when subfolder is regular file")
	}
	_ = os.Remove(subFile)

	// c) query file path is a directory instead of a regular file -> line 881
	_ = os.MkdirAll(filepath.Join(queriesFile, "sub", "nested-q.query.json"), 0700)
	if _, err := rqrStore.resolveQueryRef("invoice-list"); err == nil {
		t.Fatalf("expected ensureAbsentOrRegular error when query file is directory")
	}
	storeResolveQueryID = origResolveQID
}
