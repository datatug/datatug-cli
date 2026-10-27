package dbcopy

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestMongoDBSourceParseAndSecrets(t *testing.T) {
	for _, scheme := range []string{"mongodb", "mongodb+srv"} {
		ref, err := Parse(scheme + "://user:secret@host/store?authSource=admin")
		if err != nil {
			t.Fatal(err)
		}
		if ref.Scheme != scheme || strings.Contains(ref.String(), "secret") || strings.Contains(ref.String(), "authSource") {
			t.Fatalf("unsafe ref %v", ref)
		}
		if _, err = ref.OpenForWrite(context.Background()); err == nil {
			t.Fatal("write destination opened")
		}
	}
	for _, uri := range []string{"mongodb://host", "mongodb://host/", "mongodb://host/db/extra", "mongodb:///db"} {
		if _, err := Parse(uri); err == nil {
			t.Fatal("invalid URI accepted", uri)
		}
	}
	t.Setenv("MONGO_TEST_SECRET_URI", "mongodb://user:secret@host/db")
	ref, err := Parse("env:MONGO_TEST_SECRET_URI")
	if err != nil {
		t.Fatal(err)
	}
	if ref.String() != "env:MONGO_TEST_SECRET_URI" {
		t.Fatal(ref)
	}
}
func TestMongoDBSourceIntegration(t *testing.T) {
	uri := os.Getenv("MONGODB_TEST_URI")
	if uri == "" {
		t.Skip("set MONGODB_TEST_URI for real-server integration")
	}
	ref, err := Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	db, err := ref.OpenProtected(context.Background())
	if err != nil {
		t.Fatal(ref.OpenFailure(err))
	}
	defer func() { _ = db.(interface{ Close() error }).Close() }()
	if db.Adapter().Name() != "mongodb" {
		t.Fatal(db.Adapter().Name())
	}
}
