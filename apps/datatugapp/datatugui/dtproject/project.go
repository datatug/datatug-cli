package dtproject

import (
	"strconv"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// section identifies a node of the project menu; it is the node's Ref.
type section string

const (
	sectionProject      section = "project"
	sectionDashboards   section = "dashboards"
	sectionDatabases    section = "databases"
	sectionEnvironments section = "environments"
	sectionQueries      section = "queries"
	sectionEntities     section = "entities"
	sectionLogs         section = "logs"

	projectMenuID = "project-menu"
)

// projectPage is the page of an open project: its menu on the left and the
// section the menu points at on the right.
func projectPage(data projectData) nav.Page {
	return nav.Page{
		Title:   data.title(),
		Menu:    newProjectMenu(data),
		Content: newSectionScreen(data, sectionProject),
		Focus:   nav.FocusToMenu,
	}
}

// newSectionScreen returns the content screen of a section.
func newSectionScreen(data projectData, s section) nav.Screen {
	switch s {
	case sectionDashboards:
		return newTextScreen("Dashboards", "List of dashboards here")
	case sectionQueries:
		return newTextScreen("Queries", "List of queries here")
	case sectionEnvironments:
		return newItemList("Environments", environmentItems(data.environments), data.envsErr)
	case sectionDatabases:
		return newItemList("Databases", databaseItems(data.databases), data.dbsErr)
	}
	return newTextScreen("Project: "+data.title(), projectSummary(data))
}

// projectSummary lists what is known about the project without loading more.
func projectSummary(data projectData) string {
	text := "ID: " + data.ref.ID
	if data.ref.Path != "" {
		text += "\nPath: " + data.ref.Path
	}
	if data.ref.Url != "" {
		text += "\nURL: " + data.ref.Url
	}
	return text
}

func environmentItems(environments datatug.Environments) []widgets.MenuItem {
	items := make([]widgets.MenuItem, len(environments))
	for i, e := range environments {
		items[i] = idTitleItem(e.ID, e.Title)
	}
	return items
}

func databaseItems(databases datatug.ProjDbDrivers) []widgets.MenuItem {
	items := make([]widgets.MenuItem, len(databases))
	for i, d := range databases {
		items[i] = idTitleItem(d.ID, d.Title)
	}
	return items
}

// idTitleItem is a list row showing an ID with its title under it, unless the
// title only repeats the ID. The first letter of the ID is the row's shortcut.
func idTitleItem(id, title string) widgets.MenuItem {
	if title == id {
		title = ""
	}
	item := widgets.MenuItem{ID: id, Label: id, Detail: title}
	if runes := []rune(id); len(runes) > 0 {
		item.Shortcut = runes[0]
	}
	return item
}

// projectMenu is the menu panel of a project: a tree whose nodes open sections.
// The cursor previews a section in the content panel, Enter moves focus to it.
type projectMenu struct {
	geo  geometry
	data projectData
	tree widgets.Tree
}

var (
	_ nav.Screen       = projectMenu{}
	_ nav.ShortHelper  = projectMenu{}
	_ widgets.Boundary = projectMenu{}
)

func newProjectMenu(data projectData) projectMenu {
	environments := make([]widgets.TreeNode, len(data.environments))
	for i, e := range data.environments {
		environments[i] = widgets.TreeNode{ID: "env:" + e.ID, Text: e.ID, Ref: sectionEnvironments}
	}
	leaf := func(s section, text string) widgets.TreeNode {
		return widgets.TreeNode{ID: string(s), Text: text, Ref: s}
	}
	root := widgets.TreeNode{
		ID:   string(sectionProject),
		Text: projectShortTitle(&data.ref),
		Ref:  sectionProject,
		Children: []widgets.TreeNode{
			leaf(sectionDashboards, "Dashboards"),
			leaf(sectionDatabases, "Databases"),
			{
				ID:       string(sectionEnvironments),
				Text:     "Environments (" + strconv.Itoa(len(data.environments)) + ")",
				Ref:      sectionEnvironments,
				Children: environments,
			},
			leaf(sectionEntities, "Entities"),
			leaf(sectionQueries, "Queries"),
			leaf(sectionLogs, "Logs"),
		},
	}
	tree := widgets.NewTree(projectMenuID, root)
	tree.Expand(string(sectionProject))
	return projectMenu{data: data, tree: tree}
}

// Init implements nav.Screen.
func (m projectMenu) Init() tea.Cmd { return nil }

func (m *projectMenu) sync() {
	m.tree.SetSize(m.geo.w, m.geo.h)
	if m.geo.focused {
		m.tree.Focus()
	} else {
		m.tree.Blur()
	}
}

// Update implements nav.Screen.
func (m projectMenu) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if m.geo.track(msg) {
		m.sync()
		return m, nil
	}
	switch msg := msg.(type) {
	case widgets.NodeHighlightedMsg:
		return m, m.show(msg.Node, nav.FocusToKeep)
	case widgets.NodeSelectedMsg:
		return m, m.show(msg.Node, nav.FocusToContent)
	}
	tree, cmd := m.tree.Update(msg)
	m.tree = tree
	return m, cmd
}

// show opens the section a node stands for in the content panel. Sections
// without a screen of their own leave the content as it is.
func (m projectMenu) show(node widgets.TreeNode, focus nav.FocusTo) tea.Cmd {
	switch s := node.Ref.(section); s {
	case sectionEntities, sectionLogs:
		return nil
	default:
		return nav.SetPanels(nil, newSectionScreen(m.data, s), focus)
	}
}

// View implements nav.Screen.
func (m projectMenu) View() string { return m.tree.View() }

// ShortHelp implements nav.ShortHelper.
func (m projectMenu) ShortHelp() []key.Binding { return m.tree.KeyMap.ShortHelp() }

// AtEdge implements widgets.Boundary.
func (m projectMenu) AtEdge(dir widgets.Direction) bool { return m.tree.AtEdge(dir) }

// itemList is a titled list of IDs, or the error that stopped it loading.
type itemList struct {
	geo   geometry
	title string
	list  widgets.List
	err   error
}

var (
	_ nav.Screen       = itemList{}
	_ nav.Titled       = itemList{}
	_ nav.ShortHelper  = itemList{}
	_ widgets.Boundary = itemList{}
)

func newItemList(title string, items []widgets.MenuItem, err error) itemList {
	l := widgets.NewList(title)
	rows := make([]list.Item, len(items))
	for i, item := range items {
		rows[i] = item
	}
	l.SetItems(rows...)
	l.SetFilteringEnabled(false)
	if err == nil {
		title += " (" + strconv.Itoa(len(items)) + ")"
	}
	return itemList{title: title, list: l, err: err}
}

// Init implements nav.Screen.
func (l itemList) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (l itemList) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if l.geo.track(msg) {
		l.list.SetSize(l.geo.w, l.geo.h)
		if l.geo.focused {
			l.list.Focus()
		} else {
			l.list.Blur()
		}
		return l, nil
	}
	list, cmd := l.list.Update(msg)
	l.list = list
	return l, cmd
}

// View implements nav.Screen.
func (l itemList) View() string {
	if l.err != nil {
		return widgets.Fit(errorText(l.err), l.geo.w, l.geo.h)
	}
	return l.list.View()
}

// Title implements nav.Titled.
func (l itemList) Title() string { return l.title }

// ShortHelp implements nav.ShortHelper.
func (l itemList) ShortHelp() []key.Binding { return l.list.ShortHelp() }

// AtEdge implements widgets.Boundary.
func (l itemList) AtEdge(dir widgets.Direction) bool { return l.err != nil || l.list.AtEdge(dir) }
