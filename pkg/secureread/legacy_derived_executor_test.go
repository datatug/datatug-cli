package secureread

import (
	"context"
	"errors"
	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/tugql"
	"testing"
)

func TestPrivateNoPoliciesDerivedExecutorCompatibility(t *testing.T) {
	source := newSQLiteFixture(t)
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.SourceBound() {
		t.Fatal("legacy session unexpectedly bound")
	}
	query, err := tugql.Deserialize([]byte("from: {query: {from: {name: customers}, columns: [{field: id}, {field: name}], as: d}}\ncolumns: [{field: id}, {field: name}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewExecutor(session).RunStructured(context.Background(), source, query, nil)
	if err != nil || len(result.Rows) != 3 {
		t.Fatal("legacy NoPolicies derived records", len(result.Rows), err)
	}
	t.Log("actual NoPolicies unbound Executor.RunStructured derivedpositive3rows")
	denied := access.WithPolicy(context.Background(), access.MustPolicy("mandatory", access.Root(access.Deny(access.Query)), access.OpaqueQueryScope(access.Deny(access.Query))))
	if result, err = NewExecutor(session).RunStructured(denied, source, query, nil); !errors.Is(err, access.ErrAccessDenied) || len(result.Rows) != 0 {
		t.Fatal("mandatory context skipped", len(result.Rows), err)
	}
}
