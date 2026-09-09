package secureread

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

// TestRunNativeSQL_Labelled covers AC native-sql-labelled: a SQL-text query
// executes and the Result always carries the "row/column policies not
// applied to native SQL" limitation when the session is secured.
func TestRunNativeSQL_Labelled(t *testing.T) {
	sourceURL := newSQLiteFixture(t)
	session := aliceSession(t, opaqueSQLAllowedPolicy)
	executor := NewExecutor(session)
	result, err := executor.RunNativeSQL(context.Background(), sourceURL, "SELECT name, price FROM products ORDER BY name")
	if err != nil {
		t.Fatalf("RunNativeSQL: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(result.Rows))
	}
	label := findLimitation(result.Limitations, LimitationNativeSQL)
	if label == nil {
		t.Fatal("no LimitationNativeSQL in Result.Limitations")
	}
	if label.Policy != "native-sql" {
		t.Errorf("Policy = %q, want native-sql", label.Policy)
	}
	if !strings.Contains(label.Note, "row/column policies not applied to native SQL") {
		t.Errorf("Note = %q", label.Note)
	}
}

// TestRunNativeSQL_Unauthorized_Refused covers "unauthorised source
// refused" for native SQL: without an access.OpaqueQueryScope rule granting
// it, the request is denied before the SQL ever runs — source-level ACL is
// still enforced even though row/column policy is not.
func TestRunNativeSQL_Unauthorized_Refused(t *testing.T) {
	sourceURL := newSQLiteFixture(t)
	session := aliceSession(t, ownerScopedPolicy) // no opaqueQuery scope
	executor := NewExecutor(session)
	_, err := executor.RunNativeSQL(context.Background(), sourceURL, "SELECT * FROM products")
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("RunNativeSQL without an opaque-query grant = %v, want ErrAccessDenied", err)
	}
}

// TestRunNativeSQL_ReadOnlyEnforced proves the SQLite session is pinned
// read-only at the engine level (PRAGMA query_only): a write attempt inside
// the SQL text fails, and the data is unchanged afterwards.
func TestRunNativeSQL_ReadOnlyEnforced(t *testing.T) {
	sourceURL := newSQLiteFixture(t)
	session := aliceSession(t, opaqueSQLAllowedPolicy)
	executor := NewExecutor(session)
	ctx := context.Background()

	_, err := executor.RunNativeSQL(ctx, sourceURL, "DELETE FROM products")
	if err == nil {
		t.Fatal("DELETE through RunNativeSQL succeeded, want a read-only failure")
	}

	// The table must be untouched: a plain structured read (its own,
	// separate connection) still sees every row.
	admin, adminErr := NewSession(SessionOptions{NoPolicies: true})
	if adminErr != nil {
		t.Fatalf("NewSession: %v", adminErr)
	}
	check, checkErr := NewExecutor(admin).RunStructured(ctx, sourceURL, productsQuery(), nil)
	if checkErr != nil {
		t.Fatalf("verify RunStructured: %v", checkErr)
	}
	if len(check.Rows) != 3 {
		t.Fatalf("products rows after a refused DELETE = %d, want 3 (unchanged)", len(check.Rows))
	}
}

// TestRunNativeSQL_UnsupportedScheme_TypedError covers a source scheme with
// no SQL-text execution surface (ingitdb:// executes only
// dal.StructuredQuery).
func TestRunNativeSQL_UnsupportedScheme_TypedError(t *testing.T) {
	sourceURL := newInGitDBFixture(t)
	session := aliceSession(t, opaqueSQLAllowedPolicy)
	executor := NewExecutor(session)
	_, err := executor.RunNativeSQL(context.Background(), sourceURL, "SELECT 1")
	if !errors.Is(err, ErrNativeSQLUnsupported) {
		t.Fatalf("RunNativeSQL(ingitdb) = %v, want ErrNativeSQLUnsupported", err)
	}
}

// TestRunNativeSQL_NamedArgBindsThroughDriver proves a named
// dal.QueryArg{Name, Value} reaches the SQLite driver as a real bind value
// (dal-go/dalgo2sql v0.11.7+, sql.Named) rather than failing or being
// substituted into the SQL text by hand: an "@name" placeholder in sqlText
// binds to exactly the row that value names, no more, no less.
func TestRunNativeSQL_NamedArgBindsThroughDriver(t *testing.T) {
	sourceURL := newSQLiteFixture(t)
	session := aliceSession(t, opaqueSQLAllowedPolicy)
	executor := NewExecutor(session)
	result, err := executor.RunNativeSQL(context.Background(), sourceURL,
		"SELECT name, price FROM products WHERE name = @name",
		dal.QueryArg{Name: "name", Value: "book"})
	if err != nil {
		t.Fatalf("RunNativeSQL: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(result.Rows), result.Rows)
	}
	if got := result.Rows[0].Data["name"]; got != "book" {
		t.Errorf("name = %v, want book", got)
	}
	if got := result.Rows[0].Data["price"]; got != float64(10) {
		t.Errorf("price = %v (%T), want 10", got, got)
	}
}

// TestRunNativeSQL_Unrestricted_NoLabelNeeded covers the Unrestricted
// bypass for native SQL too: nothing is enforced, so the "policies not
// applied" note (which exists to warn about *skipped* policy) is pointless
// noise and is omitted.
func TestRunNativeSQL_Unrestricted_NoLabelNeeded(t *testing.T) {
	sourceURL := newSQLiteFixture(t)
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	executor := NewExecutor(session)
	result, err := executor.RunNativeSQL(context.Background(), sourceURL, "SELECT * FROM products")
	if err != nil {
		t.Fatalf("RunNativeSQL: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(result.Rows))
	}
	if findLimitation(result.Limitations, LimitationNativeSQL) != nil {
		t.Error("LimitationNativeSQL present on an Unrestricted run")
	}
}
