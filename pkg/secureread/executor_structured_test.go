package secureread

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

func customersQuery(columns ...dal.Column) dal.Query {
	return dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).SelectColumns(columns...)
}

func productsQuery() dal.Query {
	return dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("products", ""))).SelectColumns()
}

func aliceSession(t *testing.T, policyYAML string) Session {
	t.Helper()
	dir := policyDir(t, map[string]string{"p.yaml": policyYAML})
	session, err := NewSession(SessionOptions{As: "alice", PoliciesDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

// runOnBothBackends runs test against a sqlite:// and an ingitdb:// fixture
// carrying identical rows, so the matrix is proven on both DALgo adapters
// pkg/dbcopy wires — not just SQLite.
func runOnBothBackends(t *testing.T, test func(t *testing.T, sourceURL string)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { test(t, newSQLiteFixture(t)) })
	t.Run("ingitdb", func(t *testing.T) { test(t, newInGitDBFixture(t)) })
}

// TestRunStructured_UnauthorizedSource_Refused covers "unauthorised source
// refused": a policy that grants only /products must refuse a /customers
// query outright.
func TestRunStructured_UnauthorizedSource_Refused(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, sourceURL string) {
		session := aliceSession(t, productsOnlyPolicy)
		executor := NewExecutor(session)
		_, err := executor.RunStructured(context.Background(), sourceURL, customersQuery(), nil)
		if !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("RunStructured(customers, products-only policy) = %v, want ErrAccessDenied", err)
		}
	})
}

// TestRunStructured_RowConditionAppliesRegardlessOfQueryShape covers
// "restricted principal sees only permitted rows even with an alternative
// query shape": alice's policy allows only her own rows
// (ownerID == $currentUser); a query that explicitly asks for Cid's row (id
// == "c3", owned by bob) must still come back empty — the policy's row
// condition is AND-ed in, not offered as an alternative the query can pick
// instead of.
func TestRunStructured_RowConditionAppliesRegardlessOfQueryShape(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, sourceURL string) {
		session := aliceSession(t, ownerScopedPolicy)
		executor := NewExecutor(session)
		ctx := context.Background()

		wildcard, err := executor.RunStructured(ctx, sourceURL, customersQuery(), nil)
		if err != nil {
			t.Fatalf("wildcard RunStructured: %v", err)
		}
		assertOnlyAliceRows(t, wildcard)

		targetingBob := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("customers", ""))).
			WhereField("id", dal.Equal, "c3").
			SelectColumns()
		shaped, err := executor.RunStructured(ctx, sourceURL, targetingBob, nil)
		if err != nil {
			t.Fatalf("shaped RunStructured: %v", err)
		}
		if len(shaped.Rows) != 0 {
			t.Fatalf("query explicitly asking for bob's row = %d rows, want 0 (policy must still apply)", len(shaped.Rows))
		}
	})
}

func assertOnlyAliceRows(t *testing.T, result Result) {
	t.Helper()
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (alice's own customers only)", len(result.Rows))
	}
	for _, row := range result.Rows {
		if row.Data["ownerID"] != "alice" {
			t.Errorf("row ownerID = %v, want alice: %+v", row.Data["ownerID"], row.Data)
		}
	}
}

// TestRunStructured_HiddenColumnExplicit_Refused covers "requesting a hidden
// column explicitly → refusal, not silent drop" (AC hidden-column-refused):
// a policy hiding `email` must refuse a query that explicitly selects it,
// and the refusal must name only the field the caller already asked for —
// never a value.
func TestRunStructured_HiddenColumnExplicit_Refused(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, sourceURL string) {
		session := aliceSession(t, fieldRestrictedPolicy)
		executor := NewExecutor(session)
		query := customersQuery(dal.Column{Expression: dal.Field("id")}, dal.Column{Expression: dal.Field("email")})
		_, err := executor.RunStructured(context.Background(), sourceURL, query, nil)
		if !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("explicit hidden-column select = %v, want ErrAccessDenied", err)
		}
		if !strings.Contains(err.Error(), "email") {
			t.Errorf("error %q does not name the refused field", err.Error())
		}
		// Never echoes a value the policy hides (e.g. an actual email address).
		if strings.Contains(err.Error(), "@example.com") {
			t.Errorf("error %q leaks a hidden value", err.Error())
		}
	})
}

// TestRunStructured_HiddenColumn_WildcardRedactedAndLimitation covers
// "hidden column absent from structured/DTQL results": an implicit
// (wildcard) select silently drops the hidden fields per row (DALgo's own
// redaction) and the Result reports which columns were hidden, rather than
// pretending nothing was limited.
func TestRunStructured_HiddenColumn_WildcardRedactedAndLimitation(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, sourceURL string) {
		session := aliceSession(t, fieldRestrictedPolicy)
		executor := NewExecutor(session)
		result, err := executor.RunStructured(context.Background(), sourceURL, customersQuery(), nil)
		if err != nil {
			t.Fatalf("RunStructured: %v", err)
		}
		if len(result.Rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(result.Rows))
		}
		for _, row := range result.Rows {
			if _, present := row.Data["email"]; present {
				t.Errorf("row still carries email: %+v", row.Data)
			}
			if _, present := row.Data["passwordHash"]; present {
				t.Errorf("row still carries passwordHash: %+v", row.Data)
			}
		}
		hidden := findLimitation(result.Limitations, LimitationHiddenColumns)
		if hidden == nil {
			t.Fatal("no LimitationHiddenColumns in Result.Limitations")
		}
		wantHidden := map[string]bool{"email": true, "passwordHash": true}
		if len(hidden.Columns) != len(wantHidden) {
			t.Fatalf("hidden columns = %v, want %v", hidden.Columns, wantHidden)
		}
		for _, name := range hidden.Columns {
			if !wantHidden[name] {
				t.Errorf("unexpected hidden column %q", name)
			}
		}
		if findLimitation(result.Limitations, LimitationRowsFiltered) == nil {
			t.Error("row condition also applied (ownerID == alice); expected a LimitationRowsFiltered too")
		}
	})
}

func findLimitation(limitations []Limitation, kind LimitationKind) *Limitation {
	for i := range limitations {
		if limitations[i].Kind == kind {
			return &limitations[i]
		}
	}
	return nil
}

// TestRunStructured_Unrestricted_BypassesPolicies covers "Unrestricted
// bypass only when explicitly set": with Session.Unrestricted, every row
// and column comes back, including ones a loaded policy would have hidden —
// and it must not happen unless Unrestricted was deliberately set.
func TestRunStructured_Unrestricted_BypassesPolicies(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, sourceURL string) {
		session, err := NewSession(SessionOptions{NoPolicies: true})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		executor := NewExecutor(session)
		result, err := executor.RunStructured(context.Background(), sourceURL, customersQuery(), nil)
		if err != nil {
			t.Fatalf("RunStructured: %v", err)
		}
		if len(result.Rows) != 3 {
			t.Fatalf("rows = %d, want 3 (every customer, unrestricted)", len(result.Rows))
		}
		sawBob := false
		for _, row := range result.Rows {
			if row.Data["ownerID"] == "bob" {
				sawBob = true
			}
			if _, present := row.Data["email"]; !present {
				t.Errorf("unrestricted row missing email: %+v", row.Data)
			}
		}
		if !sawBob {
			t.Error("bob's row is missing from an unrestricted run")
		}
		if len(result.Limitations) != 0 {
			t.Errorf("Limitations = %+v, want none when Unrestricted", result.Limitations)
		}
	})
}

// TestRunStructured_RestrictedButNoFieldOrRowRule_NoLimitations confirms a
// policy that allows everything for a collection (no where, no fields)
// produces no Limitations for that collection — a Result must not claim a
// limitation applied when none did.
func TestRunStructured_RestrictedButNoFieldOrRowRule_NoLimitations(t *testing.T) {
	sourceURL := newSQLiteFixture(t)
	session := aliceSession(t, permissivePolicy)
	executor := NewExecutor(session)
	result, err := executor.RunStructured(context.Background(), sourceURL, productsQuery(), nil)
	if err != nil {
		t.Fatalf("RunStructured: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(result.Rows))
	}
	if len(result.Limitations) != 0 {
		t.Errorf("Limitations = %+v, want none (policy allows every product row/field)", result.Limitations)
	}
}

// TestRunDTQL_RunsThroughPolicyPath covers REQ:dtql-query-type / AC
// dtql-query-runs: a DTQL-YAML document runs through the exact same policy
// path as a structured query.
func TestRunDTQL_RunsThroughPolicyPath(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, sourceURL string) {
		session := aliceSession(t, ownerScopedPolicy)
		executor := NewExecutor(session)
		doc := []byte("from:\n  name: customers\n")
		result, err := executor.RunDTQL(context.Background(), sourceURL, doc, nil)
		if err != nil {
			t.Fatalf("RunDTQL: %v", err)
		}
		assertOnlyAliceRows(t, result)
	})
}

// TestRunDTQL_InvalidDocument_Errors confirms a malformed DTQL document
// fails before ever touching the source or the policies.
func TestRunDTQL_InvalidDocument_Errors(t *testing.T) {
	session := aliceSession(t, ownerScopedPolicy)
	executor := NewExecutor(session)
	_, err := executor.RunDTQL(context.Background(), newSQLiteFixture(t), []byte("not: [valid, dtql"), nil)
	if err == nil {
		t.Fatal("RunDTQL(invalid document) = nil error")
	}
}

// TestRunStructured_UnknownScheme_TypedError covers "unknown scheme → typed
// error" for source opening.
func TestRunStructured_UnknownScheme_TypedError(t *testing.T) {
	session := aliceSession(t, permissivePolicy)
	executor := NewExecutor(session)
	_, err := executor.RunStructured(context.Background(), "mongodb://host/db", productsQuery(), nil)
	if err == nil || !strings.Contains(err.Error(), "mongodb") || !strings.Contains(err.Error(), "sqlite") {
		t.Fatalf("RunStructured(unknown scheme) = %v, want an error naming the scheme and the supported list", err)
	}
}

// TestRunStructured_MissingSourceFile_TypedError covers S80 Fix 2's "test
// per source kind": a source URL whose file-backed path does not exist
// (`datatug serve --project` against the demo project before `datatug demo`
// has fetched its fixtures, per S77's finding) must fail with
// dbcopy.ErrSourceFileMissing — checkable via errors.Is, so
// pkg/server/endpoints can map it to SOURCE_UNAVAILABLE instead of letting
// the raw driver text reach an HTTP 500 — for both backends this Executor
// opens through pkg/dbcopy.
func TestRunStructured_MissingSourceFile_TypedError(t *testing.T) {
	tests := []struct {
		name      string
		sourceURL func(t *testing.T) string
	}{
		{"sqlite", func(t *testing.T) string { return "sqlite://" + filepath.Join(t.TempDir(), "missing.db") }},
		{"ingitdb", func(t *testing.T) string { return "ingitdb://" + filepath.Join(t.TempDir(), "missing-project") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := aliceSession(t, permissivePolicy)
			executor := NewExecutor(session)
			_, err := executor.RunStructured(context.Background(), tc.sourceURL(t), productsQuery(), nil)
			if !errors.Is(err, dbcopy.ErrSourceFileMissing) {
				t.Fatalf("RunStructured(missing %s source) = %v, want dbcopy.ErrSourceFileMissing", tc.name, err)
			}
		})
	}
}
