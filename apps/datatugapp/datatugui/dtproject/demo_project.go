package dtproject

import (
	"context"
	"io"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/go-git/go-git/v5"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

const (
	demoProjectsRepoID = "datatug-demo-project"
	datatugOrg         = "datatug"
	demoProjectOrigin  = "github.com/" + datatugOrg + "/" + demoProjectsRepoID

	demoProject1DirName = "demo-project-1"
	demoProject1LocalID = "github.com~" + datatugOrg + "~" + demoProjectsRepoID + "~" + demoProject1DirName
	demoProject1Title   = "Demo Project 1"
	demoProject1FullID  = demoProjectOrigin + "/" + demoProject1LocalID

	datatugDemoProjectsGitURL = "https://" + demoProjectOrigin
	datatugDemoProjectsDir    = "~/datatug/" + demoProjectOrigin
	demoProjectDir            = datatugDemoProjectsDir + "/" + demoProject1DirName
)

func newDemoProject1Ref() *dtconfig.ProjectRef {
	return &dtconfig.ProjectRef{
		ID:    demoProject1LocalID,
		Path:  demoProjectDir,
		Url:   demoProjectOrigin,
		Title: demoProject1Title,
	}
}

// openDemoProject returns the command that opens the demo project, cloning the
// demo repository first when it is not on the disk yet.
func openDemoProject() tea.Cmd {
	return func() tea.Msg {
		ref := newDemoProject1Ref()
		exists, err := fsutil.DirExists(fsutil.ExpandHome(ref.Path))
		if err != nil {
			return datatugui.ReportError("open demo project", err)()
		}
		if exists {
			return openProject(*ref)()
		}
		return nav.PushMsg{Page: nav.Page{Title: "Cloning project", Content: newCloneDemo(*ref)}}
	}
}

// progressWriter turns the progress text of a git operation into messages.
type progressWriter struct {
	report func(tea.Msg)
	wrap   func(text string) tea.Msg
}

// Write implements io.Writer.
func (w progressWriter) Write(p []byte) (int, error) {
	w.report(w.wrap(string(p)))
	return len(p), nil
}

var _ io.Writer = progressWriter{}

// cloneRepo clones url into dir, reporting git's progress with wrap.
func cloneRepo(ctx context.Context, url, dir string, report func(tea.Msg), wrap func(string) tea.Msg) error {
	_, err := gitClone(ctx, dir, false, &git.CloneOptions{
		URL:      url,
		Progress: progressWriter{report: report, wrap: wrap},
	})
	return err
}

// cloneProgress is the latest progress text of the clone of the demo repository.
type cloneProgress struct{ text string }

// cloneDone ends the clone of the demo repository.
type cloneDone struct{ err error }

// cloneDemo is the screen shown while the demo repository is cloned; it opens
// the demo project when the clone is complete.
type cloneDemo struct {
	geo    geometry
	ref    dtconfig.ProjectRef
	stream *stream
	pane   widgets.TextPane
}

var (
	_ nav.Screen = cloneDemo{}
	_ nav.Titled = cloneDemo{}
)

func newCloneDemo(ref dtconfig.ProjectRef) cloneDemo {
	c := cloneDemo{ref: ref, pane: widgets.NewTextPane("clone-demo")}
	c.stream = newStream(cloneDemoWork)
	return c
}

// cloneDemoWork is the work of the stream: clone the demo repository.
func cloneDemoWork(ctx context.Context, report func(tea.Msg)) tea.Msg {
	if err := os.MkdirAll(fsutil.ExpandHome(filepath.Dir(datatugDemoProjectsDir)), 0o755); err != nil {
		return cloneDone{err: err}
	}
	wrap := func(text string) tea.Msg { return cloneProgress{text: text} }
	return cloneDone{err: cloneRepo(ctx, datatugDemoProjectsGitURL, fsutil.ExpandHome(datatugDemoProjectsDir), report, wrap)}
}

// Init implements nav.Screen.
func (c cloneDemo) Init() tea.Cmd { return c.stream.Start() }

// Update implements nav.Screen.
func (c cloneDemo) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if c.geo.track(msg) {
		c.pane.SetSize(c.geo.w, c.geo.h)
		return c, nil
	}
	switch msg := msg.(type) {
	case cloneProgress:
		c.pane.SetContent(msg.text)
		return c, c.stream.Next()
	case cloneDone:
		if msg.err != nil {
			return c, datatugui.ReportError("clone "+datatugDemoProjectsGitURL, msg.err)
		}
		return c, tea.Sequence(nav.Pop(), openProject(c.ref))
	}
	return c, nil
}

// View implements nav.Screen.
func (c cloneDemo) View() string { return c.pane.View() }

// Title implements nav.Titled.
func (c cloneDemo) Title() string { return "Cloning project..." }
