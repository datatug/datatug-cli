package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func TestSQLiteStandaloneReadsResumeAfterAnObservedExclusiveWriter(t *testing.T) {
	for _, kind := range []string{"record", "records"} {
		t.Run(kind, func(t *testing.T) {
			s, _, path := fixture(t)
			create(t, s, actor("alice"), definition("one"))
			create(t, s, actor("alice"), definition("two"))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			writer, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Close() }()
			conn, err := writer.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if _, err = conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()

			busy := make(chan error, 1)
			released := make(chan struct{})
			var unblock sync.Once
			release := func() { unblock.Do(func() { close(released) }) }
			defer release()
			var observed sync.Once
			observe := func(err error) {
				var code interface{ Code() int }
				if errors.As(err, &code) && code.Code()&255 == 5 {
					observed.Do(func() {
						busy <- err
						<-released
					})
				}
			}
			oldGet, oldQuery := watchReadRecord, watchReadRecords
			watchReadRecord = func(ctx context.Context, getter dal.Getter, rec record.Record) error {
				err := oldGet(ctx, getter, rec)
				observe(err)
				return err
			}
			watchReadRecords = func(ctx context.Context, executor dal.QueryExecutor, query dal.Query) (dal.RecordsReader, error) {
				reader, err := oldQuery(ctx, executor, query)
				observe(err)
				return reader, err
			}
			t.Cleanup(func() { watchReadRecord, watchReadRecords = oldGet, oldQuery })
			type result struct {
				items []Definition
				err   error
			}
			results := make(chan result, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				if kind == "record" {
					item, err := s.GetDefinition(ctx, actor("alice"), "one")
					results <- result{[]Definition{item}, err}
				} else {
					items, err := s.ListDefinitions(ctx, actor("alice"))
					results <- result{items, err}
				}
			}()
			defer func() {
				cancel()
				release()
				<-finished
			}()
			select {
			case <-busy:
				// Release only after the actual provider read has returned BUSY.
				// The old one-shot read therefore fails deterministically.
			case <-ctx.Done():
				t.Fatal("provider read did not observe the held exclusive writer")
			}
			if _, err = conn.ExecContext(ctx, "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			release()
			var got result
			select {
			case got = <-results:
			case <-ctx.Done():
				t.Fatal("read did not finish after the writer released its lock")
			}
			if got.err != nil {
				t.Fatalf("standalone %s read did not survive observed SQLite contention: %v", kind, got.err)
			}
			want := 1
			if kind == "records" {
				want = 2
			}
			if len(got.items) != want {
				t.Fatalf("read returned %d definitions, want %d", len(got.items), want)
			}
			seen := map[string]bool{}
			for _, item := range got.items {
				if item.ID != "one" && item.ID != "two" || seen[item.ID] {
					t.Fatalf("unexpected or repeated definition: %+v", item)
				}
				seen[item.ID] = true
			}
		})
	}
}

type sqliteReadPartialReader struct {
	dal.RecordsReader
	row      record.Record
	read     bool
	closed   bool
	closeErr error
}

func (r *sqliteReadPartialReader) Next() (record.Record, error) {
	if !r.read {
		r.read = true
		return r.row, nil
	}
	return nil, startupSQLiteError(5)
}

func (r *sqliteReadPartialReader) Close() error {
	r.closed = true
	return r.closeErr
}

type sqliteReadCloseFailure struct {
	dal.RecordsReader
	closed bool
	err    error
}

func (r *sqliteReadCloseFailure) Close() error {
	r.closed = true
	return errors.Join(r.RecordsReader.Close(), r.err)
}

func TestSQLiteStandaloneListStopsWithoutRowsWhenCleanupFails(t *testing.T) {
	for _, phase := range []string{"acquisition", "iteration", "complete"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _ := fixture(t)
			create(t, s, actor("alice"), definition("one"))
			payload, err := json.Marshal(definition("stale"))
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("reader close unavailable")
			partial := &sqliteReadPartialReader{row: record.NewRecordWithData(key(definitions, testScope, "stale"), map[string]any{"payload": string(payload)}), closeErr: failure}
			previous := watchReadRecords
			t.Cleanup(func() { watchReadRecords = previous })
			attempts := 0
			var complete *sqliteReadCloseFailure
			watchReadRecords = func(ctx context.Context, executor dal.QueryExecutor, query dal.Query) (dal.RecordsReader, error) {
				attempts++
				if phase == "acquisition" {
					return partial, startupSQLiteError(5)
				}
				if phase == "iteration" {
					return partial, nil
				}
				reader, err := previous(ctx, executor, query)
				if err != nil {
					return reader, err
				}
				complete = &sqliteReadCloseFailure{RecordsReader: reader, err: failure}
				return complete, nil
			}
			items, err := s.ListDefinitions(context.Background(), actor("alice"))
			if items != nil || attempts != 1 || !errors.Is(err, failure) || !errors.Is(err, errSQLiteReadCleanup) {
				t.Fatalf("cleanup failure returned rows or retried: attempts=%d rows=%+v error=%v", attempts, items, err)
			}
			if phase == "complete" {
				if complete == nil || !complete.closed {
					t.Fatal("completed reader was not closed")
				}
			} else if !partial.closed {
				t.Fatal("failed reader was not closed")
			}
		})
	}
}

func TestSQLiteStandaloneListClosesFailedReaderAndDiscardsItsRows(t *testing.T) {
	for _, acquisitionFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "iteration", true: "acquisition"}[acquisitionFailure], func(t *testing.T) {
			testSQLiteStandaloneListRetryCleanup(t, acquisitionFailure)
		})
	}
}

func testSQLiteStandaloneListRetryCleanup(t *testing.T, acquisitionFailure bool) {
	t.Helper()
	s, _, _ := fixture(t)
	create(t, s, actor("alice"), definition("one"))
	create(t, s, actor("alice"), definition("two"))
	payload, err := json.Marshal(definition("stale"))
	if err != nil {
		t.Fatal(err)
	}
	partial := &sqliteReadPartialReader{row: record.NewRecordWithData(key(definitions, testScope, "stale"), map[string]any{"payload": string(payload)})}
	previous := watchReadRecords
	t.Cleanup(func() { watchReadRecords = previous })
	attempts := 0
	watchReadRecords = func(ctx context.Context, executor dal.QueryExecutor, query dal.Query) (dal.RecordsReader, error) {
		attempts++
		if attempts == 1 {
			if acquisitionFailure {
				return partial, startupSQLiteError(5)
			}
			return partial, nil
		}
		if !partial.closed {
			t.Fatal("failed reader was still open when the next query began")
		}
		return previous(ctx, executor, query)
	}
	items, err := s.ListDefinitions(context.Background(), actor("alice"))
	if err != nil || attempts != 2 || !partial.closed || len(items) != 2 {
		t.Fatalf("attempts=%d closed=%v rows=%+v error=%v", attempts, partial.closed, items, err)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.ID != "one" && item.ID != "two" || seen[item.ID] {
			t.Fatalf("partial or duplicate row escaped the failed read: %+v", items)
		}
		seen[item.ID] = true
	}
}

func TestSQLiteStandaloneReadsRespectCancellationAndNonLockFailures(t *testing.T) {
	for _, kind := range []string{"record", "records"} {
		for _, mode := range []string{"non-lock", "cancel", "already-cancelled"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, _, _ := fixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "already-cancelled" {
					cancel()
				}
				failure := error(startupSQLiteError(26))
				attempts := 0
				fail := func() error {
					attempts++
					if mode == "cancel" {
						cancel()
						return startupSQLiteError(5)
					}
					return failure
				}
				oldGet, oldQuery := watchReadRecord, watchReadRecords
				t.Cleanup(func() { watchReadRecord, watchReadRecords = oldGet, oldQuery })
				watchReadRecord = func(context.Context, dal.Getter, record.Record) error { return fail() }
				watchReadRecords = func(context.Context, dal.QueryExecutor, dal.Query) (dal.RecordsReader, error) { return nil, fail() }
				var err error
				if kind == "record" {
					var result Definition
					result, err = read[Definition](ctx, s.db, definitions, testScope, "one")
					if result.ID != "" {
						t.Fatalf("failed record read returned private data: %+v", result)
					}
				} else {
					var result []Definition
					result, err = list[Definition](ctx, s.db, definitions, testScope, "")
					if result != nil {
						t.Fatalf("failed list returned private data: %+v", result)
					}
				}
				wantCalls := 1
				if mode == "already-cancelled" {
					wantCalls = 0
				}
				wantErr := failure
				if mode != "non-lock" {
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) || attempts != wantCalls {
					t.Fatalf("attempts=%d, error=%v; want %d and %v", attempts, err, wantCalls, wantErr)
				}
			})
		}
	}
}

func TestSQLiteReadInsideTransactionDoesNotRetryItsFailedSnapshot(t *testing.T) {
	for _, kind := range []string{"record", "records"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := fixture(t)
			oldGet, oldQuery := watchReadRecord, watchReadRecords
			t.Cleanup(func() { watchReadRecord, watchReadRecords = oldGet, oldQuery })
			attempts := 0
			busy := startupSQLiteError(5)
			watchReadRecord = func(context.Context, dal.Getter, record.Record) error { attempts++; return busy }
			watchReadRecords = func(context.Context, dal.QueryExecutor, dal.Query) (dal.RecordsReader, error) {
				attempts++
				return nil, busy
			}
			// Deliberately use the raw adapter transaction. The Store's outer
			// transaction policy owns any restart, not a read within this snapshot.
			err := s.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				if kind == "record" {
					_, err := read[Definition](ctx, tx, definitions, testScope, "one")
					return err
				}
				_, err := list[Definition](ctx, tx, definitions, testScope, "")
				return err
			})
			if !errors.Is(err, busy) || attempts != 1 {
				t.Fatalf("transaction snapshot was retried: attempts=%d error=%v", attempts, err)
			}
		})
	}
}
