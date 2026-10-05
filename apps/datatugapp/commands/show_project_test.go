package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
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

// exitCodeOf is the process exit code of an error, as main.go resolves it: the code the error
// carries, 1 for any other error, and 0 for none.
func exitCodeOf(t *testing.T, err error) int {
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
	assert.Equal(t, 2, exitCodeOf(t, err), "a format the command does not print is an invalid argument")
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
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "not allowed")
	assert.Empty(t, stdout)

	require.NoError(t, os.Remove(descriptor))
	_, _, err = runShowCommand(t, "-d", projectDir)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.ErrorContains(t, err, `source "shop"`)

	catalog := filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json")
	require.NoError(t, os.WriteFile(catalog, []byte(`{"id":"shop","driver":"postgres","path":"../outside.json","dbModel":"shop"}`), 0o644))
	_, _, err = runShowCommand(t, "-d", projectDir)
	assert.Equal(t, 1, exitCodeOf(t, err))
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
		assert.Equal(t, 3, exitCodeOf(t, err), folder)
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
	assert.Equal(t, 3, exitCodeOf(t, err), "the current folder is not a project")

	_, _, err = runShowCommand(t, "-p", "shop-project", "-d", projectDir)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "--project")
	assert.ErrorContains(t, err, "--directory")
}

// A project registered by name is shown as its folder is, and one that is not registered is not found.
func TestShowByRegisteredName(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // a person with no settings file: nothing is registered
	projectDir := scannedJourneyProject(t)
	_, _, err := runShowCommand(t, "-p", "no-such-project-registered")
	assert.Equal(t, 3, exitCodeOf(t, err))
	assert.ErrorContains(t, err, `"no-such-project-registered"`)

	registerProjectForTest(t, "shop-project", projectDir)
	_, _, err = runShowCommand(t, "-p", "another-project")
	assert.Equal(t, 3, exitCodeOf(t, err), "a name that is not among the registered ones")
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
		assert.Equal(t, 1, exitCodeOf(t, err))
		assert.Empty(t, stdout)
	})
	t.Run("a columns file that is not JSON", func(t *testing.T) {
		projectDir := scannedJourneyProject(t)
		columns := filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "Customer", "main.Customer.columns.json")
		require.NoError(t, os.WriteFile(columns, []byte("not json"), 0o644))
		stdout, _, err := runShowCommand(t, "-d", projectDir)
		assert.Equal(t, 1, exitCodeOf(t, err))
		assert.ErrorContains(t, err, `catalog "shop" in environment "local" could not be read`)
		assert.Empty(t, stdout)
	})
	t.Run("a project at an address", func(t *testing.T) {
		_, _, err := runShowCommand(t, "-d", "https://example.com/acme/repo")
		assert.Equal(t, 2, exitCodeOf(t, err), "show reads a folder of this machine")
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

// An environment file that lists a server that is not there lists nothing for it.
func TestShowSkipsAServerThatIsNotThere(t *testing.T) {
	projectDir := scannedJourneyProject(t)
	envFile := filepath.Join(projectDir, "environments", "local", "local.env.json")
	require.NoError(t, os.WriteFile(envFile, []byte(`{"id":"local","dbServers":[null]}`), 0o644))
	stdout, _, err := runShowCommand(t, "-d", projectDir)
	require.NoError(t, err)
	assert.Equal(t, "Project shop-project\nEnvironment local\nNo database has been scanned into this project yet: scan one with datatug scan.\n", stdout)
}

// A project file that cannot be read is a failure of the command, and the settings file of the user
// that cannot be read is one too, when a project is asked for by name.
func TestShowFailsWhenTheProjectCannotBeLoaded(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "datatug-project.json"), []byte("not json"), 0o644))
	_, _, err := runShowCommand(t, "-d", projectDir)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "failed to load project")

	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".datatug.yaml"), 0o755))
	_, _, err = runShowCommand(t, "-p", "anything")
	assert.Equal(t, 1, exitCodeOf(t, err))
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
