package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// registerQueryTestProject wires api.ConfigureSecureSession and
// storage.NewDatatugStore the way `datatug serve` does at startup
// (pkg/server/http_server.go's ServeHTTP), so GetQuery's own
// api.ProjectDir/projectStoreForID calls have real served-project state to
// resolve against.
func registerQueryTestProject(t *testing.T, projectID, dir string) {
	t.Helper()
	filestore.SetProjectPath(projectID, dir)
	pathsByID := map[string]string{projectID: dir}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	ConfigureSecureSession(session, pathsByID, Capabilities{})
}

// TestGetQuery_S97_IDConvention covers S97's required table: bare id unique
// -> resolves; folder-qualified -> resolves; unknown -> ErrQueryNotFound
// (get_query's legacy envelope maps this to 404); ambiguous ->
// ErrAmbiguousQueryID (mapped to legacy INVALID_REQUEST) — end to end
// through the real GetQuery function, never the store's own raw filesystem
// error text.
func TestGetQuery_S97_IDConvention(t *testing.T) {
	dir := t.TempDir()
	projectID := "get-query-s97-test-project"
	writeQueryFile(t, dir, "customers/customer-invoices.query.json", "customer-invoices")
	writeQueryFile(t, dir, "x/q.query.json", "q")
	writeQueryFile(t, dir, "y/q.query.json", "q")
	registerQueryTestProject(t, projectID, dir)

	refFor := func(id string) dto.ProjectItemRef {
		return dto.ProjectItemRef{
			ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID},
			ID:         id,
		}
	}

	t.Run("bare id unique resolves", func(t *testing.T) {
		got, err := GetQuery(context.Background(), refFor("customer-invoices"))
		if err != nil {
			t.Fatalf("GetQuery: %v", err)
		}
		// datatug.QueryDef.ID itself stays bare (fsQueriesStore.LoadQuery's
		// own SetID convention — see ResolveQueryID's doc comment); it is
		// the REQUEST id that accepts both forms, not the stored value.
		if got.ID != "customer-invoices" {
			t.Errorf("got.ID = %q, want %q", got.ID, "customer-invoices")
		}
	})

	t.Run("folder-qualified id resolves", func(t *testing.T) {
		got, err := GetQuery(context.Background(), refFor("customers/customer-invoices"))
		if err != nil {
			t.Fatalf("GetQuery: %v", err)
		}
		if got.ID != "customer-invoices" {
			t.Errorf("got.ID = %q, want %q", got.ID, "customer-invoices")
		}
	})

	t.Run("unknown id is ErrQueryNotFound, never a raw filesystem error", func(t *testing.T) {
		_, err := GetQuery(context.Background(), refFor("no-such-query"))
		if !errors.Is(err, ErrQueryNotFound) {
			t.Fatalf("GetQuery error = %v, want ErrQueryNotFound", err)
		}
		if err != nil && (strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "open ")) {
			t.Errorf("error message leaked raw filesystem text: %q", err.Error())
		}
	})

	t.Run("ambiguous bare id is ErrAmbiguousQueryID", func(t *testing.T) {
		_, err := GetQuery(context.Background(), refFor("q"))
		if !errors.Is(err, ErrAmbiguousQueryID) {
			t.Fatalf("GetQuery error = %v, want ErrAmbiguousQueryID", err)
		}
	})

	t.Run("unambiguous explicit folder-qualified form still resolves", func(t *testing.T) {
		got, err := GetQuery(context.Background(), refFor("x/q"))
		if err != nil {
			t.Fatalf("GetQuery: %v", err)
		}
		if got.ID != "q" {
			t.Errorf("got.ID = %q, want %q", got.ID, "q")
		}
	})
}
