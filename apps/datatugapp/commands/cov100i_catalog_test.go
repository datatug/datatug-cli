package commands

import (
	"context"
	"strings"
	"testing"

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

	cat.Driver = "postgres"
	cat.Path = "x"
	_, err = querySourceURLFromCatalog(cat, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

func TestCov100iSavedQueryIDLongTitle(t *testing.T) {
	id := savedQueryID(strings.Repeat("Ab Cd!", 20))
	assert.Contains(t, id, "-")
	// slug capped near 48 then uuid suffix
	assert.LessOrEqual(t, len(strings.Split(id, "-")[0])+1, 60)
}

func TestCov100iRunDTQLAndHTTPVariableParseErrors(t *testing.T) {
	svc := chatSavedQueries{}
	_, err := svc.RunDTQLWithVariables(context.Background(), "q", map[string]string{"=bad": "x"})
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
