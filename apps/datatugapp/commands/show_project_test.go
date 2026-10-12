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

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runShowCommand runs `datatug show` as the root command dispatches it, and returns what it wrote
// to its two streams.
func runShowCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := DatatugCommand()
	var out, errOut bytes.Buffer
	root.SetArgs(append([]string{"show"}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// showExitCodeOf is the process exit code of an error, as main.go resolves it: the code the error
// carries, 1 for any other error, and 0 for none.
func showExitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var coder ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}

// scannedJourneyProject scans the SQLite database of the journey into a new project folder named
// shop-project, and returns the folder.
func scannedJourneyProject(t *testing.T) string {
	t.Helper()
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.NoError(t, err)
	return projectDir
}

const showJourneySQLite = `Project shop-project
Environment local
  Source shop (sqlite3)
    Schema main
      Table Customer
        CustomerId INTEGER pk
        FirstName TEXT
        LastName TEXT
      Table order_line
        order_id INTEGER pk 2
        line_no INTEGER pk 1
        customer_id INTEGER
        sku TEXT
        qty INTEGER
      View customer_names
        CustomerId INTEGER
        full_name -
`

// What a scan wrote is what `show -d` lists: the project, each environment, each source with
// its driver, each schema, each table and view with its columns, their types and key positions.
func TestShowListsWhatAScanWrote(t *testing.T) {
	projectDir := scannedJourneyProject(t)

	stdout, stderr, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, showJourneySQLite, stdout)
	assert.Empty(t, stderr)
	for _, line := range strings.Split(stdout, "\n") {
		assert.LessOrEqual(t, len(line), 80, "the output reads in a terminal of 80 columns")
	}
	assert.NotContains(t, stdout, "\t", "plain text, no tabs")

	again, _, err := runShowCommand(t, "--directory", projectDir)
	require.NoError(t, err)
	assert.Equal(t, stdout, again, "two runs over one project are byte-identical, whichever spelling of the flag")
}

// --format json is the same document as one JSON value.
func TestShowFormatJSON(t *testing.T) {
	projectDir := scannedJourneyProject(t)

	stdout, _, err := runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &got), stdout)
	want := map[string]any{
		"project": "shop-project",
		"access":  "private", // a scan writes the access of the project file
		"environments": []any{map[string]any{
			"id": "local",
			"sources": []any{map[string]any{
				"id":     "shop",
				"driver": "sqlite3",
				"schemas": []any{map[string]any{
					"name": "main",
					"tables": []any{
						map[string]any{"name": "Customer", "columns": []any{
							map[string]any{"name": "CustomerId", "type": "INTEGER", "primaryKeyPosition": float64(1)},
							map[string]any{"name": "FirstName", "type": "TEXT"},
							map[string]any{"name": "LastName", "type": "TEXT"},
						}},
						map[string]any{"name": "order_line", "columns": []any{
							map[string]any{"name": "order_id", "type": "INTEGER", "primaryKeyPosition": float64(2)},
							map[string]any{"name": "line_no", "type": "INTEGER", "primaryKeyPosition": float64(1)},
							map[string]any{"name": "customer_id", "type": "INTEGER"},
							map[string]any{"name": "sku", "type": "TEXT"},
							map[string]any{"name": "qty", "type": "INTEGER"},
						}},
					},
					"views": []any{
						map[string]any{"name": "customer_names", "columns": []any{
							map[string]any{"name": "CustomerId", "type": "INTEGER"},
							map[string]any{"name": "full_name"},
						}},
					},
				}},
			}},
		}},
	}
	assert.Equal(t, want, got)

	_, _, err = runShowCommand(t, "-d", projectDir, "--format", "yaml")
	assert.Equal(t, 2, showExitCodeOf(t, err), "a format the command does not print is an invalid argument")
	assert.ErrorContains(t, err, `"yaml"`)
}

// A PostgreSQL source is shown by the name of the variable that holds its URL, and by nothing of
// the URL; the variable need not be set to list the project.
func TestShowNamesTheVariableOfAPostgresSource(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
	_, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
	require.NoError(t, err)

	want := `Project shop-project
Environment local
  Source shop (postgres, URL in $DATATUG_JOURNEY_PG_URL)
    Schema public
      Table Customer
        CustomerId int pk
        FirstName string
        LastName string
      Table order_line
        order_id int pk 2
        line_no int pk 1
        customer_id int
        sku string
        qty int
      View customer_names
        CustomerId int
        full_name string
    Schema sales
      Table Invoice
        InvoiceId int pk
        Total decimal
      View OpenInvoices
        InvoiceId int
`
	for _, args := range [][]string{{"-d", projectDir}, {"-d", projectDir, "--format", "json"}} {
		stdout, stderr, err := runShowCommand(t, args...)
		require.NoError(t, err)
		for _, part := range []string{journeyPgSecret, journeyPgUser, journeyPgHost, journeyPgPort, "sslmode", "postgres://"} {
			assert.NotContains(t, stdout+stderr, part, "show never prints a part of the connection URL")
		}
		if len(args) == 2 {
			assert.Equal(t, want, stdout)
		} else {
			assert.Contains(t, stdout, `"dsnEnv": "`+journeyPgVar+`"`)
		}
	}

	// The variable is not set where the project is only read.
	old, wasSet := os.LookupEnv(journeyPgVar)
	require.NoError(t, os.Unsetenv(journeyPgVar))
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(journeyPgVar, old)
		}
	})
	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, want, stdout)
}

// The source of a PostgreSQL catalog is shown only when its descriptor can be read and names a
// variable the project may name: nothing else is printed in its place.
func TestShowRefusesAPostgresSourceWithAnUnusableDescriptor(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
	_, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
	require.NoError(t, err)
	descriptor := filepath.Join(projectDir, "connections", "local", "shop.json")

	require.NoError(t, os.WriteFile(descriptor, []byte(`{"dsnEnv":"SOMETHING_ELSE"}`), 0o644))
	stdout, _, err := runShowCommand(t, "-d", projectDir)
	assert.Equal(t, 1, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, "not allowed")
	assert.Empty(t, stdout)

	require.NoError(t, os.Remove(descriptor))
	_, _, err = runShowCommand(t, "-d", projectDir)
	assert.Equal(t, 1, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, `source "shop"`)

	catalog := filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json")
	require.NoError(t, os.WriteFile(catalog, []byte(`{"id":"shop","driver":"postgres","path":"../outside.json","dbModel":"shop"}`), 0o644))
	_, _, err = runShowCommand(t, "-d", projectDir)
	assert.Equal(t, 1, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, "connection descriptor")
}

// A project with no scanned database says so, in one sentence, and exits 0.
func TestShowAProjectWithNoCatalog(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "datatug-project.json"), []byte(`{"id":"empty-one"}`), 0o644))

	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, "Project empty-one\nNo database has been scanned into this project yet: scan one with datatug scan.\n", stdout)

	stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"project":"empty-one","environments":[]}`, stdout)
}

// A folder that is not a project is one sentence that names the folder and the command that
// makes a project, and the exit code of a missing project.
func TestShowAFolderThatIsNotAProject(t *testing.T) {
	notProject := t.TempDir()
	for _, folder := range []string{notProject, filepath.Join(notProject, "does-not-exist")} {
		stdout, _, err := runShowCommand(t, "-d", folder)
		assert.Equal(t, 3, showExitCodeOf(t, err), folder)
		assert.Empty(t, stdout)
		require.Error(t, err)
		assert.Equal(t, `"`+folder+`" is not a DataTug project: make one with datatug scan -d "`+folder+`" -D sqlite3 --path <database file> --db <name> --env <environment>`, err.Error())
		assert.NotContains(t, err.Error(), "\n", "one sentence")
	}
}

// With neither --project nor --directory the folder it runs in is the project, as the
// project's own readme says; both together are refused.
func TestShowWithoutAFolderReadsTheCurrentOne(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	t.Chdir(projectDir)
	stdout, _, err := runShowCommand(t)
	require.NoError(t, err)
	assert.Equal(t, showJourneySQLite, stdout)

	t.Chdir(t.TempDir())
	_, _, err = runShowCommand(t)
	assert.Equal(t, 3, showExitCodeOf(t, err), "the current folder is not a project")

	_, _, err = runShowCommand(t, "-p", "shop-project", "-d", projectDir)
	assert.Equal(t, 2, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, "--project")
	assert.ErrorContains(t, err, "--directory")
}

// A project registered by name is shown as its folder is, and one that is not registered is not found.
func TestShowByRegisteredName(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // a person with no settings file: nothing is registered
	projectDir := scannedJourneyProject(t)
	_, _, err := runShowCommand(t, "-p", "no-such-project-registered")
	assert.Equal(t, 3, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, `"no-such-project-registered"`)

	registerProjectForTest(t, "shop-project", projectDir)
	_, _, err = runShowCommand(t, "-p", "another-project")
	assert.Equal(t, 3, showExitCodeOf(t, err), "a name that is not among the registered ones")
	stdout, _, err := runShowCommand(t, "-p", "Shop-Project")
	require.NoError(t, err)
	assert.Equal(t, showJourneySQLite, stdout)
}

// What the files of a scanned project can be, and is not, is a failure that names the source and
// says nothing the file system said.
func TestShowFailsWhenAStoredFileCannotBeRead(t *testing.T) {
	t.Run("a catalog file that is not JSON", func(t *testing.T) {
		projectDir := scannedJourneyProject(t)
		catalog := filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json")
		require.NoError(t, os.WriteFile(catalog, []byte("not json"), 0o644))
		stdout, _, err := runShowCommand(t, "-d", projectDir)
		assert.Equal(t, 1, showExitCodeOf(t, err))
		assert.EqualError(t, err, `environment "local": its catalogs cannot be read`, "no path of the project in the answer")
		assert.Empty(t, stdout)
	})
	t.Run("a columns file that is not JSON", func(t *testing.T) {
		projectDir := scannedJourneyProject(t)
		columns := filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "Customer", "main.Customer.columns.json")
		require.NoError(t, os.WriteFile(columns, []byte("not json"), 0o644))
		stdout, _, err := runShowCommand(t, "-d", projectDir)
		assert.Equal(t, 1, showExitCodeOf(t, err))
		assert.ErrorContains(t, err, `catalog "shop" in environment "local" could not be read`)
		assert.Empty(t, stdout)
	})
	t.Run("a project at an address", func(t *testing.T) {
		_, _, err := runShowCommand(t, "-d", "https://example.com/acme/repo")
		assert.Equal(t, 2, showExitCodeOf(t, err), "show reads a folder of this machine")
		assert.ErrorContains(t, err, "https://example.com/acme/repo")
	})
}

// A write that fails is the failure of the command.
func TestShowFailsWhenTheOutputCannotBeWritten(t *testing.T) {
	doc := &showDocument{Project: "p", Environments: []showEnvironment{{ID: "e"}}}
	assert.ErrorIs(t, writeShowText(failingWriter{}, doc), errWriteFailed)
	assert.ErrorIs(t, writeShowJSON(failingWriter{}, doc), errWriteFailed)
}

var errWriteFailed = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

// registerProjectForTest lists the folder in the settings of the user (a folder of the test,
// since every test binary has a home of its own) under the id.
func registerProjectForTest(t *testing.T, id, folder string) {
	t.Helper()
	require.NoError(t, dtconfig.AddProjectToSettings(dtconfig.ProjectRef{ID: id, Path: folder}))
}

// --dir is another spelling of --directory, as the spec of the command names it.
func TestShowDirIsTheFlagDirectory(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	stdout, _, err := runShowCommand(t, "--dir", projectDir)
	require.NoError(t, err)
	assert.Equal(t, showJourneySQLite, stdout)
}

// Environments, and the sources of an environment, are listed in the order of their IDs, whatever
// order they were scanned in.
func TestShowListsEnvironmentsAndSourcesInOrder(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "company")
	shopPath, crmPath := filepath.Join(t.TempDir(), "shop.db"), filepath.Join(t.TempDir(), "crm.db")
	writeJourneyDB(t, shopPath)
	writeCRMDB(t, crmPath)
	for _, scan := range [][]string{
		{"--path", shopPath, "--db", "shop", "--env", "local"},
		{"--path", crmPath, "--db", "crm", "--env", "local"},
		{"--path", crmPath, "--db", "crm", "--env", "dev"},
	} {
		_, err := runScanCommand(t, append([]string{"-d", projectDir, "-D", "sqlite3"}, scan...)...)
		require.NoError(t, err)
	}
	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	var headings []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "Environment ") || strings.HasPrefix(strings.TrimSpace(line), "Source ") {
			headings = append(headings, line)
		}
	}
	assert.Equal(t, []string{
		"Environment dev",
		"  Source crm (sqlite3)",
		"Environment local",
		"  Source crm (sqlite3)",
		"  Source shop (sqlite3)",
	}, headings)
}

// What the environment file lists is not what is listed: an environment whose folder of catalogs is gone has
// no source, whatever its file says.
func TestShowListsNoSourceForAnEnvironmentWithoutCatalogs(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	envFile := filepath.Join(projectDir, "environments", "local", "local.env.json")
	require.NoError(t, os.WriteFile(envFile, []byte(`{"id":"local","dbServers":[null,{"driver":"sqlite3","catalogs":["shop"]}]}`), 0o644))
	require.NoError(t, os.RemoveAll(filepath.Join(projectDir, "environments", "local", "catalogs")))
	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, "Project shop-project\nEnvironment local\nNo database has been scanned into this project yet: scan one with datatug scan.\n", stdout)
	// In JSON the environment is there and has no source; the list of environments is empty only for a project that has none.
	stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"project":"shop-project","access":"private","environments":[{"id":"local","sources":[]}]}`, stdout)
}

// A source that was scanned and has nothing to list says so on one line, and in JSON, and is not the line
// of a source that was never scanned: the model of its catalog has no file (its folder was removed), as it
// has none for a database with no table.
func TestShowSaysSoForAScannedSourceWithNoTableOrView(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	require.NoError(t, os.RemoveAll(filepath.Join(projectDir, "dbmodels")))
	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, "Project shop-project\nEnvironment local\n  Source shop (sqlite3)\n    no tables or views\n", stdout)
	stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"project":"shop-project","access":"private","environments":[{"id":"local","sources":[{"id":"shop","driver":"sqlite3","empty":true,"schemas":[]}]}]}`, stdout)

	// A source that is not scanned is said to be that, and not to be empty; a source with tables is neither.
	stdout, _, err = runShowCommand(t, "-d", demoShapedProject(t))
	require.NoError(t, err)
	assert.Contains(t, stdout, "not scanned\n")
	assert.NotContains(t, stdout, "no tables or views")
	stdout, _, err = runShowCommand(t, "-d", scannedJourneyProject(t))
	require.NoError(t, err)
	assert.NotContains(t, stdout, "no tables or views")
}

// A name with a space or a double quote is printed quoted, so that on its line it is told from the type and
// the key that follow it; a name of one word is as it is. JSON is exact.
func TestShowQuotesNamesWithASpaceOrAQuote(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE "Order Details" ("id INTEGER pk" INTEGER PRIMARY KEY, "say ""hi""" TEXT, plain TEXT)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	projectDir := filepath.Join(t.TempDir(), "spaces-project")
	_, err = runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.NoError(t, err)

	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, `Project spaces-project
Environment local
  Source shop (sqlite3)
    Schema main
      Table "Order Details"
        "id INTEGER pk" INTEGER pk
        "say \"hi\"" TEXT
        plain TEXT
`, stdout)
	stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"name": "Order Details"`)
	assert.Contains(t, stdout, `"name": "id INTEGER pk"`)
	assert.Contains(t, stdout, `"name": "say \"hi\""`)
}

// A project file that cannot be read is a failure of the command, and the settings file of the user
// that cannot be read is one too, when a project is asked for by name.
func TestShowFailsWhenTheProjectCannotBeLoaded(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "datatug-project.json"), []byte("not json"), 0o644))
	_, _, err := runShowCommand(t, "-d", projectDir)
	assert.Equal(t, 1, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, "failed to load project")

	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".datatug.yaml"), 0o755))
	_, _, err = runShowCommand(t, "-p", "anything")
	assert.Equal(t, 1, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, "settings")
}

// A command that is run with no context, as a test of another package may, still runs.
func TestShowActionWithoutAContext(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	cmd := showCommandArgs()
	require.NoError(t, cmd.Flags().Set("directory", projectDir))
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.Nil(t, cmd.Context())
	require.NoError(t, showCommandAction(cmd, nil))
	assert.Equal(t, showJourneySQLite, out.String())
}

// writeProjectFiles writes the files (path relative to the folder, content) into the folder.
func writeProjectFiles(t *testing.T, folder string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(folder, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
}

// demoShapedProject is a project of the shape of datatug-demo-projects/demo-project-1, which `show` must
// list as chat does: environments whose file lists a catalog that has no folder (QA), catalogs that were
// registered and never scanned (no dbModel), an inGitDB catalog, one catalog folder the environment file
// does not list, and one source that was scanned.
func demoShapedProject(t *testing.T) string {
	t.Helper()
	folder := t.TempDir()
	writeProjectFiles(t, folder, map[string]string{
		"datatug-project.json": `{"id":"demo"}`,
		// the environment files list catalogs: QA lists one that has no folder, local does not list "unlisted"
		"environments/QA/QA.env.json":                                     `{"id":"QA","dbServers":[{"driver":"sqlite3","catalogs":["chinook-local"]}]}`,
		"environments/local/local.env.json":                               `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["orders","chinook-local","geo"]}]}`,
		"environments/local/catalogs/orders/orders.db.json":               `{"driver":"sqlite3","path":"fixtures/orders.sqlite"}`,
		"environments/local/catalogs/geo/geo.db.json":                     `{"driver":"ingitdb","path":"data/geo"}`,
		"environments/local/catalogs/chinook-local/chinook-local.db.json": `{"driver":"sqlite3","path":"x.sqlite","dbModel":"chinook"}`,
		"environments/local/catalogs/unlisted/unlisted.db.json":           `{"driver":"sqlite3","path":"u.sqlite","dbModel":"chinook"}`,
		"dbmodels/chinook/main/tables/Album/main.Album.columns.json":      `{"columns":[{"name":"AlbumId","dbType":"INTEGER","pkPosition":1},{"name":"Title","dbType":"TEXT"}]}`,
	})
	return folder
}

// What chat lists, show lists: the sources are the catalogs the environment holds (not the catalogs its
// file lists), and a source that was never scanned is listed as a source that is not scanned, not a failure.
func TestShowListsTheSourcesChatListsOnAProjectOfTheShapeOfTheDemo(t *testing.T) {
	projectDir := demoShapedProject(t)
	want := `Project demo
Environment QA
Environment local
  Source chinook-local (sqlite3)
    Schema main
      Table Album
        AlbumId INTEGER pk
        Title TEXT
  Source geo (ingitdb)
    not scanned
  Source orders (sqlite3)
    not scanned
  Source unlisted (sqlite3)
    Schema main
      Table Album
        AlbumId INTEGER pk
        Title TEXT
`
	stdout, stderr, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, want, stdout)
	assert.Empty(t, stderr)

	stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	var doc showDocument
	require.NoError(t, json.Unmarshal([]byte(stdout), &doc), stdout)
	require.Len(t, doc.Environments, 2)
	require.Len(t, doc.Environments[1].Sources, 4)
	orders := doc.Environments[1].Sources[2]
	assert.Equal(t, showSource{ID: "orders", Driver: "sqlite3", NotScanned: true, Schemas: []showSchema{}}, orders)
	assert.Contains(t, stdout, `"notScanned": true`)
	assert.Contains(t, stdout, `"schemas": []`)
}

// A name that holds a line break or a terminal escape sequence is printed quoted, so that it forges no line
// of the output and acts on no terminal; the JSON of the same project is the encoder's, and holds the names
// as they are.
func TestShowQuotesNamesThatAreNotPrintable(t *testing.T) {
	forged := "a\nEnvironment prod\n  Source billing (sqlite3)"
	escape := "b\x1b[2Jc"
	dbPath := filepath.Join(t.TempDir(), "billing.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE t ("` + forged + `" TEXT, "` + escape + `" TEXT, " padded " TEXT)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	projectDir := filepath.Join(t.TempDir(), "billing-project")
	_, err = runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "billing", "--env", "local")
	require.NoError(t, err)

	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, `Project billing-project
Environment local
  Source billing (sqlite3)
    Schema main
      Table t
        "a\nEnvironment prod\n  Source billing (sqlite3)" TEXT
        "b\x1b[2Jc" TEXT
        " padded " TEXT
`, stdout)
	for _, b := range []byte(strings.TrimSuffix(stdout, "\n")) {
		assert.False(t, b < 0x20 && b != '\n', "no control byte in the output: %q", stdout)
	}
	headings := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "Environment ") {
			headings++
		}
	}
	assert.Equal(t, 1, headings, "no line was forged")

	stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"name": "a\nEnvironment prod\n  Source billing (sqlite3)"`)
}

func TestShowText(t *testing.T) {
	for value, want := range map[string]string{
		"Customer":      "Customer",
		"order line":    `"order line"`, // a space: the name could not be told from the type and the key after it
		"Order Details": `"Order Details"`,
		"id INTEGER pk": `"id INTEGER pk"`,
		`say "hi"`:      `"say \"hi\""`,
		`a"b`:           `"a\"b"`,
		"double  space": `"double  space"`,
		"café":          "café",
		"":              `""`,
		" lead":         `" lead"`,
		"trail ":        `"trail "`,
		"tab\there":     `"tab\there"`,
		"line\u2028two": `"line\u2028two"`,
		"bad\xffutf8":   `"bad\xffutf8"`,
	} {
		assert.Equal(t, want, showText(value), "%q", value)
	}
}

// The driver is printed only when it is a plain name: a catalog file can hold anything, a URL with a password
// included.
func TestShowDoesNotPrintADriverThatIsNotAPlainName(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	catalog := filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json")
	for _, driver := range []string{"postgres://u:hunter2@db.internal.example/shop", "sqlite3\nEnvironment prod", ""} {
		content, err := json.Marshal(map[string]string{"id": "shop", "driver": driver, "path": "shop.db", "dbModel": "shop"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(catalog, content, 0o644))
		for _, args := range [][]string{{"-d", projectDir}, {"-d", projectDir, "--format", "json"}} {
			stdout, stderr, err := runShowCommand(t, args...)
			require.NoError(t, err, driver)
			if len(args) == 2 { // the text: the label is printed as it is, with no quotes
				assert.Contains(t, stdout, "\n  Source shop (unknown driver)\n")
				assert.NotContains(t, stdout, `"unknown driver"`)
			} else {
				assert.Contains(t, stdout, "unknown driver")
			}
			for _, part := range []string{"hunter2", "db.internal", "postgres://", "prod"} {
				assert.NotContains(t, stdout+stderr, part)
			}
		}
	}
}

// The first line names the project by its file; a file that holds no ID leaves the folder's own name
// (and not "." or the path as it was given).
func TestShowNamesAProjectWithNoIDByItsFolder(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "orders-project")
	writeProjectFiles(t, folder, map[string]string{"datatug-project.json": `{}`})
	stdout, _, err := runShowCommand(t, "-d", folder)
	require.NoError(t, err)
	assert.Equal(t, "Project orders-project\nNo database has been scanned into this project yet: scan one with datatug scan.\n", stdout)

	t.Chdir(folder)
	stdout, _, err = runShowCommand(t)
	require.NoError(t, err)
	assert.Equal(t, "Project orders-project\nNo database has been scanned into this project yet: scan one with datatug scan.\n", stdout)

	// If the folder cannot be made absolute, the folder as given is named.
	old := absPath
	t.Cleanup(func() { absPath = old })
	absPath = func(string) (string, error) { return "", errors.New("no working directory") }
	assert.Equal(t, "named-as-given", showProjectID(&datatug.Project{}, "x/named-as-given"))
}

// A path that is a file is not a folder with a project in it: the sentence of a folder that is not a project,
// not the system's text.
func TestShowAFileIsNotAProject(t *testing.T) {
	file := filepath.Join(t.TempDir(), "shop.db")
	require.NoError(t, os.WriteFile(file, []byte("SQLite format 3"), 0o644))
	stdout, _, err := runShowCommand(t, "-d", file)
	assert.Equal(t, 3, showExitCodeOf(t, err))
	require.Error(t, err)
	assert.Equal(t, notAProjectSentence(file), err.Error())
	assert.NotContains(t, err.Error(), "not a directory")
	assert.Empty(t, stdout)
}

// A project registered at an address is not a folder of this machine: it exits 2, as the address given as the
// folder does.
func TestShowByRegisteredNameAtAnAddress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, dtconfig.AddProjectToSettings(dtconfig.ProjectRef{ID: "remote-one", Url: "https://example.com/acme/repo"}))
	stdout, _, err := runShowCommand(t, "-p", "remote-one")
	assert.Equal(t, 2, showExitCodeOf(t, err))
	assert.ErrorContains(t, err, "https://example.com/acme/repo")
	assert.Empty(t, stdout)
}

// catalogsStore is a project store whose list of the catalogs of an environment is the one given.
type catalogsStore struct {
	datatug.ProjectStore
	catalogs datatug.DbCatalogs
	err      error
}

func (s catalogsStore) LoadEnvDbCatalogs(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	return s.catalogs, s.err
}

// A list of catalogs with a hole in it lists the others; a list that cannot be had is the failure of the
// environment, said without the store's text.
func TestReadShowDocumentSkipsAHoleAndNamesTheEnvironmentOfAFailure(t *testing.T) {
	project := &datatug.Project{ID: "p", Environments: datatug.Environments{{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "local"}}}}}
	catalog := &datatug.DbCatalog{}
	catalog.ID = "orders"
	catalog.Driver = "sqlite3"
	doc, err := readShowDocument(context.Background(), catalogsStore{catalogs: datatug.DbCatalogs{nil, catalog}}, project, t.TempDir())
	require.NoError(t, err)
	require.Len(t, doc.Environments, 1)
	assert.Equal(t, []showSource{{ID: "orders", Driver: "sqlite3", NotScanned: true, Schemas: []showSchema{}}}, doc.Environments[0].Sources)

	_, err = readShowDocument(context.Background(), catalogsStore{err: errors.New("open /secret/path: denied")}, project, t.TempDir())
	assert.EqualError(t, err, `environment "local": its catalogs cannot be read`)
}

// writeProjectFile replaces the project file of a project folder.
func writeProjectFile(t *testing.T, folder, content string) {
	t.Helper()
	writeProjectFiles(t, folder, map[string]string{"datatug-project.json": content})
}

// The title and the access of the project file are in the JSON beside the project, each only when the file holds
// it, and the text never carries them.
func TestShowJSONCarriesTitleAndAccess(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	text, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)

	writeProjectFile(t, projectDir, `{"id":"shop-project","title":"Shop \"data\"","access":"public"}`)
	stdout, _, err := runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, "{\n  \"project\": \"shop-project\",\n  \"title\": \"Shop \\\"data\\\"\",\n  \"access\": \"public\",\n  \"environments\""), stdout)
	again, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, text, again, "the text is what it was")

	writeProjectFile(t, projectDir, `{"id":"shop-project"}`)
	stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, "{\n  \"project\": \"shop-project\",\n  \"environments\""), stdout)
	assert.NotContains(t, stdout, `"title"`)
	assert.NotContains(t, stdout, `"access"`)
	again, _, err = runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, text, again)
}

const showJourneySQLiteTables = `Project shop-project
Environment local
  Source shop (sqlite3)
    Schema main
      Table Customer
      Table order_line
      View customer_names
`

const showJourneySQLiteSources = `Project shop-project
Environment local
  Source shop (sqlite3)
`

// --depth stops the listing: tables list each table and view with no column, sources list nothing below a source;
// columns and no --depth are the same bytes, and two runs at a depth are the same bytes.
func TestShowDepthTrimsTheText(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	for depth, want := range map[string]string{
		"columns": showJourneySQLite,
		"tables":  showJourneySQLiteTables,
		"sources": showJourneySQLiteSources,
	} {
		stdout, stderr, err := runShowCommand(t, "-d", projectDir, "--depth", depth)
		require.NoError(t, err, depth)
		assert.Equal(t, want, stdout, depth)
		assert.Empty(t, stderr)
		again, _, err := runShowCommand(t, "-d", projectDir, "--depth", depth)
		require.NoError(t, err)
		assert.Equal(t, stdout, again, "two runs at depth %s are byte-identical", depth)
	}
	plain, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	explicit, _, err := runShowCommand(t, "-d", projectDir, "--depth", "columns")
	require.NoError(t, err)
	assert.Equal(t, plain, explicit)
}

func TestShowDepthTrimsTheJSON(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	plain, _, err := runShowCommand(t, "-d", projectDir, "--format", "json")
	require.NoError(t, err)
	explicit, _, err := runShowCommand(t, "-d", projectDir, "--format", "json", "--depth", "columns")
	require.NoError(t, err)
	assert.Equal(t, plain, explicit)

	tables, _, err := runShowCommand(t, "-d", projectDir, "--format", "json", "--depth", "tables")
	require.NoError(t, err)
	assert.Equal(t, `{
  "project": "shop-project",
  "access": "private",
  "environments": [
    {
      "id": "local",
      "sources": [
        {
          "id": "shop",
          "driver": "sqlite3",
          "schemas": [
            {
              "name": "main",
              "tables": [
                {
                  "name": "Customer"
                },
                {
                  "name": "order_line"
                }
              ],
              "views": [
                {
                  "name": "customer_names"
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}
`, tables)
	tablesAgain, _, err := runShowCommand(t, "-d", projectDir, "--format", "json", "--depth", "tables")
	require.NoError(t, err)
	assert.Equal(t, tables, tablesAgain)

	sources, _, err := runShowCommand(t, "-d", projectDir, "--format", "json", "--depth", "sources")
	require.NoError(t, err)
	assert.Equal(t, `{
  "project": "shop-project",
  "access": "private",
  "environments": [
    {
      "id": "local",
      "sources": [
        {
          "id": "shop",
          "driver": "sqlite3"
        }
      ]
    }
  ]
}
`, sources)
	sourcesAgain, _, err := runShowCommand(t, "-d", projectDir, "--format", "json", "--depth", "sources")
	require.NoError(t, err)
	assert.Equal(t, sources, sourcesAgain)
}

// notScanned and empty, and their lines, are kept at every depth; a table with no column keeps its empty list at
// the depth columns only.
func TestShowDepthKeepsNotScannedAndEmpty(t *testing.T) {
	projectDir := demoShapedProject(t)
	for _, depth := range []string{"tables", "sources"} {
		stdout, _, err := runShowCommand(t, "-d", projectDir, "--depth", depth)
		require.NoError(t, err)
		assert.Contains(t, stdout, "  Source geo (ingitdb)\n    not scanned\n", depth)
		assert.Contains(t, stdout, "  Source orders (sqlite3)\n    not scanned\n", depth)
		assert.NotContains(t, stdout, "AlbumId")

		stdout, _, err = runShowCommand(t, "-d", projectDir, "--format", "json", "--depth", depth)
		require.NoError(t, err)
		assert.Contains(t, stdout, `"notScanned": true`, depth)
		assert.Equal(t, depth == "tables", strings.Contains(stdout, `"schemas": []`), "schemas is kept at tables and left out at sources: %s", depth)
		assert.NotContains(t, stdout, `"columns"`, depth)
	}

	emptyDir := scannedJourneyProject(t)
	require.NoError(t, os.RemoveAll(filepath.Join(emptyDir, "dbmodels")))
	for _, depth := range []string{"tables", "sources"} {
		stdout, _, err := runShowCommand(t, "-d", emptyDir, "--depth", depth)
		require.NoError(t, err)
		assert.Equal(t, "Project shop-project\nEnvironment local\n  Source shop (sqlite3)\n    no tables or views\n", stdout, depth)
		stdout, _, err = runShowCommand(t, "-d", emptyDir, "--format", "json", "--depth", depth)
		require.NoError(t, err)
		assert.Contains(t, stdout, `"empty": true`, depth)
		assert.Equal(t, depth == "tables", strings.Contains(stdout, `"schemas": []`), depth)
	}

	// At the depth columns a table with no column keeps "columns": [] and a source with no schema keeps "schemas": [].
	noColumns := t.TempDir()
	writeProjectFiles(t, noColumns, map[string]string{
		"datatug-project.json":                         `{"id":"p"}`,
		"environments/local/local.env.json":            `{"id":"local"}`,
		"environments/local/catalogs/c/c.db.json":      `{"driver":"sqlite3","path":"x","dbModel":"m"}`,
		"dbmodels/m/main/tables/T/main.T.columns.json": `{"columns":[]}`,
	})
	stdout, _, err := runShowCommand(t, "-d", noColumns, "--format", "json")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"columns": []`)
	stdout, _, err = runShowCommand(t, "-d", noColumns, "--format", "json", "--depth", "tables")
	require.NoError(t, err)
	assert.NotContains(t, stdout, `"columns"`)
}

func TestShowRefusesAnUnknownDepth(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	for _, depth := range []string{"rows", "", "Tables"} {
		stdout, stderr, err := runShowCommand(t, "-d", projectDir, "--depth", depth)
		assert.Equal(t, 2, showExitCodeOf(t, err), depth)
		assert.ErrorContains(t, err, "unsupported --depth")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
	}
	_, _, err := runShowCommand(t, "-d", t.TempDir(), "--depth", "rows")
	assert.Equal(t, 2, showExitCodeOf(t, err), "the depth is checked before the project is read")
}
