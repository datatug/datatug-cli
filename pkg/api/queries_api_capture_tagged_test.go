//go:build datatug_query_capture

package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/validation"
)

// A query's capture provenance is the server's record of a queries/capture
// save. The legacy create_query and update_query routes used to store a
// client-supplied capture block as sent, author included, so any client
// could claim a query was captured by someone else, from anywhere. These
// tests pin that provenance is server-owned on those routes.

// capturedQuery is root query id with two parameters and the capture
// provenance queries/capture would record for it.
func capturedQuery(id string) datatug.QueryDefWithFolderPath {
	q := legacyQuery("", id)
	q.Parameters = datatug.Parameters{{ID: "customer", Type: "integer"}, {ID: "invoice", Type: "integer"}}
	q.Capture = &datatug.QueryCapture{
		Author: "alice", Environment: "local", Source: "chinook", Collection: "Invoice",
		Bindings: []datatug.QueryCaptureBinding{
			{ParameterID: "customer", Origin: datatug.QueryCaptureOriginSelection},
			{ParameterID: "invoice", Origin: datatug.QueryCaptureOriginManual},
		},
	}
	return q
}

// revisionedStoreOf is projectID's project store as the revisioned store
// queries/capture writes through.
func revisionedStoreOf(t *testing.T, projectID string) datatug.RevisionedQueriesStore {
	t.Helper()
	projStore, err := ProjectStoreFor(projectID)
	if err != nil {
		t.Fatal(err)
	}
	store, ok := projStore.(datatug.RevisionedQueriesStore)
	if !ok {
		t.Fatalf("%T is not a RevisionedQueriesStore", projStore)
	}
	return store
}

// seedCapturedQuery stores capturedQuery(id) the way queries/capture does.
func seedCapturedQuery(t *testing.T, projectID, id string) datatug.QueryDefWithFolderPath {
	t.Helper()
	q := capturedQuery(id)
	if _, err := revisionedStoreOf(t, projectID).PutQuery(context.Background(), &q, datatug.QueryWriteCondition{IfNoneMatch: true}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return q
}

// storedQuery loads the query stored at id.
func storedQuery(t *testing.T, projectID, id string) datatug.QueryDefWithFolderPath {
	t.Helper()
	stored, err := revisionedStoreOf(t, projectID).LoadQueryRevision(context.Background(), id)
	if err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	return stored.Query
}

// assertCaptureRefused fails unless err is a 400 naming query.capture.
func assertCaptureRefused(t *testing.T, err error) {
	t.Helper()
	if !validation.IsBadRequestError(err) || !strings.Contains(err.Error(), "query.capture") {
		t.Fatalf("expected a bad-request error naming query.capture, got %v", err)
	}
}

// assertStoredCapture fails unless the query stored at id has capture want.
func assertStoredCapture(t *testing.T, projectID, id string, want *datatug.QueryCapture) {
	t.Helper()
	if got := storedQuery(t, projectID, id).Capture; !sameCapture(got, want) {
		t.Errorf("stored capture %+v, want %+v", got, want)
	}
}

func TestCreateQuery_RefusesAClientCaptureBlock(t *testing.T) {
	t.Run("on a new query", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		q := capturedQuery("q1")
		q.FolderPath = "~"
		q.Capture.Author = "mallory"
		_, err := CreateQuery(context.Background(), createRequest(projectID, q))
		assertCaptureRefused(t, err)
		assertNotExists(t, filepath.Join(queriesDir, "q1.query.json"))
	})
	t.Run("even one equal to the stored query's", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		seeded := seedCapturedQuery(t, projectID, "q1")
		q := capturedQuery("q1")
		q.FolderPath, q.Title = "~", "renamed"
		_, err := CreateQuery(context.Background(), createRequest(projectID, q))
		assertCaptureRefused(t, err)
		if got := storedQuery(t, projectID, "q1"); got.Title != seeded.Title {
			t.Errorf("stored title %q, want %q unchanged", got.Title, seeded.Title)
		}
	})
}

func TestUpdateQuery_CaptureIsServerOwned(t *testing.T) {
	ctx := context.Background()
	t.Run("a get-then-update round trip keeps the capture", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		seeded := seedCapturedQuery(t, projectID, "q1")
		got, err := GetQuery(ctx, deleteRef(projectID, "q1"))
		if err != nil {
			t.Fatalf("GetQuery: %v", err)
		}
		if !sameCapture(got.Capture, seeded.Capture) {
			t.Fatalf("get_query capture %+v, want %+v", got.Capture, seeded.Capture)
		}
		got.Title = "renamed"
		if _, err := UpdateQuery(ctx, updateRequest(projectID, "q1", *got)); err != nil {
			t.Fatalf("UpdateQuery: %v", err)
		}
		if stored := storedQuery(t, projectID, "q1"); stored.Title != "renamed" {
			t.Errorf("stored title %q, want the update's", stored.Title)
		}
		assertStoredCapture(t, projectID, "q1", seeded.Capture)
	})
	t.Run("bindings in another order are the same capture", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		seeded := seedCapturedQuery(t, projectID, "q1")
		q := capturedQuery("q1")
		q.FolderPath, q.Title = "~", "renamed"
		q.Capture.Bindings[0], q.Capture.Bindings[1] = q.Capture.Bindings[1], q.Capture.Bindings[0]
		if _, err := UpdateQuery(ctx, updateRequest(projectID, "q1", q)); err != nil {
			t.Fatalf("UpdateQuery: %v", err)
		}
		assertStoredCapture(t, projectID, "q1", seeded.Capture)
	})
	refused := []struct {
		name   string
		mutate func(c *datatug.QueryCapture)
	}{
		{"a forged author", func(c *datatug.QueryCapture) { c.Author = "mallory" }},
		{"another environment", func(c *datatug.QueryCapture) { c.Environment = "prod" }},
		{"another source", func(c *datatug.QueryCapture) { c.Source = "billing" }},
		{"another binding origin", func(c *datatug.QueryCapture) { c.Bindings[1].Origin = datatug.QueryCaptureOriginContext }},
		{"a dropped binding", func(c *datatug.QueryCapture) { c.Bindings = c.Bindings[:1] }},
	}
	for _, tt := range refused {
		t.Run(tt.name+" is refused", func(t *testing.T) {
			projectID, _ := servedWritableProject(t, adminSession(t), true)
			seeded := seedCapturedQuery(t, projectID, "q1")
			q := capturedQuery("q1")
			q.FolderPath, q.Title = "~", "renamed"
			tt.mutate(q.Capture)
			_, err := UpdateQuery(ctx, updateRequest(projectID, "q1", q))
			assertCaptureRefused(t, err)
			if stored := storedQuery(t, projectID, "q1"); stored.Title != seeded.Title {
				t.Errorf("stored title %q, want %q unchanged", stored.Title, seeded.Title)
			}
			assertStoredCapture(t, projectID, "q1", seeded.Capture)
		})
	}
	t.Run("a capture on a query stored without one is refused", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		if _, err := CreateQuery(ctx, createRequest(projectID, legacyQuery("~", "q1"))); err != nil {
			t.Fatalf("CreateQuery: %v", err)
		}
		q := capturedQuery("q1")
		q.FolderPath = "~"
		_, err := UpdateQuery(ctx, updateRequest(projectID, "q1", q))
		assertCaptureRefused(t, err)
		assertStoredCapture(t, projectID, "q1", nil)
	})
	t.Run("a capture on a query not stored yet is refused", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		q := capturedQuery("q1")
		q.FolderPath = "~"
		_, err := UpdateQuery(ctx, updateRequest(projectID, "q1", q))
		assertCaptureRefused(t, err)
		assertNotExists(t, filepath.Join(queriesDir, "q1.query.json"))
	})
	t.Run("an omitted capture keeps the stored one", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		seeded := seedCapturedQuery(t, projectID, "q1")
		q := capturedQuery("q1")
		q.FolderPath, q.Title, q.Capture = "~", "renamed", nil
		saved, err := UpdateQuery(ctx, updateRequest(projectID, "q1", q))
		if err != nil {
			t.Fatalf("UpdateQuery: %v", err)
		}
		if !sameCapture(saved.Capture, seeded.Capture) {
			t.Errorf("response capture %+v, want the stored %+v", saved.Capture, seeded.Capture)
		}
		if stored := storedQuery(t, projectID, "q1"); stored.Title != "renamed" {
			t.Errorf("stored title %q, want the update's", stored.Title)
		}
		assertStoredCapture(t, projectID, "q1", seeded.Capture)
	})
	t.Run("a create over a captured query keeps its capture", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		seeded := seedCapturedQuery(t, projectID, "q1")
		q := capturedQuery("q1")
		q.FolderPath, q.Title, q.Capture = "~", "renamed", nil
		if _, err := CreateQuery(ctx, createRequest(projectID, q)); err != nil {
			t.Fatalf("CreateQuery: %v", err)
		}
		assertStoredCapture(t, projectID, "q1", seeded.Capture)
	})
	t.Run("an update dropping a parameter the capture binds is refused", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		seeded := seedCapturedQuery(t, projectID, "q1")
		q := capturedQuery("q1")
		q.FolderPath, q.Capture = "~", nil
		q.Parameters = q.Parameters[:1]
		if _, err := UpdateQuery(ctx, updateRequest(projectID, "q1", q)); !validation.IsBadRequestError(err) {
			t.Fatalf("expected a bad-request error, got %v", err)
		}
		if stored := storedQuery(t, projectID, "q1"); len(stored.Parameters) != len(seeded.Parameters) {
			t.Errorf("stored %d parameters, want %d unchanged", len(stored.Parameters), len(seeded.Parameters))
		}
	})
}

// racingStore is a revisioned store whose first PutQuery lets race commit
// another write first - the window between a legacy write's read of the
// stored query and its own write.
type racingStore struct {
	datatug.RevisionedQueriesStore
	race func()
	puts int
}

func (s *racingStore) PutQuery(ctx context.Context, q *datatug.QueryDefWithFolderPath, c datatug.QueryWriteCondition) (*datatug.StoredQuery, error) {
	s.puts++
	if race := s.race; race != nil {
		s.race = nil
		race()
	}
	return s.RevisionedQueriesStore.PutQuery(ctx, q, c)
}

// A write that commits between a legacy write's read and its own write is
// never overwritten by a decision made against what was read: the store's
// revision condition fails, and the legacy write decides again against
// what is stored now.
func TestWriteRevisionedLegacyQuery_DecidesAgainstTheCommittedQuery(t *testing.T) {
	ctx := context.Background()
	// racingWrite replaces the stored query with mutate applied, as
	// another writer would.
	racingWrite := func(t *testing.T, store datatug.RevisionedQueriesStore, mutate func(q *datatug.QueryDefWithFolderPath)) func() {
		return func() {
			current, err := store.LoadQueryRevision(ctx, "q1")
			if err != nil {
				t.Errorf("race: load: %v", err)
				return
			}
			q := current.Query
			mutate(&q)
			if _, err := store.PutQuery(ctx, &q, datatug.QueryWriteCondition{IfMatch: current.Revision}); err != nil {
				t.Errorf("race: put: %v", err)
			}
		}
	}
	t.Run("a capture changed in between refuses the echoed one", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		seedCapturedQuery(t, projectID, "q1")
		real := revisionedStoreOf(t, projectID)
		bob := capturedQuery("q1").Capture
		bob.Author = "bob"
		store := &racingStore{RevisionedQueriesStore: real, race: racingWrite(t, real, func(q *datatug.QueryDefWithFolderPath) {
			q.Title, q.Capture = "bob's", bob
		})}
		q := capturedQuery("q1") // echoes alice's capture, as read
		q.Title = "renamed"
		err := writeRevisionedLegacyQuery(ctx, store, "q1", &q)
		assertCaptureRefused(t, err)
		if stored := storedQuery(t, projectID, "q1"); stored.Title != "bob's" {
			t.Errorf("stored title %q, want the racing write's", stored.Title)
		}
		assertStoredCapture(t, projectID, "q1", bob)
	})
	t.Run("a capture written in between is kept by an update omitting it", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		plain := capturedQuery("q1")
		plain.Capture = nil
		if _, err := revisionedStoreOf(t, projectID).PutQuery(ctx, &plain, datatug.QueryWriteCondition{IfNoneMatch: true}); err != nil {
			t.Fatal(err)
		}
		real := revisionedStoreOf(t, projectID)
		bob := capturedQuery("q1").Capture
		bob.Author = "bob"
		store := &racingStore{RevisionedQueriesStore: real, race: racingWrite(t, real, func(q *datatug.QueryDefWithFolderPath) { q.Capture = bob })}
		q := capturedQuery("q1")
		q.Title, q.Capture = "renamed", nil
		if err := writeRevisionedLegacyQuery(ctx, store, "q1", &q); err != nil {
			t.Fatalf("writeRevisionedLegacyQuery: %v", err)
		}
		if store.puts != 2 {
			t.Errorf("%d puts, want 2: the first loses to the racing write, the second commits", store.puts)
		}
		if stored := storedQuery(t, projectID, "q1"); stored.Title != "renamed" {
			t.Errorf("stored title %q, want the retried update's", stored.Title)
		}
		assertStoredCapture(t, projectID, "q1", bob)
	})
}

// conflictingStore fails every PutQuery's revision condition.
type conflictingStore struct {
	datatug.RevisionedQueriesStore
	puts int
}

func (s *conflictingStore) PutQuery(context.Context, *datatug.QueryDefWithFolderPath, datatug.QueryWriteCondition) (*datatug.StoredQuery, error) {
	s.puts++
	return nil, &datatug.QueryRevisionConflictError{ID: "q1", Reason: "stale revision"}
}

func TestWriteRevisionedLegacyQuery_GivesUpAfterRepeatedConflicts(t *testing.T) {
	projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
	store := &conflictingStore{RevisionedQueriesStore: revisionedStoreOf(t, projectID)}
	q := legacyQuery("", "q1")
	if err := writeRevisionedLegacyQuery(context.Background(), store, "q1", &q); !errors.Is(err, ErrLegacyQueryWriteConflict) {
		t.Fatalf("expected ErrLegacyQueryWriteConflict, got %v", err)
	}
	if store.puts != maxLegacyWriteAttempts {
		t.Errorf("%d puts, want %d", store.puts, maxLegacyWriteAttempts)
	}
	assertNotExists(t, filepath.Join(queriesDir, "q1.query.json"))
}

// plainProjectStore hides every method but datatug.ProjectStore's.
type plainProjectStore struct{ datatug.ProjectStore }

func TestWriteLegacyQuery_RefusesAStoreWithNoConditionalWrite(t *testing.T) {
	projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
	projStore, err := ProjectStoreFor(projectID)
	if err != nil {
		t.Fatal(err)
	}
	q := legacyQuery("", "q1")
	if err := writeLegacyQuery(context.Background(), plainProjectStore{projStore}, "q1", &q); !errors.Is(err, errLegacyStoreNotRevisioned) {
		t.Fatalf("expected errLegacyStoreNotRevisioned, got %v", err)
	}
	assertNotExists(t, filepath.Join(queriesDir, "q1.query.json"))
}

func TestSameCapture(t *testing.T) {
	base := func() *datatug.QueryCapture { return capturedQuery("q").Capture }
	with := func(mutate func(c *datatug.QueryCapture)) *datatug.QueryCapture {
		c := base()
		mutate(c)
		return c
	}
	tests := []struct {
		name string
		a, b *datatug.QueryCapture
		want bool
	}{
		{"nil and nil", nil, nil, true},
		{"nil and a capture", nil, base(), false},
		{"equal", base(), base(), true},
		{"bindings reordered", base(), with(func(c *datatug.QueryCapture) {
			c.Bindings[0], c.Bindings[1] = c.Bindings[1], c.Bindings[0]
		}), true},
		{"no bindings and an empty list",
			with(func(c *datatug.QueryCapture) { c.Bindings = nil }),
			with(func(c *datatug.QueryCapture) { c.Bindings = []datatug.QueryCaptureBinding{} }), true},
		{"another author", base(), with(func(c *datatug.QueryCapture) { c.Author = "mallory" }), false},
		{"another collection", base(), with(func(c *datatug.QueryCapture) { c.Collection = "Customer" }), false},
		{"another origin", base(), with(func(c *datatug.QueryCapture) { c.Bindings[0].Origin = "manual" }), false},
		{"a duplicated binding", base(), with(func(c *datatug.QueryCapture) {
			c.Bindings = append(c.Bindings[:1], c.Bindings[0])
		}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameCapture(tt.a, tt.b); got != tt.want {
				t.Errorf("sameCapture = %v, want %v", got, tt.want)
			}
			if got := sameCapture(tt.b, tt.a); got != tt.want {
				t.Errorf("sameCapture reversed = %v, want %v", got, tt.want)
			}
		})
	}
}
