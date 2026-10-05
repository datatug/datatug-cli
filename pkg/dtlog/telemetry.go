package dtlog

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/strongo/logus"
)

// The variables that turn telemetry off. Enabled is the only place that reads
// them: every sender in this package asks it, and nothing outside this package
// can send, because nothing outside it imports the PostHog client (a test
// walks the module to prove it).
const (
	// EnvTelemetry set to 0, false or off (any case) turns telemetry off.
	EnvTelemetry = "DATATUG_TELEMETRY"
	// EnvDoNotTrack set to anything but an empty string or 0 turns telemetry
	// off (https://consoledonottrack.com).
	EnvDoNotTrack = "DO_NOT_TRACK"
	// EnvCI set to anything but an empty string or false turns telemetry off.
	EnvCI = "CI"
)

// The names and the title of what is sent. NoticeText and the README list the
// fields of each; a test builds each event and fails when the fields change.
const (
	EventStarted    = "DataTug CLI started"
	EventExited     = "DataTug CLI exited"
	EventScreen     = "Screen opened"
	PanicEventTitle = "panic"
)

// NoticeText is printed on stderr the first time the CLI runs with telemetry
// on. At most six lines; a test holds it to that and to what it must say.
const NoticeText = `DataTug sends anonymous usage events: "DataTug CLI started", "DataTug CLI exited", "Screen opened" (terminal UI) and crash reports.
Each carries a random install id, a session id and its timing, the OS name and version, and the Go and DataTug versions; a crash report adds the Go type of the error and its stack frames.
No database content, query text, path, host or credential is sent. Details: https://github.com/datatug/datatug-cli#telemetry
Nothing is sent on this run; events start with the next one.
To turn it off, run: export DATATUG_TELEMETRY=0 (DO_NOT_TRACK=1 and CI=true turn it off too).
`

const noticeMarkerFile = ".telemetry-notice-shown"

// seams for testing
var (
	noticeMarkerPath = func() string { return fsutil.ExpandHome("~/datatug/" + noticeMarkerFile) }
	statFile         = os.Stat
	mkdirAll         = os.MkdirAll
	writeFile        = os.WriteFile

	// inTestBinary and newPosthogClient let the one function that creates a
	// PostHog client refuse, loudly, in a test binary.
	inTestBinary     = testing.Testing
	newPosthogClient = posthog.NewWithConfig
)

// noticeShownThisRun is true on the run that printed the first-run notice:
// nothing is sent on it, so nobody is measured before they were told.
var noticeShownThisRun atomic.Bool

// Enabled reports whether telemetry is on for this process. It is the one
// function that decides: Start, Enqueue and so every event ask it, and when it
// says no no client is created, no event is queued and no request leaves.
func Enabled() bool {
	return envAllows() && !noticeShownThisRun.Load()
}

func envAllows() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvTelemetry))) {
	case "0", "false", "off":
		return false
	}
	if v := os.Getenv(EnvDoNotTrack); v != "" && v != "0" {
		return false
	}
	if v := os.Getenv(EnvCI); v != "" && !strings.EqualFold(v, "false") {
		return false
	}
	return true
}

// ShowNoticeOnce prints NoticeText on w the first time it runs with telemetry
// on and leaves a marker in the CLI's state folder (~/datatug) so it is printed
// once per user. When the marker cannot be written the notice is printed again
// next time and the run does not fail. With telemetry off it does nothing at
// all: nothing printed, nothing created.
func ShowNoticeOnce(w io.Writer) {
	if !envAllows() {
		return
	}
	marker := noticeMarkerPath()
	if _, err := statFile(marker); err == nil {
		return
	}
	_, _ = io.WriteString(w, NoticeText)
	noticeShownThisRun.Store(true)
	if err := mkdirAll(filepath.Dir(marker), 0o755); err != nil {
		logus.Warningf(context.Background(), "could not record that the telemetry notice was shown: %v", err)
		return
	}
	if err := writeFile(marker, []byte("The DataTug telemetry notice was shown. Delete this file to see it again.\n"), 0o644); err != nil {
		logus.Warningf(context.Background(), "could not record that the telemetry notice was shown: %v", err)
	}
}

// StartedEvent is the event sent when a command starts. It has no fields of
// its own: what it carries is added by prepare.
func StartedEvent() posthog.Capture { return posthog.Capture{Event: EventStarted} }

// ExitedEvent is the event sent when a command ends.
func ExitedEvent() posthog.Capture { return posthog.Capture{Event: EventExited} }

// screenEvent is the event sent when a terminal UI screen opens. id and name
// are fixed labels of the application's own screens (such as viewers/sqlite and
// SQLite Viewer), never a project, database or file a person chose.
func screenEvent(id, name string) posthog.Capture {
	props := posthog.NewProperties().
		Set("$app_name", "DataTug").
		Set("$app_version", version).
		Set("$screen_id", id)
	if name != "" {
		props.Set("$screen_name", name)
	}
	return posthog.Capture{Event: EventScreen, Properties: props}
}

// PanicEvent is the crash report for a recovered panic value r. It carries the
// Go type of r (for a panic of the Go runtime itself, such as an index out of
// range, its message, which names numbers and types only) and the stack, with
// each frame's source file reduced to its name. The text of a panic can hold a
// path, a host or an argument a person typed, so it is never sent, and neither
// is the path of the binary or of any file.
func PanicEvent(r any) posthog.Exception {
	event := posthog.NewDefaultException(time.Time{}, "", PanicEventTitle, panicValue(r))
	scrubException(&event)
	return event
}

// scrubException removes from event what could name a person's files: the
// directory of every frame's source file and the path of the binary.
func scrubException(event *posthog.Exception) {
	for i := range event.ExceptionList {
		if st := event.ExceptionList[i].Stacktrace; st != nil {
			for j := range st.Frames {
				st.Frames[j].Filename = path.Base(st.Frames[j].Filename)
			}
		}
	}
	for i := range event.DebugImages {
		event.DebugImages[i].CodeFile = ""
	}
}

func panicValue(r any) string {
	typ := fmt.Sprintf("%T", r)
	if err, ok := r.(runtime.Error); ok && strings.HasPrefix(strings.TrimPrefix(typ, "*"), "runtime.") {
		return err.Error()
	}
	return typ
}

// newClientFromConfig creates the PostHog client. It panics in a test binary:
// a test that reaches it would send events, so it fails instead.
func newClientFromConfig(apiKey string, config posthog.Config) (posthog.Client, error) {
	if inTestBinary() {
		panic("dtlog: a real PostHog client must not be created in a test binary; fake posthogNewWithConfig")
	}
	return newPosthogClient(apiKey, config)
}
