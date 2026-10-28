package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	dalgo2libsql "github.com/dal-go/dalgo2libsql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// TestQueryFailureForLibSQLShowsOnlyAFixedClassification pins the command-level
// boundary: the adapter seals its own errors, but an outer wrapper is not that
// boundary and can carry SQL, a response body or a credential. A libSQL query
// failure therefore shows only the fixed libSQL classification.
func TestQueryFailureForLibSQLShowsOnlyAFixedClassification(t *testing.T) {
	t.Parallel()
	backend := dbcopy.BackendRef{Scheme: dbcopy.LibSQLScheme}
	const secret = "s3cret-bearer-value"

	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "wrapped backend failure",
			err: fmt.Errorf("query wrapper for libsql://db.example.com: %w",
				errors.Join(dalgo2libsql.ErrBackend, errors.New("Authorization: Bearer "+secret+" select * from items"))),
			want: dbcopy.LibSQLBearerEnv,
		},
		{
			name: "unsupported native SQL",
			err:  fmt.Errorf("outer: %w", dalgo2libsql.ErrNotSupported),
			want: "no native SQL, joins, cursors or transactions",
		},
		{
			name: "cancelled",
			err:  fmt.Errorf("outer with %s: %w", secret, errors.Join(dalgo2libsql.ErrBackend, context.Canceled)),
			want: "cancelled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := queryFailureFor(backend, tc.err)
			var coder ExitCoder
			if !errors.As(err, &coder) || coder.ExitCode() != exitCodeDatabase {
				t.Fatalf("exit = %v, want the database code", err)
			}
			message := err.Error()
			if !strings.Contains(message, "libSQL read failed") || !strings.Contains(message, tc.want) {
				t.Fatalf("message = %q, want the fixed classification %q", message, tc.want)
			}
			for _, leaked := range []string{secret, "select *", "db.example.com", "Authorization"} {
				if strings.Contains(message, leaked) {
					t.Fatalf("message %q echoes %q", message, leaked)
				}
			}
		})
	}
}

// TestQueryFailureForLibSQLKeepsPolicyRefusals checks that the safe libSQL
// classification does not swallow a policy decision taken outside the adapter.
func TestQueryFailureForLibSQLKeepsPolicyRefusals(t *testing.T) {
	t.Parallel()
	backend := dbcopy.BackendRef{Scheme: dbcopy.LibSQLScheme}
	err := queryFailureFor(backend, fmt.Errorf("saved query: %w", access.ErrAccessDenied))
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != exitCodeAccessDenied {
		t.Fatalf("exit = %v, want the access-denied code", err)
	}
}
