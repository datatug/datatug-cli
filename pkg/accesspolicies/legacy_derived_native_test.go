package accesspolicies

import (
	"context"
	"database/sql"
	"errors"
	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"io"
	_ "modernc.org/sqlite"
	"path/filepath"
	"testing"
)

func TestPrivateLegacyDerivedNativeEntryCompatibility(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "owned.sqlite")
	native, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = native.Exec(`CREATE TABLE Item(id TEXT PRIMARY KEY,name TEXT);INSERT INTO Item VALUES('i1','one'),('i2','two')`); err != nil {
		t.Fatal(err)
	}
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	ref, err := dbcopy.Parse(dbcopy.LocalSourceURL("sqlite", path))
	if err != nil {
		t.Fatal(err)
	}
	db, err := ref.OpenProtected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer dbcopy.CloseSource(db)
	leaf := dal.From(dal.NewRootCollectionRef("Item", "")).NewQuery().SelectColumns(dal.Column{Expression: dal.Field("id")}, dal.Column{Expression: dal.Field("name")})
	derived := dal.From(dal.NewQuerySource(leaf, "d")).NewQuery().SelectColumns(dal.Column{Expression: dal.Field("id")}, dal.Column{Expression: dal.Field("name")})
	recordsCount := func(reader dal.RecordsReader) int {
		t.Helper()
		defer func() {
			if err := reader.Close(); err != nil {
				t.Error(err)
			}
		}()
		n := 0
		for {
			_, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return n
			}
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	recordsetCount := func(reader dal.RecordsetReader) int {
		t.Helper()
		defer func() {
			if err := reader.Close(); err != nil {
				t.Error(err)
			}
		}()
		n := 0
		for {
			row, set, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return n
			}
			if err != nil {
				t.Fatal(err)
			}
			values, err := row.Data(set)
			if err != nil || len(values) != 2 {
				t.Fatal("owned row", values, err)
			}
			n++
		}
	}
	// Native leaf positives establish that this real database works for each entry type.
	rr, err := db.ExecuteQueryToRecordsReader(ctx, leaf)
	if err != nil || rr == nil {
		t.Fatal("native leaf records", err)
	}
	if n := recordsCount(rr); n != 2 {
		t.Fatal(n)
	}
	rs, err := db.ExecuteQueryToRecordsetReader(ctx, leaf)
	if err != nil || rs == nil {
		t.Fatal("native leaf recordset", err)
	}
	if n := recordsetCount(rs); n != 2 {
		t.Fatal(n)
	}
	// The same derived source may be delegated to DALgo's generic engine rather than SQL compiler.
	rr, err = db.ExecuteQueryToRecordsReader(ctx, derived)
	if err != nil {
		t.Logf("raw native derived records refuses: %v", err)
	} else {
		t.Logf("raw native derived records count=%d", recordsCount(rr))
	}
	rs, err = db.ExecuteQueryToRecordsetReader(ctx, derived)
	if err != nil {
		t.Logf("raw native derived recordset refuses: %v", err)
	} else {
		t.Logf("raw native derived recordset count=%d", recordsetCount(rs))
	}
	baseRecords, err := dal.ExecuteRecursiveQuery(ctx, db, derived)
	if err != nil {
		t.Fatal("generic baseline records", err)
	}
	if n := recordsCount(baseRecords); n != 2 {
		t.Fatal(n)
	}
	baseSet, err := dal.ExecuteRecursiveRecordset(ctx, db, derived)
	if err != nil {
		t.Fatal("generic baseline recordset", err)
	}
	if n := recordsetCount(baseSet); n != 2 {
		t.Fatal(n)
	}
	secured := secureReadSession(db, Options{Unrestricted: true})
	rr, err = secured.ExecuteQueryToRecordsReader(ctx, derived)
	if err != nil {
		t.Fatal("policyfree records", err)
	}
	if n := recordsCount(rr); n != 2 {
		t.Fatal(n)
	}
	rs, err = secured.ExecuteQueryToRecordsetReader(ctx, derived)
	if err != nil {
		t.Fatal("policyfree recordset", err)
	}
	if n := recordsetCount(rs); n != 2 {
		t.Fatal(n)
	}
	t.Log("owned derived positives: generic baseline + policyfree records and recordset all2rows")
	denied := access.WithPolicy(ctx, access.MustPolicy("mandatory-deny", access.Root(access.Deny(access.Query)), access.OpaqueQueryScope(access.Deny(access.Query))))
	if _, err = secured.ExecuteQueryToRecordsReader(denied, derived); !errors.Is(err, access.ErrAccessDenied) {
		t.Fatal("mandatory records denial lost", err)
	}
	if _, err = secured.ExecuteQueryToRecordsetReader(denied, derived); !errors.Is(err, access.ErrAccessDenied) {
		t.Fatal("mandatory recordset denial lost", err)
	}
	_, err = PrepareRecordset(ctx, derived, Options{Unrestricted: true}, func(dal.Query) error { return nil })
	t.Logf("PrepareRecordset derived shape with permissive diagnostic guard: %v", err)
}
