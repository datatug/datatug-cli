package dbmirror

import "context"

// Doc is one document read from the source.
type Doc struct {
	// Path is the document path relative to the database (a/1/b/2).
	Path string
	// Data holds the fields as the Google Firestore SDK returns them.
	Data map[string]any
}

// Query reads the documents of one collection, or of every collection with
// one id (a collection group), in document-path order.
type Query struct {
	// Collection is a root collection id, or a collection id for a group.
	Collection string
	// Group reads every collection named Collection, at any depth.
	Group bool
	// Under, when set, keeps a group query to the documents below this
	// document path (a range on the document name; the engine still filters).
	Under string
	// After continues the read after this document path.
	After string
	// Limit is the most documents to return (> 0).
	Limit int
}

// Source reads a Firestore database. Every method only reads: there is no
// method that could change the source, so the engine cannot write to it.
type Source interface {
	// RootCollections lists the ids of the root collections.
	RootCollections(ctx context.Context) ([]string, error)
	// Page returns at most q.Limit documents of q, in document-path order.
	Page(ctx context.Context, q Query) ([]Doc, error)
	// Get returns the document at path, or nil when there is none.
	Get(ctx context.Context, path string) (*Doc, error)
	// Count returns how many documents q (without its Limit and After) matches.
	Count(ctx context.Context, q Query) (int64, error)
}
