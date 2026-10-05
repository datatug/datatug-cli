package api

import (
	"fmt"
	"log"
)

// itemNotFound is the answer for a project item (a board, an entity, a recordset
// definition, a db server) that the store could not load. It is built from the kind of
// item and the ID as it may be shown (see dbcopy.SourceIDDisplay) and from nothing the
// store said, whatever the failure was: the store's error quotes the path it built, which
// is no part of an answer to a client. The cause goes to the log of the server.
func itemNotFound(kind, shown string, cause error) error {
	log.Printf("api: %s %q could not be loaded: %v", kind, shown, cause)
	return fmt.Errorf("%s %q not found", kind, shown)
}

// itemWriteFailed is the answer for a project item that the store could not save or
// delete, with the same rule as itemNotFound: action is "save", "delete" or "create".
func itemWriteFailed(action, kind, shown string, cause error) error {
	log.Printf("api: could not %s %s %q: %v", action, kind, shown, cause)
	return fmt.Errorf("could not %s %s %q", action, kind, shown)
}
