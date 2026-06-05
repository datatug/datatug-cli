package firestoreschema

import (
	"context"
	"errors"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"google.golang.org/api/iterator"
)

// seams – replaced by tests to avoid requiring a live Firestore backend
var (
	firestoreDoc = func(client *firestore.Client, path string) firestoreCollectionsProvider {
		return client.Doc(path)
	}
	firestoreCollections = func(ctx context.Context, p firestoreCollectionsProvider) *firestore.CollectionIterator {
		return p.Collections(ctx)
	}
	iterCollectionNext = func(iter *firestore.CollectionIterator) (*firestore.CollectionRef, error) {
		return iter.Next()
	}
	closeFirestoreClient = func(c *firestore.Client) error {
		return c.Close()
	}
)

type GetClient func(ctx context.Context) (client *firestore.Client, err error)

func NewProvider(getConnection GetClient) schemers.Provider {
	return &simpleProvider{
		getClient: getConnection,
	}
}

type simpleProvider struct {
	getClient GetClient
}

type firestoreCollectionsProvider interface {
	Collections(ctx context.Context) *firestore.CollectionIterator
}

func (p simpleProvider) GetCollection(ctx context.Context, collectionRef *dal.CollectionRef) (collection *schemers.Collection, err error) {
	var client *firestore.Client
	if client, err = p.getClient(ctx); err != nil {
		return
	}

	var fsCollectionsProvider firestoreCollectionsProvider

	if strings.Contains(collectionRef.Path(), "/") {
		fsCollectionsProvider = firestoreDoc(client, collectionRef.Parent().String())
	} else {
		fsCollectionsProvider = client
	}

	iter := firestoreCollections(ctx, fsCollectionsProvider)

	for {
		var ref *firestore.CollectionRef
		if ref, err = iterCollectionNext(iter); err != nil {
			if errors.Is(err, iterator.Done) {
				err = nil
				break
			}
			return
		}
		if ref.Path == collectionRef.Name() {
			return &schemers.Collection{
				ID: ref.ID,
			}, nil
		}
	}
	return
}

func (p simpleProvider) GetCollections(ctx context.Context, parentKey *dal.Key) (collections []*schemers.Collection, err error) {
	var client *firestore.Client
	if client, err = p.getClient(ctx); err != nil {
		return
	}
	defer func() {
		_ = closeFirestoreClient(client)
	}()

	var fsCollectionsProvider firestoreCollectionsProvider

	if parentKey == nil {
		fsCollectionsProvider = client
	} else {
		fsCollectionsProvider = firestoreDoc(client, parentKey.String())
	}

	iter := firestoreCollections(ctx, fsCollectionsProvider)
	for {
		var ref *firestore.CollectionRef
		if ref, err = iterCollectionNext(iter); err != nil {
			if errors.Is(err, iterator.Done) {
				err = nil
				break
			}
			return
		}
		collections = append(collections, &schemers.Collection{
			ID: ref.ID,
		})
	}
	return
}
