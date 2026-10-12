package commands

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

// covCFailWriter fails every write.
type covCFailWriter struct{}

func (covCFailWriter) Write([]byte) (int, error) { return 0, errors.New("covC write failed") }

// covCReader is a scripted dal.RecordsReader.
type covCReader struct {
	recs []record.Record
	err  error
}

func (r *covCReader) Next() (record.Record, error) {
	if len(r.recs) > 0 {
		rec := r.recs[0]
		r.recs = r.recs[1:]
		return rec, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return nil, dal.ErrNoMoreRecords
}
func (r *covCReader) Cursor() (string, error) { return "", nil }
func (r *covCReader) Close() error            { return nil }

// covCFakeDB is a dal.DB whose only working method is query execution.
type covCFakeDB struct {
	dal.DB
	reader dal.RecordsReader
}

func (d covCFakeDB) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return d.reader, nil
}

func covCSeamOpenBackend(t *testing.T, db dal.DB) {
	t.Helper()
	orig := openBackend
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return db, nil }
	t.Cleanup(func() { openBackend = orig })
}

// covCSQLiteFile creates a sqlite file with the given statements executed.
func covCSQLiteFile(t *testing.T, name string, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return path
}

// covCRunCmd builds the `query run` command with flags set, out/err captured.
func covCRunCmd(t *testing.T, flags map[string]string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := queryRunCommand()
	for name, value := range flags {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	return cmd, out, errOut
}

func TestCovCQueryRunAdHocNilContext(t *testing.T) {
	path := covCSQLiteFile(t, "q.sqlite", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO t VALUES (1,'a')`)
	cmd, out, _ := covCRunCmd(t, map[string]string{queryDBFlag: "sqlite://" + path, queryFromFlag: "t", queryNoPoliciesFlag: "true", queryFormatFlag: "json"})
	// The command is called directly, never through Execute, so its context is nil.
	if err := queryRunCommandAction(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"name"`) {
		t.Fatalf("output: %s", out.String())
	}
}

func TestCovCQueryRunReservedKeyColumn(t *testing.T) {
	path := covCSQLiteFile(t, "q.sqlite", `CREATE TABLE t(id INTEGER PRIMARY KEY, "$key" TEXT)`, `INSERT INTO t VALUES (1,'a')`)
	cmd, _, _ := covCRunCmd(t, map[string]string{queryDBFlag: "sqlite://" + path, queryFromFlag: "t", queryNoPoliciesFlag: "true", queryFormatFlag: "json"})
	err := queryRunCommandAction(cmd, nil)
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 1 {
		t.Fatalf("want exit 1 for reserved key column, got %v", err)
	}
}

func TestCovCQueryRunCollectRowsFailure(t *testing.T) {
	covCSeamOpenBackend(t, covCFakeDB{reader: &covCReader{err: errors.New("covC reader boom")}})
	cmd, _, _ := covCRunCmd(t, map[string]string{queryDBFlag: "sqlite:///unused", queryFromFlag: "t", queryNoPoliciesFlag: "true"})
	err := queryRunCommandAction(cmd, nil)
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != exitCodeDatabase {
		t.Fatalf("want database exit code, got %v", err)
	}
}

func TestCovCQueryRunWriteFailure(t *testing.T) {
	rec := record.NewRecordWithData(record.NewKeyWithID("t", "1"), map[string]any{"a": 1})
	covCSeamOpenBackend(t, covCFakeDB{reader: &covCReader{recs: []record.Record{rec}}})
	cmd, _, _ := covCRunCmd(t, map[string]string{queryDBFlag: "sqlite:///unused", queryFromFlag: "t", queryNoPoliciesFlag: "true", queryFormatFlag: "json"})
	cmd.SetOut(covCFailWriter{})
	err := queryRunCommandAction(cmd, nil)
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 1 {
		t.Fatalf("want exit 1 for write failure, got %v", err)
	}
}

func TestCovCQueryFailureDefaultAndBuildQueryBadDocument(t *testing.T) {
	err := queryFailure(errors.New("plain failure"))
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != exitCodeDatabase {
		t.Fatalf("queryFailure default: %v", err)
	}
	file := filepath.Join(t.TempDir(), "bad.dtql")
	if err := os.WriteFile(file, []byte("from: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = buildQuery(queryOptions{file: file}, strings.NewReader(""))
	if !errors.As(err, &coder) || coder.ExitCode() != exitCodeUsage {
		t.Fatalf("buildQuery bad document: %v", err)
	}
}
