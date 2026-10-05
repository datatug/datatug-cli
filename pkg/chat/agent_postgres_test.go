package chat

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// A test of the chat must never dial a PostgreSQL server: the real one is the journey test of CI. For the whole test
// binary the constructor that dbcopy opens a source through is one that stops the run, and a test that reaches a
// PostgreSQL source with the preview on stands in with a fake of its own.
func init() {
	dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		panic("a test of the chat opened a PostgreSQL database for real: it must stand in and never dial")
	})
}

const chatPostgresPassword = "PWMARKER-dt02-chat"

// chatPolicy lets alice read the customers of a source.
const chatPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: customers
default: deny
scopes:
  - path: /customers
    rules:
      - id: all-customers
        effect: allow
        operations: [query]
`

// The chat reads a source through the executor, so what the executor answers for a PostgreSQL source is what the chat
// says: with the preview off the sentence about the preview, with it on and a policy in the session the sentence about
// policy-enforced reads, and in both cases the opener is never called.
func TestRunDTQL_APostgresSourceIsAnsweredBeforeItIsOpened(t *testing.T) {
	var opens atomic.Int32
	restore := dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		opens.Add(1)
		return &dalgo2postgres.Database{}, nil
	})
	t.Cleanup(restore)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(chatPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	secured, err := secureread.NewSession(secureread.SessionOptions{As: "alice", PoliciesDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	const source = "postgres://alice:" + chatPostgresPassword + "@db.example.com/shop"

	for name, tc := range map[string]struct {
		preview string
		want    string
	}{
		"the preview is off":                             {"", "Query failed: " + dbcopy.ErrPostgresPreview.Error()},
		"the preview is on and the session has a policy": {"1", "Query failed: " + dbcopy.ErrPostgresPolicyReads.Error()},
	} {
		t.Setenv(dbcopy.PostgresPreviewEnv, tc.preview)
		conversation := &AIConversation{}
		response, err := conversation.runDTQL(context.Background(), secureread.NewExecutor(secured), source, runDTQLArgs{DTQL: "from: {name: customers}\nlimit: 5"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if response.OK || response.Error != tc.want {
			t.Errorf("%s: response = %+v, want the error %q", name, response, tc.want)
		}
	}
	if opens.Load() != 0 {
		t.Errorf("the opener was called %d times", opens.Load())
	}
}
