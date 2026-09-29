package gcloudui

import (
	"context"
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/strongo/strongo-tui/pkg/grid"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const (
	docsGridID = "gcloudui.documents"
	// docFieldMaxWidth caps the width of a field column.
	docFieldMaxWidth = 15
	// connectHint follows an error that happened before any document was read.
	connectHint = "Hint: re-login or check scopes in the Firestore Collections screen"
)

// docsLoaded is the result of loadDocs.
type docsLoaded struct {
	docs []firestoreDocRow
	err  error
	// connect is true when the error happened while connecting, not while reading.
	connect bool
}

// loadDocs returns the command that reads the first documents of a collection.
func loadDocs(projectID, collectionID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		client, err := newFirestoreClientFunc(ctx, projectID)
		if err != nil {
			return docsLoaded{err: err, connect: true}
		}
		defer closeFirestoreClient(client)
		docs, err := readDocuments(openDocuments(ctx, client, collectionID))
		return docsLoaded{docs: docs, err: err}
	}
}

// collection shows the first documents of a Firestore collection.
type collection struct {
	ctx        *CGProjectContext
	collection *schemers.Collection
	grid       *grid.Model
	message    string
	w, h       int
	focused    bool
}

var (
	_ nav.Screen       = collection{}
	_ nav.Titled       = collection{}
	_ widgets.Boundary = collection{}
	_ widgets.Editor   = collection{}
)

func newCollection(ctx *CGProjectContext, c *schemers.Collection) collection {
	return collection{ctx: ctx, collection: c, message: "Loading..."}
}

// Init implements nav.Screen.
func (c collection) Init() tea.Cmd { return loadDocs(c.ctx.projectID(), c.collection.ID) }

// Update implements nav.Screen.
func (c collection) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.w, c.h = msg.Width, msg.Height
		if c.grid != nil {
			c.grid.SetSize(c.w, c.h)
		}
		return c, nil
	case nav.ScreenFocusMsg:
		c.focused = msg.Focused
		if c.grid != nil {
			c.grid.SetFocused(c.focused)
		}
		return c, nil
	case docsLoaded:
		return c.loaded(msg)
	}
	if c.grid == nil {
		return c, nil
	}
	_, cmd := c.grid.Update(msg)
	return c, cmd
}

// loaded shows the result of the load.
func (c collection) loaded(msg docsLoaded) (nav.Screen, tea.Cmd) {
	switch {
	case msg.err != nil:
		c.message = "Error\n" + msg.err.Error()
		if msg.connect {
			c.message += "\n" + connectHint
		}
		return c, datatugui.ReportError("load Firestore documents", msg.err)
	case len(msg.docs) == 0:
		c.message = "No documents"
		return c, nil
	}
	c.grid = newDocsGrid(msg.docs)
	c.grid.SetSize(c.w, c.h)
	c.grid.SetFocused(c.focused)
	return c, nil
}

// newDocsGrid builds the grid of documents: the document ID, then a column for
// every field found in any document, in alphabetical order.
func newDocsGrid(docs []firestoreDocRow) *grid.Model {
	var fields []string
	for _, doc := range docs {
		for field := range doc.data {
			if !slices.Contains(fields, field) {
				fields = append(fields, field)
			}
		}
	}
	slices.Sort(fields)

	columns := []grid.Column{{Name: "#"}}
	for _, field := range fields {
		columns = append(columns, grid.Column{Name: field, MaxWidth: docFieldMaxWidth})
	}
	rows := make([]grid.Row, len(docs))
	for i, doc := range docs {
		values := []any{doc.id}
		for _, field := range fields {
			if v, ok := doc.data[field]; ok {
				values = append(values, fmt.Sprintf("%v", v))
			} else {
				values = append(values, grid.Absent)
			}
		}
		rows[i] = grid.Row{Key: doc.id, Values: values}
	}
	return grid.New(columns, rows, grid.WithID(docsGridID), grid.WithoutFrame(), grid.WithFixedColumns(1))
}

// View implements nav.Screen.
func (c collection) View() string {
	if c.grid == nil {
		return widgets.Fit(c.message, c.w, c.h)
	}
	return widgets.Fit(c.grid.View(c.w, c.focused), c.w, c.h)
}

// Title implements nav.Titled.
func (c collection) Title() string { return "Collection: " + c.collection.ID + projectSuffix(c.ctx) }

// AtEdge implements widgets.Boundary.
func (c collection) AtEdge(dir widgets.Direction) bool { return c.grid == nil || c.grid.AtEdge(dir) }

// Editing implements widgets.Editor.
func (c collection) Editing() bool { return c.grid != nil && c.grid.Editing() }
