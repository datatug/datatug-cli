package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"testing"
)

func TestFirestoreStoreClaimCursorCommitsPastOverBudgetAndUnauthorizedJobs(t *testing.T) {
	stores, _, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	for i := 0; i < 10; i++ {
		create(t, s, a, definition(fmt.Sprintf("cursor-%d", i)))
	}
	if e := s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	rows, e := cloudRecords(ctx, s.db, cloudQuery(runs, testScope, "", []dal.Condition{dal.WhereField("active", dal.Equal, "yes")}, 100, dal.Ascending(dal.DocumentID())))
	if e != nil || len(rows) != 10 {
		t.Fatal(len(rows), e)
	}
	decoded := make([]Run, len(rows))
	for i, row := range rows {
		if e = json.Unmarshal([]byte(row.Data().(map[string]any)["payload"].(string)), &decoded[i]); e != nil {
			t.Fatal(e)
		}
	}
	blocked, eligibleID := decoded[0].WatchID, decoded[len(decoded)-1].WatchID
	admissions := 0
	s.cloud.options.AdmitDefinition = func(_ context.Context, _ dal.ReadTransaction, d Definition, operation string) (string, error) {
		if operation == "claim" {
			admissions++
			if d.ID == blocked {
				return "", ErrCapacity
			}
		}
		return "revision-1", nil
	}
	s.cloud.options.AdmitRunner = func(_ context.Context, _ dal.ReadTransaction, d Definition, _ RunnerIdentity) (bool, error) {
		return d.ID == eligibleID, nil
	}
	r := register(t, s, "cursor-runner")
	cl, e := s.ClaimDue(ctx, r)
	if cl != nil || !errors.Is(e, ErrCapacity) || admissions > 4 {
		t.Fatal("first bounded page", cl, e, admissions)
	}
	reopened, e := NewFirestoreStore(s.cloud.db, s.cloud.options)
	if e != nil {
		t.Fatal(e)
	}
	// Registration refresh must preserve the private cursor, not restart at the
	// first oversized graph after every runner/client restart.
	if e = reopened.RegisterRunner(ctx, r); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4 && cl == nil; i++ {
		before := admissions
		cl, e = reopened.ClaimDue(ctx, r)
		if e != nil && !errors.Is(e, ErrCapacity) {
			t.Fatal(e)
		}
		if admissions-before > 4 {
			t.Fatal("unbounded source admissions", admissions-before)
		}
	}
	if cl == nil || cl.Definition.ID != eligibleID {
		t.Fatal("later authorized job starved", cl, e)
	}
	jobs, e := s.ListRuns(ctx, a, blocked)
	if e != nil || len(jobs) != 1 || jobs[0].State != "due" || jobs[0].Attempts != 0 {
		t.Fatal("oversized graph mutated job", jobs, e)
	}
	obs, e := read[Observation](ctx, s.db, observations, testScope, blocked)
	if e != nil || obs.LastKnown != "never" || obs.Value != "never" {
		t.Fatal("oversized graph altered business truth", obs, e)
	}
}
