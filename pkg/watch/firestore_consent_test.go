package watch

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func cloudConsentFixture(t *testing.T) (*Store, Actor, Definition, RunnerIdentity, *Claim) {
	t.Helper()
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	a := actor("alice")
	a.Tier = "paid"
	a.Phone = "+15551234567"
	s.cloud.options.AdmitRecipient = func(context.Context, dal.ReadTransaction, Definition, string) (RecipientAuthority, error) {
		return RecipientAuthority{AccountID: a.ID, Email: a.Email, Phone: a.Phone, Paid: true}, nil
	}
	d := definition("consent")
	d.Visibility = "shared"
	d.InitialObservation = "notify"
	d = create(t, s, a, d)
	subscribe(t, s, a, d.ID, "email", "sms", "in_app")
	r := register(t, s, "runner")
	return s, a, d, r, claim(t, s, r)
}
func TestFirestoreGenerationUsesFreshRecipientAuthority(t *testing.T) {
	for _, mode := range []string{"forbidden", "missing", "unavailable", "account", "email", "phone", "unpaid"} {
		t.Run(mode, func(t *testing.T) {
			s, a, d, r, cl := cloudConsentFixture(t)
			ctx := context.Background()
			before := cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB))
			s.cloud.options.AdmitRecipient = func(context.Context, dal.ReadTransaction, Definition, string) (RecipientAuthority, error) {
				authority := RecipientAuthority{AccountID: a.ID, Email: a.Email, Phone: a.Phone, Paid: true}
				switch mode {
				case "forbidden":
					return authority, ErrForbidden
				case "missing":
					return authority, ErrNotFound
				case "unavailable":
					return authority, errWatchInjected
				case "account":
					authority.AccountID = "different-account"
				case "email":
					authority.Email = "changed@example.test"
				case "phone":
					authority.Phone = "+15550000000"
				case "unpaid":
					authority.Paid = false
				}
				return authority, nil
			}
			_, e := s.FinishRun(ctx, r, cloudDone(cl, "pass"))
			if mode == "unavailable" {
				cloudRequireError(t, e, errWatchInjected)
				if cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB)) != before {
					t.Fatal("recipient outage partially accepted result")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			deliveries, e := s.ListDeliveries(ctx, a, d.ID)
			if e != nil {
				t.Fatal(e)
			}
			want := 2
			if mode == "account" || mode == "forbidden" || mode == "missing" {
				want = 0
			}
			if len(deliveries) != want {
				t.Fatal("current authority ignored", mode, len(deliveries), want)
			}
		})
	}
}
func TestFirestoreDispatchRechecksSourceAndRecipient(t *testing.T) {
	for _, mode := range []string{"source-conflict", "source-forbidden", "source-missing", "source-unavailable", "recipient-forbidden", "recipient-missing", "recipient-unavailable", "account", "email", "unpaid"} {
		t.Run(mode, func(t *testing.T) {
			s, a, _, r, cl := cloudConsentFixture(t)
			ctx := context.Background()
			if _, e := s.FinishRun(ctx, r, cloudDone(cl, "pass")); e != nil {
				t.Fatal(e)
			}
			before := cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB))
			s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
				switch mode {
				case "source-conflict":
					return "changed", nil
				case "source-forbidden":
					return "", ErrForbidden
				case "source-missing":
					return "", ErrNotFound
				case "source-unavailable":
					return "", errWatchInjected
				}
				return "revision-1", nil
			}
			s.cloud.options.AdmitRecipient = func(context.Context, dal.ReadTransaction, Definition, string) (RecipientAuthority, error) {
				a := RecipientAuthority{AccountID: a.ID, Email: a.Email, Phone: a.Phone, Paid: true}
				switch mode {
				case "recipient-forbidden":
					return a, ErrForbidden
				case "recipient-missing":
					return a, ErrNotFound
				case "recipient-unavailable":
					return a, errWatchInjected
				case "account":
					a.AccountID = "other"
				case "email":
					a.Email = "changed@example.test"
				case "unpaid":
					a.Paid = false
				}
				return a, nil
			}
			delivery, e := s.ClaimNotifications(ctx)
			if mode == "source-unavailable" || mode == "recipient-unavailable" {
				cloudRequireError(t, e, errWatchInjected)
				if cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB)) != before {
					t.Fatal("authority outage partially claimed delivery")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			expected := ""
			if mode == "email" {
				expected = "sms"
			}
			if mode == "unpaid" {
				expected = "email"
			}
			if expected == "" && delivery != nil || expected != "" && (delivery == nil || delivery.Channel != expected) {
				t.Fatal("revoked source/recipient channel claimed", mode, delivery)
			}
		})
	}
}
func TestFirestoreCompoundViewsFollowCurrentVisibility(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	alice, bob := actor("alice"), actor("bob")
	d := definition("shared")
	d.Visibility = "shared"
	d.InitialObservation = "notify"
	d = create(t, s, alice, d)
	subscribe(t, s, bob, d.ID, "in_app", "email")
	r := register(t, s, "runner")
	cl := claim(t, s, r)
	if _, e := s.FinishRun(ctx, r, cloudDone(cl, "pass")); e != nil {
		t.Fatal(e)
	}
	before, e := s.Inbox(ctx, bob)
	if e != nil || len(before) != 1 {
		t.Fatal(len(before), e)
	}
	d.Visibility = "personal"
	if _, e = s.PutDefinition(ctx, alice, d, "make-private", d.Version); e != nil {
		t.Fatal(e)
	}
	after, e := s.Inbox(ctx, bob)
	if e != nil || len(after) != 0 {
		t.Fatal("stale shared inbox remained visible", after, e)
	}
	for _, read := range []func() error{func() error { _, e := s.GetSubscription(ctx, bob, d.ID); return e }, func() error { _, e := s.ListDeliveries(ctx, bob, d.ID); return e }, func() error { _, e := s.ListRuns(ctx, bob, d.ID); return e }, func() error { _, e := s.Audit(ctx, bob, d.ID); return e }} {
		if !errors.Is(read(), ErrNotFound) {
			t.Fatal("private view disclosed")
		}
	}
	if delivery, e := s.ClaimNotifications(ctx); e != nil || delivery != nil {
		t.Fatal("privacy downgrade retained external consent", delivery, e)
	}
}
func TestFirestoreSourceAdmissionFailurePreservesDefinitionMutation(t *testing.T) {
	for _, operation := range []string{"save", "reset", "resume"} {
		t.Run(operation, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			a := actor("alice")
			d := create(t, s, a, definition("source-edit"))
			ctx := context.Background()
			if operation == "resume" {
				var e error
				d, e = s.SetLifecycle(ctx, a, d.ID, "paused", "pause", d.Version)
				if e != nil {
					t.Fatal(e)
				}
			}
			s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
				return "", errWatchInjected
			}
			before := cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB))
			var e error
			switch operation {
			case "save":
				d.Title = "edit"
				_, e = s.PutDefinition(ctx, a, d, "failed-edit", d.Version)
			case "reset":
				_, e = s.ResetBaseline(ctx, a, d.ID, "failed-reset", d.Version)
			case "resume":
				_, e = s.SetLifecycle(ctx, a, d.ID, "enabled", "failed-resume", d.Version)
			}
			cloudRequireError(t, e, errWatchInjected)
			if cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB)) != before {
				t.Fatal("unadmitted definition partially changed")
			}
		})
	}
}
