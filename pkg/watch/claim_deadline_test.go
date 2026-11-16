package watch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func TestClaimExecutionCeiling(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	c := Claim{Deadline: now.Add(10 * time.Second), LeaseUntil: now.Add(time.Second)}
	for _, tc := range []struct {
		name   string
		claim  Claim
		at     time.Time
		bounds []time.Time
		want   time.Time
	}{
		{"original", c, now, nil, c.Deadline},
		{"narrow", c, now, []time.Time{now.Add(20 * time.Second), now.Add(3 * time.Second), now.Add(5 * time.Second)}, now.Add(3 * time.Second)},
		{"cannot extend", c, now, []time.Time{now.Add(time.Hour)}, c.Deadline},
		{"missing now", c, time.Time{}, nil, time.Time{}},
		{"missing original", Claim{LeaseUntil: c.LeaseUntil}, now, nil, time.Time{}},
		{"missing lease", Claim{Deadline: c.Deadline}, now, nil, time.Time{}},
		{"expired original", c, c.Deadline, nil, time.Time{}},
		{"expired lease", c, c.LeaseUntil, nil, time.Time{}},
		{"lease beyond original", Claim{Deadline: c.Deadline, LeaseUntil: c.Deadline.Add(time.Nanosecond)}, now, nil, time.Time{}},
		{"missing bound", c, now, []time.Time{{}}, time.Time{}},
		{"expired bound", c, now, []time.Time{now}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.claim.ExecutionCeiling(tc.at, tc.bounds...)
			if !got.Equal(tc.want) || (tc.want.IsZero() && !errors.Is(err, ErrStaleLease)) || (!tc.want.IsZero() && err != nil) {
				t.Fatalf("ceiling=%v err=%v", got, err)
			}
		})
	}
}

func TestClaimDeadlineAllConstructorsAndHeartbeat(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "firestore-contract"}[cloud], func(t *testing.T) {
			var s *Store
			var c *clock
			if cloud {
				stores, clock, _ := firestoreContractFixture(t)
				s, c = stores[0], clock
			} else {
				s, c, _ = fixture(t)
			}
			create(t, s, actor("alice"), definition("deadline"))
			r := register(t, s, "runner")
			cl := claim(t, s, r)
			original := c.now().Add(s.config.ExecutionTimeout)
			if !cl.Deadline.Equal(original) {
				t.Fatal("claim did not copy stored original deadline")
			}
			pinned, err := cl.ExecutionCeiling(c.now(), original.Add(-time.Nanosecond))
			if err != nil {
				t.Fatal(err)
			}
			check := func(current Claim) {
				t.Helper()
				if !current.Deadline.Equal(original) || current.LeaseUntil.After(original) {
					t.Fatal("original deadline changed")
				}
				encoded, e := json.Marshal(current)
				if e != nil {
					t.Fatal(e)
				}
				var decoded Claim
				if json.Unmarshal(encoded, &decoded) != nil || !decoded.Deadline.Equal(original) {
					t.Fatal("wire deadline lost")
				}
			}
			fresh, err := s.GetClaim(context.Background(), r, cl.RunID, cl.Fence)
			if err != nil {
				t.Fatal(err)
			}
			check(fresh)
			if cloud {
				err = s.WithClaim(context.Background(), r, cl.RunID, cl.Fence, func(_ context.Context, _ dal.ReadTransaction, current Claim) error { check(current); return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			c.advance(s.config.LeaseDuration / 2)
			if err = s.Heartbeat(context.Background(), r, cl.RunID, cl.Fence); err != nil {
				t.Fatal(err)
			}
			fresh, err = s.GetClaim(context.Background(), r, cl.RunID, cl.Fence)
			if err != nil {
				t.Fatal(err)
			}
			check(fresh)
			if !fresh.LeaseUntil.After(cl.LeaseUntil) || !pinned.Before(fresh.Deadline) {
				t.Fatal("heartbeat/original ceiling discriminator failed")
			}
			if cloud {
				err = s.WithClaim(context.Background(), r, cl.RunID, cl.Fence, func(_ context.Context, _ dal.ReadTransaction, current Claim) error { check(current); return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			// Keep the lease live until its renewal would cross the ORIGINAL
			// deadline. This distinguishes clamping from merely adding duration.
			for c.now().Add(s.config.LeaseDuration / 2).Before(original) {
				c.advance(s.config.LeaseDuration / 2)
				if err = s.Heartbeat(context.Background(), r, cl.RunID, cl.Fence); err != nil {
					t.Fatal(err)
				}
			}
			fresh, err = s.GetClaim(context.Background(), r, cl.RunID, cl.Fence)
			if err != nil {
				t.Fatal(err)
			}
			check(fresh)
			if !fresh.LeaseUntil.Equal(original) {
				t.Fatal("heartbeat failed to clamp to original deadline")
			}
			c.advance(original.Sub(c.now()))
			if err = s.Heartbeat(context.Background(), r, cl.RunID, cl.Fence); !errors.Is(err, ErrStaleLease) {
				t.Fatal("expired heartbeat admitted", err)
			}
		})
	}
}
