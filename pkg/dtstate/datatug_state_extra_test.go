package dtstate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/apps/global"
	"github.com/rivo/tview"
)

// stateSeams holds backups of all seam vars so each test can restore them.
type stateSeams struct {
	origGetState   func() (*DatatugState, error)
	origSaveState  func(*DatatugState) error
	origFilePathFn func() string
	origOsOpen     func(string) (*os.File, error)
	origGoAsync    func(func())
	origAppStop    func()
	origHadRecent  bool
}

func backupSeams() stateSeams {
	return stateSeams{
		origGetState:   getState,
		origSaveState:  saveState,
		origFilePathFn: filePathFn,
		origOsOpen:     osOpen,
		origGoAsync:    goAsync,
		origAppStop:    appStop,
		origHadRecent:  hadRecentProjects,
	}
}

func (s stateSeams) restore() {
	getState = s.origGetState
	saveState = s.origSaveState
	filePathFn = s.origFilePathFn
	osOpen = s.origOsOpen
	goAsync = s.origGoAsync
	appStop = s.origAppStop
	hadRecentProjects = s.origHadRecent
}

// tempStateDir creates a temp dir and points filePathFn at a file inside it.
// Returns the path to the state file.
func tempStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stateFile := filepath.Join(dir, cliStateFileName)
	filePathFn = func() string { return stateFile }
	return stateFile
}

// ---- GetDatatugState --------------------------------------------------------

func TestGetDatatugState_FileNotExist(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	dir := t.TempDir()
	filePathFn = func() string { return filepath.Join(dir, "no-such-file.json") }

	state, err := GetDatatugState()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
}

func TestGetDatatugState_EmptyFile(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	stateFile := tempStateDir(t)
	// Create a zero-byte file.
	f, err := os.Create(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	state, err := GetDatatugState()
	if err != nil {
		t.Fatalf("expected no error for empty file, got %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if len(state.RecentProjects) != 0 {
		t.Fatalf("expected no recent projects, got %d", len(state.RecentProjects))
	}
}

func TestGetDatatugState_ValidJSON(t *testing.T) {
	s := backupSeams()
	defer s.restore()
	hadRecentProjects = false

	stateFile := tempStateDir(t)
	data := &DatatugState{
		RecentProjects:    []*RecentProject{{ID: "proj1", Timestamp: "2024-01-01T00:00:00Z"}},
		CurrentScreenPath: "/some/path",
	}
	writeStateFile(t, stateFile, data)

	state, err := GetDatatugState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.RecentProjects) != 1 {
		t.Fatalf("expected 1 recent project, got %d", len(state.RecentProjects))
	}
	if state.RecentProjects[0].ID != "proj1" {
		t.Errorf("expected proj1, got %s", state.RecentProjects[0].ID)
	}
	if state.CurrentScreenPath != "/some/path" {
		t.Errorf("expected /some/path, got %s", state.CurrentScreenPath)
	}
	if !hadRecentProjects {
		t.Error("expected hadRecentProjects to be true after reading state with projects")
	}
}

func TestGetDatatugState_MalformedJSON(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	stateFile := tempStateDir(t)
	if err := os.WriteFile(stateFile, []byte("not json {{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := GetDatatugState()
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func writeStateFile(t *testing.T, path string, state *DatatugState) {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- SaveState --------------------------------------------------------------

func TestSaveState_WritesFile(t *testing.T) {
	s := backupSeams()
	defer s.restore()
	hadRecentProjects = false

	stateFile := tempStateDir(t)
	data := &DatatugState{
		RecentProjects:    []*RecentProject{{ID: "p1", Timestamp: "2024-01-01T00:00:00Z"}},
		CurrentScreenPath: "/screen",
	}

	if err := SaveState(data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	var readBack DatatugState
	if err = json.Unmarshal(b, &readBack); err != nil {
		t.Fatalf("could not unmarshal written state: %v", err)
	}
	if len(readBack.RecentProjects) != 1 || readBack.RecentProjects[0].ID != "p1" {
		t.Errorf("unexpected state: %+v", readBack)
	}
}

func TestSaveState_CreateError(t *testing.T) {
	s := backupSeams()
	defer s.restore()
	hadRecentProjects = false

	// Point at a path inside a non-existent directory so os.Create fails.
	filePathFn = func() string { return filepath.Join(t.TempDir(), "nonexistent-subdir", "state.json") }

	err := SaveState(&DatatugState{})
	if err == nil {
		t.Fatal("expected error when parent dir does not exist, got nil")
	}
}

func TestSaveState_PanicWhenHadRecentProjectsButNowEmpty(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	hadRecentProjects = true
	appStopCalled := false
	appStop = func() { appStopCalled = true }

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic, got none")
		}
		if !appStopCalled {
			t.Error("expected appStop to be called before panic")
		}
	}()

	_ = SaveState(&DatatugState{RecentProjects: nil})
}

// ---- getFilePath ------------------------------------------------------------

func TestGetFilePath(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	// Reset filePathFn to the real function to exercise it.
	filePathFn = getFilePath

	path := filePathFn()
	if !strings.HasSuffix(path, cliStateFileName) {
		t.Errorf("expected path ending with %s, got %s", cliStateFileName, path)
	}
}

// ---- BumpRecentProject (goroutine) ------------------------------------------

func TestBumpRecentProject_Async_Success(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	done := make(chan struct{})
	var savedState *DatatugState

	getState = func() (*DatatugState, error) {
		return &DatatugState{}, nil
	}
	saveState = func(st *DatatugState) error {
		savedState = st
		close(done)
		return nil
	}
	// Run synchronously so the test can observe the result.
	goAsync = func(fn func()) { fn() }

	BumpRecentProject("async-proj")

	select {
	case <-done:
	default:
		// goAsync was synchronous so done is already closed; no wait needed.
	}

	if savedState == nil {
		t.Fatal("saveState was not called")
	}
	if len(savedState.RecentProjects) != 1 || savedState.RecentProjects[0].ID != "async-proj" {
		t.Errorf("unexpected saved state: %+v", savedState)
	}
}

func TestBumpRecentProject_Async_ErrorLogged(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	var wg sync.WaitGroup
	wg.Add(1)

	getState = func() (*DatatugState, error) {
		return nil, errors.New("forced error")
	}
	// Use real goroutine but wait for it.
	goAsync = func(fn func()) {
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	BumpRecentProject("proj-err")
	wg.Wait() // error branch was hit; logus.Errorf called inside goroutine
}

// ---- SaveCurrentScreePathSync -----------------------------------------------

func TestSaveCurrentScreePathSync_FileNotExist(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	var savedState *DatatugState
	getState = func() (*DatatugState, error) {
		return nil, fs.ErrNotExist
	}
	saveState = func(st *DatatugState) error {
		savedState = st
		return nil
	}

	SaveCurrentScreePathSync("/my/screen")

	if savedState == nil {
		t.Fatal("saveState was not called")
	}
	if savedState.CurrentScreenPath != "/my/screen" {
		t.Errorf("expected /my/screen, got %s", savedState.CurrentScreenPath)
	}
}

func TestSaveCurrentScreePathSync_HappyPath(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	existing := &DatatugState{RecentProjects: []*RecentProject{{ID: "p1"}}}
	getState = func() (*DatatugState, error) { return existing, nil }

	var savedState *DatatugState
	saveState = func(st *DatatugState) error {
		savedState = st
		return nil
	}

	SaveCurrentScreePathSync("/new/screen")

	if savedState == nil {
		t.Fatal("saveState was not called")
	}
	if savedState.CurrentScreenPath != "/new/screen" {
		t.Errorf("expected /new/screen, got %s", savedState.CurrentScreenPath)
	}
}

func TestSaveCurrentScreePathSync_SaveError(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	getState = func() (*DatatugState, error) { return &DatatugState{}, nil }
	saveState = func(*DatatugState) error { return errors.New("save failed") }

	// Should not panic; error is logged only.
	SaveCurrentScreePathSync("/screen")
}

func TestSaveCurrentScreePathSync_GetStateNonNotExistError(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	appStopCalled := false
	appStop = func() { appStopCalled = true }
	getState = func() (*DatatugState, error) {
		return nil, errors.New("disk failure")
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic, got none")
		}
		if !appStopCalled {
			t.Error("expected appStop to be called")
		}
	}()

	SaveCurrentScreePathSync("/screen")
}

// ---- SaveCurrentScreePath ---------------------------------------------------

func TestSaveCurrentScreePath_Passthrough(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	var savedState *DatatugState
	getState = func() (*DatatugState, error) { return &DatatugState{}, nil }
	saveState = func(st *DatatugState) error {
		savedState = st
		return nil
	}

	SaveCurrentScreePath("/pass/through")

	if savedState == nil {
		t.Fatal("saveState was not called")
	}
	if savedState.CurrentScreenPath != "/pass/through" {
		t.Errorf("expected /pass/through, got %s", savedState.CurrentScreenPath)
	}
}

// ---- GetDatatugState osOpen error path --------------------------------------

func TestGetDatatugState_OpenError(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	stateFile := tempStateDir(t)
	// Write a non-empty file so Stat succeeds and size > 0.
	if err := os.WriteFile(stateFile, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stub osOpen to return an error.
	osOpen = func(string) (*os.File, error) {
		return nil, errors.New("open denied")
	}

	_, err := GetDatatugState()
	if err == nil {
		t.Fatal("expected error from osOpen stub, got nil")
	}
}

// ---- bumpRecentProject saveState error path ---------------------------------

func TestBumpRecentProject_SaveStateError(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	getState = func() (*DatatugState, error) { return &DatatugState{}, nil }
	saveState = func(*DatatugState) error { return errors.New("save error") }

	err := bumpRecentProject("proj1")
	if err == nil {
		t.Fatal("expected error when saveState fails, got nil")
	}
}

// ---- goAsync default var body -----------------------------------------------

func TestGoAsync_DefaultBody(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	// Restore the real default so its body is exercised.
	goAsync = s.origGoAsync

	done := make(chan struct{})
	goAsync(func() { close(done) })
	<-done // blocks until the goroutine body runs
}

func TestAppStop_DefaultBody(t *testing.T) {
	s := backupSeams()
	defer s.restore()

	// Set a real (non-started) tview application so global.App.Stop() is safe to call.
	origApp := global.App
	global.App = tview.NewApplication()
	defer func() { global.App = origApp }()

	appStop = s.origAppStop
	appStop() // exercises the `global.App.Stop()` call
}
