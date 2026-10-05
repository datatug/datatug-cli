package api

import (
	"errors"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// LookupError builds the error for a lookup of IDs that a client or a flag
// supplied (an environment, a database, a catalog). format has one %q for each
// of ids, in order.
//
// A caller may put anything in an ID field, a whole source string with its
// password included, and the error of a lookup that failed quotes the path the
// ID was turned into. So each ID is shown only when it is a plain name (see
// dbcopy.SourceIDDisplay), and cause, the error of the lookup, is wrapped only
// when every ID is one: otherwise the message says what was not found with the
// IDs not shown, and cause is left out (so errors.Is no longer sees it, which is
// the price of not printing it). A nil cause gives the bare message.
func LookupError(format string, cause error, ids ...string) error {
	shown := make([]any, len(ids))
	plain := true
	for i, id := range ids {
		shown[i] = dbcopy.SourceIDDisplay(id)
		plain = plain && shown[i] == id
	}
	message := fmt.Sprintf(format, shown...)
	if cause == nil || !plain {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, cause)
}
