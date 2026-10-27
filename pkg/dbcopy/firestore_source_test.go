package dbcopy

import (
	"cloud.google.com/go/firestore"
	"context"
	"errors"
	"github.com/dal-go/dalgo/dal"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFirestoreSourceURL(t *testing.T) {
	for _, tc := range []struct{ source, project, database string }{
		{"firestore://my-project", "my-project", "(default)"},
		{"firestore://my-project/(default)", "my-project", "(default)"},
		{"firestore://my-project/custom-db", "my-project", "custom-db"},
	} {
		r, err := Parse(tc.source)
		if err != nil {
			t.Fatal(err)
		}
		if r.ProjectID != tc.project || r.DatasetID != tc.database {
			t.Fatalf("wrong selectors: %+v", r)
		}
		if _, err = r.OpenForWrite(context.Background()); err == nil {
			t.Fatal("write target accepted")
		}
	}
	for _, source := range []string{"firestore://", "firestore://p/db/nested", "firestore://user:secret@p/db", "firestore://p/db?credentials=secret", "firestore://p/db#secret", "firestore://p:8080/db"} {
		_, err := Parse(source)
		if err == nil {
			t.Fatalf("accepted %s", source)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("leaked secret: %v", err)
		}
	}
}

// This uses a real Firestore emulator, never a production project. Run with
// FIRESTORE_EMULATOR_HOST set to the task-owned emulator endpoint.
func TestFirestoreSourceEmulator(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := firestore.NewClient(ctx, "demo-datatug-source-test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	collection := "source_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	doc := client.Collection(collection).Doc("alice")
	if _, err = doc.Set(ctx, map[string]any{"name": "Alice", "score": int64(42)}); err != nil {
		t.Fatal(err)
	}
	defer doc.Delete(context.Background())
	source, err := Parse("firestore://demo-datatug-source-test")
	if err != nil {
		t.Fatal(err)
	}
	db, err := source.OpenProtected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	query := dal.From(dal.NewRootCollectionRef(collection, "")).NewQuery().Where(dal.NewComparison(dal.Field("score"), dal.GreaterOrEqual, dal.NewConstant(40))).Limit(1).SelectColumns()
	reader, err := db.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	row, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if row.Key().ID != "alice" || row.Data().(map[string]any)["name"] != "Alice" {
		t.Fatalf("wrong row: %v %v", row.Key(), row.Data())
	}
	if _, err = reader.Next(); !errors.Is(err, dal.ErrNoMoreRecords) {
		t.Fatalf("end: %v", err)
	}
}
