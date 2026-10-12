package gcloudui

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/firestore"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// offlineClient returns a real Firestore client that talks to nothing: no
// credentials, and an endpoint nobody listens on.
func offlineClient(t *testing.T) *firestore.Client {
	t.Helper()
	client, err := firestore.NewClient(context.Background(), "offline",
		option.WithoutAuthentication(),
		option.WithEndpoint("127.0.0.1:1"),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	require.NoError(t, err)
	return client
}

func TestNewFirestoreClient(t *testing.T) {
	_, err := newFirestoreClient(context.Background(), "")
	assert.EqualError(t, err, "project ID is empty")

	oldNew, oldTS := firestoreNewClient, gauthTokenSource
	t.Cleanup(func() { firestoreNewClient, gauthTokenSource = oldNew, oldTS })
	signedIn := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "a"})

	t.Run("not signed in falls back to ADC", func(t *testing.T) {
		gauthTokenSource = func(context.Context, []string) (oauth2.TokenSource, error) {
			return nil, gauth.ErrNotSignedIn
		}
		var got string
		var opts int
		firestoreNewClient = func(_ context.Context, projectID string, o ...option.ClientOption) (*firestore.Client, error) {
			got, opts = projectID, len(o)
			return nil, errors.New("offline")
		}
		_, err := newFirestoreClient(context.Background(), "alpha-1")
		assert.EqualError(t, err, "offline")
		assert.Equal(t, "alpha-1", got)
		assert.Zero(t, opts)
	})

	t.Run("signed in uses the token source", func(t *testing.T) {
		var gotScopes []string
		gauthTokenSource = func(_ context.Context, scopes []string) (oauth2.TokenSource, error) {
			gotScopes = scopes
			return signedIn, nil
		}
		client := offlineClient(t)
		t.Cleanup(func() { closeFirestoreClient(client) })
		var opts int
		firestoreNewClient = func(_ context.Context, _ string, o ...option.ClientOption) (*firestore.Client, error) {
			opts = len(o)
			return client, nil
		}
		got, err := newFirestoreClient(context.Background(), "alpha-1")
		require.NoError(t, err)
		assert.Same(t, client, got)
		assert.Equal(t, 1, opts)
		assert.Equal(t, firestoreScopes, gotScopes)
	})

	t.Run("rejected token falls back to ADC", func(t *testing.T) {
		gauthTokenSource = func(context.Context, []string) (oauth2.TokenSource, error) { return signedIn, nil }
		var calls []int
		firestoreNewClient = func(_ context.Context, _ string, o ...option.ClientOption) (*firestore.Client, error) {
			calls = append(calls, len(o))
			return nil, errors.New("invalid_grant")
		}
		_, err := newFirestoreClient(context.Background(), "alpha-1")
		assert.EqualError(t, err, "invalid_grant")
		assert.Equal(t, []int{1, 0}, calls)
	})
}

func TestCloseFirestoreClient(t *testing.T) {
	closeFirestoreClient(nil)
	closeFirestoreClient(offlineClient(t))
}

func TestOpenDocuments_ReadsThroughTheClient(t *testing.T) {
	client := offlineClient(t)
	t.Cleanup(func() { closeFirestoreClient(client) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no attempt to reach the endpoint
	rows, err := readDocuments(openDocuments(ctx, client, "users"))
	assert.Error(t, err)
	assert.Empty(t, rows)
}

func TestReadDocuments(t *testing.T) {
	rows, err := readDocuments(&fakeIterator{snaps: []*firestore.DocumentSnapshot{snapshot("a")}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "a", rows[0].id)
	assert.Nil(t, rows[0].data)

	rows, err = readDocuments(&fakeIterator{snaps: []*firestore.DocumentSnapshot{snapshot("a")}, err: errors.New("cut off")})
	assert.EqualError(t, err, "cut off")
	assert.Nil(t, rows)
}

func TestProjectContext(t *testing.T) {
	ctx := projectCtx(nil)
	assert.NotNil(t, ctx.Schema())
	assert.Equal(t, "alpha-1", ctx.projectID())
	assert.Equal(t, "", (&CGProjectContext{}).projectID())

	// The schema opens its Firestore client through newFirestoreClientFunc.
	old := newFirestoreClientFunc
	t.Cleanup(func() { newFirestoreClientFunc = old })
	var asked string
	newFirestoreClientFunc = func(_ context.Context, projectID string) (*firestore.Client, error) {
		asked = projectID
		return nil, errors.New("offline")
	}
	_, err := ctx.Schema().GetCollections(context.Background(), nil)
	assert.Error(t, err)
	assert.Equal(t, "alpha-1", asked)
}
