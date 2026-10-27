package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runProjectMetadata(t *testing.T, command *cobra.Command, args ...string) (string, error) {
	t.Helper()
	command.SilenceUsage, command.SilenceErrors = true, true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

// registeredSQLiteSource is a registered project "shop" whose environment "local" lists the SQLite
// database of the journey as the source "shop", added with `db add`.
func registeredSQLiteSource(t *testing.T) string {
	t.Helper()
	addProject(t)
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	_, _, err := runDbAdd(t, "--project", "shop", "--db", dbPath, "--env", "local")
	require.NoError(t, err)
	return dbPath
}

func TestMetadataCommandsOpenADatabaseOfAProjectByReference(t *testing.T) {
	dbPath := registeredSQLiteSource(t)

	byPath, err := runDBSchema(t, "--db", dbPath)
	require.NoError(t, err)
	var direct, listed map[string]any
	require.NoError(t, json.Unmarshal([]byte(byPath), &direct))
	for _, ref := range []string{"datatug://shop/shop", "datatug://shop/shop?env=local", "datatug:///shop"} {
		byProject, err := runProjectMetadata(t, dbSchemaCommand(), "--db", ref)
		require.NoError(t, err, ref)
		require.NoError(t, json.Unmarshal([]byte(byProject), &listed))
		assert.Equal(t, "project", listed["detectedFrom"], ref)
		assert.Equal(t, direct["tables"], listed["tables"], "the same tables as the path, "+ref)
		assert.Equal(t, ref, listed["target"], "the target is the reference: no path")
		assert.NotContains(t, byProject, dbPath)
	}

	table, err := runProjectMetadata(t, dbTableCommand(), "Customer", "--db", "datatug://shop/shop")
	require.NoError(t, err)
	assert.Contains(t, table, `"detectedFrom": "project"`)
	assert.Contains(t, table, `"name": "Customer"`)
	assert.NotContains(t, table, dbPath)
	column, err := runProjectMetadata(t, dbColumnCommand(), "Customer", "FirstName", "--db", "datatug://shop/shop")
	require.NoError(t, err)
	assert.Contains(t, column, `"detectedFrom": "project"`)
	text, err := runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/shop", "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, text, "target: datatug://shop/shop\n")
	assert.Contains(t, text, "main\ttable\tCustomer")
}

func TestQueryRunAndCopyOpenADatabaseOfAProjectByReference(t *testing.T) {
	dbPath := registeredSQLiteSource(t)
	var out, errOut bytes.Buffer
	root := DatatugCommand()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	root.SetArgs([]string{"query", "run", "--db", "datatug://shop/shop", "--from", "Customer", "--no-policies", "--format", "json"})
	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "Ada")
	assert.NotContains(t, out.String()+errOut.String(), dbPath)

	root = DatatugCommand()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	root.SetArgs([]string{"query", "run", "--db", "datatug://nope/shop", "--from", "Customer", "--no-policies"})
	err := root.Execute()
	assert.Equal(t, 3, exitCodeOf(t, err))

	// db copy and db export read the reference too
	target := filepath.Join(t.TempDir(), "copy")
	require.NoError(t, os.MkdirAll(target, 0o755))
	root = DatatugCommand()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	root.SetArgs([]string{"db", "copy", "--from", "datatug://shop/shop", "--to", "ingitdb://" + target})
	require.NoError(t, root.Execute())
	assert.NotEmpty(t, projectFilesOf(t, target), "the tables were copied")
	root = DatatugCommand()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	root.SetArgs([]string{"db", "export", "--from", "datatug://nope/shop", "--to", "ingitdb://" + target})
	assert.Equal(t, 3, exitCodeOf(t, root.Execute()))
	root = DatatugCommand()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	root.SetArgs([]string{"db", "export", "--from", "datatug://shop/shop", "--to", "datatug://nope/x"})
	assert.Equal(t, 3, exitCodeOf(t, root.Execute()))
	root = DatatugCommand()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	root.SetArgs([]string{"db", "copy", "--from", "datatug://shop/shop", "--to", "datatug://nope/x"})
	assert.Equal(t, 3, exitCodeOf(t, root.Execute()))
}

func TestReferencesAreRefusedWithoutEchoingThem(t *testing.T) {
	registeredSQLiteSource(t)
	for _, ref := range []string{"datatug://u:secret@shop/shop", "datatug://shop", "datatug://shop/shop?x=secret", "datatug://shop/a/b"} {
		_, err := runProjectMetadata(t, dbSchemaCommand(), "--db", ref)
		require.Error(t, err, ref)
		assert.Equal(t, 2, exitCodeOf(t, err), ref)
		assert.NotContains(t, err.Error(), "secret")
	}
}

func TestReferencesSayWhatIsNotThere(t *testing.T) {
	registeredSQLiteSource(t)
	for name, ref := range map[string]string{
		"project":     "datatug://nope/shop",
		"environment": "datatug://shop/shop?env=nope",
		"database":    "datatug://shop/nope",
	} {
		_, err := runProjectMetadata(t, dbSchemaCommand(), "--db", ref)
		require.Error(t, err, name)
		assert.Equal(t, 3, exitCodeOf(t, err), name)
	}
	_, err := runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/nope")
	assert.EqualError(t, err, `source "nope" not found in any environment of the project`)
	_, err = runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/shop?env=nope")
	assert.EqualError(t, err, `source "shop" not found in environment "nope"`)
}

func TestReferenceToADatabaseOfSeveralEnvironmentsNeedsEnv(t *testing.T) {
	dbPath := registeredSQLiteSource(t)
	_, _, err := runDbAdd(t, "--project", "shop", "--db", dbPath, "--env", "dev", "--name", "shop")
	require.NoError(t, err)
	_, err = runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/shop")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.EqualError(t, err, `database "shop": the source is listed in several environments (local, dev): add ?env=<ENV> to the reference`)
	_, err = runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/shop?env=dev")
	require.NoError(t, err)
}

func TestReferenceWithoutAProjectName(t *testing.T) {
	registeredSQLiteSource(t) // exactly one registered project
	_, err := runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug:///shop")
	require.NoError(t, err)

	// in a project folder, that project is meant, registered or not
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"here","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`), 0o644))
	t.Chdir(dir)
	_, err = runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug:///shop")
	assert.Equal(t, 3, exitCodeOf(t, err), "the folder is the project, and it lists no such database")

	// elsewhere, with two registered projects, there is none to mean
	t.Chdir(t.TempDir())
	registerProjectForTest(t, "second", t.TempDir())
	_, err = runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug:///shop")
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "the reference names no project")
}

func TestReferenceSaysWhyADatabaseCannotBeOpened(t *testing.T) {
	dir := addProject(t)
	catalogs := filepath.Join(dir, "environments", "local", "catalogs")
	for id, catalog := range map[string]string{
		"mssql": `{"id":"mssql","driver":"firestore","dbModel":"","schemas":[]}`,
		"url":   `{"id":"url","driver":"sqlite3","path":"https://u:secret@h/x.db","dbModel":"","schemas":[]}`,
		"pg":    `{"id":"pg","driver":"postgres","path":"connections/local/pg.json","dbModel":"","schemas":[]}`,
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(catalogs, id), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(catalogs, id, id+".db.json"), []byte(catalog), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "local", "local.env.json"), []byte(`{"id":"local"}`), 0o644))
	_, err := runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/mssql")
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.EqualError(t, err, `database "mssql" cannot be opened: its driver is not one that can be opened (sqlite3, ingitdb, openvaultdb, postgres, mysql, oracle, sqlserver, firestore or couchdb)`)
	for _, id := range []string{"url", "pg"} {
		_, err = runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/"+id)
		assert.Equal(t, 2, exitCodeOf(t, err), id)
		assert.NotContains(t, err.Error(), "secret")
		assert.ErrorContains(t, err, "cannot be resolved", id)
	}
}

func TestReferenceToAPostgresDatabaseIsReadFromItsVariable(t *testing.T) {
	addProject(t)
	withLookupEnv(t, map[string]string{})
	_, _, err := runDbAdd(t, "--project", "shop", "--db", "env:DATATUG_SHOP_PG", "--name", "pg", "--env", "prod")
	require.NoError(t, err)

	// the variable is not set: opening says so, and shows no URL
	t.Setenv("DATATUG_SHOP_PG", "")
	_, err = runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/pg")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "DATATUG_SHOP_PG")

	t.Setenv("DATATUG_SHOP_PG", "postgres://alice:hunter2@db.example.com:5432/shop?sslmode=require")
	var opened dbcopy.BackendRef
	withSchemaSource(t, func(_ context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		opened = ref
		return fakePostgres{schemas: []string{"public"}, collection: map[string][]string{"public": {"users"}}}, nil
	})
	out, err := runProjectMetadata(t, dbSchemaCommand(), "--db", "datatug://shop/pg", "--format", "text")
	require.NoError(t, err)
	assert.Equal(t, "provider: postgres\ntarget: datatug://shop/pg\npublic\ttable\tusers\n", out)
	assert.Equal(t, "env:DATATUG_SHOP_PG", opened.Raw)
	assert.NotContains(t, out, "hunter2")
}

// `show` gives a SQLite source its resolved file as path, and none to a source whose path is a URL,
// which is never shown.
func TestShowPathOfSQLiteSources(t *testing.T) {
	dir := addProject(t)
	inside := filepath.Join(dir, "data", "inside.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(inside), 0o755))
	require.NoError(t, os.WriteFile(inside, []byte("x"), 0o644))
	_, _, err := runDbAdd(t, "--project", "shop", "--db", inside, "--env", "local")
	require.NoError(t, err)
	require.Contains(t, projectFilesOf(t, dir)["environments/local/catalogs/inside/inside.db.json"], `"path": "data/inside.db"`, "stored relative to the project")
	catalog := filepath.Join(dir, "environments", "local", "catalogs", "url")
	require.NoError(t, os.MkdirAll(catalog, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(catalog, "url.db.json"),
		[]byte(`{"id":"url","driver":"sqlite3","path":"https://u:secret@h/x.db","dbModel":"","schemas":[]}`), 0o644))

	out, _, err := runShowCommand(t, "-p", "shop", "--format", "json", "--depth", "sources")
	require.NoError(t, err)
	var doc struct {
		Environments []struct {
			Sources []map[string]any
		}
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	sources := doc.Environments[0].Sources
	require.Len(t, sources, 2)
	assert.Equal(t, "inside", sources[0]["id"])
	assert.Equal(t, inside, sources[0]["path"], "absolute, resolved from the stored relative path")
	assert.Equal(t, "url", sources[1]["id"])
	assert.NotContains(t, sources[1], "path")
	assert.NotContains(t, out, "secret")
}
