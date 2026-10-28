package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCov100iQuerySourceURLFromCatalog(t *testing.T) {
	dir := t.TempDir()
	cat := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "c1"}},
		Driver:      "sqlite3", Path: "db.sqlite",
	}}
	url, err := querySourceURLFromCatalog(cat, dir)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "sqlite://"))

	cat.Path = ""
	_, err = querySourceURLFromCatalog(cat, dir)
	require.Error(t, err)

	cat = datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "c2"}},
		Driver:      "ingitdb", Path: "data",
	}}
	url, err = querySourceURLFromCatalog(cat, dir)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "ingitdb://"))

	cat.Path = ""
	_, err = querySourceURLFromCatalog(cat, dir)
	require.Error(t, err)

	cat.Driver = "unsupported-driver"
	cat.Path = "x"
	_, err = querySourceURLFromCatalog(cat, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

// A saved PostgREST query resolves through the same connection descriptor as a
// PostgreSQL one: the project names the operator's variable, never the API root.
func TestCov100iQuerySourceURLFromCatalogPostgREST(t *testing.T) {
	dir := t.TempDir()
	const variable = "DATATUG_COV_POSTGREST_URL"
	t.Setenv(variable, "postgrest+http://127.0.0.1:3000/rest/v1?schema=app")
	undo, err := api.WritePostgresDescriptor(dir, "local", "items", variable)
	require.NoError(t, err)
	t.Cleanup(func() { _ = undo() })
	cat := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "items"}},
		Driver:      api.DriverPostgREST,
		Path:        "connections/local/items.json",
	}}
	url, err := querySourceURLFromCatalog(cat, dir)
	require.NoError(t, err)
	assert.Equal(t, "env:"+variable, url)
	assert.NotContains(t, url, "127.0.0.1")
}

func TestCov100iSavedQueryIDLongTitle(t *testing.T) {
	id := savedQueryID(strings.Repeat("Ab Cd!", 20))
	// The slug is capped at 48 characters (47 when the cut lands on a dash),
	// followed by "-" and an 8-character uuid prefix.
	const suffix = 1 + 8
	slug := id[:len(id)-suffix]
	assert.Equal(t, "-", id[len(slug):len(slug)+1])
	assert.LessOrEqual(t, len(slug), 48)
	assert.GreaterOrEqual(t, len(slug), 47)
	assert.True(t, strings.HasPrefix(slug, "ab-cd-ab-cd-"), slug)
	assert.False(t, strings.HasSuffix(slug, "-"), slug)
}

func TestCov100iRunTugQLAndHTTPVariableParseErrors(t *testing.T) {
	svc := chatSavedQueries{}
	_, err := svc.RunTugQLWithVariables(context.Background(), "q", map[string]string{"=bad": "x"})
	require.Error(t, err)
	_, err = svc.RunHTTPWithVariables(context.Background(), "q", map[string]string{"=bad": "x"})
	require.Error(t, err)
}

func TestCov100iResolveGitMode(t *testing.T) {
	m, err := resolveGitMode("")
	require.NoError(t, err)
	assert.Equal(t, gitModeNone, m)
	m, err = resolveGitMode("stage")
	require.NoError(t, err)
	assert.Equal(t, gitModeStage, m)
	_, err = resolveGitMode("commit")
	require.Error(t, err)
	_, err = resolveGitMode("weird")
	require.Error(t, err)
}

func TestCov100iGitPreflightAndApply(t *testing.T) {
	require.NoError(t, gitPreflight(t.TempDir(), gitModeNone))
	require.Error(t, gitPreflight(t.TempDir(), gitModeStage))
	require.NoError(t, applyGit(t.TempDir(), gitModeNone, nil))
}
