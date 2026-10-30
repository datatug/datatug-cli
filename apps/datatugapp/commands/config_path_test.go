package commands

import (
	"bytes"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/dtconfig"
)

func runIsolatedCommand(t *testing.T, settings string, args ...string) error {
	t.Helper()
	root := DatatugCommand()
	root.SilenceErrors, root.SilenceUsage = true, true
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"--config", settings}, args...))
	return root.Execute()
}

func TestExplicitConfigRegistersAndAddsWithoutUserSettings(t *testing.T) {
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "datatug-project.json"), []byte(`{"id":"export-demo","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runIsolatedCommand(t, settings, "projects", "add", "--project", "export-demo", "--directory", project); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "edited.sqlite")
	writeJourneyDB(t, db)
	if err := runIsolatedCommand(t, settings, "db", "add", "--project", "export-demo", "--db", db, "--env", "local", "--name", "chinook-browser"); err != nil {
		t.Fatal(err)
	}
	config, err := dtconfig.GetSettings()
	if err != nil || len(config.Projects) != 1 || config.Projects[0].Path != project {
		t.Fatalf("isolated settings: %+v %v", config, err)
	}
	if _, err := os.Stat(filepath.Join(project, "environments", "local", "catalogs", "chinook-browser", "chinook-browser.db.json")); err != nil {
		t.Fatal(err)
	}
	root := DatatugCommand()
	command, _, err := root.Find([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if err = command.ParseFlags([]string{"--config", settings, "--project", project}); err != nil {
		t.Fatal(err)
	}
	if err = root.PersistentPreRunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = loadServeRuntimeSettings(); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitConfigRejectsRelativeAndPreservesDefault(t *testing.T) {
	dtconfig.SetConfigFilePath("")
	want := dtconfig.GetConfigFilePath()
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	err := runIsolatedCommand(t, "relative.yaml", "projects")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path: %v", err)
	}
	if got := dtconfig.GetConfigFilePath(); got != want {
		t.Fatalf("rejected selection changed path: %q", got)
	}
}

func TestExplicitConfigHookReadsFlagsBeforeSettings(t *testing.T) {
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	root := DatatugCommand()
	if err := root.PersistentPreRunE(&cobra.Command{}, nil); err == nil {
		t.Fatal("missing configuration flag should fail")
	}
	if err := root.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	dtconfig.SetConfigFilePath("/tmp/previous.yaml")
	if err := root.PersistentPreRunE(root, nil); err != nil {
		t.Fatal(err)
	}
	if got := dtconfig.GetConfigFilePath(); got == "/tmp/previous.yaml" {
		t.Fatal("default flag did not restore default path")
	}
}
