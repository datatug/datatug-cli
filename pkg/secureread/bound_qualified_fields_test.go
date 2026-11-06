package secureread

import (
	"context"
	"database/sql"
	"errors"
	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"testing"
)

func TestBoundQualifiedOpaqueGrantKeepsFieldRestrictions(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	native, err := sql.Open("sqlite", f.target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = native.Exec("ALTER TABLE A RENAME TO Customer"); err != nil {
		t.Fatal(err)
	}
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	policy := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: customer-fields}
default: deny
scopes:
  - path: /Customer
    rules: [{id: id-only, effect: allow, operations: [query], fields: [id]}]
  - opaqueQuery: true
    rules: [{id: opaque, effect: allow, operations: [query]}]
`
	writeBindingFile(t, f.policy, []byte(policy))
	executor := NewExecutor(f.load(t))
	old := openBoundSourceFn
	calls := 0
	t.Cleanup(func() { openBoundSourceFn = old })
	openBoundSourceFn = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, func(), error) {
		db, closeDB, err := old(ctx, ref)
		if err != nil {
			return nil, closeDB, err
		}
		return phase1DispatchDB{DB: db, calls: &calls}, closeDB, nil
	}
	for _, tc := range []struct {
		name, tail string
		want       error
	}{
		{"allowed-id", "columns: [{field: id}]\n", nil},
		{"hidden-alias", "columns: [{field: label, as: exposed}]\n", accesspolicies.ErrInvalidQuery},
		{"hidden-filter", "columns: [{field: id}]\nwhere: {op: '==', left: {field: label}, right: {value: allowed-a}}\n", access.ErrAccessDenied},
		{"wildcard-redacted", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := []byte("from: {schema: main, name: Customer}\n" + tc.tail)
			query, err := tugql.Deserialize(doc)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := access.WithNativeTableNamespace(context.Background(), "main")
			if err != nil {
				t.Fatal(err)
			}
			lines := accesspolicies.Explain(ctx, executor.session.Policies, query, nil)
			hasFields := false
			for _, line := range lines {
				for _, fields := range line.FieldLists {
					if len(fields) == 1 && fields[0] == "id" {
						hasFields = true
					}
				}
			}
			if !hasFields {
				t.Fatal("Explain omitted canonical id field list", lines)
			}
			before := calls
			result, err := executor.RunTugQL(context.Background(), executor.session.binding.url, doc, nil)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || len(result.Rows) != 0 || calls != before {
					t.Fatal("hidden field reached native data", err, result.Rows, calls-before)
				}
				return
			}
			if err != nil || len(result.Rows) != 1 || calls != before+1 {
				t.Fatal("qualified positive failed", err, result.Rows, calls-before)
			}
			if len(result.Rows[0].Data) != 1 || result.Rows[0].Data["id"] == nil {
				t.Fatal("hidden label leaked", result.Rows)
			}
		})
	}
}
