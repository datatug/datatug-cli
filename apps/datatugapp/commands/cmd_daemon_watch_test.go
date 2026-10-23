package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/storage"
)

func TestWatchProjectManifestCoordinatesWithoutStorageCredentials(t *testing.T) {
	dir := t.TempDir()
	body := `{"id":"northwind","title":"Northwind","access":"private","created":{"at":"2026-10-23T10:00:00Z"},"coordination":{"endpoint":"http://127.0.0.1:8990","namespace":"northwind"}}`
	if err := os.WriteFile(filepath.Join(dir, storage.ProjectSummaryFileName), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	id, endpoint, namespace, err := watchProjectCoordination(dir)
	if err != nil || id != "northwind" || endpoint != "http://127.0.0.1:8990" || namespace != "northwind" {
		t.Fatalf("manifest %s %s %s %v", id, endpoint, namespace, err)
	}
	bad := `{"id":"northwind","coordination":{"endpoint":"https://user:secret@coord.example","namespace":"northwind"}}`
	if err = os.WriteFile(filepath.Join(dir, storage.ProjectSummaryFileName), []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = watchProjectCoordination(dir); err == nil {
		t.Fatal("manifest URL credentials accepted")
	}
}

func TestWatchRunnerDoesNotSendTokenToChangedManifestEndpoint(t *testing.T) {
	if _, err := resolveWatchCoordinator("https://attacker.example", "https://trusted.example", ""); err == nil {
		t.Fatal("manifest change redirected runner credential")
	}
	if endpoint, err := resolveWatchCoordinator("https://trusted.example", "https://trusted.example/", ""); err != nil || endpoint != "https://trusted.example" {
		t.Fatal(endpoint, err)
	}
	if endpoint, err := resolveWatchCoordinator("https://old.example", "https://trusted.example", "https://explicit.example"); err != nil || endpoint != "https://explicit.example" {
		t.Fatal(endpoint, err)
	}
}
func TestWatchProjectRequiresCoordinationAfterTypedManifestLoad(t *testing.T) {
	dir := t.TempDir()
	body := `{"id":"northwind","title":"Northwind","access":"private","created":{"at":"2026-10-23T10:00:00Z"}}`
	if e := os.WriteFile(filepath.Join(dir, storage.ProjectSummaryFileName), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := watchProjectCoordination(dir); e == nil {
		t.Fatal("missing coordination admitted")
	}
}
