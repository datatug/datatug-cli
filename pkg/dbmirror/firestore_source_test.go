package dbmirror

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/type/latlng"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func openFake(t *testing.T) (*fakeFirestore, *FirestoreSource) {
	t.Helper()
	fake := startFakeFirestore(t, "demo-mirror")
	source, err := OpenFirestore(context.Background(), "demo-mirror", "(default)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return fake, source
}

func TestFirestoreSourceReads(t *testing.T) {
	fake, source := openFake(t)
	ctx := context.Background()
	fake.put("teams/t1", map[string]any{"title": "one", "at": created, "bytes": []byte("b"), "ref": refTo("teams/t2"),
		"geo": &latlng.LatLng{Latitude: 1, Longitude: 2}, "list": []any{1, "x"}, "map": map[string]any{"n": nil, "f": 1.5, "b": true}})
	fake.put("teams/t2", map[string]any{"title": "two"})
	fake.put("teams/t1/ext/a/events/e1", map[string]any{"title": "e1"})
	fake.put("teams/t1/ext/a/events/e2", map[string]any{"title": "e2"})
	fake.put("teams/t2/ext/a/events/e3", map[string]any{"title": "e3"})
	fake.put("teams/t1x/ext/a/events/e4", map[string]any{"title": "a sibling's id starts with t1"})
	fake.put("teams/t1-2/ext/a/events/e5", map[string]any{"title": "and so does this one's"})

	roots, err := source.RootCollections(ctx)
	if err != nil || !slices.Equal(roots, []string{"teams"}) {
		t.Fatal(roots, err)
	}
	source.ReadAt(created) // the fake ignores the read time; the SDK sends it
	if roots, err = source.RootCollections(ctx); err != nil {
		t.Fatal(roots, err)
	}
	docs, err := source.Page(ctx, Query{Collection: "teams", Limit: 1})
	if err != nil || len(docs) != 1 || docs[0].Path != "teams/t1" {
		t.Fatal(docs, err)
	}
	data := docs[0].Data
	if data["at"].(time.Time).UTC() != created || string(data["bytes"].([]byte)) != "b" || data["ref"].(*firestore.DocumentRef).ID != "t2" {
		t.Errorf("%#v", data)
	}
	docs, err = source.Page(ctx, Query{Collection: "teams", Limit: 5, After: "teams/t1"})
	if err != nil || len(docs) != 1 || docs[0].Path != "teams/t2" {
		t.Fatal(docs, err)
	}
	docs, err = source.Page(ctx, Query{Collection: "events", Group: true, Under: "teams/t1", Limit: 5})
	if err != nil || len(docs) != 2 || docs[1].Path != "teams/t1/ext/a/events/e2" {
		t.Fatal(docs, err)
	}
	n, err := source.Count(ctx, Query{Collection: "events", Group: true})
	if err != nil || n != 5 {
		t.Fatal(n, err)
	}
	if n, err = source.Count(ctx, Query{Collection: "events", Group: true, Limit: 2}); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, err = source.Count(ctx, Query{Collection: "teams/t1/ext/a/events"}); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	doc, err := source.Get(ctx, "teams/t2")
	if err != nil || doc == nil || doc.Data["title"] != "two" {
		t.Fatal(doc, err)
	}
	if doc, err = source.Get(ctx, "teams/none"); err != nil || doc != nil {
		t.Fatal(doc, err)
	}
	if _, err = source.Get(ctx, "teams"); err == nil {
		t.Error("read a collection path as a document")
	}
}

func TestFirestoreSourceFailures(t *testing.T) {
	fake, source := openFake(t)
	ctx := context.Background()
	unavailable := status.Error(codes.InvalidArgument, "refused")
	fake.failNext["ListCollectionIds"] = unavailable
	if _, err := source.RootCollections(ctx); err == nil {
		t.Error("RootCollections")
	}
	fake.failNext["RunQuery"] = unavailable
	if _, err := source.Page(ctx, Query{Collection: "teams", Limit: 1}); err == nil {
		t.Error("Page")
	}
	fake.failNext["BatchGetDocuments"] = status.Error(codes.PermissionDenied, "no")
	if _, err := source.Get(ctx, "teams/t1"); err == nil {
		t.Error("Get")
	}
	fake.failNext["RunAggregationQuery"] = unavailable
	if _, err := source.Count(ctx, Query{Collection: "teams"}); err == nil {
		t.Error("Count")
	}
	fake.noCount = true
	if _, err := source.Count(ctx, Query{Collection: "teams"}); err == nil || !strings.Contains(err.Error(), "no count") {
		t.Errorf("%v", err)
	}
	withSeam(t, &newFirestoreClient, func(context.Context, string, string, ...optionClientOption) (*firestore.Client, error) {
		return nil, errBoom
	})
	if _, err := OpenFirestore(ctx, "p", "d"); err != errBoom {
		t.Error(err)
	}
}

// TestFirestoreSourceSendsNoWrite runs a whole mirror through the Firestore
// source against a server that refuses and counts every write RPC.
func TestFirestoreSourceSendsNoWrite(t *testing.T) {
	fake, source := openFake(t)
	for path, data := range exampleSource().docs {
		fake.put(path, toFakeData(data))
	}
	report, out := runExample(t, source, Options{PageSize: 2})
	if len(fake.writes) != 0 {
		t.Fatalf("write RPCs sent: %v", fake.writes)
	}
	if report.Records != 10 || len(report.Outside) != 2 || readFile(t, filepath.Join(out, "teams/t1/ext/cal/events/e1.json")) != "{\n  \"at\": \"2026-11-01T10:30:00.000000Z\",\n  \"title\": \"Event\"\n}\n" {
		t.Errorf("%d records", report.Records)
	}
	if _, _ = runExample(t, source, Options{DryRun: true, Under: "teams/t1"}); len(fake.writes) != 0 {
		t.Fatalf("write RPCs sent: %v", fake.writes)
	}
}

// toFakeData turns SDK-typed example data into fake server values.
func toFakeData(data map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range data {
		switch typed := value.(type) {
		case int64:
			out[key] = int(typed)
		case map[string]any:
			out[key] = toFakeData(typed)
		case []any:
			items := make([]any, len(typed))
			for i, item := range typed {
				if m, ok := item.(map[string]any); ok {
					item = toFakeData(m)
				}
				items[i] = item
			}
			out[key] = items
		default:
			out[key] = value
		}
	}
	return out
}

// writeMethods are the Google Firestore SDK methods that change data.
var writeMethods = []string{"Set", "Create", "Update", "Delete", "Commit", "Batch", "BulkWriter", "RunTransaction", "Add", "NewDoc", "BatchWrite"}

// TestNoWriteCallInPackage checks the source text: no code of this package
// calls a method named like a writing method of the Firestore SDK.
func TestNoWriteCallInPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(parsed, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && slices.Contains(writeMethods, selector.Sel.Name) {
					t.Errorf("%s calls %s", file, selector.Sel.Name)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source file checked")
	}
	if _, err := os.Stat("firestore_source.go"); err != nil {
		t.Fatal(err)
	}
}

// TestFirestoreClientRefusesWrites: the client a source opens cannot send a
// write, whatever code holds it: the request is refused before it is sent.
func TestFirestoreClientRefusesWrites(t *testing.T) {
	ctx := context.Background()
	check := func(name string, fake *fakeFirestore, client *firestore.Client) {
		t.Helper()
		_, err := client.Doc("teams/t1").Set(ctx, map[string]any{"title": "changed"})
		if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "only reads") {
			t.Errorf("%s: Set: %v", name, err)
		}
		err = client.RunTransaction(ctx, func(context.Context, *firestore.Transaction) error { return nil })
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s: RunTransaction: %v", name, err)
		}
		// A streaming RPC that is not a read the mirror makes (here a listen) is refused too.
		snapshots := client.Collection("teams").Snapshots(ctx)
		_, err = snapshots.Next()
		snapshots.Stop()
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s: Snapshots: %v", name, err)
		}
		if len(fake.writes) != 0 {
			t.Errorf("%s: write RPCs reached the server: %v", name, fake.writes)
		}
	}
	fake, source := openFake(t)
	check("emulator", fake, source.client)
	if _, err := source.RootCollections(ctx); err != nil { // reads still pass
		t.Error(err)
	}

	// Without an emulator the filter rides on the SDK's own connection: here
	// pointed at the fake server, without credentials.
	address := os.Getenv("FIRESTORE_EMULATOR_HOST")
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	withSeam(t, &newFirestoreClient, func(ctx context.Context, project, database string, opts ...optionClientOption) (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(ctx, project, database, append(opts, option.WithEndpoint(address), option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))...)
	})
	direct, err := OpenFirestore(ctx, "demo-mirror", "(default)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = direct.Close() }()
	if direct.conn != nil {
		t.Error("dialled an emulator connection")
	}
	check("direct", fake, direct.client)
	if _, err := direct.RootCollections(ctx); err != nil {
		t.Error(err)
	}
}

func TestReadOnlyCall(t *testing.T) {
	for method := range readRPCs {
		if readOnlyCall(method) != nil {
			t.Error(method)
		}
	}
	for _, method := range []string{"/google.firestore.v1.Firestore/Commit", "/google.firestore.v1.Firestore/Write", "/google.firestore.v1.Firestore/Listen", ""} {
		if status.Code(readOnlyCall(method)) != codes.PermissionDenied {
			t.Error(method)
		}
	}
}

func TestOpenFirestoreClosesItsConnectionOnFailure(t *testing.T) {
	startFakeFirestore(t, "demo-mirror")
	withSeam(t, &newGRPCClient, func(string, ...grpc.DialOption) (*grpc.ClientConn, error) { return nil, errBoom })
	if _, err := OpenFirestore(context.Background(), "p", "d"); err != errBoom {
		t.Error(err)
	}
}
