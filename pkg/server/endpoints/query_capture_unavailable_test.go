//go:build !datatug_query_capture

package endpoints

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// TestCaptureStoreFor_DefaultBuildFailsClosed pins the default build: the
// datatug-core release go.mod pins has no revisioned query store, so the
// real captureStoreFor refuses rather than falling back to the legacy,
// unconditional SaveQuery.
func TestCaptureStoreFor_DefaultBuildFailsClosed(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	pathsByID := map[string]string{projectID: projectDir}
	configureServedProjects(t, pathsByID)
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	store, err := captureStoreFor(projectID)
	if store != nil || !errors.Is(err, errCaptureStoreUnavailable) {
		t.Fatalf("captureStoreFor = %v, %v; want errCaptureStoreUnavailable", store, err)
	}

	storeErr := errors.New("no store configured")
	storage.NewDatatugStore = func(string) (storage.Store, error) { return nil, storeErr }
	if _, err := captureStoreFor(projectID); !errors.Is(err, storeErr) {
		t.Fatalf("captureStoreFor = %v; want the store resolution error", err)
	}
}
