package commands

import (
	"bytes"
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
	"github.com/datatug/datatug-cli/pkg/callercontext"
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

	// db copy --json reports the refusal too, and the current-project form is checked.
	stdout, _, code = runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--json")
	require.Equal(t, exitCodeAccessDenied, code)
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	assert.Equal(t, "failed", report.Status)
	assert.Contains(t, report.Error, meaninglabels.CodeFieldProtected)
	expectRefused("current project", sqliteFile, "db", "copy", "--from", "datatug:///chinook", "--to", "sqlite://"+sqliteFile)
	filterConfig := filepath.Join(dir, "filter.yaml")
	require.NoError(t, os.WriteFile(filterConfig, []byte("include: [Customer]\n"), 0o644))
	expectRefused("filter config", sqliteFile, "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--filter-config", filterConfig)

	// A usage error is reported before the labels are checked.
	_, stderr, code := runCLI(t, "", "db", "copy", "--from", ref, "--to", "ingitdb://"+dir+"?records-format=json")
	assert.Equal(t, exitCodeUsage, code, stderr)
	assert.NotContains(t, stderr, meaninglabels.CodeFieldProtected)

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
	stdout, stderr, code = runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--include", "Genre,MediaType")
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
	restoreVar(t, &openCopySource)
	restoreVar(t, &listCollections)
	restoreVar(t, &describeCollection)
	restoreVar(t, &listSourceViews)
	restoreVar(t, &callerLookup)
	agent := func(on bool) {
		callerLookup = func(name string) (string, bool) {
			if on && name == callercontext.HarnessEnv {
				return "claude-code", true
			}
			return "", false
		}
	}
	agent(false)
	project := &projectSource{dir: "/p", catalog: "crm"}
	src := dbcopy.BackendRef{Scheme: "sqlite", Path: "/x.sqlite"}
	var stderr bytes.Buffer
	cmd := copyLabelCommand(t)
	cmd.SetErr(&stderr)
	check := func() string { return copyLabelRefusal(cmd, src, project, "sales") }

	assert.Empty(t, copyLabelRefusal(cmd, src, nil, ""), "a source that is not a project source has no labels")

	loadLabels = func(context.Context, string) (meaninglabels.Document, error) {
		return meaninglabels.Document{}, errors.New("broken")
	}
	assert.Contains(t, check(), "datatug-labels.json")

	doc := meaninglabels.NewDocument()
	loadLabels = func(context.Context, string) (meaninglabels.Document, error) { return doc, nil }
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) { return nil, errors.New("no source") }
	assert.Empty(t, check(), "with no stored label, a person's copy reports the source it cannot open itself")
	agent(true)
	assert.Contains(t, check(), "source crm cannot be opened", "an agent's copy that cannot be checked is refused")
	agent(false)

	var openedSchema string
	openCopySource = func(_ context.Context, _ dbcopy.BackendRef, schema string) (dal.DB, error) {
		openedSchema = schema
		return nil, nil
	}
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
	assert.Equal(t, "sales", openedSchema, "the check reads the schema the copy reads")
	assert.Empty(t, stderr.String())

	// A read-time suggestion alone warns a person and refuses an agent.
	fields["Account"] = append(fields["Account"], dbschema.FieldDef{Name: "email"})
	assert.Empty(t, check(), "a person copies a column only a suggestion protects")
	assert.Contains(t, stderr.String(), "warning: this copy is not masked; read-time suggestions mark Account.email of source crm sensitive")
	agent(true)
	assert.Contains(t, check(), "crm.Account.email is protected (pii)", "a suggestion refuses an agent")
	assert.Empty(t, copyLabelRefusal(copyLabelCommand(t, "--include", "Region"), src, project, ""), "only the selected tables count")
	assert.Empty(t, copyLabelRefusal(copyLabelCommand(t, "--exclude", "Account"), src, project, ""))
	assert.NotEmpty(t, copyLabelRefusal(copyLabelCommand(t, "--include", "Region,Missing"), src, project, ""), "a filter the copy refuses checks every table")
	assert.NotEmpty(t, copyLabelRefusal(copyLabelCommand(t, "--include", "Region", "--exclude", "Account"), src, project, ""), "directives the copy refuses check every table")
	agent(false)
	fields["Account"] = fields["Account"][:2]

	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return nil, errors.New("no list")
	}
	assert.Empty(t, check(), "with no stored label, a person's copy reports the tables it cannot list itself")
	agent(true)
	assert.Contains(t, check(), "the tables of crm cannot be listed", "an agent's copy that cannot see its tables is refused")
	agent(false)

	// A protected label stored for the source checks it fully, for everyone.
	doc, _, err := meaninglabels.Apply(doc, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "crm", Table: "Account", Column: "Hidden"}, Class: meaninglabels.ClassFinancial, Reason: "test"}, meaninglabels.Actor{ID: "owner"}, projectReadNow())
	require.NoError(t, err)
	assert.Contains(t, check(), "the tables of crm cannot be listed", "a labelled source whose tables cannot be listed refuses a person")
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) { return nil, errors.New("no source") }
	assert.Contains(t, check(), "source crm cannot be opened", "a labelled source that cannot be opened refuses a person")
	openCopySource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) { return nil, nil }
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
	assert.Contains(t, check(), "crm.Gone.Id is protected (pii)", "an unreviewed view of a labelled source is masked whole, for a person too")
}

func TestCopyRefusedFormat(t *testing.T) {
	assert.True(t, strings.HasPrefix(copyRefusedFormat, "access denied: FIELD_PROTECTED: "))
	assert.Equal(t, exitCodeAccessDenied, copyRefusedCode)
}

// The demo's chinook source stores labels on Customer and Employee only. A
// person may copy a table that read-time suggestions alone protect (Invoice:
// its billing address), with a warning; an agent may not. A refused --json
// copy names the SQLite engine of each end, and a table filter that is a
// usage error exits 2, not 5, even on a labelled source.
func TestDBCopyLabelledSourceFollowUps(t *testing.T) {
	demoForTest(t)
	const ref = "datatug://datatug-demo/chinook"
	dir := t.TempDir()
	prev := callerLookup
	t.Cleanup(func() { callerLookup = prev })
	callerLookup = func(string) (string, bool) { return "", false }

	sqliteFile := filepath.Join(dir, "copy.sqlite")
	stdout, _, code := runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--json")
	require.Equal(t, exitCodeAccessDenied, code)
	var report struct {
		From struct {
			Engine string `json:"engine"`
		} `json:"from"`
		To struct {
			Engine string `json:"engine"`
		} `json:"to"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	assert.Equal(t, "sqlite", report.From.Engine)
	assert.Equal(t, "sqlite", report.To.Engine, "the target's own engine, not ingitdb")

	_, stderr, code := runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--include", "Genre", "--exclude", "Customer")
	assert.Equal(t, exitCodeUsage, code, stderr)
	assert.NotContains(t, stderr, meaninglabels.CodeFieldProtected)
	stdout, _, code = runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--include", "Genre", "--exclude", "Customer", "--json")
	assert.Equal(t, exitCodeUsage, code)
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	assert.Equal(t, "sqlite", report.To.Engine)

	stdout, stderr, code = runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+sqliteFile, "--include", "Invoice")
	require.Equal(t, 0, code, stdout+stderr)
	assert.Contains(t, stderr, "warning: this copy is not masked")
	assert.Contains(t, stderr, "Invoice.BillingAddress")

	callerLookup = func(name string) (string, bool) { return "claude-code", name == callercontext.HarnessEnv }
	agentFile := filepath.Join(dir, "agent.sqlite")
	_, stderr, code = runCLI(t, "", "db", "copy", "--from", ref, "--to", "sqlite://"+agentFile, "--include", "Invoice")
	assert.Equal(t, exitCodeAccessDenied, code, stderr)
	assert.Contains(t, stderr, meaninglabels.CodeFieldProtected)
	_, err := os.Stat(agentFile)
	assert.True(t, os.IsNotExist(err))
}
