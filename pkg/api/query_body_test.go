package api

import (
	"context"
	"errors"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

func TestLoadTugQLBodyUsesServedStoreAndExplicitRepresentation(t *testing.T) {
	for _, sample := range []struct {
		name, body string
		structured bool
		loadErr    error
		wantErr    string
	}{
		{name: "missing", loadErr: os.ErrNotExist, wantErr: "read query document"},
		{name: "empty", wantErr: "body is missing or empty"},
		{name: "structured", body: "from: {name: Invoice}\n", structured: true},
		{name: "text", body: "from Invoice\nselect InvoiceId"},
		{name: "mismatched", body: "from: {name: Invoice}\n", wantErr: "invalid TugQL text"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			serveProjects(t, "p1")
			useProjectStore(t, mockProjectStore{loadQueryFunc: func(_ context.Context, id string, _ ...datatug.StoreOption) (*datatug.QueryDef, error) {
				if id != "q" {
					t.Fatalf("query changed: %s", id)
				}
				return &datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: sample.body, StructuredBody: sample.structured}, sample.loadErr
			}})
			got, err := LoadQueryDocument("p1", "q", datatug.QueryTypeTugQL)
			if sample.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), sample.wantErr) {
					t.Fatalf("body failure: %q %v", got, err)
				}
				return
			}
			if err != nil || got == "" {
				t.Fatalf("body read: %q %v", got, err)
			}
			if sample.structured && got != sample.body {
				t.Fatal("structured body bytes changed")
			}
		})
	}
	t.Run("store unavailable", func(t *testing.T) {
		serveProjects(t, "p1")
		previous := storage.NewDatatugStore
		t.Cleanup(func() { storage.NewDatatugStore = previous })
		failed := errors.New("served store unavailable")
		storage.NewDatatugStore = func(string) (storage.Store, error) { return nil, failed }
		_, err := LoadQueryDocument("p1", "q", datatug.QueryTypeTugQL)
		if !errors.Is(err, failed) {
			t.Fatalf("store failure lost: %v", err)
		}
	})
}

func TestAttestLocalQueryBodyRequiresOwnedCanonicalBytes(t *testing.T) {
	for _, sample := range []string{"missing", "directory", "mismatch", "matching"} {
		t.Run(sample, func(t *testing.T) {
			dir := t.TempDir()
			folder := filepath.Join(dir, "queries")
			if err := os.MkdirAll(folder, 0755); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(folder, "q.query.tugql.yaml")
			if sample == "directory" {
				if err := os.Mkdir(name, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if sample == "mismatch" || sample == "matching" {
				body := "wrong"
				if sample == "matching" {
					body = "from: {name: Invoice}"
				}
				if err := os.WriteFile(name, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := attestLocalQueryBody(dir, "q", &datatug.QueryDef{Type: datatug.QueryTypeTugQL, StructuredBody: true, Text: "from: {name: Invoice}"})
			if sample == "matching" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, dto.ErrUnsupportedCapability) {
				t.Fatalf("unattested body accepted: %v", err)
			}
		})
	}
}

// These cases exercise the adapter's mutation boundary, including an external
// edit between Core's revision read and the retry's canonical-body attestation.
func TestLocalQueryBodyAttestationBeforeNewIntentAndPendingPut(t *testing.T) {
	for _, mode := range []string{"legacy-read-only", "pending-unchanged", "pending-external-edit"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
			const project = "body-attestation"
			dir, git := setupTestGitProject(t, project)
			paths := map[string]string{project: dir}
			previous := storage.NewDatatugStore
			t.Cleanup(func() {
				storage.NewDatatugStore = previous
				ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
			})
			factory := func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
			storage.NewDatatugStore = factory
			session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
			require.NoError(t, err)
			ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})
			request := dto.SaveQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, OperationID: "create-attested", Branch: git("symbolic-ref", "--short", "HEAD"), ExpectedBranchHead: git("rev-parse", "HEAD"), IfNoneMatch: true,
				Query: datatug.QueryDefWithFolderPath{FolderPath: datatug.RootSharedFolderName, QueryDef: datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q", Title: "Attested"}}, Type: datatug.QueryTypeTugQL, StructuredBody: true, Text: "from: {name: Invoice}\n"}}}
			scope, err := SecureProjectMutationScope(request.ProjectRef, request.Branch, "save-query")
			require.NoError(t, err)
			adapter := LocalProjectQueryAdapter{}
			created, err := adapter.SaveQuery(context.Background(), scope, request)
			require.NoError(t, err)
			request.OperationID = "update-attested"
			request.IfNoneMatch, request.IfMatch = false, created.Revision
			request.Query.Text = "from: {name: Invoice}\nlimit: 1\n"
			base, err := factory(LocalStoreID)
			require.NoError(t, err)
			projectStore := filestore.NewProjectStore(project, dir)
			revisioned := projectStore.(datatug.RevisionedQueriesStore)
			name := filepath.Join(dir, "queries", "q.query.tugql.yaml")
			if mode == "legacy-read-only" {
				// Controlled pre-cutover fixture accepted only by Core's temporary reader.
				git("mv", "queries/q.query.tugql.yaml", "queries/q.query.dtql")
				loaded, err := revisioned.LoadQueryRevision(context.Background(), "q")
				require.NoError(t, err)
				request.IfMatch = string(loaded.Revision)
				index := git("write-tree")
				_, err = adapter.SaveQuery(context.Background(), scope, request)
				require.ErrorIs(t, err, dto.ErrUnsupportedCapability)
				require.Equal(t, index, git("write-tree"))
				require.NoFileExists(t, name)
				return
			}
			storage.NewDatatugStore = func(string) (storage.Store, error) {
				return mockWrappedStore{Store: base, ps: mockPutQueryErrStore{ProjectStore: projectStore, RevisionedQueriesStore: revisioned, putErr: errors.New("interrupted before PutQuery")}}, nil
			}
			_, err = adapter.SaveQuery(context.Background(), scope, request)
			require.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
			storage.NewDatatugStore = factory
			if mode == "pending-external-edit" {
				storage.NewDatatugStore = func(string) (storage.Store, error) {
					return mockWrappedStore{Store: base, ps: bodyEditedAfterRevisionRead{ProjectStore: projectStore, RevisionedQueriesStore: revisioned, afterRead: func() { require.NoError(t, os.WriteFile(name, []byte("foreign editor bytes"), 0600)) }}}, nil
				}
				index := git("write-tree")
				_, err = adapter.SaveQuery(context.Background(), scope, request)
				require.ErrorIs(t, err, dto.ErrUnsupportedCapability)
				require.Equal(t, index, git("write-tree"))
				body, err := os.ReadFile(name)
				require.NoError(t, err)
				require.Equal(t, "foreign editor bytes", string(body))
				return
			}
			recovered, err := adapter.SaveQuery(context.Background(), scope, request)
			require.NoError(t, err)
			require.NotEmpty(t, recovered.Revision)
			body, err := os.ReadFile(name)
			require.NoError(t, err)
			require.Equal(t, request.Query.Text, string(body))
		})
	}
}

type bodyEditedAfterRevisionRead struct {
	datatug.ProjectStore
	datatug.RevisionedQueriesStore
	afterRead func()
}

func (s bodyEditedAfterRevisionRead) LoadQueryRevision(ctx context.Context, id string, options ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	query, err := s.RevisionedQueriesStore.LoadQueryRevision(ctx, id, options...)
	if err == nil {
		s.afterRead()
	}
	return query, err
}
