package api

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
)

// ErrItemNotFound is what errors.Is finds in the answer for a project item that a delete
// found missing (see deleteItem): the routes that delete an item answer it with a 404, where
// every other failure of a delete is a 500. The text of the answer is the one itemNotFound
// builds, and holds nothing of the store.
var ErrItemNotFound = errors.New("item not found")

// missingItem is the answer for an item that is not there, with the same sentence as
// itemNotFound, and is an ErrItemNotFound.
type missingItem struct{ message string }

func (e missingItem) Error() string { return e.message }

// Is makes the answer an ErrItemNotFound.
func (missingItem) Is(target error) bool { return target == ErrItemNotFound }

// itemNotFound is the answer for a project item (a board, an entity, a recordset
// definition, a db server) that the store could not load. It is built from the kind of
// item and the ID as it may be shown (see dbcopy.SourceIDDisplay) and from nothing the
// store said, whatever the failure was: the store's error quotes the path it built, which
// is no part of an answer to a client. The cause goes to the log of the server.
func itemNotFound(kind, shown string, cause error) error {
	log.Printf("api: %s %q could not be loaded: %v", kind, shown, cause)
	return fmt.Errorf("%s %q not found", kind, shown)
}

// itemsNotLoaded is the answer for the items of a kind that the store could not list for a
// project (its entities, its recordset definitions), with the same rule as itemNotFound: the
// sentence is built from the kind and the ID of the project as it may be shown, and from
// nothing the store said, whatever the failure was. The cause goes to the log of the server.
func itemsNotLoaded(kind, shownProject string, cause error) error {
	log.Printf("api: the %s of project %q could not be loaded: %v", kind, shownProject, cause)
	return fmt.Errorf("%s of project %q could not be loaded", kind, shownProject)
}

// itemWriteFailed is the answer for a project item that the store could not save or
// delete, with the same rule as itemNotFound: action is "save", "delete" or "create" (or
// "list", for what a connection could not list).
func itemWriteFailed(action, kind, shown string, cause error) error {
	log.Printf("api: could not %s %s %q: %v", action, kind, shown, cause)
	return fmt.Errorf("could not %s %s %q", action, kind, shown)
}

// deleteItem deletes a project item that the project holds. The stores of a project treat a
// delete of an item that is not there as done, and say nothing, so the item is looked up
// first (load is the store's own lookup of it, as the route that gets it does): an answer
// of the store that says it is not there (an error that is fs.ErrNotExist) is a missing
// item, whose answer is the built sentence of itemNotFound that is an ErrItemNotFound, and so
// is a delete that says the same. An item that cannot be read for any other reason is
// deleted all the same, and a delete that fails is the built sentence of
// itemWriteFailed. kind and shown name the item as in those (shown is an ID that passes the
// display of a client's ID).
func deleteItem(kind, shown string, load, remove func() error) error {
	if err := load(); err != nil && errors.Is(err, fs.ErrNotExist) {
		return missingItemAnswer(kind, shown, err)
	}
	if err := remove(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return missingItemAnswer(kind, shown, err)
		}
		return itemWriteFailed("delete", kind, shown, err)
	}
	return nil
}

// missingItemAnswer logs what the store said and returns the answer for a missing item.
func missingItemAnswer(kind, shown string, cause error) error {
	log.Printf("api: %s %q is not there: %v", kind, shown, cause)
	return missingItem{fmt.Sprintf("%s %q not found", kind, shown)}
}
