package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func TestSQLiteStartupWaitsForExistingWriter(t *testing.T) {
	s, _, path := fixture(t)
	// Model a live writer whose own deadline rolls back its transaction. Startup
	// must survive more than one 50 ms driver busy timeout without weaker locks.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	held := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, _ dal.ReadwriteTransaction) error {
			close(held)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-held:
	case err := <-finished:
		t.Fatalf("could not hold SQLite writer: %v", err)
	case <-ctx.Done():
		t.Fatal("writer did not begin before its deadline")
	}
	opened, err := NewSQLiteStore(path, s.config)
	cancel()
	if opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	select {
	case writerErr := <-finished:
		if writerErr == nil {
			t.Fatal("writer committed despite its cancelled context")
		}
	case <-cleanupCtx.Done():
		t.Fatal("writer did not stop after cancellation")
	}
	if err != nil {
		t.Fatalf("startup failed while another DALGO writer was active: %v", err)
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
}

type startupSQLiteError int

func (e startupSQLiteError) Error() string { return fmt.Sprintf("SQLite startup code %d", e) }
func (e startupSQLiteError) Code() int     { return int(e) }

func TestSQLiteStartupRetryClassificationAndCancellation(t *testing.T) {
	for _, code := range []int{5, 6, 5 | 256, 6 | 256} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			attempts := 0
			err := retrySQLiteBusy(context.Background(), func() error {
				attempts++
				if attempts == 1 {
					return fmt.Errorf("adapter startup: %w", startupSQLiteError(code))
				}
				return nil
			})
			if err != nil || attempts != 2 {
				t.Fatalf("retry attempts=%d error=%v", attempts, err)
			}
		})
	}
	for _, failure := range []error{startupSQLiteError(26), os.ErrPermission, errors.New("database is locked"), ErrInvalid} {
		attempts := 0
		err := retrySQLiteBusy(context.Background(), func() error { attempts++; return failure })
		if !errors.Is(err, failure) || attempts != 1 {
			t.Fatalf("non-lock error retried: attempts=%d error=%v", attempts, err)
		}
	}
	for _, alreadyCancelled := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		if alreadyCancelled {
			cancel()
		}
		attempts := 0
		err := retrySQLiteBusy(ctx, func() error { attempts++; cancel(); return startupSQLiteError(5) })
		cancel()
		want := 1
		if alreadyCancelled {
			want = 0
		}
		if !errors.Is(err, context.Canceled) || attempts != want {
			t.Fatalf("cancellation attempts=%d error=%v", attempts, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	attempts := 0
	err := retrySQLiteBusy(ctx, func() error { attempts++; return startupSQLiteError(6) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persistent lock exceeded its retry bound: attempts=%d error=%v", attempts, err)
	}
}
