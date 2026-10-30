package dtviewers

import "github.com/dal-go/dalgo/dal"

// CollectionContext is a collection of a database.
type CollectionContext struct {
	DbContext
	CollectionRef dal.CollectionRef
}
