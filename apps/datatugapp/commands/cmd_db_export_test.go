package commands

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

func TestDBExportFailureRedactsSourcePassword(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	secret := "export-secret-marker"
	command := dbExportCommand()
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetArgs([]string{"--from", "postgres://alice:" + secret + "@example.invalid/shop", "--to", "ingitdb://" + filepath.Join(t.TempDir(), "native")})
	err := command.Execute()
	if err == nil {
		t.Fatal("preview source unexpectedly opened")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("source password leaked: %v", err)
	}
}

func TestDBExportFailureRedactsResolvedEnvSourcePassword(t *testing.T) {
	secret := "export-env-secret-marker"
	t.Setenv("EXPORT_SOURCE_URL", "postgres://alice:"+secret+"@example.invalid/shop")
	originalOpen, originalExport := openExportSource, runNativeExport
	t.Cleanup(func() { openExportSource, runNativeExport = originalOpen, originalExport })
	openExportSource = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, nil }
	runNativeExport = func(context.Context, dal.DB, string, ...dbcopy.ExportOptions) (map[string]int64, error) {
		return nil, errors.New("describe collection: provider echoed " + secret)
	}
	command := dbExportCommand()
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetArgs([]string{"--from", "env:EXPORT_SOURCE_URL", "--to", "ingitdb://" + filepath.Join(t.TempDir(), "native")})
	err := command.Execute()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("CLI export failure leaked source password or unexpectedly succeeded: %v", err)
	}
}
