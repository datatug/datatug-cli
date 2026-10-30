package dtviewers

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

type detailScreen struct{ text string }

func (d detailScreen) Init() tea.Cmd                        { return nil }
func (d detailScreen) Update(tea.Msg) (nav.Screen, tea.Cmd) { return d, nil }
func (d detailScreen) View() string                         { return d.text }

func testViewers() []Viewer {
	return []Viewer{
		{ID: "sql", Name: "Databases", Description: "Browse SQL", Shortcut: '1',
			Root: func() nav.Page { return nav.Page{Content: detailScreen{"SQL HOME"}} }},
		{ID: "gc", Name: "Google Cloud", Description: "Firestore",
			Root: func() nav.Page { return nav.Page{Title: "GCP", Content: detailScreen{"GCP HOME"}} }},
	}
}

func stubScreenOpened(t *testing.T) *[][2]string {
	t.Helper()
	var calls [][2]string
	old := screenOpened
	screenOpened = func(path, name string) tea.Cmd {
		calls = append(calls, [2]string{path, name})
		return nil
	}
	t.Cleanup(func() { screenOpened = old })
	return &calls
}

func TestModule(t *testing.T) {
	m := Module(testViewers()...)
	assert.Equal(t, datatugui.ScreenViewers, m.ID)
	assert.Equal(t, "Viewers", m.Text)
	assert.Equal(t, 'v', m.Shortcut)
	page := m.Root()
	assert.IsType(t, viewersScreen{}, page.Content)
}

func TestViewersScreen_ListsViewers(t *testing.T) {
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(testViewers())})
	h.RequireContains("Databases").RequireContains("Browse SQL").RequireContains("Google Cloud")
	s := newViewersScreen(testViewers())
	assert.Equal(t, "Viewers", s.Title())
	assert.NotEmpty(t, s.ShortHelp())
	assert.False(t, s.Editing())
	assert.True(t, s.AtEdge(widgets.Up))
	assert.Nil(t, s.Init())
}

func TestViewersScreen_EnterOpensViewer(t *testing.T) {
	calls := stubScreenOpened(t)
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(testViewers())})
	h.Press("enter")
	h.RequireContains("SQL HOME")
	assert.Equal(t, [][2]string{{"viewers/sql", "Databases"}}, *calls)
	assert.Len(t, h.Model().Breadcrumbs(), 2)
	assert.Equal(t, "Databases", h.Model().Breadcrumbs()[1].Title)
}

func TestViewersScreen_ShortcutAndTitleFromPage(t *testing.T) {
	stubScreenOpened(t)
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(testViewers())})
	h.Press("down", "enter")
	h.RequireContains("GCP HOME")
	assert.Equal(t, "GCP", h.Model().Breadcrumbs()[1].Title)
}

func TestViewersScreen_ForeignSelectionIgnored(t *testing.T) {
	s := newViewersScreen(testViewers())
	_, cmd := s.Update(widgetsSelected("other"))
	assert.Nil(t, cmd)
}

func TestViewersScreen_FocusMsg(t *testing.T) {
	s := newViewersScreen(testViewers())
	next, cmd := s.Update(nav.ScreenFocusMsg{Focused: true})
	assert.Nil(t, cmd)
	assert.True(t, next.(viewersScreen).list.Focused())
	next, _ = next.Update(nav.ScreenFocusMsg{Focused: false})
	assert.False(t, next.(viewersScreen).list.Focused())
}

func TestViewersScreen_Empty(t *testing.T) {
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(nil)})
	h.Press("enter", "down")
	assert.Len(t, h.Model().Breadcrumbs(), 1)
}

func TestOpen_DefaultSeam(t *testing.T) {
	old := screenOpened
	t.Cleanup(func() { screenOpened = old })
	assert.NotNil(t, screenOpened)
}

func TestDbContextBase(t *testing.T) {
	c := &DbContextBase{name: "n", driver: Driver{ID: "d", ShortTitle: "D"}}
	assert.Equal(t, "n", c.Name())
	assert.Equal(t, Driver{ID: "d", ShortTitle: "D"}, c.Driver())
	assert.Nil(t, c.Schema())
	wantErr := errors.New("boom")
	c.getDB = func(context.Context) (dal.DB, error) { return nil, wantErr }
	_, err := c.GetDB(context.Background())
	assert.ErrorIs(t, err, wantErr)
}

func TestNewSqlDBContext(t *testing.T) {
	drv := Driver{ID: "x", ShortTitle: "X"}
	wantErr := errors.New("open failed")
	c := NewSqlDBContext(drv, "name", func(context.Context, string) (*sql.DB, error) { return nil, wantErr }, nil)
	assert.Equal(t, "name", c.Name())
	_, err := c.GetDB(context.Background())
	assert.ErrorIs(t, err, wantErr)

	c = NewSqlDBContext(drv, "name", func(context.Context, string) (*sql.DB, error) { return &sql.DB{}, nil }, nil)
	db, err := c.GetDB(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, db)
}

func TestGetSQLiteDbContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := GetSQLiteDbContext("~/data/my.db")
	assert.Equal(t, "my.db", c.Name())
	assert.Equal(t, "sqlite3", c.Driver().ID)
	assert.Equal(t, "SQLite", c.Driver().ShortTitle)
	assert.NotNil(t, c.Schema())
	_, err := c.GetSqlDB(context.Background(), "no-such-driver")
	assert.Error(t, err)
	_, err = c.GetDB(context.Background())
	assert.Error(t, err)                                          // sqlite3 driver is not linked into this test binary
	_, err = c.Schema().GetCollections(context.Background(), nil) // the file is not there: no schema to read
	assert.Error(t, err)
}

func TestCollectionContext(t *testing.T) {
	c := CollectionContext{DbContext: &DbContextBase{name: "db"}, CollectionRef: dal.NewRootCollectionRef("t", "")}
	assert.Equal(t, "db", c.Name())
}

func widgetsSelected(id string) tea.Msg { return widgets.ItemSelectedMsg{ID: id} }

// TestNewSqlDBContext_SetsSQLiteStructuredQueryDialect proves the viewer opens
// a SQLite database with the sqlite structured-query dialect, so every
// structured read goes through the safe compiler rather than the legacy text
// emitter, and that a context for any other driver does not get the
// SQLite-only dialect.
func TestNewSqlDBContext_SetsSQLiteStructuredQueryDialect(t *testing.T) {
	orig := newSQLDatabase
	t.Cleanup(func() { newSQLDatabase = orig })
	for driverID, want := range map[string]string{"sqlite3": "sqlite", "postgres": "", "x": ""} {
		var got dalgo2sql.DbOptions
		newSQLDatabase = func(db *sql.DB, schema dal.Schema, opts dalgo2sql.DbOptions) dal.DB {
			got = opts
			return orig(db, schema, opts)
		}
		c := NewSqlDBContext(Driver{ID: driverID}, "name", func(context.Context, string) (*sql.DB, error) { return &sql.DB{}, nil }, nil)
		db, err := c.GetDB(context.Background())
		require.NoError(t, err, driverID)
		assert.NotNil(t, db, driverID)
		assert.Equal(t, want, got.StructuredQueryDialect, driverID)
	}
}

// writeViewerDB makes a SQLite file at path with the statements.
func writeViewerDB(t *testing.T, path string, statements ...string) {
	t.Helper()
	file, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, statement := range statements {
		_, err = file.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, file.Close())
}

func viewerCollections(t *testing.T, schema schemer.SchemaProvider) []string {
	t.Helper()
	reader, err := schema.GetCollections(context.Background(), nil)
	require.NoError(t, err)
	var names []string
	for {
		collection, err := reader.NextCollection()
		if errors.Is(err, io.EOF) {
			return names
		}
		require.NoError(t, err)
		names = append(names, collection.Name())
	}
}

// The viewer opens the file when it first reads it, and tries again after an open that failed.
func TestSQLiteSchema_OpensOnFirstReadAndRetries(t *testing.T) {
	saved := openSQLiteSchema
	t.Cleanup(func() { openSQLiteSchema = saved })
	opens := 0
	boom := errors.New("database is locked")
	openSQLiteSchema = func(uri string) (sqliteSchemaReader, error) {
		opens++
		if opens == 1 {
			return nil, boom
		}
		return saved(uri)
	}
	path := filepath.Join(t.TempDir(), "my.db")
	writeViewerDB(t, path, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)

	schema := sqliteSchema(path)
	assert.Zero(t, opens, "nothing is opened before the first read")
	_, err := schema.GetCollections(context.Background(), nil)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, []string{"t"}, viewerCollections(t, schema))
	assert.Equal(t, []string{"t"}, viewerCollections(t, schema))
	assert.Equal(t, 2, opens, "an open file is kept")
}

// Every read asks afresh: the viewer shows a table as it is now, not as it was first read.
func TestSQLiteSchema_ReadsTheFileAsItIsNow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my.db")
	writeViewerDB(t, path, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	schema := sqliteSchema(path)
	ref := dal.NewRootCollectionRef("t", "")
	columns, err := schema.GetColumns(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)
	require.Len(t, columns, 1)

	writeViewerDB(t, path, `ALTER TABLE t ADD COLUMN note TEXT`, `CREATE TABLE u (id INTEGER PRIMARY KEY)`)

	columns, err = schema.GetColumns(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)
	require.Len(t, columns, 2)
	assert.Equal(t, "note", columns[1].Name)
	assert.Equal(t, []string{"t", "u"}, viewerCollections(t, schema))
}

// A view whose table was dropped is listed, and reading its columns fails with the reason.
func TestSQLiteSchema_ListsABrokenView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my.db")
	writeViewerDB(t, path,
		`CREATE TABLE kept (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE doomed (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW broken AS SELECT id FROM doomed`,
		`DROP TABLE doomed`,
	)
	schema := sqliteSchema(path)
	assert.Equal(t, []string{"kept", "broken"}, viewerCollections(t, schema))
	ref := dal.NewRootCollectionRef("broken", "")
	_, err := schema.GetColumns(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	assert.ErrorContains(t, err, "no such table")
}

// Every read of the schema goes to the file, and fails with the open's reason when it cannot be opened.
func TestSQLiteSchema_EveryRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "my.db")
	writeViewerDB(t, path,
		`CREATE TABLE parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id), code TEXT UNIQUE)`,
		`CREATE INDEX child_parent ON child(parent_id)`,
	)
	ref := dal.NewRootCollectionRef("child", "")
	reads := map[string]func(schemer.SchemaProvider) error{
		"GetCollections": func(s schemer.SchemaProvider) error { _, err := s.GetCollections(ctx, nil); return err },
		"GetColumnsReader": func(s schemer.SchemaProvider) error {
			_, err := s.GetColumnsReader(ctx, "", schemer.ColumnsFilter{CollectionRef: &ref})
			return err
		},
		"GetColumns": func(s schemer.SchemaProvider) error {
			_, err := s.GetColumns(ctx, "", schemer.ColumnsFilter{CollectionRef: &ref})
			return err
		},
		"GetIndexes": func(s schemer.SchemaProvider) error { _, err := s.GetIndexes(ctx, "", "", "child"); return err },
		"GetIndexColumns": func(s schemer.SchemaProvider) error {
			_, err := s.GetIndexColumns(ctx, "", "", "child", "child_parent")
			return err
		},
		"GetForeignKeysReader": func(s schemer.SchemaProvider) error { _, err := s.GetForeignKeysReader(ctx, "", "child"); return err },
		"GetForeignKeys":       func(s schemer.SchemaProvider) error { _, err := s.GetForeignKeys(ctx, "", "child"); return err },
		"GetReferrers":         func(s schemer.SchemaProvider) error { _, err := s.GetReferrers(ctx, "", "parent"); return err },
		"GetConstraints":       func(s schemer.SchemaProvider) error { _, err := s.GetConstraints(ctx, "", "", "child"); return err },
		"RecordsCount":         func(s schemer.SchemaProvider) error { _, err := s.RecordsCount(ctx, "", "", "child"); return err },
	}
	missing := sqliteSchema(filepath.Join(t.TempDir(), "missing.db"))
	present := sqliteSchema(path)
	assert.False(t, present.IsBulkProvider())
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, read(present))
			assert.Error(t, read(missing), "a file that is not there cannot be read")
		})
	}
}

// The viewer reads a SQLite file's schema read-only, through DALgo's lenient reader.
func TestSQLiteSchema_OpensTheFileReadOnly(t *testing.T) {
	saved := openSQLiteSchema
	t.Cleanup(func() { openSQLiteSchema = saved })
	var opened string
	openSQLiteSchema = func(uri string) (sqliteSchemaReader, error) {
		opened = uri
		return saved(uri)
	}
	path := filepath.Join(t.TempDir(), "my.db")
	file, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = file.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, doc JSON)`)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	reader, err := sqliteSchema(path).GetCollections(context.Background(), nil)

	require.NoError(t, err)
	assert.Equal(t, dbcopy.SQLiteFileURIMode(path, dbcopy.SQLiteReadOnly), opened)
	collection, err := reader.NextCollection()
	require.NoError(t, err)
	assert.Equal(t, "t", collection.Name())
}
