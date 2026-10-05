package dtlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noSender replaces every seam through which this package reaches the
// network or the disk for telemetry, and reports which of them were touched.
// A test that proves "nothing is sent" asserts the returned slice is empty.
func noSender(t *testing.T) *[]string {
	t.Helper()
	var touched []string
	oldFetch, oldNew, oldCreate, oldHTTP := getPostHogApiKeyFromServerFunc, posthogNewWithConfig, osCreate, httpDoRequest
	oldStat, oldMkdir, oldWrite := statFile, mkdirAll, writeFile
	getPostHogApiKeyFromServerFunc = func() (string, error) { touched = append(touched, "key fetch"); return "", errors.New("no") }
	posthogNewWithConfig = func(string, posthog.Config) (posthog.Client, error) {
		touched = append(touched, "client")
		return nil, errors.New("no")
	}
	osCreate = func(string) (*os.File, error) { touched = append(touched, "config file"); return nil, errors.New("no") }
	httpDoRequest = func(*http.Request) (*http.Response, error) {
		touched = append(touched, "http")
		return nil, errors.New("no")
	}
	statFile = func(string) (os.FileInfo, error) {
		touched = append(touched, "marker stat")
		return nil, fs.ErrNotExist
	}
	mkdirAll = func(string, os.FileMode) error { touched = append(touched, "marker dir"); return nil }
	writeFile = func(string, []byte, os.FileMode) error { touched = append(touched, "marker write"); return nil }
	t.Cleanup(func() {
		getPostHogApiKeyFromServerFunc, posthogNewWithConfig, osCreate, httpDoRequest = oldFetch, oldNew, oldCreate, oldHTTP
		statFile, mkdirAll, writeFile = oldStat, oldMkdir, oldWrite
	})
	return &touched
}

// resetState puts the package-level telemetry state back after a test.
func resetState(t *testing.T) {
	t.Helper()
	mu.Lock()
	oldPh, oldInit, oldStarted, oldQueue, oldID, oldSession := ph, initialized, started, queue, posthogDistinctID, sessionID
	mu.Unlock()
	oldShown := heldBackThisRun.Load()
	t.Cleanup(func() {
		mu.Lock()
		ph, initialized, started, queue, posthogDistinctID, sessionID = oldPh, oldInit, oldStarted, oldQueue, oldID, oldSession
		mu.Unlock()
		heldBackThisRun.Store(oldShown)
	})
}

// --- item 1: one function decides ---

func TestEnabled_Matrix(t *testing.T) {
	type vars struct{ telemetry, doNotTrack, ci string }
	for name, tc := range map[string]struct {
		vars vars
		want bool
	}{
		"nothing set":                   {vars{}, true},
		"telemetry 1":                   {vars{telemetry: "1"}, true},
		"telemetry on":                  {vars{telemetry: "on"}, true},
		"telemetry yes":                 {vars{telemetry: "yes"}, true},
		"telemetry TRUE":                {vars{telemetry: "TRUE"}, true},
		"telemetry true padded":         {vars{telemetry: " true "}, true},
		"telemetry blank":               {vars{telemetry: "  "}, true},
		"telemetry no":                  {vars{telemetry: "no"}, false},
		"telemetry n":                   {vars{telemetry: "n"}, false},
		"telemetry disabled":            {vars{telemetry: "disabled"}, false},
		"telemetry never":               {vars{telemetry: "never"}, false},
		"telemetry typo of":             {vars{telemetry: "of"}, false},
		"telemetry garbage":             {vars{telemetry: "garbage"}, false},
		"telemetry 0":                   {vars{telemetry: "0"}, false},
		"telemetry false":               {vars{telemetry: "false"}, false},
		"telemetry off":                 {vars{telemetry: "off"}, false},
		"telemetry OFF":                 {vars{telemetry: "OFF"}, false},
		"telemetry False":               {vars{telemetry: "False"}, false},
		"telemetry padded":              {vars{telemetry: " 0 "}, false},
		"do not track 1":                {vars{doNotTrack: "1"}, false},
		"do not track true":             {vars{doNotTrack: "true"}, false},
		"do not track anything":         {vars{doNotTrack: "please"}, false},
		"do not track 0":                {vars{doNotTrack: "0"}, true},
		"do not track empty":            {vars{doNotTrack: ""}, true},
		"ci true":                       {vars{ci: "true"}, false},
		"ci 1":                          {vars{ci: "1"}, false},
		"ci 0 is still set":             {vars{ci: "0"}, false},
		"ci false":                      {vars{ci: "false"}, true},
		"ci FALSE":                      {vars{ci: "FALSE"}, true},
		"ci empty":                      {vars{ci: ""}, true},
		"explicit on does not beat ci":  {vars{telemetry: "1", ci: "true"}, false},
		"explicit on does not beat dnt": {vars{telemetry: "1", doNotTrack: "1"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			resetState(t)
			heldBackThisRun.Store(false)
			t.Setenv(EnvTelemetry, tc.vars.telemetry)
			t.Setenv(EnvDoNotTrack, tc.vars.doNotTrack)
			t.Setenv(EnvCI, tc.vars.ci)
			assert.Equal(t, tc.want, Enabled())
		})
	}
}

func TestEnabled_FalseOnTheRunThatPrintedTheNotice(t *testing.T) {
	resetState(t)
	heldBackThisRun.Store(true)
	assert.False(t, Enabled())
}

// Every variable turns every sender off: no client, no queue, no request, no file.
func TestOff_NoSenderTouchesAnything(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{EnvTelemetry, "0"}, {EnvTelemetry, "false"}, {EnvTelemetry, "off"}, {EnvTelemetry, "no"}, {EnvTelemetry, "typo"},
		{EnvDoNotTrack, "1"}, {EnvCI, "true"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			resetState(t)
			heldBackThisRun.Store(false)
			touched := noSender(t)
			t.Setenv(tc.name, tc.value)
			mock := &mockPosthogClient{}
			mu.Lock()
			started, initialized, ph, queue = false, false, nil, nil
			mu.Unlock()

			// A whole run, as main drives it.
			var stderr bytes.Buffer
			ShowNoticeOnce(&stderr, true)
			Start()
			Enqueue(StartedEvent())
			ScreenOpened("viewers/sqlite", "SQLite Viewer")
			Enqueue(PanicEvent("boom"))
			Enqueue(ExitedEvent())
			Close()

			// And the same senders when a client already exists.
			mu.Lock()
			started, initialized, ph = true, true, mock
			mu.Unlock()
			Enqueue(StartedEvent())
			ScreenOpened("viewers/sqlite", "SQLite Viewer")
			Enqueue(PanicEvent("boom"))
			Enqueue(ExitedEvent())
			mu.Lock()
			assert.Empty(t, queue)
			mu.Unlock()

			assert.Empty(t, *touched, "nothing may be fetched, created, written or sent while telemetry is off")
			assert.Empty(t, mock.enqueued)
			assert.Empty(t, stderr.String(), "no notice while telemetry is off")
		})
	}
}

// Nothing is queued on the run that printed the notice either.
func TestNoticeRun_SendsNothing(t *testing.T) {
	resetState(t)
	touched := noSender(t)
	heldBackThisRun.Store(false)
	mock := &mockPosthogClient{}
	mu.Lock()
	started, initialized, ph, queue = false, false, nil, nil
	mu.Unlock()

	ShowNoticeOnce(&bytes.Buffer{}, true)
	Start()
	Enqueue(StartedEvent())
	mu.Lock()
	started, initialized, ph = true, true, mock
	mu.Unlock()
	Enqueue(ExitedEvent())

	assert.Empty(t, mock.enqueued)
	assert.NotContains(t, *touched, "client")
	assert.NotContains(t, *touched, "http")
}

// The structural half of "a test fails when a new sender does not ask":
// the exported API of this package is classified, so a new exported name
// fails this test until its author says whether it sends and, if it does,
// makes it ask Enabled and adds it to TestOff_NoSenderTouchesAnything.
func TestExportedAPIIsClassified(t *testing.T) {
	classified := map[string]string{
		"Enabled":         "the decision",
		"Start":           "sender: asks Enabled",
		"Enqueue":         "sender: asks Enabled",
		"ScreenOpened":    "sender: goes through Enqueue",
		"ShowNoticeOnce":  "asks the environment; writes only the marker",
		"Close":           "no-op unless Start ran",
		"DistinctID":      "reads",
		"StartedEvent":    "builds an event",
		"ExitedEvent":     "builds an event",
		"PanicEvent":      "builds an event",
		"Notice":          "builds the notice text",
		"EnvTelemetry":    "const",
		"EnvDoNotTrack":   "const",
		"EnvCI":           "const",
		"EventStarted":    "const",
		"EventExited":     "const",
		"EventScreen":     "const",
		"PanicEventTitle": "const",
	}
	var found []string
	for _, f := range parsePackage(t, ".") {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					found = append(found, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								found = append(found, n.Name)
							}
						}
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							found = append(found, s.Name.Name)
						}
					}
				}
			}
		}
	}
	sort.Strings(found)
	for _, name := range found {
		_, ok := classified[name]
		assert.True(t, ok, "dtlog.%s is new: say in this test whether it sends anything and, if it does, make it ask Enabled and add it to TestOff_NoSenderTouchesAnything", name)
	}
	for name := range classified {
		assert.Contains(t, found, name, "classified name %s no longer exists", name)
	}
}

// Only one place in this package may hand a message to the PostHog client or
// create one, and each is reachable only through a function that asks Enabled.
func TestClientIsReachedOnlyThroughTheGate(t *testing.T) {
	var sawEnqueue, sawNew, sawGet bool
	for _, f := range parsePackage(t, ".") {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if id, ok := fun.X.(*ast.Ident); ok && id.Name == "ph" && fun.Sel.Name == "Enqueue" {
						sawEnqueue = true
						assert.Equal(t, "enqueue", fn.Name.Name, "ph.Enqueue may be called only from enqueue, which Enqueue reaches after asking Enabled")
					}
				case *ast.Ident:
					switch fun.Name {
					case "posthogNewWithConfig":
						sawNew = true
						assert.Equal(t, "getPostHogClient", fn.Name.Name, "a client may be created only by getPostHogClient")
					case "newPosthogClient":
						assert.Equal(t, "newClientFromConfig", fn.Name.Name, "the SDK's constructor may be called only by newClientFromConfig")
					case "getPostHogClient":
						sawGet = true
						assert.Equal(t, "Start", fn.Name.Name, "getPostHogClient may be called only from Start, which asks Enabled")
					}
				}
				return true
			})
		}
	}
	assert.True(t, sawEnqueue && sawNew && sawGet, "the scan found nothing to check: has the code been renamed?")
}

// The SDK's constructors (posthog.New, posthog.NewWithConfig) are named once in
// this package, as the default of the newPosthogClient seam: a function that
// called either directly would create a client around the gate. Anywhere,
// in a function or a variable, a mention fails.
func TestSDKConstructorsAreNamedOnlyByTheSeam(t *testing.T) {
	mentions := 0
	for _, f := range parsePackage(t, ".") {
		allowed := map[ast.Node]bool{}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == "newPosthogClient" {
					for _, v := range vs.Values {
						allowed[v] = true
					}
				}
			}
		}
		var walk func(n ast.Node, ok bool)
		walk = func(n ast.Node, inSeam bool) {
			ast.Inspect(n, func(c ast.Node) bool {
				if c != n && allowed[c] {
					walk(c, true)
					return false
				}
				sel, isSel := c.(*ast.SelectorExpr)
				if !isSel {
					return true
				}
				if id, isID := sel.X.(*ast.Ident); isID && id.Name == "posthog" && (sel.Sel.Name == "New" || sel.Sel.Name == "NewWithConfig") {
					mentions++
					assert.True(t, inSeam, "posthog.%s is named outside the newPosthogClient seam: a client must be created only through newClientFromConfig", sel.Sel.Name)
				}
				return true
			})
		}
		walk(f, false)
	}
	assert.Equal(t, 1, mentions, "the scan found the seam once: has the code been renamed?")
}

// Nothing outside this package may import the PostHog client: it can only
// send through dtlog, so every sender asks Enabled.
func TestNoOtherPackageImportsPostHog(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "dtlog":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // not this test's business
		}
		checked++
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			assert.False(t, strings.HasPrefix(p, "github.com/posthog/posthog-go"), "%s imports PostHog: send through pkg/dtlog so the opt-out applies", path)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Greater(t, checked, 100, "the walk found too few files: is the root right?")
}

func parsePackage(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)
		files = append(files, f)
	}
	require.NotEmpty(t, files)
	return files
}

// --- the test binary never creates a real client ---

func TestRealClientCannotBeCreatedInATestBinary(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = newClientFromConfig("phc_test", posthog.Config{})
	})
}

func TestRealClientIsCreatedOutsideATestBinary(t *testing.T) {
	oldIn, oldNew := inTestBinary, newPosthogClient
	defer func() { inTestBinary, newPosthogClient = oldIn, oldNew }()
	inTestBinary = func() bool { return false }
	want := &mockPosthogClient{}
	newPosthogClient = func(key string, _ posthog.Config) (posthog.Client, error) {
		assert.Equal(t, "phc_test", key)
		return want, nil
	}
	got, err := newClientFromConfig("phc_test", posthog.Config{})
	require.NoError(t, err)
	assert.Same(t, want, got)
}

// --- items 2 and 3: the notice ---

func TestNotice_AtMostSixLinesAndSaysWhatItMust(t *testing.T) {
	lines := strings.Split(strings.TrimRight(Notice(runtime.GOOS), "\n"), "\n")
	assert.LessOrEqual(t, len(lines), 6)
	for _, want := range []string{
		EventStarted, EventExited, EventScreen, "crash",
		"No database content, query text, path, host or credential",
		EnvDoNotTrack, EnvCI, "shell profile",
		"a random install id", "session id", "DataTug version", "screen's name", "Go type of the error",
	} {
		assert.Contains(t, Notice("linux"), want)
	}
	assert.Contains(t, Notice("linux"), "export "+EnvTelemetry+"=0")
	assert.NotContains(t, Notice("linux"), "setx")
	assert.True(t, strings.HasSuffix(Notice("linux"), "\n"))
}

// Windows has no export, and a variable set in one window is gone in the next:
// the line that turns telemetry off for good is setx there.
func TestNotice_WindowsGivesSetxNotExport(t *testing.T) {
	text := Notice("windows")
	assert.Contains(t, text, "setx "+EnvTelemetry+" 0")
	assert.NotContains(t, text, "export")
	assert.LessOrEqual(t, len(strings.Split(strings.TrimRight(text, "\n"), "\n")), 6)
	assert.Contains(t, text, EventStarted)
}

// The notice names a field only when the event carries it (the wire tests pin
// each event's fields): the DataTug version is on the screen event alone, the
// session on the three events but not the crash report.
func TestNotice_FieldClaimsMatchTheEvents(t *testing.T) {
	prepareFixture(t)
	_, started := wire(t, StartedEvent())
	_, screen := wire(t, screenEvent("viewers", "Viewers"))
	_, crash := wire(t, capturePanic("x"))
	assert.NotContains(t, started, "$app_version", "the notice says only a screen event adds the DataTug version")
	assert.Contains(t, screen, "$app_version")
	assert.Contains(t, started, "$session_id")
	assert.Contains(t, screen, "$session_id")
	assert.NotContains(t, crash, "$session_id", "the notice says a crash report adds no session")
	assert.Contains(t, Notice("linux"), "Started, exited and screen events add a session id")
	assert.Contains(t, Notice("linux"), "a screen event adds the DataTug version")
}

func TestShowNoticeOnce_FirstRunPrintsMarksAndHoldsBack(t *testing.T) {
	resetState(t)
	heldBackThisRun.Store(false)
	marker := filepath.Join(t.TempDir(), "datatug", ".telemetry-notice-shown")
	oldPath := noticeMarkerPath
	noticeMarkerPath = func() string { return marker }
	defer func() { noticeMarkerPath = oldPath }()

	var first bytes.Buffer
	ShowNoticeOnce(&first, true)
	assert.Equal(t, Notice(runtime.GOOS), first.String())
	assert.FileExists(t, marker)
	assert.False(t, Enabled(), "nothing is sent on the run that printed the notice")

	// The next run: the marker exists, no notice, telemetry on.
	heldBackThisRun.Store(false)
	var second bytes.Buffer
	ShowNoticeOnce(&second, true)
	assert.Empty(t, second.String())
	assert.True(t, Enabled())
}

func TestShowNoticeOnce_UnwritableMarkerPrintsAgainAndDoesNotFail(t *testing.T) {
	for name, fail := range map[string]func(){
		"mkdir fails": func() { mkdirAll = func(string, os.FileMode) error { return errors.New("read-only") } },
		"write fails": func() { writeFile = func(string, []byte, os.FileMode) error { return errors.New("read-only") } },
	} {
		t.Run(name, func(t *testing.T) {
			resetState(t)
			noSender(t)
			fail()
			var first, second bytes.Buffer
			ShowNoticeOnce(&first, true)
			heldBackThisRun.Store(false)
			ShowNoticeOnce(&second, true)
			assert.Equal(t, Notice(runtime.GOOS), first.String())
			assert.Equal(t, Notice(runtime.GOOS), second.String(), "an unwritten marker means the notice is printed again")
			assert.False(t, Enabled())
		})
	}
}

func TestNoticeMarkerPath_IsInTheCLIStateFolder(t *testing.T) {
	assert.True(t, strings.HasSuffix(filepath.ToSlash(noticeMarkerPath()), "/datatug/.telemetry-notice-shown"), noticeMarkerPath())
	assert.True(t, filepath.IsAbs(noticeMarkerPath()))
}

func TestStateFile_NeedsAnAbsoluteHome(t *testing.T) {
	old := userHomeDir
	defer func() { userHomeDir = old }()
	abs := t.TempDir()
	for name, tc := range map[string]struct {
		home string
		err  error
		want string
	}{
		"resolved":   {home: abs, want: filepath.Join(abs, "datatug", "f")},
		"error":      {err: errors.New("$HOME is not defined"), want: ""},
		"empty":      {home: "", want: ""},
		"relative":   {home: "some/dir", want: ""},
		"tilde text": {home: "~", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			userHomeDir = func() (string, error) { return tc.home, tc.err }
			assert.Equal(t, tc.want, stateFile("f"))
		})
	}
}

// With no home folder (a service without User=, a container) the first run
// creates nothing, in the working directory or anywhere, and sends nothing.
// Before this was fixed it created a folder named "~" in the working directory.
func TestShowNoticeOnce_HomeUnresolvedCreatesNothingAndSendsNothing(t *testing.T) {
	resetState(t)
	heldBackThisRun.Store(false)
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("home", "")
	assert.Empty(t, noticeMarkerPath(), "the real home cannot be resolved here")

	var out bytes.Buffer
	ShowNoticeOnce(&out, true)

	entries, err := os.ReadDir(cwd)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be created in the working directory")
	assert.Equal(t, Notice(runtime.GOOS), out.String(), "a person at a terminal is still told")
	assert.False(t, Enabled(), "nothing is sent on a run that could not record the notice")

	// Nor does the configuration file of the client.
	assert.Empty(t, getPosthogConfigFilePath())
	assert.Equal(t, posthogConfig{}, readPostHogConfig())
	err = writePostHogConfigToFile(t.Context(), posthogConfig{ApiKey: "k"})
	assert.ErrorIs(t, err, errNoStateFolder)
	entries, err = os.ReadDir(cwd)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestWritePostHogConfig_NeverCreatesAFileWithoutAPath(t *testing.T) {
	touched := noSender(t)
	old := getPosthogConfigFilePath
	getPosthogConfigFilePath = func() string { return "" }
	defer func() { getPosthogConfigFilePath = old }()
	assert.ErrorIs(t, writePostHogConfigToFile(t.Context(), posthogConfig{}), errNoStateFolder)
	assert.NotContains(t, *touched, "config file")
}

// A run whose stderr nobody reads (a script, a service, shell completion with
// 2>/dev/null) prints nothing, records nothing and sends nothing: the notice
// waits for the first run in a terminal.
func TestShowNoticeOnce_NotATerminalPrintsAndRecordsNothingAndSendsNothing(t *testing.T) {
	resetState(t)
	heldBackThisRun.Store(false)
	touched := noSender(t)
	var out bytes.Buffer

	ShowNoticeOnce(&out, false)

	assert.Empty(t, out.String())
	assert.NotContains(t, *touched, "marker dir")
	assert.NotContains(t, *touched, "marker write")
	assert.False(t, Enabled())

	// The first run in a terminal then prints and records it.
	heldBackThisRun.Store(false)
	ShowNoticeOnce(&out, true)
	assert.Equal(t, Notice(runtime.GOOS), out.String())
	assert.Contains(t, *touched, "marker write")
}

// A person who was told earlier is measured on a run with stderr discarded.
func TestShowNoticeOnce_MarkerExistsAndNotATerminalStaysOn(t *testing.T) {
	resetState(t)
	heldBackThisRun.Store(false)
	marker := filepath.Join(t.TempDir(), ".telemetry-notice-shown")
	require.NoError(t, os.WriteFile(marker, nil, 0o600))
	oldPath := noticeMarkerPath
	noticeMarkerPath = func() string { return marker }
	defer func() { noticeMarkerPath = oldPath }()
	var out bytes.Buffer

	ShowNoticeOnce(&out, false)

	assert.Empty(t, out.String())
	assert.True(t, Enabled())
}

// The second run, end to end in this package: the marker exists, no notice, and
// the started event reaches the client.
func TestSecondRun_SendsTheStartedEvent(t *testing.T) {
	resetState(t)
	heldBackThisRun.Store(false)
	marker := filepath.Join(t.TempDir(), ".telemetry-notice-shown")
	require.NoError(t, os.WriteFile(marker, nil, 0o600))
	oldPath := noticeMarkerPath
	noticeMarkerPath = func() string { return marker }
	defer func() { noticeMarkerPath = oldPath }()
	_, restoreConfig := withTempConfig(t, "api_key: k\ndistinct_id: install-id\n")
	defer restoreConfig()
	mock := &mockPosthogClient{}
	oldFetch, oldNew := getPostHogApiKeyFromServerFunc, posthogNewWithConfig
	defer func() { getPostHogApiKeyFromServerFunc, posthogNewWithConfig = oldFetch, oldNew }()
	getPostHogApiKeyFromServerFunc = func() (string, error) { return "", errors.New("not fetched") }
	posthogNewWithConfig = func(string, posthog.Config) (posthog.Client, error) { return mock, nil }
	mu.Lock()
	started, initialized, ph, queue = false, false, nil, nil
	mu.Unlock()

	var out bytes.Buffer
	ShowNoticeOnce(&out, true)
	Start()
	Enqueue(StartedEvent())
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := initialized
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	assert.Empty(t, out.String())
	require.Len(t, mock.enqueued, 1)
	assert.Equal(t, EventStarted, mock.enqueued[0].(posthog.Capture).Event)
}

// Neither real sender can run in a test binary: the key fetch refuses before
// any request, the client refuses before any event.
func TestTheKeyFetchCannotRunInATestBinary(t *testing.T) {
	assert.Panics(t, func() { _, _ = fetchPostHogApiKey() })
}

func TestTheKeyFetchRunsOutsideATestBinary(t *testing.T) {
	oldIn, oldDo := inTestBinary, httpDoRequest
	defer func() { inTestBinary, httpDoRequest = oldIn, oldDo }()
	inTestBinary = func() bool { return false }
	httpDoRequest = func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }
	_, err := fetchPostHogApiKey()
	assert.Error(t, err)
}

// --- item 4: what is sent is what the notice says ---

// wire returns the JSON the PostHog SDK builds for msg after this package
// prepared it: the top-level keys and the properties.
func wire(t *testing.T, msg posthog.Message) (top map[string]any, props map[string]any) {
	t.Helper()
	var apified any
	switch m := prepare(msg).(type) {
	case posthog.Capture:
		apified = m.APIfy()
	case posthog.Exception:
		apified = m.APIfy()
	default:
		t.Fatalf("unexpected message %T", m)
	}
	data, err := json.Marshal(apified)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &top))
	props, _ = top["properties"].(map[string]any)
	return top, props
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if !platformKeys[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// platformKeys are the SDK's optional, per-machine fields: the operating
// system version and distribution, and the identity of the running
// executable ($debug_images, a crash report only). The SDK adds them where it
// can read them, so a test cannot require them on every machine.
var platformKeys = map[string]bool{"$os_version": true, "$os_distro": true, "$debug_images": true}

func prepareFixture(t *testing.T) {
	t.Helper()
	resetState(t)
	mu.Lock()
	posthogDistinctID, sessionID = "install-id", "session-id"
	mu.Unlock()
}

// What every event carries: set by this package ($session_*) and by the SDK.
// The SDK client adds two constants on top when it sends: $geoip_disable and
// $is_server.
var commonProps = []string{"$go_version", "$lib", "$lib_version", "$os", "$session_duration", "$session_id", "$session_start_time"}

func TestWire_StartedAndExited(t *testing.T) {
	prepareFixture(t)
	for name, tc := range map[string]struct {
		event posthog.Capture
		name  string
	}{
		"started": {StartedEvent(), "DataTug CLI started"},
		"exited":  {ExitedEvent(), "DataTug CLI exited"},
	} {
		t.Run(name, func(t *testing.T) {
			top, props := wire(t, tc.event)
			assert.Equal(t, []string{"distinct_id", "event", "properties", "timestamp", "uuid"}, keys(top))
			assert.Equal(t, tc.name, top["event"])
			assert.Equal(t, "install-id", top["distinct_id"])
			assert.Equal(t, commonProps, keys(props))
		})
	}
}

func TestWire_ScreenOpened(t *testing.T) {
	prepareFixture(t)
	oldVersion := version
	version = "1.2.3"
	defer func() { version = oldVersion }()

	top, props := wire(t, screenEvent("viewers/sqlite", "SQLite Viewer"))
	assert.Equal(t, "Screen opened", top["event"])
	want := append(append([]string{}, commonProps...), "$app_name", "$app_version", "$screen_id", "$screen_name")
	sort.Strings(want)
	assert.Equal(t, want, keys(props))
	assert.Equal(t, "viewers/sqlite", props["$screen_id"])
	assert.Equal(t, "1.2.3", props["$app_version"])

	// An empty name is not sent.
	_, props = wire(t, screenEvent("viewers", ""))
	assert.NotContains(t, props, "$screen_name")
}

func TestWire_Panic(t *testing.T) {
	prepareFixture(t)
	event := capturePanic("open /Users/alice/projects/secret-project/db.sqlite: no such file; host db.internal.example.com")
	top, props := wire(t, event)
	assert.Equal(t, []string{"event", "library", "library_version", "properties", "timestamp", "type", "uuid"}, keys(top), "the SDK names itself at the top of a crash report")
	assert.Equal(t, "$exception", top["event"])
	assert.Equal(t, []string{"$exception_list", "$go_version", "$lib", "$lib_version", "$os", "distinct_id"}, keys(props), "a crash report carries no session fields")

	list := props["$exception_list"].([]any)
	require.Len(t, list, 1)
	item := list[0].(map[string]any)
	assert.Equal(t, []string{"stacktrace", "type", "value"}, keys(item))
	assert.Equal(t, "panic", item["type"])
	assert.Equal(t, "string", item["value"], "the value of a crash report is the Go type, never the text")

	frames := item["stacktrace"].(map[string]any)["frames"].([]any)
	require.NotEmpty(t, frames)
	for _, raw := range frames {
		file := raw.(map[string]any)["filename"].(string)
		assert.False(t, strings.ContainsAny(file, `/\`), "a frame names its source file only, not its path: %q", file)
	}

	whole, err := json.Marshal(top)
	require.NoError(t, err)
	for _, secret := range []string{"alice", "secret-project", "db.internal", "/Users", os.TempDir()} {
		assert.NotContains(t, string(whole), secret)
	}
	exe, _ := os.Executable()
	assert.NotContains(t, string(whole), filepath.Dir(exe), "the path of the binary is not sent")
}

func TestPanicEvent_BinaryPathIsRemovedFromDebugImages(t *testing.T) {
	event := capturePanic("x")
	for _, image := range event.DebugImages {
		assert.Empty(t, image.CodeFile)
	}
	assert.Equal(t, "panic", event.ExceptionList[0].Type)
	assert.Empty(t, event.DistinctId, "the distinct id is added when the event is prepared")
}

func TestPanicEvent_WithoutStacktrace(t *testing.T) {
	// A defensive branch: an item without a stack must not crash the scrub.
	assert.NotPanics(t, func() {
		scrubException(&posthog.Exception{ExceptionList: []posthog.ExceptionItem{{Type: "t", Value: "v"}}})
	})
}

func TestPanicValue(t *testing.T) {
	t.Run("a string is its type only", func(t *testing.T) {
		assert.Equal(t, "string", panicValue("anything /a/path"))
	})
	t.Run("an error is its type only", func(t *testing.T) {
		assert.Equal(t, "*errors.errorString", panicValue(errors.New("dial db.example.com")))
		assert.Equal(t, "*fmt.wrapError", panicValue(fmt.Errorf("x: %w", errors.New("y"))))
	})
	t.Run("a runtime error keeps its message: it names numbers and types only", func(t *testing.T) {
		var got string
		func() {
			defer func() { got = panicValue(recover()) }()
			var s []int
			_ = s[len(os.Args)+5]
		}()
		assert.Contains(t, got, "index out of range")
		// A failed type assertion is a *runtime.TypeAssertionError, the case that
		// needs the "*" trimmed from the type name.
		var assertion string
		func() {
			defer func() { assertion = panicValue(recover()) }()
			var v any = "text"
			_ = v.(int)
		}()
		assert.Contains(t, assertion, "interface conversion")
	})
	t.Run("an error of another package that merely implements runtime.Error is its type only", func(t *testing.T) {
		assert.Equal(t, "dtlog.fakeRuntimeError", panicValue(fakeRuntimeError{}))
	})
	t.Run("nil", func(t *testing.T) {
		assert.Equal(t, "<nil>", panicValue(nil))
	})
}

type fakeRuntimeError struct{}

func (fakeRuntimeError) Error() string { return "/Users/alice/secret" }
func (fakeRuntimeError) RuntimeError() {}

var _ runtime.Error = fakeRuntimeError{}

// capturePanic panics with v and returns the event main would build.
func capturePanic(v any) posthog.Exception {
	var event posthog.Exception
	func() {
		defer func() { event = PanicEvent(recover()) }()
		panic(v)
	}()
	return event
}

func TestEventNamesAreWhatTheNoticeSays(t *testing.T) {
	assert.Equal(t, "DataTug CLI started", EventStarted)
	assert.Equal(t, "DataTug CLI exited", EventExited)
	assert.Equal(t, "Screen opened", EventScreen)
	assert.Equal(t, "panic", PanicEventTitle)
	assert.Equal(t, EventStarted, StartedEvent().Event)
	assert.Equal(t, EventExited, ExitedEvent().Event)
}
