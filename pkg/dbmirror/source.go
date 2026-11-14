package dbmirror

import (
	"context"
	"time"
)

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
	// Collection is a collection path (a root collection id, or a/1/b for the
	// collection b of the document a/1), or, for a group, a collection id.
	Collection string
	// Group reads every collection named Collection, at any depth.
	Group bool
	// Under, when set, keeps a group query to the documents at or below this
	// document path: a range on the document name, so documents of a sibling
	// whose id starts with the same text (a/1x below a/1) are not read.
	Under string
	// After continues the read after this document path.
	After string
	// Limit is, for Page, the most documents to return (> 0); for Count, the
	// most documents to count (0: all of them).
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
	// Count returns how many documents q (without After) matches, counting at
	// most q.Limit of them when it is set.
	Count(ctx context.Context, q Query) (int64, error)
}

// snapshotSource is a Source that can read every document as it was at one
// time (Options.Snapshot).
type snapshotSource interface {
	ReadAt(t time.Time)
}
