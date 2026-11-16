package dtproject

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/cloudproject"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

// projectData is everything the screens of an open project show. It is loaded
// once, before the project page opens, so the screens never wait.
type projectData struct {
	ref          dtconfig.ProjectRef
	project      *datatug.Project
	environments datatug.Environments
	envsErr      error
	databases    datatug.ProjDbDrivers
	dbsErr       error
	// hasQueries is set when queries were loaded: a cloud project lists them,
	// a project on disk keeps the placeholder screen.
	hasQueries bool
	queries    []cloudproject.Item
	queriesErr error
	// readOnly marks a DataTug Cloud project: it is edited on datatug.app.
	readOnly bool
}

// title is the project's own title, or the one of its reference.
func (d projectData) title() string {
	if d.project.Title != "" {
		return d.project.Title
	}
	return projectTitle(&d.ref)
}

// loadProjectData loads the project and the lists its menu counts. Only a
// project that fails to load is an error: a failing list is shown in its own
// panel.
func loadProjectData(ctx context.Context, ref dtconfig.ProjectRef, store datatug.ProjectStore) (projectData, error) {
	project, err := store.LoadProject(ctx)
	if err != nil {
		return projectData{}, err
	}
	data := projectData{ref: ref, project: project}
	data.environments, data.envsErr = project.GetEnvironments(ctx)
	data.databases, data.dbsErr = project.GetDBs(ctx)
	return data, nil
}

// openProject returns the command that loads the project of ref from its files
// and opens its page under the current one. A project whose files are missing is
// an alert; any other failure is shown in the content panel.
func openProject(ref dtconfig.ProjectRef) tea.Cmd { return openProjectAt(ref, sectionProject, false) }

// openProjectAt is openProject showing section s. With register, a project that
// loads is added to the settings first, so it is listed with the others.
func openProjectAt(ref dtconfig.ProjectRef, s section, register bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		store := newProjectStore(ref.ID, fsutil.ExpandHome(ref.Path))
		if _, err := store.LoadProjectFile(ctx); errors.Is(err, datatug.ErrProjectDoesNotExist) {
			return nav.AlertMsg{Title: "Not able to open DataTug project", Message: err.Error(), FocusBack: nav.FocusToContent}
		}
		data, err := loadProjectData(ctx, ref, store)
		if err != nil {
			return datatugui.ReportError("open project "+projectTitle(&ref), err)()
		}
		if register {
			if err = addProjectToSettings(ref); err != nil {
				return datatugui.ReportError("add project "+projectTitle(&ref)+" to settings", err)()
			}
		}
		bumpRecentProject(ref.ID)
		return nav.PushMsg{Page: projectPageAt(data, s)}
	}
}

// openProjectFromProjects returns the commands that show the projects list and
// open the project of ref under it, for a flow that just created the project.
func openProjectFromProjects(ref dtconfig.ProjectRef) tea.Cmd {
	return tea.Sequence(datatugui.Open(datatugui.ScreenProjects, nav.FocusToContent), openProject(ref))
}
