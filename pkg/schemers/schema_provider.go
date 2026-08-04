package schemers

import (
	"context"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

type Provider interface {
	GetCollection(ctx context.Context, collectionRef *dal.CollectionRef) (*Collection, error)
	GetCollections(ctx context.Context, parentKey *record.Key) ([]*Collection, error)
}

type Collection struct {
	ID string
}
