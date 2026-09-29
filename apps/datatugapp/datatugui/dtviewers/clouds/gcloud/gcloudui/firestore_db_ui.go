package gcloudui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const firestoreListID = "gcloudui.firestore"

// Items of the Firestore list.
const (
	itemCollections = "collections"
	itemIndexes     = "indexes"
)

// firestoreDb is the Firestore Database screen: Collections and Indexes.
type firestoreDb struct {
	listPane
	ctx *CGProjectContext
}

var (
	_ nav.Screen       = firestoreDb{}
	_ nav.Titled       = firestoreDb{}
	_ nav.ShortHelper  = firestoreDb{}
	_ widgets.Boundary = firestoreDb{}
	_ widgets.Editor   = firestoreDb{}
)

func newFirestoreDb(ctx *CGProjectContext) firestoreDb {
	return firestoreDb{
		ctx: ctx,
		listPane: newListPane(firestoreListID,
			widgets.MenuItem{ID: itemCollections, Label: "Collections"},
			widgets.MenuItem{ID: itemIndexes, Label: "Indexes"},
		),
	}
}

// Init implements nav.Screen.
func (firestoreDb) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (f firestoreDb) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if sel, ok := msg.(widgets.ItemSelectedMsg); ok && sel.ID == firestoreListID {
		if menuItem(sel).ID == itemCollections {
			return f, datatugui.Drill("Collections", newCollections(f.ctx))
		}
		return f, datatugui.Drill("Indexes", newIndexes(f.ctx))
	}
	var cmd tea.Cmd
	f.listPane, cmd = f.listPane.update(msg)
	return f, cmd
}

// Title implements nav.Titled.
func (firestoreDb) Title() string { return "Firestore Database" }
