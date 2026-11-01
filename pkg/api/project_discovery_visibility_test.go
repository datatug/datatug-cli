package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

func ordinaryDiscoveryFixture(t *testing.T) (string, string, dto.ProjectRef) {
	t.Helper()
	dir, git := setupTestGitProject(t, "ordinary-discovery")
	branch := git("symbolic-ref", "--short", "HEAD")
	configureWriteSession(t, secureread.SessionOptions{As: "reader", Roles: []string{"reader"}, PoliciesDir: writePoliciesDir(t)}, map[string]string{"ordinary-discovery": dir}, Capabilities{})
	secureMu.RLock()
	owner := secureSession.LocalProjectOwner
	secureMu.RUnlock()
	require.False(t, owner, "fixture must exercise an ordinary reader")
	return dir, branch, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "ordinary-discovery"}
}

func writeDiscoveryFixture(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	filename := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o700))
	require.NoError(t, os.WriteFile(filename, body, 0o600))
}

func TestOrdinaryServerDiscoveryUsesAuthoritativeArchiveState(t *testing.T) {
	dir, branch, ref := ordinaryDiscoveryFixture(t)
	for _, host := range []string{"active", "archived"} {
		id := "mysql:" + host + ":3306"
		body, err := json.Marshal(map[string]any{"id": id, "title": host, "server": map[string]any{"driver": "mysql", "host": host, "port": 3306}, "catalogs": []any{}})
		require.NoError(t, err)
		writeDiscoveryFixture(t, dir, "dbs/mysql/"+id+".dbserver.json", body)
	}
	servers, err := LocalProjectDbServers(t.Context(), ref, branch)
	require.NoError(t, err)
	require.Len(t, servers, 2)
	selection := LocalLifecycleSelection{ProjectRef: ref, Branch: branch, ProjectAPI: "local"}
	_, err = LocalProjectLifecycleCatalog(t.Context(), selection, true)
	require.ErrorIs(t, err, secureread.ErrAccessDenied, "ordinary discovery must not require owner-only history")
	writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte(`{"version":1,"archived":[{"kind":"server","id":"mysql/mysql:archived:3306"}]}`))
	servers, err = LocalProjectDbServerDiscovery(t.Context(), ref, branch)
	require.NoError(t, err)
	require.Len(t, servers, 1, "archived server must be excluded by the ordinary provider read")
	require.Equal(t, "active", servers[0].Title)
	full, err := LocalProjectDbServers(t.Context(), ref, branch)
	require.NoError(t, err)
	require.Len(t, full, 2, "existing full catalog must still resolve archived definitions")
	_, err = LocalProjectItemTags(t.Context(), ItemTagsSelector{ProjectRef: ref, Branch: branch, Kind: "dbserver", Driver: "mysql", ID: "mysql:archived:3306"})
	require.NoError(t, err, "archive must preserve direct object resolution")
	writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte(`{"version":1,"archived":"corrupt"}`))
	servers, err = LocalProjectDbServerDiscovery(t.Context(), ref, branch)
	require.Error(t, err, "unreadable archive state must not become all-active")
	require.Empty(t, servers)
}

func TestOrdinaryMeaningDiscoveryPreservesExplicitReferences(t *testing.T) {
	dir, branch, ref := ordinaryDiscoveryFixture(t)
	document := datatug.ProjectMeaningsDocument{Version: 1, Entities: []datatug.MeaningEntity{
		{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person"}, Name: "Archived person", Origin: "project", Fields: []datatug.MeaningField{{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person-name"}, Name: "Name"}}},
		{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "active"}, Name: "Active", Origin: "project", Fields: []datatug.MeaningField{
			{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "visible-field"}, Name: "Visible"},
			{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "hidden-field"}, Name: "Hidden"},
		}},
	}}
	body, err := json.Marshal(document)
	require.NoError(t, err)
	writeDiscoveryFixture(t, dir, localMeaningsFile, body)
	var model datatug.QuerySemanticModel
	require.NoError(t, json.Unmarshal([]byte(localMeaningSemanticFixture), &model))
	query := datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q", Title: "Query"}}, Type: datatug.QueryTypeSQL, Text: "select 1", SemanticModel: &model}
	body, err = json.Marshal(query)
	require.NoError(t, err)
	writeDiscoveryFixture(t, dir, "queries/q.query.json", body)
	before, err := LocalProjectMeaningsCatalog(t.Context(), ref, branch)
	require.NoError(t, err)
	require.Len(t, before.Entities, 3)
	require.False(t, before.Writable)
	_, err = LocalProjectLifecycleCatalog(t.Context(), LocalLifecycleSelection{ProjectRef: ref, Branch: branch, ProjectAPI: "local"}, true)
	require.True(t, errors.Is(err, secureread.ErrAccessDenied))
	writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte(`{"version":1,"archived":[{"kind":"meaning","id":"project/person"},{"kind":"meaning","id":"project/hidden-field"}]}`))
	after, err := LocalProjectMeaningDiscovery(t.Context(), ref, branch)
	require.NoError(t, err)
	require.Len(t, after.Entities, 2, "archive projection must preserve a distinct query-owned identity with the same concept ID")
	require.Equal(t, before.Revision, after.Revision, "display projection must retain the full reference/mutation revision")
	full, err := LocalProjectMeaningsCatalog(t.Context(), ref, branch)
	require.NoError(t, err)
	require.Len(t, full.Entities, 3, "existing full catalog must preserve all reference targets")
	for _, entity := range after.Entities {
		if entity.DocumentID == "project" {
			require.Equal(t, "active", entity.ConceptID)
			require.Len(t, entity.Fields, 1)
			require.Equal(t, "visible-field", entity.Fields[0].ConceptID)
		} else {
			require.Equal(t, datatug.QueryMeaningDocumentID("q", "query"), entity.DocumentID)
			require.Equal(t, "person", entity.ConceptID)
		}
	}
	scope := datatug.MeaningScope{Storage: LocalStoreID, Project: ref.ProjectID, Branch: branch}
	model.Meaning.Concepts[0].MeaningRef = &datatug.MeaningReference{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person"}, Scope: scope}
	model.Meaning.Concepts[1].MeaningRef = &datatug.MeaningReference{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person-name"}, Scope: scope}
	require.NoError(t, validateLocalQueryMeaningReferences(dir, scope, &model), "archived meaning definitions must still resolve explicit saved references")
	writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte(`{"version":1,"archived":"corrupt"}`))
	after, err = LocalProjectMeaningDiscovery(t.Context(), ref, branch)
	require.Error(t, err)
	require.Empty(t, after.Entities)
}
