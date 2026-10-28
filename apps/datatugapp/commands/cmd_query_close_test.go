package commands

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// countingQueryBackend delegates to a real adapter backend and can be closed. A
// DALgo DB built from it is the framework's sealed value: Close is only
// reachable through dal.As, as it is for the CouchDB and PostgREST adapters.
type countingQueryBackend struct {
	dal.Backend
	closes int
}

func (b *countingQueryBackend) Close() error {
	b.closes++
	return nil
}

func TestQueryRunClosesTheOwnedSourceOnSuccessAndFailure(t *testing.T) {
	path := covCSQLiteFile(t, "close.sqlite", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO t VALUES (1,'a')`)
	ref, err := dbcopy.Parse("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	realDB, err := ref.OpenProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbcopy.CloseSource(realDB) })
	backend := &countingQueryBackend{Backend: dal.BackendOf(realDB)}
	covCSeamOpenBackend(t, dal.NewDB(backend))

	run := func(table string) (bool, *countingQueryBackend) {
		cmd, _, _ := covCRunCmd(t, map[string]string{
			queryDBFlag:         "sqlite://whatever.db",
			queryFromFlag:       table,
			queryLimitFlag:      "1",
			queryNoPoliciesFlag: "true",
			queryFormatFlag:     "json",
		})
		return queryRunCommandAction(cmd, nil) == nil, backend
	}

	if ok, counter := run("t"); !ok || counter.closes != 1 {
		t.Fatalf("success: ok=%v closes=%d, want true and 1", ok, counter.closes)
	}

	if ok, counter := run("missing_table"); ok || counter.closes != 2 {
		t.Fatalf("query failure: ok=%v closes=%d, want false and 2", ok, counter.closes)
	}

	cmd, _, _ := covCRunCmd(t, map[string]string{
		queryDBFlag:         "sqlite://whatever.db",
		queryFromFlag:       "t",
		queryLimitFlag:      "1",
		queryNoPoliciesFlag: "true",
		queryFormatFlag:     "json",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(ctx)
	_ = queryRunCommandAction(cmd, nil)
	if backend.closes != 3 {
		t.Fatalf("close after cancellation = %d, want 3", backend.closes)
	}
}
