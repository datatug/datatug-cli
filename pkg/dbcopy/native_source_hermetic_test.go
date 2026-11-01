package dbcopy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/dal-go/dalgo/dal"
	"google.golang.org/api/option"
)

// A capture backend checks the source's query contract without any RPC or server.
type firestoreCaptureDB struct {
	dal.DB
	received dal.StructuredQuery
	failure  error
}

func (d *firestoreCaptureDB) ExecuteQueryToRecordsReader(_ context.Context, q dal.Query) (dal.RecordsReader, error) {
	d.received = q.(dal.StructuredQuery)
	return nil, d.failure
}

func TestFirestoreStructuredQueryBoundaryHermetic(t *testing.T) {
	ctx := context.Background()
	marker := errors.New("provider failed")
	capture := &firestoreCaptureDB{failure: marker}
	source := firestoreQuerySource{capture}
	base := dal.NewRootCollectionRef("customers", "")
	query := dal.From(base).NewQuery().Where(dal.WhereField("score", dal.GreaterOrEqual, 40)).OrderBy(dal.Descending(dal.Field("score"))).Limit(2).Offset(1).SelectColumns(dal.Column{Expression: dal.Field("name")})
	_, err := source.ExecuteQueryToRecordsReader(ctx, query)
	if !errors.Is(err, marker) {
		t.Fatalf("provider error lost: %v", err)
	}
	got := capture.received
	if got.Limit() != 2 || got.Offset() != 1 || !reflect.DeepEqual(got.Where(), query.Where()) || !reflect.DeepEqual(got.OrderBy(), query.OrderBy()) || !reflect.DeepEqual(got.Columns(), query.Columns()) {
		t.Fatalf("query changed: %v", got)
	}
	a, b := got.IntoRecord(), got.IntoRecord()
	if a.Key().IDKind != reflect.String {
		t.Fatal("document IDs must be strings")
	}
	a.Data().(map[string]any)["name"] = "Alice"
	if len(b.Data().(map[string]any)) != 0 {
		t.Fatal("records share mutable data")
	}
	for _, tc := range []struct {
		name  string
		query dal.Query
	}{
		{"text", dal.NewTextQuery("SELECT * FROM customers", nil)},
		{"computed", dal.From(base).NewQuery().SelectColumns(dal.Column{Expression: dal.NewConstant(1)})},
		{"joined", dal.From(base).Join(dal.NewJoinedSource(dal.NewRootCollectionRef("orders", ""), dal.JoinInner, dal.WhereField("id", dal.Equal, 1))).NewQuery().SelectColumns()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture.received = nil
			_, err := source.ExecuteQueryToRecordsReader(ctx, tc.query)
			if !errors.Is(err, dal.ErrNotSupported) || capture.received != nil {
				t.Fatalf("unsupported query reached provider: %v", err)
			}
		})
	}
}

func TestFirestoreOpenCacheAndFailureHermetic(t *testing.T) {
	original := newFirestoreSourceClient
	defer func() { newFirestoreSourceClient = original }()
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:1")
	// The SDK constructor creates the client locally. No query or other RPC runs.
	client, err := firestore.NewClientWithDatabase(context.Background(), "demo-hermetic", "named-db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	calls := 0
	newFirestoreSourceClient = func(_ context.Context, project, database string, _ ...option.ClientOption) (*firestore.Client, error) {
		calls++
		if project != "demo-hermetic" || database != "named-db" {
			t.Fatalf("wrong destination %s/%s", project, database)
		}
		return client, nil
	}
	ref, err := Parse("firestore://demo-hermetic/named-db")
	if err != nil {
		t.Fatal(err)
	}
	key := ref.Path + "@127.0.0.1:1"
	defer func() { firestoreSources.Lock(); delete(firestoreSources.dbs, key); firestoreSources.Unlock() }()
	first, err := ref.OpenProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ref.Open(context.Background())
	if err != nil || first != second || calls != 1 {
		t.Fatalf("cache failed: %v calls=%d", err, calls)
	}
	if first.Adapter().Name() != "firestore" {
		t.Fatal(first.Adapter())
	}
	marker := errors.New("constructor failed")
	newFirestoreSourceClient = func(context.Context, string, string, ...option.ClientOption) (*firestore.Client, error) {
		return nil, marker
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:2")
	if _, err := ref.Open(context.Background()); !errors.Is(err, marker) {
		t.Fatalf("constructor error lost: %v", err)
	}
}

func TestMongoDBOpenCanceledContextHermetic(t *testing.T) {
	const uri = "mongodb://synthetic-secret-user:synthetic-secret-password@127.0.0.1:1/hermetic?authSource=synthetic-secret-auth"
	ref, err := Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("synthetic-secret-context-cause")
	canceled, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	// A past deadline is already expired, so this case needs no sleep or RPC.
	expired, stopDeadline := context.WithDeadlineCause(context.Background(), time.Unix(1, 0), cause)
	defer stopDeadline()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		want  error
		other error
	}{
		{"canceled", canceled, context.Canceled, context.DeadlineExceeded},
		{"deadline", expired, context.DeadlineExceeded, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := ref.OpenProtected(tc.ctx)
			if db != nil || !errors.Is(err, tc.want) || errors.Is(err, tc.other) {
				t.Fatalf("context failure lost: db=%v error=%v", db, err)
			}
			if errors.Is(err, cause) {
				t.Fatal("custom context cause escaped the provider boundary")
			}
			for _, secret := range []string{uri, "synthetic-secret-user", "synthetic-secret-password", "synthetic-secret-auth", cause.Error()} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("context failure exposed a credential or custom cause")
				}
			}
		})
	}
}
