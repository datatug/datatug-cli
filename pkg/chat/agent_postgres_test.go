package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/internal/pgstandin"
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
	_ = os.Unsetenv(dbcopy.PostgresPreviewEnv)
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

// The opener of this test binary is the one that stops the run, which the init above cannot show: the test opens a
// PostgreSQL source with the preview on, and nothing stands in.
func TestTheOpenerOfThisTestBinaryIsTheOneThatStopsTheRun(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	ref, err := dbcopy.Parse("postgres://alice:" + chatPostgresPassword + "@127.0.0.1:1/shop")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recovered := recover(); recovered != "a test of the chat opened a PostgreSQL database for real: it must stand in and never dial" {
			t.Fatalf("the open did not stop the run: %v", recovered)
		}
	}()
	_, _ = ref.Open(t.Context())
	t.Fatal("the open returned: the opener of this test binary is not the one that stops the run")
}

// The switch of the developer's shell does not reach a test: the init above clears it.
func TestThePreviewSwitchIsOffUnlessATestTurnsItOn(t *testing.T) {
	if err := dbcopy.CheckPostgresPreview(); err != dbcopy.ErrPostgresPreview { //nolint:errorlint // the sentinel itself
		t.Fatalf("CheckPostgresPreview() = %v, want the preview sentence", err)
	}
}

// With the preview on and a session with no policy, a source that opened and whose pool cannot make a connection again
// (the server was restarted, the password was changed) fails the read with the text pgx writes, which names the user and
// holds the password. What the chat answers, which goes to the message, the model and the store, is one fixed sentence.
func TestRunDTQL_AReadThatLosesItsConnectionAnswersOneFixedSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	const user = "USERMARKER-dt02-chat"
	// The stand-in is built here, on the goroutine of the test: the opener runs on another one, where a failure of
	// the setup (FailNow) would end that goroutine and not the test.
	standIn := pgstandin.Unreachable(t, user, chatPostgresPassword)
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return standIn, nil
	}))
	secured, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	savedLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(savedLog) })

	conversation := &AIConversation{}
	response, err := conversation.runDTQL(context.Background(), secureread.NewExecutor(secured), "postgres://"+user+":"+chatPostgresPassword+"@db.example.com/shop", runDTQLArgs{DTQL: "from: {name: customers}\nlimit: 5"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "Query failed: the connection failed; the PostgreSQL connection string is the one the source was given"
	if response.OK || response.Error != want {
		t.Errorf("response = %+v, want the error %q", response, want)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, shown := range []string{string(encoded), logged.String()} {
		for _, marker := range []string{user, chatPostgresPassword} {
			if strings.Contains(shown, marker) {
				t.Errorf("%q is in what the chat answered or logged: %s", marker, shown)
			}
		}
	}
}
