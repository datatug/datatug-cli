package gcloudui

import (
	"context"
	"errors"

	"cloud.google.com/go/firestore"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// firestoreScopes are the OAuth2 scopes the Firestore screens need.
var firestoreScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/datastore",
}

// Seams over the network, the keychain and the browser, replaced in tests.
var (
	newFirestoreClientFunc    = newFirestoreClient
	firestoreNewClient        = firestore.NewClient
	gauthTokenSource          = gauth.TokenSource
	startInteractiveLoginFunc = gauth.StartInteractiveLogin
	deleteRefreshTokenFunc    = gauth.DeleteRefreshToken
	openDocuments             = func(ctx context.Context, client *firestore.Client, collectionID string) documentIterator {
		return client.Collection(collectionID).Limit(maxDocuments).Documents(ctx)
	}
)

// maxDocuments is how many documents of a collection are shown.
const maxDocuments = 100

// newFirestoreClient builds a Firestore client for a project. It authenticates
// with the refresh token stored by the Google sign-in when there is one, and
// falls back to Application Default Credentials when not signed in or when the
// stored token is rejected.
func newFirestoreClient(ctx context.Context, projectID string) (*firestore.Client, error) {
	if projectID == "" {
		return nil, errors.New("project ID is empty")
	}
	if ts, err := gauthTokenSource(ctx, firestoreScopes); err == nil {
		if client, err := firestoreNewClient(ctx, projectID, option.WithTokenSource(ts)); err == nil {
			return client, nil
		}
	}
	return firestoreNewClient(ctx, projectID)
}

// closeFirestoreClient closes a client; a nil client is ignored.
func closeFirestoreClient(c *firestore.Client) {
	if c != nil {
		_ = c.Close()
	}
}

// documentIterator is what the documents of a collection are read from.
type documentIterator interface {
	Next() (*firestore.DocumentSnapshot, error)
}

// firestoreDocRow is a document: its ID and fields.
type firestoreDocRow struct {
	id   string
	data map[string]any
}

// readDocuments reads all the documents of an iterator.
func readDocuments(iter documentIterator) ([]firestoreDocRow, error) {
	var rows []firestoreDocRow
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, firestoreDocRow{id: snap.Ref.ID, data: snap.Data()})
	}
}
