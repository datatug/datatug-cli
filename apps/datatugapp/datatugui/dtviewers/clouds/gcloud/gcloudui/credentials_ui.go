package gcloudui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const credentialsListID = "gcloudui.credentials"

// credentials is the Credentials screen.
type credentials struct {
	listPane
}

var (
	_ nav.Screen       = credentials{}
	_ nav.Titled       = credentials{}
	_ nav.ShortHelper  = credentials{}
	_ widgets.Boundary = credentials{}
	_ widgets.Editor   = credentials{}
)

func newCredentials() credentials {
	return credentials{listPane: newListPane(credentialsListID,
		widgets.MenuItem{ID: "login", Label: "Login", Shortcut: 'i'},
		widgets.MenuItem{ID: "logout", Label: "Logout", Shortcut: 'o'},
	)}
}

// Init implements nav.Screen.
func (credentials) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (c credentials) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	var cmd tea.Cmd
	c.listPane, cmd = c.listPane.update(msg)
	return c, cmd
}

// Title implements nav.Titled.
func (credentials) Title() string { return "Google Cloud Credentials" }
