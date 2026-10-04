package sqlexecute

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// The executor prints the connection it is about to open. A password given in
// the command must never be part of that line.
func TestExecuteCommand_NeverPrintsThePassword(t *testing.T) {
	const password = "s3cr3t DT01;w0rd"
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return &datatug.EnvDb{Server: datatug.ServerRef{Driver: "totally-unknown-driver-xyz", Host: "db.example.com"}}, nil
		},
		nil,
	)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = writer
	_, execErr := e.executeCommand(RequestCommand{Env: "dev", DB: "shop", Username: "alice", Password: password, Text: "SELECT 1"})
	os.Stdout = saved
	_ = writer.Close()
	out, _ := io.ReadAll(reader)
	if execErr == nil {
		t.Fatal("an unregistered driver must fail to open")
	}
	if strings.Contains(string(out), password) || strings.Contains(string(out), "s3cr3t") || strings.Contains(string(out), "w0rd") {
		t.Fatalf("stdout leaks the password: %q", out)
	}
	if !strings.Contains(string(out), "server=db.example.com") || !strings.Contains(string(out), "password=xxxxx") {
		t.Fatalf("stdout should still describe the connection: %q", out)
	}
}
