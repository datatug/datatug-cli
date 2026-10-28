package dbcopy

import (
	"io"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

// closeCountingBackend is an adapter backend that can be closed. A DALgo DB
// built from it is the sealed validatedDB value: Close is not visible on the DB
// itself, exactly as dalgo2couchdb and dalgo2postgrest return it.
type closeCountingBackend struct {
	dal.Backend
	closes int
}

func (b *closeCountingBackend) Close() error {
	b.closes++
	return nil
}

// noCloseBackend is an adapter backend with no Close, like Firestore.
type noCloseBackend struct{ dal.Backend }

func TestCloseSourceSeesTheCloserBehindTheSealedDB(t *testing.T) {
	backend := &closeCountingBackend{}
	db := dal.NewDB(backend)
	if _, visible := any(db).(io.Closer); visible {
		t.Fatal("the test needs a sealed DB (the framework wrapper must hide Close)")
	}
	if _, found := dal.As[io.Closer](db); !found {
		t.Fatal("dal.As must find the adapter's Close behind the sealed DB")
	}

	closer := SourceCloser(db)
	closer()
	if backend.closes != 1 {
		t.Fatalf("Close calls = %d, want 1", backend.closes)
	}
	closer()
	if backend.closes != 1 {
		t.Fatalf("a second cleanup closed the owned handle again: %d", backend.closes)
	}
}

func TestCloseSourceLeavesASourceWithoutCloseAlone(t *testing.T) {
	db := dal.NewDB(&noCloseBackend{})
	CloseSource(db) // must not panic
	CloseSource(nil)
}
