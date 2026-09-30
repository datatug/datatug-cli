package gcloudui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

const collectionsListID = "gcloudui.collections"

// Items of the collections list that are not a collection.
const (
	itemCollection     = "collection"
	itemRelogin        = "relogin"
	itemForgetLogin    = "forget"
	itemRetry          = "retry"
	itemOpenCredential = "credentials"
)

// collectionsLoaded is the result of loadCollections.
type collectionsLoaded struct {
	collections []*schemers.Collection
	err         error
}

// loginDone reports that the sign-in or the sign-out that was started finished;
// the collections are loaded again.
type loginDone struct{}

// loadCollections returns the command that reads the root collections.
func loadCollections(ctx *CGProjectContext) tea.Cmd {
	return func() tea.Msg {
		collections, err := ctx.Schema().GetCollections(context.Background(), nil)
		return collectionsLoaded{collections: collections, err: err}
	}
}

// relogin returns the command that signs in again with the Firestore scopes. Its
// outcome is not reported: the collections are loaded again either way.
func relogin() tea.Cmd {
	return func() tea.Msg {
		_, _ = startInteractiveLoginFunc(context.Background(), firestoreScopes)
		return loginDone{}
	}
}

// forgetLogin returns the command that deletes the saved refresh token.
func forgetLogin() tea.Cmd {
	return func() tea.Msg {
		_ = deleteRefreshTokenFunc()
		return loginDone{}
	}
}

// collections lists the root collections of the Firestore database, or what to
// do when they cannot be read.
type collections struct {
	listPane
	ctx *CGProjectContext
}

var (
	_ nav.Screen       = collections{}
	_ nav.Titled       = collections{}
	_ nav.ShortHelper  = collections{}
	_ widgets.Boundary = collections{}
	_ widgets.Editor   = collections{}
)

func newCollections(ctx *CGProjectContext) collections {
	c := collections{ctx: ctx, listPane: newListPane(collectionsListID, loadingItem())}
	return c
}

func loadingItem() widgets.MenuItem {
	return widgets.MenuItem{ID: "loading", Label: "Loading...", Detail: "Fetching root collections"}
}

// Init implements nav.Screen.
func (c collections) Init() tea.Cmd { return loadCollections(c.ctx) }

// Update implements nav.Screen.
func (c collections) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case collectionsLoaded:
		c.list.SetItems(collectionItems(msg)...)
		return c, nil
	case loginDone:
		return c.reload()
	case widgets.ItemSelectedMsg:
		if msg.ID == collectionsListID {
			return c.selected(menuItem(msg))
		}
	}
	var cmd tea.Cmd
	c.listPane, cmd = c.update(msg)
	return c, cmd
}

// reload shows the loading state and reads the collections again.
func (c collections) reload() (nav.Screen, tea.Cmd) {
	c.list.SetItems(loadingItem())
	return c, loadCollections(c.ctx)
}

// selected acts on the chosen item.
func (c collections) selected(item widgets.MenuItem) (nav.Screen, tea.Cmd) {
	switch item.ID {
	case itemCollection:
		collection := item.Ref.(*schemers.Collection)
		return c, datatugui.Drill(collection.ID, newCollection(c.ctx, collection))
	case itemRelogin:
		return c, relogin()
	case itemForgetLogin:
		return c, forgetLogin()
	case itemRetry:
		return c.reload()
	case itemOpenCredential:
		return c, datatugui.Drill("Credentials", newCredentials())
	}
	return c, nil
}

// Title implements nav.Titled.
func (c collections) Title() string { return "Firestore Collections" + projectSuffix(c.ctx) }

// collectionItems turns the result of a load into the items of the list.
func collectionItems(msg collectionsLoaded) []list.Item {
	if msg.err != nil {
		return errorItems(msg.err)
	}
	if len(msg.collections) == 0 {
		return []list.Item{widgets.MenuItem{ID: "empty", Label: "No collections", Detail: "The Firestore database has no root collections"}}
	}
	items := make([]list.Item, len(msg.collections))
	for i, collection := range msg.collections {
		items[i] = widgets.MenuItem{ID: itemCollection, Label: "📋 " + collection.ID, Ref: collection}
	}
	return items
}

// errorItems renders an error with the actions that may recover from it.
func errorItems(err error) []list.Item {
	items := []list.Item{widgets.MenuItem{ID: "error", Label: "Error", Detail: err.Error()}}
	if insufficientScopes(err.Error()) {
		items = append(items,
			widgets.MenuItem{ID: "hint", Label: "Hint: Missing Firestore scopes", Detail: "Your sign-in lacks Datastore/Firestore scopes. Re-login to grant access."},
			widgets.MenuItem{ID: itemRelogin, Label: "Re-login (add Firestore scope)", Detail: "Open browser to re-consent and save new token", Shortcut: 'l'},
			widgets.MenuItem{ID: itemForgetLogin, Label: "Forget saved login", Detail: "Delete saved refresh token to force re-consent", Shortcut: 'f'},
		)
	} else {
		items = append(items, widgets.MenuItem{ID: "hint", Label: "Hint: Check time sync", Detail: "Ensure your system clock is correct (auto time on)"})
	}
	return append(items,
		widgets.MenuItem{ID: itemRetry, Label: "Retry", Detail: "Try loading collections again", Shortcut: 'r'},
		widgets.MenuItem{ID: itemOpenCredential, Label: "Open Credentials", Detail: "Configure or refresh Google auth", Shortcut: 'c'},
		widgets.MenuItem{ID: "adc", Label: "How to login with gcloud (ADC)", Detail: "Run: gcloud auth application-default login", Shortcut: 'g'},
	)
}

// insufficientScopes reports whether an error message says the sign-in lacks
// the needed scopes.
func insufficientScopes(message string) bool {
	for _, marker := range []string{"ACCESS_TOKEN_SCOPE_INSUF", "insufficient authentication scopes", "insufficient scopes"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
