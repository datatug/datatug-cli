package api

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

func TestLookupError(t *testing.T) {
	t.Parallel()
	cause := errors.New("open /srv/project/environments/local: no such file")
	const secretID = "postgres://alice:s3cretpw@db.example.com/shop"
	for _, tc := range []struct {
		name       string
		cause      error
		ids        []string
		want       string
		wantsCause bool
	}{
		{"plain IDs keep the cause", cause, []string{"chinook", "local"}, `database "chinook" in "local": ` + cause.Error(), true},
		{"a source string is not shown and the cause is dropped", cause, []string{secretID, "local"}, `database "` + dbcopy.SourceIDNotShown + `" in "local"`, false},
		{"the second ID decides too", cause, []string{"chinook", secretID}, `database "chinook" in "` + dbcopy.SourceIDNotShown + `"`, false},
		// SourceIDNotShown is itself text a client can send: the ID that is that text is
		// not a plain name, so the cause, which quotes the path it became, is left out.
		{"the placeholder text as an ID is not a plain name", cause, []string{dbcopy.SourceIDNotShown, "local"}, `database "` + dbcopy.SourceIDNotShown + `" in "local"`, false},
		{"no cause gives the bare message", nil, []string{"chinook", "local"}, `database "chinook" in "local"`, false},
	} {
		err := LookupError("database %q in %q", tc.cause, tc.ids...)
		if err.Error() != tc.want {
			t.Errorf("%s: error = %q, want %q", tc.name, err.Error(), tc.want)
		}
		if errors.Is(err, cause) != tc.wantsCause {
			t.Errorf("%s: errors.Is(err, cause) = %v, want %v", tc.name, !tc.wantsCause, tc.wantsCause)
		}
	}
}
