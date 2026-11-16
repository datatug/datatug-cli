package dtproject

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/cloudproject"
	"github.com/datatug/datatug-cli/pkg/deeplink"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

// Notices of DataTug Cloud projects.
const (
	// cloudDeepLinkNotice is shown when a cloud link reaches the resolver
	// without having been opened before the UI started.
	cloudDeepLinkNotice = "Open DataTug Cloud projects with: datatug ui --deep-link <datatug.app address>"
	// PersonalCloudNotice is the line for a personal (no Space) cloud project.
	PersonalCloudNotice = "Personal DataTug Cloud projects can't be opened in the terminal yet. Move the project to a Space to open it here."
	// ReadOnlyHint is shown for any edit of a cloud project.
	ReadOnlyHint = "Edit this project on datatug.app"
)

// cloudLoadTimeout bounds a cloud project read started from inside the UI.
const cloudLoadTimeout = 30 * time.Second

// CloudLoader reads a cloud project with the saved login. The commands
// package sets it; it is nil when the UI runs without one (tests).
var CloudLoader func(ctx context.Context, ref cloudproject.Ref) (cloudproject.Project, error)

// CloudErrorLine is the one-line message for a failed cloud read. The commands
// package sets it so the UI and the exit lines word errors the same way.
var CloudErrorLine = func(err error) string { return err.Error() }

// OpenCloudProject returns the command that shows an already loaded cloud
// project at the section of page and records it as a recent project.
func OpenCloudProject(p cloudproject.Project, page []string) tea.Cmd {
	return func() tea.Msg {
		data := cloudProjectData(p)
		if err := rememberCloudProject(data.ref); err != nil {
			return datatugui.ReportError("add project "+data.title()+" to settings", err)()
		}
		s, pageNotice := linkSection(page)
		cmd := tea.Cmd(func() tea.Msg { return nav.PushMsg{Page: projectPageAt(data, s)} })
		if pageNotice != "" {
			cmd = tea.Sequence(cmd, notice(pageNotice))
		}
		return cmd()
	}
}

// cloudRef is the settings entry of a cloud project: its address and title,
// and no folder.
func cloudRef(p cloudproject.Project) dtconfig.ProjectRef {
	return dtconfig.ProjectRef{
		ID:    "datatug.app/space/" + p.Ref.SpaceID + "/" + p.Ref.ProjectID,
		Url:   p.Ref.Address(),
		Title: p.Title,
	}
}

// IsCloudRef reports whether a settings entry is a DataTug Cloud project.
func IsCloudRef(ref dtconfig.ProjectRef) bool {
	return ref.Path == "" && strings.HasPrefix(ref.Url, "https://datatug.app/space/")
}

func cloudProjectData(p cloudproject.Project) projectData {
	data := projectData{
		ref:        cloudRef(p),
		project:    &datatug.Project{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: p.ID, Title: p.Title}}},
		envsErr:    p.EnvsErr,
		dbsErr:     p.DBsErr,
		hasQueries: true,
		queries:    p.Queries,
		queriesErr: p.QueriesErr,
		readOnly:   true,
	}
	for _, e := range p.Environments {
		data.environments = append(data.environments, &datatug.Environment{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: e.ID, Title: e.Title}}})
	}
	for _, d := range p.Databases {
		data.databases = append(data.databases, &datatug.ProjDbDriver{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: d.ID, Title: d.Title}}})
	}
	if data.envsErr != nil {
		data.envsErr = cloudListError(data.envsErr)
	}
	if data.dbsErr != nil {
		data.dbsErr = cloudListError(data.dbsErr)
	}
	if data.queriesErr != nil {
		data.queriesErr = cloudListError(data.queriesErr)
	}
	return data
}

type lineError string

func (e lineError) Error() string { return string(e) }

func cloudListError(err error) error { return lineError(CloudErrorLine(err)) }

// rememberCloudProject adds the project to the settings once and bumps it in
// the recent projects.
func rememberCloudProject(ref dtconfig.ProjectRef) error {
	settings, err := readSettings()
	if err != nil {
		return err
	}
	known := false
	for _, p := range settings.Projects {
		if p != nil && p.ID == ref.ID {
			known = true
			break
		}
	}
	if !known {
		if err = addProjectToSettings(ref); err != nil {
			return err
		}
	}
	bumpRecentProject(ref.ID)
	return nil
}

// openCloudRef reads a cloud project of the settings again, so the backend
// checks access again, and opens it.
func openCloudRef(ref dtconfig.ProjectRef) tea.Cmd {
	return func() tea.Msg {
		link, err := deeplink.Parse(ref.Url)
		if err != nil || link.Kind != deeplink.KindCloud || link.SpaceID == "" {
			return notice("Not a DataTug Cloud project address: " + ref.ID)()
		}
		cref := cloudproject.Ref{SpaceID: link.SpaceID, ProjectID: link.ProjectID}
		if err = cref.Validate(); err != nil {
			return notice(err.Error())()
		}
		if CloudLoader == nil {
			return notice(cloudDeepLinkNotice)()
		}
		ctx, cancel := context.WithTimeout(context.Background(), cloudLoadTimeout)
		defer cancel()
		p, err := CloudLoader(ctx, cref)
		if err != nil {
			return notice(CloudErrorLine(err))()
		}
		return OpenCloudProject(p, nil)()
	}
}
