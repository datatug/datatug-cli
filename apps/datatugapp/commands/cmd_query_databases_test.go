package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDBFlagsKeepsURLsWithEqualsAsTheDefault(t *testing.T) {
	bare, aliases, err := parseDBFlags([]string{"sqlite:///a.db?mode=ro", "geo=env:GEO_URL", "sales=datatug://shop/crm"})
	require.NoError(t, err)
	assert.Equal(t, "sqlite:///a.db?mode=ro", bare)
	assert.Equal(t, map[string]string{"geo": "env:GEO_URL", "sales": "datatug://shop/crm"}, aliases)
	_, err = projectDatabaseBinding(context.Background(), "datatug://")
	var coder ExitCoder
	require.ErrorAs(t, err, &coder, "a malformed reference")
	assert.Equal(t, exitCodeUsage, coder.ExitCode())
}

func TestSavedDatabaseBindingsFailures(t *testing.T) {
	dir := covCWriteProject(t, covCEnvFiles(map[string]string{"main": "main.sqlite"}))
	store := covCFileStore(dir)
	ctx := context.Background()
	text := func(body string) *datatug.QueryDef {
		return &datatug.QueryDef{ID: "q", Type: datatug.QueryTypeTugQL, Text: body}
	}
	_, err := savedDatabaseBindings(ctx, store, dir, "local", text("not tugql"), map[string]bool{})
	assert.ErrorContains(t, err, "invalid TugQL text")
	_, err = savedDatabaseBindings(ctx, store, dir, "local", &datatug.QueryDef{}, map[string]bool{})
	assert.NoError(t, err, "nothing to bind")
	_, err = savedDatabaseBindings(ctx, &covCStore{envsErr: errors.New("boom")}, dir, "", &datatug.QueryDef{}, map[string]bool{"main": true})
	assert.ErrorContains(t, err, "boom")

	bind := func(target string) (databaseBinding, error) {
		return savedDatabaseBinding(ctx, store, dir, "", "local", "q", "main", target)
	}
	_, err = bind("datatug://")
	assert.True(t, isDatabaseBindingError(err), err)
	_, err = bind("datatug:///missing")
	assert.ErrorContains(t, err, "missing")
	binding, err := bind("datatug:///main?env=local")
	require.NoError(t, err)
	assert.Nil(t, binding.ref, "an unregistered project has no reference")
	assert.Equal(t, "main", binding.catalog)
	// A target in another project resolves through the registry.
	_, err = bind("datatug://elsewhere/main")
	assert.Error(t, err)
}

func TestSavedTextQueryNamingAnUnknownProject(t *testing.T) {
	dir := demoForTest(t)
	writeTextQuery(t, dir, "elsewhere", "databases (\n  geo = 'datatug://no-such-project/geo'\n)\nfrom geo::countries as c\nselect c.code\n")
	_, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--query", "elsewhere", "--as", "agent", "--role", "agent")
	assert.Equal(t, exitCodeNotFound, code, stderr)
}

func TestInstallBindingLabels(t *testing.T) {
	session := secureread.Session{}
	installBindingLabels(&session, map[string]databaseBinding{"raw": {url: "sqlite:///x.db"}}, io.Discard)
	ctx := context.Background()
	assert.NoError(t, session.FederatedColumns(ctx, secureread.FederatedLeaf{Database: "raw"}))
	assert.ErrorContains(t, session.FederatedColumns(ctx, secureread.FederatedLeaf{Database: "other"}), `database "other" is not configured`)
}

func TestBindingSources(t *testing.T) {
	path := countriesDB(t)
	ref := dbcopy.DatatugRef{Project: "p", Env: "local", DB: "geo"}
	sources := bindingSources(map[string]databaseBinding{
		"geo":    {url: api.LocalSQLiteSourceURL(path), ref: &ref},
		"raw":    {url: api.LocalSQLiteSourceURL(path)},
		"bad":    {url: "nope://x"},
		"closed": {url: api.LocalSQLiteSourceURL(filepath.Join(t.TempDir(), "missing", "x.sqlite"))},
	})
	ctx := context.Background()
	db, driver, release, err := sources.open(ctx, "geo")
	require.NoError(t, err)
	assert.NotNil(t, db)
	assert.Equal(t, "sqlite", driver)
	release()
	_, _, _, err = sources.open(ctx, "none")
	assert.ErrorContains(t, err, "not configured")
	_, _, _, err = sources.open(ctx, "bad")
	assert.Error(t, err)
	restoreVar(t, &openBackend)
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, errors.New("closed") }
	_, driver, _, err = sources.open(ctx, "closed")
	assert.ErrorContains(t, err, "closed")
	assert.Equal(t, "sqlite", driver)
	assert.Equal(t, "datatug://p/geo?env=local", *sources.ref("geo"))
	assert.Nil(t, sources.ref("raw"), "a connection string is never a reference")
	_, ok := sources.scanned("raw", "", "countries")
	assert.False(t, ok)
}

func TestRootBindingDB(t *testing.T) {
	ref := dbcopy.DatatugRef{Project: "p", Env: "local", DB: "chinook"}
	text := `{"from":{"database":"sales","name":"Invoice"}}`
	db := rootBindingDB(text, map[string]databaseBinding{"sales": {url: "sqlite:///x.db", ref: &ref}})
	require.NotNil(t, db.Ref)
	assert.Equal(t, "datatug://p/chinook?env=local", *db.Ref)
	assert.Equal(t, "sqlite", db.Driver)
	db = rootBindingDB(text, map[string]databaseBinding{"sales": {url: "sqlite:///x.db"}})
	assert.Nil(t, db.Ref)
	assert.Equal(t, "sqlite", db.Driver)
	assert.Empty(t, rootBindingDB(text, nil).Driver)
	assert.Empty(t, rootBindingDB("from: [", nil).Driver)
}

// The ad-hoc result files record a connection-string participant with no
// reference, and the project source by its reference.
func TestQueryRunCrossDatabaseResultFiles(t *testing.T) {
	demoForTest(t)
	geo := countriesDB(t)
	file := writeTemp(t, "q.tugql", invoiceCountries)
	_, stderr, code := runCLI(t, "", "query", "run", "-f", file, "--db", "geo="+api.LocalSQLiteSourceURL(geo), "--no-policies", "--result-id", "adhoc-x")
	require.Equal(t, 0, code, stderr)
	meta := readResultMeta(t, "adhoc-x")
	require.NotNil(t, meta.DB.Ref)
	assert.Equal(t, "datatug://datatug-demo/chinook?env=local", *meta.DB.Ref)
	require.Len(t, meta.Participants, 2)
	assert.NotNil(t, meta.Participants[0].DB)
	assert.Nil(t, meta.Participants[1].DB, "a connection-string source has no reference")
	raw, err := os.ReadFile(filepath.Join(resultDir(t, "adhoc-x"), "meta.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), geo)
	assert.NotContains(t, string(raw), "sqlite://")
}

func TestFederatedAdHocWriteFailure(t *testing.T) {
	demoForTest(t)
	geo := countriesDB(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(failingWriter{})
	cmd.SetErr(io.Discard)
	document, err := loadAdHocQuery(queryOptions{file: writeTemp(t, "q.tugql", invoiceCountries)}, nil)
	require.NoError(t, err)
	err = runFederatedAdHocQuery(cmd, queryOptions{noPolicies: true, format: "json", dbAliases: map[string]string{"geo": api.LocalSQLiteSourceURL(geo)}}, document)
	var coder ExitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, 1, coder.ExitCode())
}

// A .tugql file that holds a YAML document keeps being read as YAML.
func TestTugQLFileHoldingYAMLStaysYAML(t *testing.T) {
	yaml := "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\n"
	assert.False(t, isTugQLText("report.tugql", []byte(yaml)))
	assert.True(t, isTugQLText("report.TUGQL", []byte("from Invoice\nselect InvoiceId\n")))
	assert.True(t, isTugQLText("report.tugql", []byte("from Invoice select")), "a .tugql file reports its own text diagnostics")
	assert.False(t, isTugQLText("-", []byte("not a query at all (")))
	assert.False(t, isTugQLText("report.yaml", []byte("from Invoice\nselect InvoiceId\n")))
	document, err := loadAdHocQuery(queryOptions{file: writeTemp(t, "y.tugql", yaml)}, nil)
	require.NoError(t, err)
	assert.Equal(t, yaml, document.text)
	assert.Empty(t, document.databases)
	assert.True(t, strings.Contains(document.text, "Invoice"))
}

func TestUnqualifiedSourceWalksJoins(t *testing.T) {
	invoice := dal.NewDatabaseCollectionRef("sales", "", "Invoice", "i")
	countries := dal.NewCollectionRef("countries", "c", nil)
	// A join with no nested from: its recordset is the joined source.
	joined := dal.From(&invoice).Join(dal.NewJoinedSource(&countries, dal.JoinInner))
	assert.Equal(t, "countries", unqualifiedSource(joined))
	assert.Empty(t, unqualifiedSource(dal.From(&invoice)))
	assert.Equal(t, "countries", unqualifiedSource(dal.From(&countries)), "a pointer base")
	document, err := loadAdHocQuery(queryOptions{file: writeTemp(t, "j.tugql", "databases (\n  sales\n)\nfrom sales::Invoice as i\njoin countries as c\n  on i.BillingCountry = c.name\nselect i.InvoiceId\n")}, nil)
	require.NoError(t, err)
	assert.Equal(t, "countries", unqualifiedSource(document.query.(dal.StructuredQuery).From()))
}
