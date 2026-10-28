package commands

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

// ownedSourceCounter counts the handles a command opens and closes. Each handle
// is a DALgo DB built from an adapter backend that can be closed, so Close is
// hidden behind the framework's sealed value exactly as it is for the CouchDB
// and PostgREST adapters: only dbcopy.CloseSource's dal.As lookup sees it.
type ownedSourceCounter struct {
	opens  int
	closes int
	// listErr, when set, makes the sealed source fail ListCollections.
	listErr error
}

type countingSchemaBackend struct {
	dal.Backend
	dbschema.SchemaReader
	counter *ownedSourceCounter
	listErr error
}

func (b *countingSchemaBackend) Close() error {
	b.counter.closes++
	if closer, ok := any(b.Backend).(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (b *countingSchemaBackend) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	if b.SchemaReader == nil {
		return nil, errors.New("the test source has no schema reader")
	}
	return b.SchemaReader.ListCollections(ctx, parent)
}

// openSQLite opens a fresh sealed handle over path, so a command's close never
// affects the handle of a later run.
func (c *ownedSourceCounter) openSQLite(t *testing.T, path string) (dal.DB, error) {
	t.Helper()
	ref, err := dbcopy.Parse("sqlite://" + path)
	if err != nil {
		return nil, err
	}
	realDB, err := ref.OpenProtected(context.Background())
	if err != nil {
		return nil, err
	}
	reader, _ := dal.As[dbschema.SchemaReader](realDB)
	c.opens++
	sealed := dal.NewDB(&countingSchemaBackend{Backend: dal.BackendOf(realDB), SchemaReader: reader, counter: c, listErr: c.listErr})
	if _, visible := any(sealed).(io.Closer); visible {
		t.Fatal("the test needs the framework's sealed DB value (Close must be hidden)")
	}
	if _, visible := any(sealed).(dbschema.SchemaReader); visible {
		t.Fatal("the test needs the framework's sealed DB value (SchemaReader must be hidden)")
	}
	return sealed, nil
}

func runOwnedSourceCommand(t *testing.T, command *cobra.Command, out io.Writer, args ...string) error {
	t.Helper()
	command.SilenceUsage = true
	command.SilenceErrors = true
	if out != nil {
		command.SetOut(out)
	}
	command.SetErr(io.Discard)
	command.SetArgs(args)
	return command.Execute()
}

func useSealedSchemaSource(t *testing.T, counter *ownedSourceCounter, path string, listErr error) {
	t.Helper()
	counter.listErr = listErr
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return counter.openSQLite(t, path)
	})
}

func TestSchemaSessionCloseToleratesAMissingHandle(t *testing.T) {
	var none *schemaSession
	none.Close()
	(&schemaSession{}).Close()
}

func TestDBMetadataCommandsCloseTheOwnedSourceOnSuccess(t *testing.T) {
	path := covCSQLiteFile(t, "metadata.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`)
	counter := &ownedSourceCounter{}
	useSealedSchemaSource(t, counter, path, nil)

	runs := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"db schema", dbSchemaCommand(), []string{"--db", "sqlite://" + path}},
		{"db table", dbTableCommand(), []string{"t", "--db", "sqlite://" + path}},
		{"db column", dbColumnCommand(), []string{"t", "name", "--db", "sqlite://" + path}},
	}
	for i, run := range runs {
		if err := runOwnedSourceCommand(t, run.cmd, io.Discard, run.args...); err != nil {
			t.Fatalf("%s: %v", run.name, err)
		}
		if counter.opens != i+1 || counter.closes != i+1 {
			t.Fatalf("%s: opens=%d closes=%d, want %d/%d", run.name, counter.opens, counter.closes, i+1, i+1)
		}
	}
}

func TestDBMetadataCommandsCloseTheOwnedSourceOnFailures(t *testing.T) {
	path := covCSQLiteFile(t, "metadata-failures.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`)
	counter := &ownedSourceCounter{}
	useSealedSchemaSource(t, counter, path, nil)

	if err := runOwnedSourceCommand(t, dbSchemaCommand(), io.Discard, "--db", "sqlite://"+path); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if counter.closes != 1 {
		t.Fatalf("close after a successful schema listing = %d, want 1", counter.closes)
	}

	err := runOwnedSourceCommand(t, dbTableCommand(), io.Discard, "missing", "--db", "sqlite://"+path)
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != exitCodeUsage {
		t.Fatalf("missing table error = %v, want usage exit", err)
	}
	if counter.closes != 2 {
		t.Fatalf("close after a table lookup failure = %d, want 2", counter.closes)
	}

	err = runOwnedSourceCommand(t, dbColumnCommand(), io.Discard, "t", "missing", "--db", "sqlite://"+path)
	if !errors.As(err, &coder) || coder.ExitCode() != exitCodeUsage {
		t.Fatalf("missing column error = %v, want usage exit", err)
	}
	if counter.closes != 3 {
		t.Fatalf("close after a column lookup failure = %d, want 3", counter.closes)
	}

	if err = runOwnedSourceCommand(t, dbSchemaCommand(), covCFailWriter{}, "--db", "sqlite://"+path); err == nil {
		t.Fatal("the schema output was expected to fail on the writer")
	}
	if counter.closes != 4 {
		t.Fatalf("close after an output write failure = %d, want 4", counter.closes)
	}
}

func TestDBMetadataCommandsCloseTheOwnedSourceWhenListingFails(t *testing.T) {
	path := covCSQLiteFile(t, "metadata-listing.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`)
	counter := &ownedSourceCounter{}
	useSealedSchemaSource(t, counter, path, errors.New("catalog unavailable"))

	for i, run := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"db schema", dbSchemaCommand(), []string{"--db", "sqlite://" + path}},
		{"db table", dbTableCommand(), []string{"t", "--db", "sqlite://" + path}},
	} {
		err := runOwnedSourceCommand(t, run.cmd, io.Discard, run.args...)
		var coder ExitCoder
		if !errors.As(err, &coder) || coder.ExitCode() != exitCodeDatabase {
			t.Fatalf("%s listing failure = %v, want database exit", run.name, err)
		}
		if counter.closes != i+1 {
			t.Fatalf("%s: close after a listing failure = %d, want %d", run.name, counter.closes, i+1)
		}
	}
}

func restoreCopyOpenSeams(t *testing.T) {
	t.Helper()
	originalSource, originalTarget, originalRunner := openCopySource, openCopyTarget, dbCopyRunner
	t.Cleanup(func() {
		openCopySource, openCopyTarget, dbCopyRunner = originalSource, originalTarget, originalRunner
	})
}

func TestDBCopyClosesTheOwnedSourceAndTargetOnSuccess(t *testing.T) {
	path := covCSQLiteFile(t, "copy-source.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	source, target := &ownedSourceCounter{}, &ownedSourceCounter{}
	restoreCopyOpenSeams(t)
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) {
		return source.openSQLite(t, path)
	}
	openCopyTarget = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return target.openSQLite(t, path)
	}
	dbCopyRunner = func(context.Context, dal.DB, dal.DB, dbcopy.CopyOpts) (dbcopy.SourceSummary, error) {
		return dbcopy.SourceSummary{Tables: 1}, nil
	}

	_, _, err := runCopy(t, "db", "copy", "--from", "ingitdb://"+t.TempDir(), "--to", "ingitdb://"+t.TempDir())
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if source.opens != 1 || source.closes != 1 || target.opens != 1 || target.closes != 1 {
		t.Fatalf("source opens/closes = %d/%d, target = %d/%d, want 1/1 and 1/1",
			source.opens, source.closes, target.opens, target.closes)
	}
}

func TestDBCopyClosesTheOwnedSourceWhenDirectivesFailBeforeTheTargetOpens(t *testing.T) {
	path := covCSQLiteFile(t, "copy-directives.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	source, target := &ownedSourceCounter{}, &ownedSourceCounter{}
	restoreCopyOpenSeams(t)
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) {
		return source.openSQLite(t, path)
	}
	openCopyTarget = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return target.openSQLite(t, path)
	}

	_, _, err := runCopy(t, "db", "copy", "--from", "ingitdb://"+t.TempDir(), "--to", "ingitdb://"+t.TempDir(), "--where", "bad")
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 2 {
		t.Fatalf("directive error = %v, want usage exit", err)
	}
	if source.closes != 1 {
		t.Fatalf("close after a directive error = %d, want 1", source.closes)
	}
	if target.opens != 0 {
		t.Fatalf("the target was opened after a directive error: %d", target.opens)
	}
}

func TestDBCopyClosesTheOwnedSourceWhenTheTargetDoesNotOpen(t *testing.T) {
	path := covCSQLiteFile(t, "copy-target.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	source := &ownedSourceCounter{}
	restoreCopyOpenSeams(t)
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) {
		return source.openSQLite(t, path)
	}
	openCopyTarget = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return nil, errors.New("target refused")
	}

	_, _, err := runCopy(t, "db", "copy", "--from", "ingitdb://"+t.TempDir(), "--to", "ingitdb://"+t.TempDir())
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 4 {
		t.Fatalf("target open error = %v, want connection exit", err)
	}
	if source.closes != 1 {
		t.Fatalf("close after a target open failure = %d, want 1", source.closes)
	}
}

func TestDBCopyClosesBothOwnedHandlesWhenTheCopyFails(t *testing.T) {
	path := covCSQLiteFile(t, "copy-failure.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	source, target := &ownedSourceCounter{}, &ownedSourceCounter{}
	restoreCopyOpenSeams(t)
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) {
		return source.openSQLite(t, path)
	}
	openCopyTarget = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return target.openSQLite(t, path)
	}
	dbCopyRunner = func(context.Context, dal.DB, dal.DB, dbcopy.CopyOpts) (dbcopy.SourceSummary, error) {
		return dbcopy.SourceSummary{}, errors.New("copy failed")
	}

	_, _, err := runCopy(t, "db", "copy", "--from", "ingitdb://"+t.TempDir(), "--to", "ingitdb://"+t.TempDir())
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 1 {
		t.Fatalf("copy failure = %v, want runtime exit", err)
	}
	if source.closes != 1 || target.closes != 1 {
		t.Fatalf("close after a copy failure: source=%d target=%d, want 1 and 1", source.closes, target.closes)
	}
}

func TestDBCopyClosesTheOwnedSourceWhenBigQueryCredentialsFail(t *testing.T) {
	path := covCSQLiteFile(t, "copy-bigquery.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	source := &ownedSourceCounter{}
	restoreCopyOpenSeams(t)
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) {
		return source.openSQLite(t, path)
	}
	originalClient := bqGoogleDefaultClient
	t.Cleanup(func() { bqGoogleDefaultClient = originalClient })
	bqGoogleDefaultClient = func(context.Context, ...string) (*http.Client, error) {
		return nil, errors.New("no credentials")
	}

	_, _, err := runCopy(t, "db", "copy", "--from", "ingitdb://"+t.TempDir(), "--to", "bigquery://demodb/research?location=US")
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 4 {
		t.Fatalf("BigQuery credential error = %v, want connection exit", err)
	}
	if source.closes != 1 {
		t.Fatalf("close after a BigQuery credential failure = %d, want 1", source.closes)
	}
}

func TestDBExportClosesTheOwnedSourceOnSuccessAndFailure(t *testing.T) {
	path := covCSQLiteFile(t, "export-source.sqlite", `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	source := &ownedSourceCounter{}
	originalOpen, originalExport := openExportSource, runNativeExport
	t.Cleanup(func() { openExportSource, runNativeExport = originalOpen, originalExport })
	openExportSource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) {
		return source.openSQLite(t, path)
	}

	runs := []struct {
		name string
		run  func(context.Context, dal.DB, string, ...dbcopy.ExportOptions) (*dbcopy.ExportResult, error)
	}{
		{"success", func(context.Context, dal.DB, string, ...dbcopy.ExportOptions) (*dbcopy.ExportResult, error) {
			return &dbcopy.ExportResult{}, nil
		}},
		{"export failure", func(context.Context, dal.DB, string, ...dbcopy.ExportOptions) (*dbcopy.ExportResult, error) {
			return nil, errors.New("export failed")
		}},
	}
	for i, run := range runs {
		runNativeExport = run.run
		destination := filepath.Join(t.TempDir(), "backup.ingitdb")
		err := runOwnedSourceCommand(t, dbExportCommand(), io.Discard,
			"--from", "ingitdb://"+t.TempDir(), "--to", destination)
		if run.name == "success" && err != nil {
			t.Fatalf("%s: %v", run.name, err)
		}
		if run.name != "success" && err == nil {
			t.Fatalf("%s: expected an error", run.name)
		}
		if source.closes != i+1 {
			t.Fatalf("%s: close = %d, want %d", run.name, source.closes, i+1)
		}
	}
}
