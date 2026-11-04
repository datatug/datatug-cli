package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mongoURISecret = "mongo-s3cret-password"

func mongoDBCatalog(id, path, model string) datatug.DbCatalog {
	return datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}},
		Driver:      "mongodb",
		Path:        path,
		DbModel:     model,
	}}
}

func TestSourceURLFromCatalog_MongoDBResolvesBothSchemesToTheVariable(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/mongo.json", `{"dsnEnv":"DATATUG_MONGO_URL"}`)
	catalog := mongoDBCatalog("saved-mongo", "connections/prod/mongo.json", "mongo")

	for name, value := range map[string]string{
		"mongodb":     "mongodb://alice:" + mongoURISecret + "@db.example.com/shop",
		"mongodb+srv": "mongodb+srv://alice:" + mongoURISecret + "@cluster.example.com/shop",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DATATUG_MONGO_URL", value)
			source, err := sourceURLFromCatalog(catalog, dir)
			require.NoError(t, err)
			assert.Equal(t, "env:DATATUG_MONGO_URL", source)
			assert.NotContains(t, source, mongoURISecret)

			resolved, err := ResolveCatalogSourceURL(catalog, dir)
			require.NoError(t, err)
			assert.Equal(t, source, resolved)
			assert.NotContains(t, resolved, mongoURISecret)
		})
	}
}

func TestSourceURLFromCatalog_MongoDBRefusesAnotherEngineAndAnUnusableVariable(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	tests := []struct {
		name, dsnEnv, value, want string
		set                       bool
	}{
		{"unset", "DATATUG_MONGO_DESCRIPTOR_UNSET_URL", "", "is not set", false},
		{"empty", "DATATUG_MONGO_DESCRIPTOR_EMPTY_URL", "", "is empty", true},
		{"recursive", "DATATUG_MONGO_DESCRIPTOR_RECURSIVE_URL", "env:DATATUG_OTHER", "not another env: reference", true},
		{"another engine", "DATATUG_MONGO_DESCRIPTOR_ENGINE_URL", "postgres://alice:" + mongoURISecret + "@db.example.com/shop", "not a postgres source", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(tc.dsnEnv, tc.value)
			}
			path := tc.dsnEnv + ".json"
			writeDescriptor(t, dir, path, `{"dsnEnv":"`+tc.dsnEnv+`"}`)
			catalog := mongoDBCatalog("saved-mongo", path, "mongo")

			source, err := sourceURLFromCatalog(catalog, dir)
			assert.Empty(t, source)
			if assert.Error(t, err) {
				assert.ErrorContains(t, err, `catalog "saved-mongo"`)
				assert.ErrorContains(t, err, tc.dsnEnv)
				assert.ErrorContains(t, err, tc.want)
				assert.NotContains(t, err.Error(), mongoURISecret)
				assert.NotContains(t, err.Error(), "alice")
			}
		})
	}
}

func TestSourceURLFromCatalog_MongoDBRefusesADescriptorOutsideTheProject(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "mongo.json", `{"dsnEnv":"DATATUG_MONGO_URL"}`)
	absolute := filepath.Join(dir, "mongo.json")
	for _, outside := range []string{"~/mongo.json", "$HOME/mongo.json", "${HOME}/mongo.json", "../mongo.json", "connections/../../mongo.json", absolute} {
		source, err := sourceURLFromCatalog(mongoDBCatalog("saved-mongo", outside, "mongo"), dir)
		assert.Empty(t, source, outside)
		if assert.ErrorIs(t, err, errDescriptorOutsideProject, outside) {
			assert.ErrorContains(t, err, `catalog "saved-mongo"`, outside)
			assert.NotContains(t, err.Error(), outside, outside)
			assert.NotContains(t, err.Error(), mongoURISecret, outside)
		}
	}

	source, err := sourceURLFromCatalog(mongoDBCatalog("saved-mongo", "missing.json", "mongo"), dir)
	assert.Empty(t, source)
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, `catalog "saved-mongo"`)
		assert.ErrorContains(t, err, "open MongoDB connection descriptor")
		assert.NotContains(t, err.Error(), mongoURISecret)
	}

	literal := "mongodb://alice:" + mongoURISecret + "@db.example.com/shop"
	source, err = sourceURLFromCatalog(mongoDBCatalog("saved-mongo", literal, "mongo"), dir)
	assert.Empty(t, source)
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, `catalog "saved-mongo"`)
		assert.NotContains(t, err.Error(), mongoURISecret)
		assert.NotContains(t, err.Error(), "mongodb://")
	}
}

func TestListSources_ExposesAMongoDBCatalogUnderBothAliases(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	t.Setenv("DATATUG_MONGO_URL", "mongodb://alice:"+mongoURISecret+"@db.example.com/shop")
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, storage.QueriesFolder), 0o755))
	writeDescriptor(t, dir, "connections/prod/mongo.json", `{"dsnEnv":"DATATUG_MONGO_URL"}`)
	store := mockProjectStore{
		loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return datatug.DbCatalogs{
				{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "saved-mongo", Title: "Saved Mongo"}}, Driver: "mongodb", Path: "connections/prod/mongo.json", DbModel: "mongo"}},
			}, nil
		},
	}
	ctx := context.Background()
	sources, err := ListSources(ctx, store, dir, "prod")
	require.NoError(t, err)
	require.Len(t, sources, 2)
	byID := map[string]ResolvedSource{}
	for _, source := range sources {
		byID[source.ID] = source
		assert.Equal(t, SourceKindSQL, source.Kind)
		assert.Equal(t, "env:DATATUG_MONGO_URL", source.URL)
		assert.NotContains(t, source.URL, mongoURISecret)
	}
	for _, id := range []string{"mongo", "saved-mongo"} {
		require.Contains(t, byID, id)
		resolved, err := ResolveSource(ctx, store, dir, "prod", id)
		require.NoError(t, err, id)
		assert.Equal(t, id, resolved.ID)
		assert.Equal(t, "env:DATATUG_MONGO_URL", resolved.URL)
		assert.NotContains(t, resolved.URL, mongoURISecret)
	}
}

func TestResolveProjectSourceURL_MongoDBReturnsTheVariableName(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	t.Setenv("DATATUG_MONGO_URL", "mongodb+srv://alice:"+mongoURISecret+"@cluster.example.com/shop")
	dir, store := sourceProject(t)
	writeDescriptor(t, dir, "connections/prod/saved-mongo.json", `{"dsnEnv":"DATATUG_MONGO_URL"}`)
	catalog := mongoDBCatalog("saved-mongo", "connections/prod/saved-mongo.json", "mongo")
	require.NoError(t, store.SaveEnvDbCatalog(context.Background(), "prod", "", "saved-mongo", &catalog))

	source, err := ResolveProjectSourceURL(context.Background(), store, dir, "prod", "saved-mongo")
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_MONGO_URL", source)
	assert.NotContains(t, source, mongoURISecret)
}
