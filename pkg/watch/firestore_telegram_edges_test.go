package watch

import (
	"context"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func TestFirestoreTelegramPrivateBindingAndDelivery(t *testing.T) {
	stores, c, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	r, poll := registerTelegramFixture(t, s, "bot", "123", "PrivateWatchBot")
	linkTelegramFixture(t, s, c, r, poll, a, "42", "nonce-owner")
	bob := actor("bob")
	hash := stable("nonce-bob")
	if e := s.BeginTelegramLink(ctx, bob, hash, c.now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	cloudRequireError(t, s.CompleteTelegramLinkByRunner(ctx, r, poll.Fence, hash, "42"), ErrForbidden)
	d := definition("telegram")
	d.InitialObservation = "notify"
	d = create(t, s, a, d)
	subscribe(t, s, a, d.ID, "telegram")
	if _, e := s.GetSubscription(ctx, a, d.ID); e != nil {
		t.Fatal(e)
	}
	cl := claim(t, s, r)
	if _, e := s.FinishRun(ctx, r, cloudDone(cl, "pass")); e != nil {
		t.Fatal(e)
	}
	item, e := s.ClaimTelegramNotification(ctx, r, poll.Fence)
	if e != nil || item == nil {
		t.Fatal(item, e)
	}
	if e = s.FinishTelegramNotification(ctx, r, poll.Fence, item.ID, item.Fence, "TELEGRAM_RECIPIENT_UNAVAILABLE"); e != nil {
		t.Fatal(e)
	}
	connected, e := s.TelegramStatus(ctx, a)
	if e != nil || connected {
		t.Fatal(connected, e)
	}
	// Replacement is a durable consent-change wakeup, without visiting all old
	// deliveries or changing evaluation state.
	r.TelegramBotID = "456"
	r.TelegramBotUsername = "ReplacementBot"
	if e = s.RegisterRunner(ctx, r); e != nil {
		t.Fatal(e)
	}
}
func TestFirestoreTelegramBufferedWriteFailuresRollback(t *testing.T) {
	for _, mode := range []string{"binding-save", "disconnect-save"} {
		t.Run(mode, func(t *testing.T) {
			stores, c, _ := firestoreContractFixture(t)
			s := stores[0]
			ctx := context.Background()
			a := actor("alice")
			r, poll := registerTelegramFixture(t, s, "bot", "123", "PrivateWatchBot")
			hash := stable("nonce-write")
			var action func() error
			if mode == "binding-save" {
				if e := s.BeginTelegramLink(ctx, a, hash, c.now().Add(time.Minute)); e != nil {
					t.Fatal(e)
				}
				action = func() error { return s.CompleteTelegramLinkByRunner(ctx, r, poll.Fence, hash, "42") }
			} else {
				linkTelegramFixture(t, s, c, r, poll, a, "42", "nonce-disconnect")
				if e := s.DisconnectTelegram(ctx, a); e != nil {
					t.Fatal(e)
				}
				action = func() error { return s.DisconnectTelegram(ctx, a) }
			}
			db := s.cloud.db.(*contractFirestoreDB)
			before := cloudUnitImage(t, db)
			control := &watchFaultControl{Kind: "exists", Table: telegramBots, FailAt: 1}
			if mode == "disconnect-save" {
				control.Table = telegramLinks
			}
			restore := watchWrapFault(s, control)
			e := action()
			restore()
			cloudRequireError(t, e, errWatchInjected)
			if cloudUnitImage(t, db) != before {
				t.Fatal("partial consent mutation")
			}
		})
	}
}
func TestFirestoreTombstoneBufferedFailureRollsBack(t *testing.T) {
	for _, mode := range []string{"consent-delete", "tombstone-save"} {
		t.Run(mode, func(t *testing.T) {
			stores, c, _ := firestoreContractFixture(t)
			s := stores[0]
			ctx := context.Background()
			a := actor("alice")
			d := create(t, s, a, definition("deleted"))
			subscribe(t, s, a, d.ID, "email")
			if e := s.DeleteDefinition(ctx, a, d.ID, "delete", d.Version); e != nil {
				t.Fatal(e)
			}
			c.advance(25 * time.Hour)
			control := &watchFaultControl{Kind: "delete", Table: subscriptions, FailAt: 1}
			if mode == "tombstone-save" {
				control.Kind = "exists"
				control.Table = definitions
			}
			before := cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB))
			e := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return s.pruneDeletedDefinition(ctx, &watchFaultTx{ReadwriteTransaction: tx, Control: control})
			})
			cloudRequireError(t, e, errWatchInjected)
			if cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB)) != before {
				t.Fatal("partial compaction")
			}
		})
	}
}
