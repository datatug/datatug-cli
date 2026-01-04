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
		fsCollectionsProvider = client.Doc(collectionRef.Parent().String())
	} else {
		fsCollectionsProvider = client
	}

	iter := fsCollectionsProvider.Collections(ctx)

	for {
		var ref *firestore.CollectionRef
		if ref, err = iter.Next(); err != nil {
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
		_ = client.Close()
	}()

	var fsCollectionsProvider firestoreCollectionsProvider

	if parentKey == nil {
		fsCollectionsProvider = client
	} else {
		fsCollectionsProvider = client.Doc(parentKey.String())
	}

	iter := fsCollectionsProvider.Collections(ctx)
	for {
		var ref *firestore.CollectionRef
		if ref, err = iter.Next(); err != nil {
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
