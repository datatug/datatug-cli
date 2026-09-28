package dbviewer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestSqliteDb(t *testing.T) string {
	dir, err := os.MkdirTemp("", "test_sqlite_*")
	require.NoError(t, err)
	dbPath := filepath.Join(dir, "test_db.sqlite")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, amount REAL, FOREIGN KEY(user_id) REFERENCES users(id));
		CREATE TABLE items (id INTEGER PRIMARY KEY, orderID INTEGER, FOREIGN KEY(orderID) REFERENCES orders(id));
		CREATE VIEW active_users AS SELECT * FROM users;
		INSERT INTO users (id, name) VALUES (1, 'Alice');
		INSERT INTO orders (id, user_id, amount) VALUES (10, 1, 99.9);
		INSERT INTO items (id, orderID) VALUES (100, 10);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dbPath
}

func TestTablesUI_SqliteFlow(t *testing.T) {
	dbPath := createTestSqliteDb(t)

	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	// Test goTables
	err := goTables(tui, sneatnav.FocusToContent, dbContext)
	require.NoError(t, err)

	// Test goViews
	err = goViews(tui, sneatnav.FocusToMenu, dbContext)
	require.NoError(t, err)

	// Test showCollections with nil dbContext
	err = showCollections(tui, sneatnav.FocusToContent, nil, SqlDbScreenTables, "Tables", datatug.CollectionTypeTable)
	assert.Error(t, err)

	// Test newSqlDbMenu
	menuPanel := newSqlDbMenu(tui, SqlDbScreenTables, dbContext)
	require.NotNil(t, menuPanel)
	menuPanel.TakeFocus()

	// Let background schema loading complete
	time.Sleep(100 * time.Millisecond)

	// Test TablesBox interactions
	box := NewTablesBox(tui, dbContext, datatug.CollectionTypeTable, "Tables")
	require.NotNil(t, box)
	nextBox := tview.NewBox()
	box.SetNextFocus(nextBox)

	// Add dummy collection to box to test filtering
	box.collections = []*datatug.CollectionInfo{
		{
			Ref: dal.NewCollectionRef("users", "", nil),
		},
		{
			Ref: dal.NewCollectionRef("orders", "", nil),
		},
	}
	box.refreshTable()

	invokeBox := func(key tcell.Key, r rune) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(box.Table, key, r, tcell.ModNone)
	}

	// Filter with rune 'u'
	assert.Nil(t, invokeBox(tcell.KeyRune, 'u'))
	assert.Equal(t, "u", box.filter)

	// Filter backspace
	assert.Nil(t, invokeBox(tcell.KeyBackspace, 0))
	assert.Equal(t, "", box.filter)

	// Filter backspace on empty
	assert.Nil(t, invokeBox(tcell.KeyBackspace, 0))

	// Filter ESC
	box.filter = "test"
	assert.Nil(t, invokeBox(tcell.KeyEsc, 0))
	assert.Equal(t, "", box.filter)

	// KeyLeft col 0
	box.Select(1, 0)
	assert.Nil(t, invokeBox(tcell.KeyLeft, 0))

	// KeyRight with nextFocus
	assert.Nil(t, invokeBox(tcell.KeyRight, 0))

	// KeyUp row 1
	box.Select(1, 0)
	assert.Nil(t, invokeBox(tcell.KeyUp, 0))

	// KeyUp row > 1
	box.Select(2, 0)
	assert.NotNil(t, invokeBox(tcell.KeyUp, 0))

	// Default key
	assert.NotNil(t, invokeBox(tcell.KeyTab, 0))

	// Test table selection trigger
	box.GetCell(1, 0).SetReference(box.collections[0])
	selectedFunc := func() {
		// Call selection directly
		goTable(tui, dtviewers.CollectionContext{
			CollectionRef: box.collections[0].Ref,
			DbContext:     dbContext,
		})
	}
	selectedFunc()

	time.Sleep(100 * time.Millisecond)
}

func TestTablesUI_FocusAndBlur(t *testing.T) {
	dbPath := createTestSqliteDb(t)

	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	var (
		capturedColBox *columnsBox
		capturedFKs    *foreignKeysBox
		capturedRefs   *referrersBox
		capturedBox    *TablesBox
	)
	onCollectionsShown = func(collectionsBox *TablesBox, columns *columnsBox, fks *foreignKeysBox, referrers *referrersBox) {
		capturedBox = collectionsBox
		capturedColBox = columns
		capturedFKs = fks
		capturedRefs = referrers
	}
	defer func() { onCollectionsShown = nil }()

	err := goTables(tui, sneatnav.FocusToContent, dbContext)
	require.NoError(t, err)

	require.NotNil(t, capturedColBox)
	require.NotNil(t, capturedFKs)
	require.NotNil(t, capturedRefs)
	require.NotNil(t, capturedBox)

	// Test selection changed callback with various rows
	capturedBox.Select(0, 0)
	capturedBox.Select(10, 0)
	capturedBox.SetCell(10, 0, tview.NewTableCell("no ref"))
	capturedBox.Select(10, 0)
	c := tview.NewTableCell("with ref")
	c.SetReference(&datatug.CollectionInfo{
		Ref: dal.NewCollectionRef("users", "", nil),
	})
	capturedBox.SetCell(10, 0, c)
	capturedBox.Select(10, 0)

	// Test columns input capture
	invokeCols := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(capturedColBox.Table, key, 0, tcell.ModNone)
	}
	assert.Nil(t, invokeCols(tcell.KeyRight))
	assert.Nil(t, invokeCols(tcell.KeyLeft))
	capturedColBox.Select(1, 0)
	assert.Nil(t, invokeCols(tcell.KeyUp))
	capturedColBox.Select(2, 0)
	assert.NotNil(t, invokeCols(tcell.KeyUp))
	assert.NotNil(t, invokeCols(tcell.KeyTab))

	// Test fks input capture
	invokeFKs := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(capturedFKs.Table, key, 0, tcell.ModNone)
	}
	assert.Nil(t, invokeFKs(tcell.KeyLeft))
	capturedFKs.Select(0, 0)
	assert.Nil(t, invokeFKs(tcell.KeyUp))
	capturedFKs.Select(1, 0)
	assert.NotNil(t, invokeFKs(tcell.KeyUp))
	assert.NotNil(t, invokeFKs(tcell.KeyTab))

	// Test referrers input capture
	invokeRefs := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(capturedRefs.Table, key, 0, tcell.ModNone)
	}
	assert.Nil(t, invokeRefs(tcell.KeyLeft))
	capturedRefs.Select(0, 0)
	assert.Nil(t, invokeRefs(tcell.KeyUp))
	capturedRefs.Select(1, 0)
	assert.NotNil(t, invokeRefs(tcell.KeyUp))

	// KeyDown at last row vs earlier
	rowCount := capturedRefs.GetRowCount()
	capturedRefs.Select(rowCount-1, 0)
	assert.Nil(t, invokeRefs(tcell.KeyDown))
	capturedRefs.SetCell(5, 0, tview.NewTableCell("extra"))
	capturedRefs.Select(0, 0)
	assert.NotNil(t, invokeRefs(tcell.KeyDown))

	assert.NotNil(t, invokeRefs(tcell.KeyTab))
}

func TestColumnsAndFksBoxes(t *testing.T) {
	dbPath := createTestSqliteDb(t)

	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	ctx := context.Background()
	colBox := newColumnsBox(ctx, dbContext, tui)
	require.NotNil(t, colBox)

	collCtx := dtviewers.CollectionContext{
		CollectionRef: dal.NewCollectionRef("orders", "", nil),
		DbContext:     dbContext,
	}

	colBox.SetCollectionContext(ctx, collCtx)

	fkBox := newForeignKeysBox(tui, dbContext.Schema())
	require.NotNil(t, fkBox)
	fkBox.SetCollectionContext(ctx, collCtx)

	refBox := newReferrersBox(tui, dbContext.Schema())
	require.NotNil(t, refBox)
	refBox.SetCollectionContext(ctx, collCtx)

	time.Sleep(100 * time.Millisecond)
}

func TestRecordsetUI_And_QueryTable(t *testing.T) {
	dbPath := createTestSqliteDb(t)

	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	collCtx := dtviewers.CollectionContext{
		CollectionRef: dal.NewCollectionRef("users", "", nil),
		DbContext:     dbContext,
	}

	rsUI := newRecordsetUI(tui, collCtx)
	require.NotNil(t, rsUI)

	q := dal.From(collCtx.CollectionRef).NewQuery().SelectIntoRecordset()
	qTable := newQueryTable(tui, "users", dbContext, q, nil)
	require.NotNil(t, qTable)

	time.Sleep(150 * time.Millisecond)

	// Test input capture on recordset table
	table := rsUI.table
	invoke := func(key tcell.Key, mod tcell.ModMask) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(table, key, 0, mod)
	}

	// KeyUp at row <= 1
	table.Select(1, 0)
	assert.Nil(t, invoke(tcell.KeyUp, tcell.ModNone))

	// KeyLeft at col 0
	table.Select(2, 0)
	assert.Nil(t, invoke(tcell.KeyLeft, tcell.ModNone))

	// Ctrl+C copy
	assert.Nil(t, invoke(tcell.KeyCtrlC, tcell.ModNone))

	// Default
	assert.NotNil(t, invoke(tcell.KeyTab, tcell.ModNone))

	// Test with items table to trigger FK bottomTable and Enter navigation
	itemsCtx := dtviewers.CollectionContext{
		CollectionRef: dal.NewCollectionRef("items", "", nil),
		DbContext:     dbContext,
	}
	itemsUI := newRecordsetUI(tui, itemsCtx)
	require.NotNil(t, itemsUI)
	time.Sleep(200 * time.Millisecond)

	// Trigger selection on row 1, col 1 (orderID)
	itemsTable := itemsUI.table
	itemsTable.Select(1, 1)
	time.Sleep(100 * time.Millisecond)

	invokeItems := func(key tcell.Key, mod tcell.ModMask) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(itemsTable, key, 0, mod)
	}

	// Alt+Down with bottomTable active
	assert.Nil(t, invokeItems(tcell.KeyDown, tcell.ModAlt))

	// Enter on column ending with ID triggers goTable
	assert.Nil(t, invokeItems(tcell.KeyEnter, tcell.ModNone))
}

func TestRecordsetUI_GetDBError(t *testing.T) {
	tui := newTestTUI(t)
	mockCtx := &testDbContext{
		name:   "err_db",
		driver: dtviewers.Driver{ShortTitle: "ErrDB"},
		err:    assert.AnError,
	}

	collCtx := dtviewers.CollectionContext{
		CollectionRef: dal.NewCollectionRef("users", "", nil),
		DbContext:     mockCtx,
	}

	rsUI := newRecordsetUI(tui, collCtx)
	require.NotNil(t, rsUI)
	time.Sleep(100 * time.Millisecond)

	// Verify error cell was rendered
	cell := rsUI.table.GetCell(0, 0)
	require.NotNil(t, cell)
	assert.Contains(t, cell.Text, "Error:")
}

func TestLoadDataIntoTable_QueryError(t *testing.T) {
	dbPath := createTestSqliteDb(t)
	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	ctx := context.Background()
	db, err := dbContext.GetDB(ctx)
	require.NoError(t, err)

	// Invalid query on nonexistent table
	badQuery := dal.From(dal.NewCollectionRef("nonexistent_table", "", nil)).NewQuery().SelectIntoRecordset()
	tbl := tview.NewTable()

	_, err = loadDataIntoTable(ctx, tui, db, badQuery, tbl, nil)
	assert.Error(t, err)
	time.Sleep(50 * time.Millisecond)
	cell := tbl.GetCell(0, 0)
	require.NotNil(t, cell)
	assert.Contains(t, cell.Text, "Error:")
}

func TestNewSqlDbMenu_Complete(t *testing.T) {
	dbPath := createTestSqliteDb(t)
	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	var capturedList *tview.List
	onSqlDbMenuCreated = func(l *tview.List) {
		capturedList = l
	}
	defer func() { onSqlDbMenuCreated = nil }()

	_ = newSqlDbMenu(tui, SqlDbScreenTables, dbContext)
	require.NotNil(t, capturedList)

	assert.Equal(t, 2, capturedList.GetItemCount())

	// Trigger item 0 ("Tables") action callback via Enter key
	capturedList.SetCurrentItem(0)
	capturedList.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) {})

	// Trigger item 1 ("Views") action callback via Enter key
	capturedList.SetCurrentItem(1)
	capturedList.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) {})

	// Input capture
	invoke := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(capturedList, key, 0, tcell.ModNone)
	}

	assert.Nil(t, invoke(tcell.KeyRight))

	capturedList.SetCurrentItem(0)
	assert.Nil(t, invoke(tcell.KeyUp))

	capturedList.SetCurrentItem(1)
	assert.NotNil(t, invoke(tcell.KeyUp))

	assert.NotNil(t, invoke(tcell.KeyTab))

	// ChangedFunc is triggered on SetCurrentItem
	capturedList.SetCurrentItem(1) // "Views"
	capturedList.SetCurrentItem(0) // "Tables"
}

type mockTestSchema struct {
	schemer.SchemaProvider
	colsErr   error
	fkErr     error
	refsErr   error
	reader    schemer.CollectionsReader
	getColErr error
}

func (m *mockTestSchema) GetColumns(ctx context.Context, catalog string, filter schemer.ColumnsFilter) ([]schemer.Column, error) {
	if m.colsErr != nil {
		return nil, m.colsErr
	}
	return m.SchemaProvider.GetColumns(ctx, catalog, filter)
}

func (m *mockTestSchema) GetForeignKeys(ctx context.Context, catalog, table string) ([]schemer.ForeignKey, error) {
	if m.fkErr != nil {
		return nil, m.fkErr
	}
	return m.SchemaProvider.GetForeignKeys(ctx, catalog, table)
}

func (m *mockTestSchema) GetReferrers(ctx context.Context, catalog, table string) ([]schemer.ForeignKey, error) {
	if m.refsErr != nil {
		return nil, m.refsErr
	}
	return m.SchemaProvider.GetReferrers(ctx, catalog, table)
}

func (m *mockTestSchema) GetCollections(c context.Context, parentKey *record.Key) (schemer.CollectionsReader, error) {
	if m.getColErr != nil {
		return nil, m.getColErr
	}
	if m.reader != nil {
		return m.reader, nil
	}
	return m.SchemaProvider.GetCollections(c, parentKey)
}

type mockCollectionsReader struct {
	nextFn func() (*datatug.CollectionInfo, error)
}

func (m *mockCollectionsReader) NextCollection() (*datatug.CollectionInfo, error) {
	return m.nextFn()
}

func TestTablesBox_Coverage_More(t *testing.T) {
	dbPath := createTestSqliteDb(t)
	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	box := NewTablesBox(tui, dbContext, datatug.CollectionTypeTable, "Tables")
	require.NotNil(t, box)

	box.collections = []*datatug.CollectionInfo{
		{
			DBCollectionKey: datatug.NewTableKey("users", "", "", nil),
		},
		{
			DBCollectionKey: datatug.NewTableKey("orders", "", "", nil),
		},
	}
	box.refreshTable()
	box.filter = "nomatch"
	box.refreshTable()

	box.filter = ""
	box.refreshTable()
	box.onSelected(1, 0)

	box.Table.Select(1, 1)
	evLeft := sneatnav.InvokeInputCapture(box.Table, tcell.KeyLeft, 0, tcell.ModNone)
	assert.NotNil(t, evLeft)

	box.SetNextFocus(nil)
	evRight := sneatnav.InvokeInputCapture(box.Table, tcell.KeyRight, 0, tcell.ModNone)
	assert.NotNil(t, evRight)
}

func TestTablesBox_SchemaReaderBranches(t *testing.T) {
	dbPath := createTestSqliteDb(t)
	tui := newTestTUI(t)
	baseCtx := dtviewers.GetSQLiteDbContext(dbPath)

	errReader := &mockCollectionsReader{
		nextFn: func() (*datatug.CollectionInfo, error) {
			return nil, errors.New("reader failure")
		},
	}
	mockSchemaErr := &mockTestSchema{
		SchemaProvider: baseCtx.Schema(),
		reader:         errReader,
	}
	mockCtxErr := &testDbContext{
		name:   "mock_err",
		schema: mockSchemaErr,
	}
	boxErr := NewTablesBox(tui, mockCtxErr, datatug.CollectionTypeTable, "Tables")
	require.NotNil(t, boxErr)
	time.Sleep(100 * time.Millisecond)

	nilReader := &mockCollectionsReader{
		nextFn: func() (*datatug.CollectionInfo, error) {
			return nil, nil
		},
	}
	mockSchemaNil := &mockTestSchema{
		SchemaProvider: baseCtx.Schema(),
		reader:         nilReader,
	}
	mockCtxNil := &testDbContext{
		name:   "mock_nil",
		schema: mockSchemaNil,
	}
	boxNil := NewTablesBox(tui, mockCtxNil, datatug.CollectionTypeTable, "Tables")
	require.NotNil(t, boxNil)
	time.Sleep(100 * time.Millisecond)
}

func TestTableColumns_And_Fks_And_Referrers_Errors(t *testing.T) {
	dbPath := createTestSqliteDb(t)
	tui := newTestTUI(t)
	baseCtx := dtviewers.GetSQLiteDbContext(dbPath)
	ctx := context.Background()
	collCtx := dtviewers.CollectionContext{
		CollectionRef: dal.NewCollectionRef("users", "", nil),
		DbContext:     baseCtx,
	}

	mockColsErr := &mockTestSchema{
		SchemaProvider: baseCtx.Schema(),
		colsErr:        errors.New("columns failed"),
	}
	colBox1 := newColumnsBox(ctx, &testDbContext{name: "db1", schema: mockColsErr}, tui)
	colBox1.SetCollectionContext(ctx, collCtx)
	time.Sleep(100 * time.Millisecond)
	assert.Contains(t, colBox1.GetCell(0, 0).Text, "columns failed")

	mockFkErr := &mockTestSchema{
		SchemaProvider: baseCtx.Schema(),
		fkErr:          errors.New("fk in cols failed"),
	}
	colBox2 := newColumnsBox(ctx, &testDbContext{name: "db2", schema: mockFkErr}, tui)
	colBox2.SetCollectionContext(ctx, collCtx)
	time.Sleep(100 * time.Millisecond)
	assert.Contains(t, colBox2.GetCell(0, 0).Text, "fk in cols failed")

	fkBox := newForeignKeysBox(tui, mockFkErr)
	fkBox.SetCollectionContext(ctx, collCtx)
	time.Sleep(100 * time.Millisecond)
	assert.Contains(t, fkBox.GetCell(0, 0).Text, "Error: fk in cols failed")

	mockRefsErr := &mockTestSchema{
		SchemaProvider: baseCtx.Schema(),
		refsErr:        errors.New("referrers failed"),
	}
	refBox := newReferrersBox(tui, mockRefsErr)
	refBox.SetCollectionContext(ctx, collCtx)
	time.Sleep(100 * time.Millisecond)
	assert.Contains(t, refBox.GetCell(0, 0).Text, "Error: referrers failed")
}

func TestRecordsetUI_BottomTable_And_Input(t *testing.T) {
	dbPath := createTestSqliteDb(t)
	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	itemsCtx := dtviewers.CollectionContext{
		CollectionRef: dal.NewCollectionRef("items", "", nil),
		DbContext:     dbContext,
	}
	itemsUI := newRecordsetUI(tui, itemsCtx)
	require.NotNil(t, itemsUI)
	time.Sleep(200 * time.Millisecond)

	itemsTable := itemsUI.table
	itemsTable.Select(1, 1)
	time.Sleep(100 * time.Millisecond)

	itemsTable.Select(1, 1)
	time.Sleep(100 * time.Millisecond)

	evDown := sneatnav.InvokeInputCapture(itemsTable, tcell.KeyDown, 0, tcell.ModNone)
	assert.NotNil(t, evDown)

	assert.GreaterOrEqual(t, itemsUI.GetItemCount(), 2)
	bottomTable, ok := itemsUI.GetItem(1).(*recordsetTable)
	require.True(t, ok)

	evUp := sneatnav.InvokeInputCapture(bottomTable, tcell.KeyUp, 0, tcell.ModNone)
	assert.Nil(t, evUp)

	evTab := sneatnav.InvokeInputCapture(bottomTable, tcell.KeyTab, 0, tcell.ModNone)
	assert.NotNil(t, evTab)
}

func TestTablesUI_SelectionEdgeCases(t *testing.T) {
	dbPath := createTestSqliteDb(t)
	tui := newTestTUI(t)
	dbContext := dtviewers.GetSQLiteDbContext(dbPath)

	var capturedColBox *TablesBox
	onCollectionsShown = func(colBox *TablesBox, cols *columnsBox, fks *foreignKeysBox, refs *referrersBox) {
		capturedColBox = colBox
	}
	defer func() { onCollectionsShown = nil }()

	err := goTables(tui, sneatnav.FocusToContent, dbContext)
	require.NoError(t, err)
	require.NotNil(t, capturedColBox)

	capturedColBox.Select(0, 0)

	capturedColBox.SetCell(10, 0, nil)
	capturedColBox.Select(10, 0)

	capturedColBox.SetCell(11, 0, tview.NewTableCell("no ref"))
	capturedColBox.Select(11, 0)
}

