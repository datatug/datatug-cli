package dbmirror

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file is the only code of the package that holds a Firestore client. It
// calls only reading methods of the SDK (Collections, Documents, Get and
// aggregation queries); TestNoWriteCallInPackage fails if a writing method is
// named anywhere in the package.

var newFirestoreClient = firestore.NewClientWithDatabase

// FirestoreSource reads one Firestore database through the Google SDK, with
// the credentials the SDK finds in the operator's environment (Application
// Default Credentials, or FIRESTORE_EMULATOR_HOST for an emulator).
type FirestoreSource struct {
	client *firestore.Client
}

var _ Source = (*FirestoreSource)(nil)

// OpenFirestore opens a reader of database (use "(default)") of project.
func OpenFirestore(ctx context.Context, project, database string) (*FirestoreSource, error) {
	client, err := newFirestoreClient(ctx, project, database)
	if err != nil {
		return nil, err
	}
	return &FirestoreSource{client: client}, nil
}

// Close releases the client.
func (s *FirestoreSource) Close() error { return s.client.Close() }

// RootCollections implements Source.
func (s *FirestoreSource) RootCollections(ctx context.Context) ([]string, error) {
	var ids []string
	it := s.client.Collections(ctx)
	for {
		ref, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, ref.ID)
	}
}

// query builds q without its window.
func (s *FirestoreSource) query(q Query) firestore.Query {
	var query firestore.Query
	if q.Group {
		query = s.client.CollectionGroup(q.Collection).Query
		if q.Under != "" {
			// Every document below Under sorts after Under and before
			// Under+"" (document names compare segment by segment).
			query = query.Where(firestore.DocumentID, ">=", s.client.Doc(q.Under)).
				Where(firestore.DocumentID, "<", s.client.Doc(q.Under+""))
		}
	} else {
		query = s.client.Collection(q.Collection).Query
	}
	return query.OrderBy(firestore.DocumentID, firestore.Asc)
}

// Page implements Source.
func (s *FirestoreSource) Page(ctx context.Context, q Query) ([]Doc, error) {
	query := s.query(q).Limit(q.Limit)
	if q.After != "" {
		query = query.StartAfter(s.client.Doc(q.After))
	}
	snapshots, err := query.Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	docs := make([]Doc, len(snapshots))
	for i, snapshot := range snapshots {
		docs[i] = Doc{Path: relativePath(snapshot.Ref.Path), Data: snapshot.Data()}
	}
	return docs, nil
}

// Get implements Source.
func (s *FirestoreSource) Get(ctx context.Context, path string) (*Doc, error) {
	ref := s.client.Doc(path)
	if ref == nil {
		return nil, fmt.Errorf("%q is not a document path", path)
	}
	snapshot, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Doc{Path: path, Data: snapshot.Data()}, nil
}

// Count implements Source.
func (s *FirestoreSource) Count(ctx context.Context, q Query) (int64, error) {
	query := s.query(q)
	result, err := query.NewAggregationQuery().WithCount("n").Get(ctx)
	if err != nil {
		return 0, err
	}
	value, ok := result["n"].(*firestorepb.Value)
	if !ok {
		return 0, fmt.Errorf("firestore returned no count")
	}
	return value.GetIntegerValue(), nil
}
