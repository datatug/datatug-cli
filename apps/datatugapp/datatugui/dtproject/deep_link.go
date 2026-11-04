package dtproject

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/deeplink"
	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

// Notices of `datatug ui --deep-link` for projects the terminal cannot open.
const (
	deepLinkTitle      = "Open DataTug link"
	cloudNotice        = "DataTug Cloud projects can't be opened in the terminal yet — coming soon"
	browserNotice      = "This project is stored in your browser; export it or push it to GitHub to open it here"
	otherStoreNotice   = "Projects of this store can't be opened in the terminal yet"
	noPageScreenNotice = "This page has no terminal screen yet; showing the project"
)

// deepLinkResolver returns the command that opens what a link names. It runs
// with the Projects screen shown, so a resolver that cannot open the project
// only explains why.
type deepLinkResolver func(link deeplink.Link) tea.Cmd

// deepLinkResolvers has one resolver per kind of link. A DataTug Cloud
// resolver replaces the notice once the terminal can read cloud projects.
var deepLinkResolvers = map[deeplink.Kind]deepLinkResolver{
	deeplink.KindGitHub:     openGitHubLink,
	deeplink.KindAgent:      openAgentLink,
	deeplink.KindCloud:      deepLinkNotice(cloudNotice),
	deeplink.KindBrowser:    deepLinkNotice(browserNotice),
	deeplink.KindOtherStore: deepLinkNotice(otherStoreNotice),
}

// OpenDeepLink returns the command that opens the project and page of link
// over the Projects screen, or nil for a link that names no project.
func OpenDeepLink(link deeplink.Link) tea.Cmd {
	if resolve := deepLinkResolvers[link.Kind]; resolve != nil {
		return resolve(link)
	}
	return nil
}

// deepLinkNotice is a resolver that only shows message.
func deepLinkNotice(message string) deepLinkResolver {
	return func(deeplink.Link) tea.Cmd { return notice(message) }
}

func notice(message string) tea.Cmd {
	return nav.Alert(deepLinkTitle, message, 0, nav.FocusToContent)
}

// linkSection is the section of the project menu that shows a page of the web
// app, and a notice when the page has no screen of its own here.
func linkSection(page []string) (section, string) {
	if len(page) == 0 {
		return sectionProject, ""
	}
	switch strings.ToLower(page[0]) {
	case "overview", "project":
		return sectionProject, ""
	case "queries", "query":
		return sectionQueries, ""
	case "board", "boards":
		return sectionDashboards, ""
	case "database", "browser-db":
		return sectionDatabases, ""
	case "env", "environments":
		return sectionEnvironments, ""
	}
	return sectionProject, noPageScreenNotice
}

// openLinkedProject opens ref at the section of page, then shows the notices.
func openLinkedProject(ref dtconfig.ProjectRef, page []string, register bool, notices ...string) tea.Cmd {
	s, pageNotice := linkSection(page)
	if pageNotice != "" {
		notices = append(notices, pageNotice)
	}
	cmd := openProjectAt(ref, s, register)
	if len(notices) == 0 {
		return cmd
	}
	return tea.Sequence(cmd, notice(strings.Join(notices, ". ")))
}

// openGitHubLink opens a project of a GitHub repository from its clone in
// ~/datatug/github.com/<owner>/<repo>, cloning the repository first when it is
// not there. A project opened for the first time is added to the settings.
func openGitHubLink(link deeplink.Link) tea.Cmd {
	p := link.GitHub
	repoDir := fmt.Sprintf(githubPathFormat, dtroot.Dir) + p.Owner + "/" + p.Repo
	ref := dtconfig.ProjectRef{
		ID:    "github.com/" + p.Owner + "/" + p.Repo,
		Path:  repoDir,
		Title: p.Repo + " @ github.com/" + p.Owner,
	}
	if p.Folder != "" {
		ref.ID += "/" + p.Folder
		ref.Path += "/" + p.Folder
		ref.Title = p.Repo + "/" + p.Folder + " @ github.com/" + p.Owner
	}
	var notices []string
	if p.Ref != "" {
		notices = append(notices, "The terminal shows the checked-out branch of "+repoDir+", not "+p.Ref)
	}
	return func() tea.Msg {
		registered, err := registeredProject(ref.Path)
		if err != nil {
			return datatugui.ReportError("read settings", err)()
		}
		if registered != nil {
			ref = *registered
		}
		exists, err := fsutil.DirExists(fsutil.ExpandHome(repoDir))
		if err != nil {
			return datatugui.ReportError("open "+ref.Title, err)()
		}
		open := openLinkedProject(ref, link.Page, registered == nil, notices...)
		if exists {
			return open()
		}
		url := "https://github.com/" + p.Owner + "/" + p.Repo + ".git"
		return nav.PushMsg{Page: nav.Page{Title: "Cloning project", Content: newCloneProject(url, repoDir, open)}}
	}
}

// registeredProject is the project of the settings in dir, or the demo
// project, which is never added to them; nil when there is none.
func registeredProject(dir string) (*dtconfig.ProjectRef, error) {
	dir = filepath.Clean(fsutil.ExpandHome(dir))
	if demo := newDemoProject1Ref(); filepath.Clean(fsutil.ExpandHome(demo.Path)) == dir {
		return demo, nil
	}
	settings, err := readSettings()
	if err != nil {
		return nil, err
	}
	for _, project := range settings.Projects {
		if project.Path != "" && filepath.Clean(fsutil.ExpandHome(project.Path)) == dir {
			return project, nil
		}
	}
	return nil, nil
}

// openAgentLink opens a project that a `datatug serve` agent on this machine
// serves: the agent names a project by its id in the settings.
func openAgentLink(link deeplink.Link) tea.Cmd {
	return func() tea.Msg {
		settings, err := readSettings()
		if err != nil {
			return datatugui.ReportError("read settings", err)()
		}
		for _, project := range settings.Projects {
			if project.ID == link.ProjectID {
				return openLinkedProject(*project, link.Page, false)()
			}
		}
		return nav.AlertMsg{
			Title:     deepLinkTitle,
			Message:   fmt.Sprintf("Project %q is not registered in the DataTug settings of this computer", link.ProjectID),
			FocusBack: nav.FocusToContent,
		}
	}
}
