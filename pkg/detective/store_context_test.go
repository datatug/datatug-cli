package detective

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestNewLocalStoreContextHeldLock(t *testing.T) {
	store := testStore(t)
	base := filepath.Dir(filepath.Dir(store.privateDir))
	lock := flock.New(filepath.Join(store.privateDir, "store.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := NewLocalStoreContext(ctx, "p", store.projectDir, base); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("constructor exceeded deadline")
	}
	ctx, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := NewLocalStoreContext(ctx, "p", store.projectDir, base); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
