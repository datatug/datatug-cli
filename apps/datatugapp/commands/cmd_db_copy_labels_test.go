package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The demo's chinook source labels Customer.Email pii: no copy or export of
// it may write the real values anywhere, whoever runs it, because a copy
// cannot mask. An independent review reproduced the leak by copying chinook
// to a SQLite file and querying that file.
func TestDBCopyAndExportRefuseALabelledProjectSource(t *testing.T) {
	demoForTest(t)
	const ref = "datatug://datatug-demo/chinook"
	dir := t.TempDir()
	expectRefused := func(name, target string, args ...string) {
		t.Helper()
		stdout, stderr, code := runCLI(t, "", args...)
		assert.Equal(t, exitCodeAccessDenied, code, name+": "+stdout+stderr)
		assert.Contains(t, stderr, meaninglabels.CodeFieldProtected, name)
		assert.Contains(t, stderr, "a copy cannot mask labelled columns", name)
		assert.NotContains(t, stdout+stderr, "@", name)
		_, err := os.Stat(target)
		assert.True(t, os.IsNotExist(err), name+": nothing is written")
	}
	sqliteFile := filepath.Join(dir, "copy.sqlite")
	expectRefused("faithful sqlite copy", sqliteFile, "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile)
	expectRefused("include a labelled table", sqliteFile, "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--include", "Genre,Customer")
	expectRefused("rows only", sqliteFile, "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--limit", "Customer:1")
	folder := filepath.Join(dir, "copy.ingitdb")
	expectRefused("new ingitdb copy", folder, "db", "copy", "--from", ref, "--to", "ingitdb://"+folder)
	script := filepath.Join(dir, "copy.sql")
	expectRefused("sql script", script, "db", "copy", "--from", ref, "--to", "sqlscript://"+script+"?dialect=mysql")
	exported := filepath.Join(dir, "export.ingitdb")
	expectRefused("export", exported, "db", "export", "--from", ref, "--to", exported)
	expectRefused("export dry run", exported, "db", "export", "--from", ref, "--to", exported, "--dry-run")

	// --json reports the refusal as a failed copy.
	stdout, _, code := runCLI(t, "", "db", "export", "--from", ref, "--to", exported, "--json")
	require.Equal(t, exitCodeAccessDenied, code)
	var report struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exitCode"`
		Error    string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	assert.Equal(t, "failed", report.Status)
	assert.Contains(t, report.Error, meaninglabels.CodeFieldProtected)

	// Copy has no principal and no policies to switch off: --no-policies and
	// --role are not flags of it, so they cannot widen what it copies.
	for _, flag := range []string{"--no-policies", "--role=owner"} {
		_, stderr, code := runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, flag)
		assert.NotEqual(t, 0, code, flag)
		assert.Contains(t, stderr, "unknown flag", flag)
		_, stderr, code = runCLI(t, "", "db", "export", "--from", ref, "--to", exported, flag)
		assert.NotEqual(t, 0, code, flag)
		assert.Contains(t, stderr, "unknown flag", flag)
	}
	_, err := os.Stat(sqliteFile)
	assert.True(t, os.IsNotExist(err))

	// Unlabelled tables of the same source copy as before.
	stdout, stderr, code := runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--include", "Genre,MediaType")
	require.Equal(t, 0, code, stdout+stderr)
	db, err := sql.Open("sqlite", sqliteFile)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var genres int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM Genre").Scan(&genres))
	assert.Positive(t, genres)
	_, stderr, code = runCLI(t, "", "db", "export", "--from", ref, "--to", exported, "--include", "Genre")
	require.Equal(t, 0, code, stderr)
	assert.DirExists(t, exported)
}

// copyLabelCommand is a `db copy` command whose flags are parsed from args.
func copyLabelCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := dbCopyCommand()
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

func TestCopyLabelRefusal(t *testing.T) {
	restoreVar(t, &loadLabels)
	restoreVar(t, &openBackend)
	restoreVar(t, &listCollections)
	restoreVar(t, &describeCollection)
	restoreVar(t, &listSourceViews)
	project := &projectSource{dir: "/p", catalog: "crm"}
	src := dbcopy.BackendRef{Scheme: "sqlite", Path: "/x.sqlite"}
	cmd := copyLabelCommand(t)
	check := func() string { return copyLabelRefusal(cmd, src, project) }

	assert.Empty(t, copyLabelRefusal(cmd, src, nil), "a source that is not a project source has no labels")

	loadLabels = func(context.Context, string) (meaninglabels.Document, error) {
		return meaninglabels.Document{}, errors.New("broken")
	}
	assert.Contains(t, check(), "datatug-labels.json")

	doc := meaninglabels.NewDocument()
	loadLabels = func(context.Context, string) (meaninglabels.Document, error) { return doc, nil }
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, errors.New("no source") }
	assert.Contains(t, check(), "source crm cannot be opened")

	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, nil }
	tables := []string{"Account", "Region"}
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		refs := make([]dal.CollectionRef, len(tables))
		for i, name := range tables {
			refs[i] = dal.NewRootCollectionRef(name, "")
		}
		return refs, nil
	}
	fields := map[string][]dbschema.FieldDef{"Account": {{Name: "Id"}, {Name: "Plan"}}, "Region": {{Name: "Id"}}}
	describeCollection = func(_ context.Context, _ dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		if fields[ref.Name()] == nil {
			return nil, errors.New("no columns")
		}
		return &dbschema.CollectionDef{Fields: fields[ref.Name()]}, nil
	}
	listSourceViews = func(context.Context, dal.DB) ([]dbschema.SourceViewDef, error) { return nil, nil }
	assert.Empty(t, check(), "an unlabelled source with no suggestion copies")

	fields["Account"] = append(fields["Account"], dbschema.FieldDef{Name: "email"})
	assert.Contains(t, check(), "crm.Account.email is protected (pii)", "a read-time suggestion counts")
	assert.Empty(t, copyLabelRefusal(copyLabelCommand(t, "--include", "Region"), src, project), "only the selected tables count")
	assert.Empty(t, copyLabelRefusal(copyLabelCommand(t, "--exclude", "Account"), src, project))
	assert.NotEmpty(t, copyLabelRefusal(copyLabelCommand(t, "--include", "Region,Missing"), src, project), "a filter the copy refuses checks every table")
	assert.NotEmpty(t, copyLabelRefusal(copyLabelCommand(t, "--include", "Region", "--exclude", "Account"), src, project), "directives the copy refuses check every table")
	fields["Account"] = fields["Account"][:2]

	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return nil, errors.New("no list")
	}
	assert.Empty(t, check(), "an unlabelled source whose tables cannot be listed: the copy reports it")

	// A protected label stored for the source checks it fully.
	doc, _, err := meaninglabels.Apply(doc, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "crm", Table: "Account", Column: "Hidden"}, Class: meaninglabels.ClassFinancial, Reason: "test"}, meaninglabels.Actor{ID: "owner"}, projectReadNow())
	require.NoError(t, err)
	assert.Contains(t, check(), "the tables of crm cannot be listed")
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("Account", ""), dal.NewRootCollectionRef("Region", ""), dal.NewRootCollectionRef("Gone", "")}, nil
	}
	assert.Contains(t, check(), "crm.Account.Hidden is protected (financial)", "a stored label on a column the source does not report")
	cmd = copyLabelCommand(t, "--exclude", "Account")
	assert.Contains(t, check(), "the columns of crm.Gone cannot be listed")
	fields["Gone"] = []dbschema.FieldDef{{Name: "Id"}}
	assert.Empty(t, check(), "a table with no protected column")
	listSourceViews = func(context.Context, dal.DB) ([]dbschema.SourceViewDef, error) {
		return []dbschema.SourceViewDef{{Name: "Gone"}}, nil
	}
	assert.Contains(t, check(), "crm.Gone.Id is protected (pii)", "an unreviewed view is masked whole")
}

func TestCopyRefusedFormat(t *testing.T) {
	assert.True(t, strings.HasPrefix(copyRefusedFormat, "access denied: FIELD_PROTECTED: "))
	assert.Equal(t, exitCodeAccessDenied, copyRefusedCode)
}
