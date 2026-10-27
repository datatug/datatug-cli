package commands

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestMongoDBQueryJourney(t *testing.T) {
	uri := os.Getenv("DATATUG_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set DATATUG_TEST_MONGODB_URI to disposable /dalgo_cli_test database")
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Path != "/dalgo_cli_test" {
		t.Fatal("integration URI must select disposable dalgo_cli_test database")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	coll := client.Database("dalgo_cli_test").Collection("products")
	_ = coll.Drop(ctx)
	defer func() { _ = coll.Drop(ctx) }()
	_, err = coll.InsertMany(ctx, []any{bson.M{"_id": "tea", "name": "Tea", "price": 10}, bson.M{"_id": "coffee", "name": "Coffee", "price": 20}, bson.M{"_id": "water", "name": "Water", "price": 5}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONGO_JOURNEY_URI", uri)
	query := "from: {name: products}\ncolumns:\n - field: name\nwhere:\n op: '>='\n left: {field: price}\n right: {param: minPrice}\norderBy:\n - field: price\n   desc: true\nlimit: 1\n"
	out, stderr, code := runQuery(t, query, "--db", "env:MONGO_JOURNEY_URI", "-f", "-", "--var", "minPrice=10", "--format", "json", "--no-policies")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	rows := decodeObjects(t, out)
	if len(rows) != 1 || rows[0]["name"] != "Coffee" || rows[0]["price"] != nil {
		t.Fatalf("bad filtered/projected result: %s", out)
	}
	out, stderr, code = runQuery(t, "", "--db", "env:MONGO_JOURNEY_URI", "--from", "products", "--format", "json", "--no-policies")
	if code != 0 || len(decodeObjects(t, out)) != 3 {
		t.Fatalf("whole collection failed: %s %s", out, stderr)
	}
	bad := strings.Replace(query, "field: price", "field: $where", 1)
	_, stderr, code = runQuery(t, bad, "--db", "env:MONGO_JOURNEY_URI", "-f", "-", "--var", "minPrice=10", "--no-policies")
	if code == 0 {
		t.Fatalf("unsafe query accepted: %s", stderr)
	}
	count, err := coll.CountDocuments(ctx, bson.D{})
	if err != nil || count != 3 {
		t.Fatal("query changed source", count, err)
	}
}
