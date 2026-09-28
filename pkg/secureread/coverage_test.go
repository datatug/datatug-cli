package secureread

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dummySchemaReader struct {
	dal.DB
	def *dbschema.CollectionDef
	err error
}

func (d dummySchemaReader) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return d.def, d.err
}

func (d dummySchemaReader) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return nil, nil
}

func (d dummySchemaReader) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func (d dummySchemaReader) ListConstraints(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, nil
}

func (d dummySchemaReader) ListReferrers(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, nil
}

type dummyReaderWithErr struct {
	dal.RecordsReader
	err error
}

func (d dummyReaderWithErr) Next() (record.Record, error) {
	return nil, d.err
}

func (d dummyReaderWithErr) Close() error {
	return nil
}

type dummyPolicyWithWrites struct{}

func (dummyPolicyWithWrites) Name() string { return "dummy" }

func (dummyPolicyWithWrites) Decide(ctx context.Context, req access.Request) access.Decision {
	return access.Decision{
		Allowed: true,
		Writes:  []*access.WriteResidual{{}},
	}
}

func (dummyPolicyWithWrites) Authorize(ctx context.Context, req access.Request) error {
	return nil
}

func TestCoverage_ContractLimitations(t *testing.T) {
	// Empty limitations
	assert.Empty(t, ToContractLimitations(nil))
	assert.Empty(t, ToContractLimitations([]Limitation{}))

	// LimitationPolicy and NativeSQL with duplicate policy
	in := []Limitation{
		{Kind: LimitationPolicy, Policy: "pol1"},
		{Kind: LimitationPolicy, Policy: "pol1"},
		{Kind: LimitationNativeSQL, Policy: "pol2"},
		{Kind: LimitationRowsFiltered},
		{Kind: LimitationHiddenColumns, Columns: []string{"colA", "colB"}},
	}
	out := ToContractLimitations(in)
	require.Len(t, out, 1)
	assert.Equal(t, "pol1,pol2", out[0].Policy)
	assert.True(t, out[0].RowsFiltered)
	assert.Equal(t, []string{"colA", "colB"}, out[0].HiddenColumns)

	// RowsFiltered only (default "policy")
	out2 := ToContractLimitations([]Limitation{{Kind: LimitationRowsFiltered}})
	require.Len(t, out2, 1)
	assert.Equal(t, "policy", out2[0].Policy)
	assert.True(t, out2[0].RowsFiltered)
	assert.Empty(t, out2[0].HiddenColumns)

	// containsStr & joinStrings helpers
	assert.True(t, containsStr([]string{"a", "b"}, "a"))
	assert.False(t, containsStr([]string{"a", "b"}, "c"))
	assert.Equal(t, "", joinStrings(nil, ","))
	assert.Equal(t, "single", joinStrings([]string{"single"}, ","))
	assert.Equal(t, "a,b", joinStrings([]string{"a", "b"}, ","))
}

func TestCoverage_Rows_Errors(t *testing.T) {
	// collectRows nil reader
	rows, stats, err := collectRows(nil)
	assert.NoError(t, err)
	assert.Nil(t, rows)
	assert.NotNil(t, stats)

	// collectRows Next error
	simErr := errors.New("simulated next error")
	_, _, err = collectRows(dummyReaderWithErr{err: simErr})
	assert.ErrorIs(t, err, simErr)

	// collectRows unmappable record
	badRec := record.NewRecordWithData(record.NewKeyWithID("test_coll", "k1"), func() {})
	r := dal.NewRecordsReader([]record.Record{badRec})
	_, _, err = collectRows(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "secureread: record")

	// columnsFor branches
	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("items", ""))).
		SelectColumns(
			dal.Column{Expression: dal.Field("f1")},
			dal.Column{Alias: "f2_alias", Expression: dal.Field("f2")},
		)
	rows = []Row{
		{Data: map[string]any{"f1": "val1", "f2_alias": "val2", "unused": "x"}},
	}
	cols := columnsFor(q, rows)
	assert.Contains(t, cols, "f1")
	assert.Contains(t, cols, "f2_alias")
}

func TestCoverage_Fields(t *testing.T) {
	// hiddenColumnsFor when anyFieldLists is false
	assert.Nil(t, hiddenColumnsFor(context.Background(), nil, nil, nil))

	lines := []accesspolicies.Line{
		{FieldLists: [][]string{{"allowed_*"}}},
	}

	// query with no base collection
	dummyQ := dal.NewQueryBuilder(dal.From(dal.NewCollectionGroupRef("group", ""))).SelectColumns()
	assert.Nil(t, hiddenColumnsFor(context.Background(), nil, dummyQ, lines))

	// baseCollectionRef pointer type
	colRef := dal.NewRootCollectionRef("coll", "")
	qPtr := dal.NewQueryBuilder(dal.From(&colRef)).SelectColumns()
	ref, ok := baseCollectionRef(qPtr)
	assert.True(t, ok)
	assert.Equal(t, "coll", ref.Name())

	// db does not implement SchemaReader
	assert.Nil(t, hiddenColumnsFor(context.Background(), nil, qPtr, lines))

	// db returns nil def
	readerDB := dummySchemaReader{def: nil, err: nil}
	assert.Nil(t, hiddenColumnsFor(context.Background(), readerDB, qPtr, lines))

	// db returns def with hidden columns
	def := &dbschema.CollectionDef{
		Fields: []dbschema.FieldDef{
			{Name: "allowed_1"},
			{Name: "secret_1"},
			{Name: "secret_2"},
		},
	}
	readerDBWithFields := dummySchemaReader{def: def, err: nil}
	hidden := hiddenColumnsFor(context.Background(), readerDBWithFields, qPtr, lines)
	assert.Equal(t, []string{"secret_1", "secret_2"}, hidden)

	// segmentMatches and fieldAllowed branches
	assert.True(t, segmentMatches("*", "anything"))
	assert.True(t, segmentMatches("*suffix", "a_suffix"))
	assert.False(t, segmentMatches("*suffix", "no_match"))
	assert.True(t, segmentMatches("prefix*", "prefix_val"))
	assert.False(t, segmentMatches("prefix*", "no_match"))
	assert.True(t, segmentMatches("exact", "exact"))
	assert.False(t, segmentMatches("exact", "other"))

	// fieldAllowed with want > segments
	assert.False(t, fieldAllowed([]string{"a.b.c"}, "a.b"))
}

func TestCoverage_NativeSQL_Errors(t *testing.T) {
	e := &Executor{session: Session{Unrestricted: true}}

	// Invalid URL
	_, err := e.RunNativeSQL(context.Background(), "::not-a-url::", "SELECT 1")
	assert.Error(t, err)

	// sqlOpenNative error
	origOpen := sqlOpenNative
	sqlOpenNative = func(driver, path string) (*sql.DB, error) {
		return nil, errors.New("simulated sql.Open error")
	}
	tmp := filepath.Join(t.TempDir(), "test.db")
	_ = os.WriteFile(tmp, []byte(""), 0644)
	_, err = e.RunNativeSQL(context.Background(), "sqlite://"+tmp, "SELECT 1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated sql.Open error")
	sqlOpenNative = origOpen

	// pragmaQueryOnly error
	origPragma := pragmaQueryOnly
	pragmaQueryOnly = func(ctx context.Context, db *sql.DB) error {
		return errors.New("simulated pragma error")
	}
	_, _, err = openReadOnlySQLite(context.Background(), tmp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated pragma error")
	pragmaQueryOnly = origPragma

	// ping error with cancelled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = openReadOnlySQLite(canceledCtx, tmp)
	assert.Error(t, err)
}

func TestCoverage_WholeCollection_Errors(t *testing.T) {
	var nilExec *Executor
	assert.Error(t, nilExec.CanReadWholeCollection(context.Background(), "coll"))

	e := &Executor{session: Session{Unrestricted: false, Policies: nil}}
	assert.Error(t, e.CanReadWholeCollection(context.Background(), ""))
	assert.Error(t, e.CanReadWholeCollection(context.Background(), "coll"))

	// Writes non-nil in decision
	eWrites := &Executor{session: Session{
		Policies: []accesspolicies.Loaded{{Policy: dummyPolicyWithWrites{}}},
	}}
	err := eWrites.CanReadWholeCollection(context.Background(), "coll")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "field policy that cannot be applied safely")
}

func TestCoverage_Result_LimitationsFromLines(t *testing.T) {
	lines := []accesspolicies.Line{
		{Allowed: false, Policy: "denied_pol"},
		{Allowed: true, Policy: "allow_all"}, // no condition, no field lists -> skipped
		{Allowed: true, Policy: "filtered_pol", Condition: "x > 1"},
	}
	lims := limitationsFromLines(lines)
	require.Len(t, lims, 2)
	assert.Equal(t, LimitationPolicy, lims[0].Kind)
	assert.Equal(t, LimitationRowsFiltered, lims[1].Kind)
}

func TestCoverage_Executor_CollectRowsError(t *testing.T) {
	origCollect := collectRowsFn
	collectRowsFn = func(reader dal.RecordsReader) ([]Row, *statisticsAccumulator, error) {
		return nil, nil, errors.New("simulated collectRows error")
	}
	defer func() { collectRowsFn = origCollect }()

	e := &Executor{session: Session{Unrestricted: true}}
	tmp := filepath.Join(t.TempDir(), "test.db")
	sqlDB, err := sql.Open("sqlite", tmp)
	require.NoError(t, err)
	_, err = sqlDB.Exec("CREATE TABLE items (id TEXT); INSERT INTO items VALUES ('1');")
	require.NoError(t, err)
	_ = sqlDB.Close()

	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("items", ""))).SelectColumns()
	_, err = e.RunStructured(context.Background(), "sqlite://"+tmp, q, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated collectRows error")
}

func TestCoverage_Federated(t *testing.T) {
	e := &Executor{session: Session{Unrestricted: true}}

	// Deserialize error
	_, err := e.StreamFederatedDTQL(context.Background(), []byte("not valid DTQL json"), nil, nil)
	assert.Error(t, err)

	// StreamFederatedDTQL openSource error (bad source URL)
	rawDoc := []byte(federatedSalesQuery)
	_, err = e.StreamFederatedDTQL(context.Background(), rawDoc, map[string]string{"orders": "unsupported://url"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "open database")

	// RunFederatedDTQL with collectRows error
	origCollect := collectRowsFederatedFn
	collectRowsFederatedFn = func(reader dal.RecordsReader) ([]Row, *statisticsAccumulator, error) {
		return nil, nil, errors.New("simulated federated collectRows error")
	}
	root := t.TempDir()
	ordersPath := filepath.Join(root, "orders.sqlite")
	countriesPath := filepath.Join(root, "countries.sqlite")
	db1, _ := sql.Open("sqlite", ordersPath)
	_, _ = db1.Exec("CREATE TABLE Invoice (id INTEGER PRIMARY KEY, country_id INTEGER, amount NUMERIC); INSERT INTO Invoice VALUES (1, 1, 10);")
	_ = db1.Close()
	db2, _ := sql.Open("sqlite", countriesPath)
	_, _ = db2.Exec("CREATE TABLE Country (id INTEGER PRIMARY KEY, name TEXT, population INTEGER); INSERT INTO Country VALUES (1, 'Alpha', 100);")
	_ = db2.Close()

	sources := map[string]string{"orders": "sqlite://" + ordersPath, "countries": "sqlite://" + countriesPath}
	_, err = e.RunFederatedDTQL(context.Background(), rawDoc, sources, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated federated collectRows error")
	collectRowsFederatedFn = origCollect

	// RunFederatedDTQL where columnsFor returns empty (columns in query don't match row data)
	origStream := streamFederatedDTQLFn
	streamFederatedDTQLFn = func(e *Executor, ctx context.Context, document []byte, sourceURLs map[string]string, variables map[string]any) (*FederatedStream, error) {
		q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("items", ""))).SelectColumns(dal.Column{Alias: "missing_col"})
		rec := record.NewRecordWithData(record.NewKeyWithID("items", "1"), map[string]any{"actual_col": "val"})
		r := dal.NewRecordsReader([]record.Record{rec})
		lims := []Limitation{}
		return &FederatedStream{Reader: r, Query: q, limitations: &lims}, nil
	}
	resFed, err := e.RunFederatedDTQL(context.Background(), rawDoc, sources, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"actual_col"}, resFed.Columns)
	streamFederatedDTQLFn = origStream

	// securedLeaf methods
	leaf := securedLeaf{
		session:     Session{Unrestricted: true},
		limitations: new([]Limitation),
	}
	_, err = leaf.ExecuteQueryToRecordsetReader(context.Background(), nil)
	assert.Error(t, err)

	// securedLeaf ExecuteQueryToRecordsReader error with valid DB
	dbOpen, _, _ := openSource(context.Background(), "sqlite://"+ordersPath, false)
	leafWithDB := securedLeaf{
		db:          dbOpen,
		session:     Session{Unrestricted: true},
		limitations: new([]Limitation),
	}
	_, err = leafWithDB.ExecuteQueryToRecordsReader(context.Background(), dal.NewTextQuery("SELECT * FROM non_existent_table", nil))
	assert.Error(t, err)

	// WithFederatedProgress observer context
	called := false
	ctx := WithFederatedProgress(context.Background(), func(p dal.FederatedProgress) {
		called = true
	})
	obs, ok := ctx.Value(federatedProgressKey{}).(func(dal.FederatedProgress))
	assert.True(t, ok)
	obs(dal.FederatedProgress{})
	assert.True(t, called)

	// FederatedStream.Limitations
	lims := []Limitation{{Kind: LimitationPolicy, Policy: "test"}}
	stream := &FederatedStream{limitations: &lims}
	assert.Equal(t, lims, stream.Limitations())
}

func TestCoverage_Snapshot_ValuesAndErrors(t *testing.T) {
	e := &Executor{session: Session{Unrestricted: true}}

	// Validation error on recordset
	_, err := e.RunSnapshot(context.Background(), "coll", apicontract.Recordset{
		Columns: []apicontract.Column{{Name: ""}}, // empty name fails validate
	})
	assert.Error(t, err)

	// Empty collection
	_, err = e.RunSnapshot(context.Background(), "  ", apicontract.Recordset{})
	assert.Error(t, err)

	// osMkdirTemp error
	origMkdir := osMkdirTemp
	osMkdirTemp = func(dir, pattern string) (string, error) {
		return "", errors.New("mkdir failed")
	}
	_, err = e.RunSnapshot(context.Background(), "c", apicontract.Recordset{})
	assert.Error(t, err)
	osMkdirTemp = origMkdir

	// osChmod error on dir
	origChmod := osChmod
	osChmod = func(name string, mode os.FileMode) error {
		return errors.New("chmod failed")
	}
	_, err = e.RunSnapshot(context.Background(), "c", apicontract.Recordset{})
	assert.Error(t, err)
	osChmod = origChmod

	// sqlOpenSnapshot error
	origOpenSnap := sqlOpenSnapshot
	sqlOpenSnapshot = func(driver, path string) (*sql.DB, error) {
		return nil, errors.New("open snap failed")
	}
	_, err = e.RunSnapshot(context.Background(), "c", apicontract.Recordset{})
	assert.Error(t, err)
	sqlOpenSnapshot = origOpenSnap

	// dbCloseSnapshot error
	origCloseSnap := dbCloseSnapshot
	dbCloseSnapshot = func(db *sql.DB) error {
		_ = db.Close()
		return errors.New("close snap failed")
	}
	_, err = e.RunSnapshot(context.Background(), "c", apicontract.Recordset{})
	assert.Error(t, err)
	dbCloseSnapshot = origCloseSnap

	// osChmod error on dbPath
	chmodCount := 0
	osChmod = func(name string, mode os.FileMode) error {
		chmodCount++
		if chmodCount == 2 {
			return errors.New("chmod dbPath failed")
		}
		return origChmod(name, mode)
	}
	_, err = e.RunSnapshot(context.Background(), "c", apicontract.Recordset{})
	assert.Error(t, err)
	osChmod = origChmod

	// restoredValidate error
	origValidate := restoredValidate
	restoredValidate = func(rs *apicontract.Recordset) error {
		return errors.New("validate failed")
	}
	validRS := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "string"}},
		Rows:    [][]apicontract.TypedValue{{{Type: apicontract.ValueTypeString, Str: "val1"}}},
	}
	_, err = e.RunSnapshot(context.Background(), "c", validRS)
	assert.ErrorIs(t, err, ErrSnapshotPolicyUnexpressible)
	restoredValidate = origValidate

	// snapshotValueMatches branches
	// Decimal
	assert.False(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeDecimal, Str: "bad-decimal"}, 1.0))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeDecimal, Str: "12.5"}, 12.5))

	// Integer
	assert.False(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "bad-int"}, int64(1)))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "42"}, int64(42)))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "42"}, int(42)))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "42"}, float64(42)))
	assert.False(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "42"}, "string"))

	// Boolean
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeBoolean, Bool: true}, true))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeBoolean, Bool: true}, int64(1)))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeBoolean, Bool: false}, int64(0)))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeBoolean, Bool: true}, float64(1)))
	assert.True(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeBoolean, Bool: false}, float64(0)))
	assert.False(t, snapshotValueMatches(apicontract.TypedValue{Type: apicontract.ValueTypeBoolean, Bool: true}, "not-bool"))

	// Default
	assert.False(t, snapshotValueMatches(apicontract.TypedValue{Type: "unknown"}, nil))

	// snapshotDriverValue
	_, err = snapshotDriverValue(apicontract.TypedValue{Type: "unknown"})
	assert.ErrorIs(t, err, ErrSnapshotPolicyUnexpressible)
	_, err = snapshotDriverValue(apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "not-int"})
	assert.ErrorIs(t, err, ErrSnapshotPolicyUnexpressible)

	// snapshotDecimalFloat
	_, err = snapshotDecimalFloat("not-a-number")
	assert.ErrorIs(t, err, ErrSnapshotPolicyUnexpressible)

	// materializeSnapshot with 0 columns and 0 rows
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "empty.db"))
	require.NoError(t, err)
	defer db.Close()
	err = materializeSnapshot(context.Background(), db, "empty_table", apicontract.Recordset{})
	assert.NoError(t, err)

	// materializeSnapshot on closed db -> exec error
	_ = db.Close()
	err = materializeSnapshot(context.Background(), db, "t", apicontract.Recordset{})
	assert.Error(t, err)

	// beginTxSnapshot error
	db2, err := sql.Open("sqlite", filepath.Join(dir, "db2.db"))
	require.NoError(t, err)
	defer db2.Close()
	origBegin := beginTxSnapshot
	beginTxSnapshot = func(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
		return nil, errors.New("beginTx failed")
	}
	rsWithRow := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "string"}},
		Rows:    [][]apicontract.TypedValue{{{Type: apicontract.ValueTypeString, Str: "val1"}}},
	}
	err = materializeSnapshot(context.Background(), db2, "t_begin", rsWithRow)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "beginTx failed")
	beginTxSnapshot = origBegin

	// prepareContextSnapshot error
	origPrepare := prepareContextSnapshot
	prepareContextSnapshot = func(ctx context.Context, tx *sql.Tx, query string) (*sql.Stmt, error) {
		return nil, errors.New("prepare failed")
	}
	err = materializeSnapshot(context.Background(), db2, "t_prep", rsWithRow)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "prepare failed")
	prepareContextSnapshot = origPrepare

	// stmtExecSnapshot error
	origStmtExec := stmtExecSnapshot
	stmtExecSnapshot = func(ctx context.Context, stmt *sql.Stmt, args ...any) (sql.Result, error) {
		return nil, errors.New("exec failed")
	}
	err = materializeSnapshot(context.Background(), db2, "t_exec", rsWithRow)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exec failed")
	stmtExecSnapshot = origStmtExec

	// restoreSnapshotRecordset unexpressible when allowed != 0
	origRS := apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "c1", Type: "string"}},
		Rows:    [][]apicontract.TypedValue{{{Type: apicontract.ValueTypeString, Str: "val1"}}},
	}
	filtered := Result{
		Columns: []string{"c1", "c2_extra"},
		Rows:    []Row{{Data: map[string]any{"c1": "val1"}}},
	}
	_, _, err = restoreSnapshotRecordset(origRS, filtered)
	assert.ErrorIs(t, err, ErrSnapshotPolicyUnexpressible)

	// restoreSnapshotRecordset match < 0 with mismatched value
	filteredMismatch := Result{
		Columns: []string{"c1"},
		Rows:    []Row{{Data: map[string]any{"c1": "different_val"}}},
	}
	_, _, err = restoreSnapshotRecordset(origRS, filteredMismatch)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot restore string from string")

	// restoreSnapshotRecordset match < 0 when all values match but row already used (line 115)
	filteredDup := Result{
		Columns: []string{"c1"},
		Rows:    []Row{{Data: map[string]any{"c1": "val1"}}, {Data: map[string]any{"c1": "val1"}}},
	}
	_, _, err = restoreSnapshotRecordset(origRS, filteredDup)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "filtered row cannot be matched to original typed evidence")
}

func TestCoverage_Statistics_TypesAndBuckets(t *testing.T) {
	// observedFromValue numeric types
	assert.Equal(t, ValueKindNumber, observedFromValue(int8(8)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(int16(16)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(int32(32)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(uint(1)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(uint8(1)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(uint16(1)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(uint32(1)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(float32(1.5)).kind)
	assert.Equal(t, ValueKindNumber, observedFromValue(json.Number("42.5")).kind)
	assert.Equal(t, ValueKindOther, observedFromValue(json.Number("invalid")).kind)
	assert.Equal(t, ValueKindOther, observedFromValue(struct{}{}).kind)

	// observedNumber NaN/Inf
	assert.Equal(t, ValueKindOther, observedNumber("nan", math.NaN()).kind)
	assert.Equal(t, ValueKindOther, observedNumber("inf", math.Inf(1)).kind)

	// observedFromTypedValue invalid datetime and default
	assert.Equal(t, ValueKindDatetime, observedFromTypedValue(apicontract.TypedValue{Type: apicontract.ValueTypeDatetime, Str: "invalid-time"}).kind)
	assert.Equal(t, ValueKindOther, observedFromTypedValue(apicontract.TypedValue{Type: "other_type"}).kind)

	// TypeObservations.add default
	var obs TypeObservations
	obs.add(ValueKindOther)
	assert.Equal(t, 1, obs.Other)

	// finalizeColumn with 0 rows
	acc := newStatisticsAccumulator()
	colStat := acc.finalizeColumn("missing_col")
	assert.Equal(t, "missing_col", colStat.Name)

	// MaxStatisticDateBuckets overflow
	acc2 := newStatisticsAccumulator()
	for i := 0; i < MaxStatisticDateBuckets+5; i++ {
		dateStr := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.RFC3339)
		acc2.addRow(map[string]any{"d": dateStr, "n": float64(i)})
	}
	res := acc2.finalize([]string{"d", "n"})
	assert.True(t, res.Columns[0].DateBucketsIncomplete || res.Columns[1].DateBucketsIncomplete || res.DateNumericSumsIncomplete)

	// Secondary sort on Frequencies by Type when Label is equal
	acc3 := newStatisticsAccumulator()
	acc3.addObservedRow(map[string]observedValue{
		"col": {kind: ValueKindString, label: "same"},
	})
	acc3.addObservedRow(map[string]observedValue{
		"col": {kind: ValueKindNumber, label: "same", numeric: 1, isNum: true},
	})
	stat3 := acc3.finalize([]string{"col"})
	require.Len(t, stat3.Columns, 1)
	assert.Len(t, stat3.Columns[0].Frequencies, 2)
}
