package commands

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// mirroredDB is an inGitDB database written by db mirror: records of nested
// collections (teams/{team}/ext/{module}/events/{event}).
func mirroredDB(t *testing.T) string {
	t.Helper()
	project := mirrorProject(t)
	useMirrorSource(t, &mirrorTestSource{docs: mirrorDocs()}, nil)
	out := filepath.Join(project, "data", "db")
	if _, stderr, code := runMirror(t, "--from", "firestore://demo-example", "--policy", filepath.Join(project, "mirrors", "backup.yaml"),
		"--collections", "teams,teams/ext/**", "--to", out); code != 0 {
		t.Fatalf("mirror: exit %d\n%s", code, stderr)
	}
	return out
}

func TestQueryRunFromANestedInGitDBCollection(t *testing.T) {
	db := mirroredDB(t)
	stdout, stderr, code := runQuery(t, "", "--db", "ingitdb://"+db, "--from", "teams/t1/ext/cal/events", "--no-policies", "--format", "json")
	if code != 0 || !strings.Contains(stdout, `"Event"`) {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	// A path to collections the database does not define is an error, not an empty result.
	for from, want := range map[string]string{
		"teams/t1/nope": `collection "nope" is not defined in the inGitDB database under teams (defined there: ext, logs)`,
		"nope/x/events": `collection "nope" is not defined in the inGitDB database at the root (defined there: ext, teams)`,
		"teams/t1/ext/cal/events/e1/notes/n1/x/y":    "is not a collection path",
		"teams/t1/ext/cal/events/e1/notes/n1/deeper": `collection "deeper" is not defined in the inGitDB database under teams/ext/events/notes (no collection is defined there)`,
		"teams//ext": "is not a collection path",
	} {
		_, stderr, code := runQuery(t, "", "--db", "ingitdb://"+db, "--from", from, "--no-policies")
		if code != exitCodeUsage || !strings.Contains(stderr, want) {
			t.Errorf("%s: exit %d\n%s", from, code, stderr)
		}
	}
}

func TestNestedFromRef(t *testing.T) {
	ref, nested, err := nestedFromRef("ext/billing/payments", dbcopy.BackendRef{Scheme: "firestore"})
	if err != nil || !nested || ref.Name() != "payments" || ref.Parent().String() != "ext/billing" {
		t.Fatalf("%v %v %v", ref, nested, err)
	}
	for _, test := range []struct {
		from   string
		scheme string
	}{{"payments", "firestore"}, {"public/payments", "postgres"}} {
		if _, nested, err := nestedFromRef(test.from, dbcopy.BackendRef{Scheme: test.scheme}); nested || err != nil {
			t.Errorf("%+v: %v %v", test, nested, err)
		}
	}
	saved := readInGitDBDefinition
	t.Cleanup(func() { readInGitDBDefinition = saved })
	readInGitDBDefinition = func(string, ...ingitdb.ReadOption) (*ingitdb.Definition, error) { return nil, errors.New("unreadable") }
	if _, _, err := nestedFromRef("a/b/c", dbcopy.BackendRef{Scheme: "ingitdb", Path: "x"}); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Error(err)
	}
}
