package watch

import (
	"context"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"testing"
)

func TestFirestoreStoreMaintenanceVisitsRevokedBotLane(t *testing.T) {
	stores, _, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	d := definition("maintenance")
	create(t, s, a, d)
	if e := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		sub := Subscription{WatchID: d.ID, UserID: a.ID, Channels: []string{"telegram"}, TelegramBotID: "222"}
		if e := s.save(ctx, tx, subscriptions, d.Scope, stable(d.ID, a.ID), d.ID, a.ID, "", sub); e != nil {
			return e
		}
		if e := s.save(ctx, tx, telegramProviders, d.Scope, "active", "", "", "", telegramProvider{Scope: d.Scope, BotID: "222"}); e != nil {
			return e
		}
		if e := s.save(ctx, tx, telegramLinks, d.Scope, a.ID, "", a.ID, "", telegramLink{Scope: d.Scope, UserID: a.ID, BotID: "222", ChatID: "42", Generation: 2}); e != nil {
			return e
		}
		for i := 0; i < 30; i++ {
			bot, generation := "222", int64(2)
			if i%2 == 0 {
				bot, generation = "111", 1
			}
			id := fmt.Sprintf("delivery-%d", i)
			delivery := Delivery{Notification: Notification{Scope: d.Scope, ID: id, WatchID: d.ID, UserID: a.ID}, Channel: "telegram", TelegramBotID: bot, TelegramChatID: "42", TelegramLinkGeneration: generation, State: "pending"}
			if e := s.save(ctx, tx, outbox, d.Scope, id, d.ID, a.ID, "", delivery); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 8; i++ {
		if e := s.Prune(ctx, 5); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 30; i++ {
		delivery, e := read[Delivery](ctx, s.db, outbox, d.Scope, fmt.Sprintf("delivery-%d", i))
		if e != nil {
			t.Fatal(e)
		}
		want := "pending"
		if i%2 == 0 {
			want = "cancelled"
		}
		if delivery.State != want {
			t.Fatal("lane cursor did not preserve/cancel expected delivery", i, delivery.State, want)
		}
	}
	gate := map[string]any{}
	if e := s.cloud.db.Get(ctx, record.NewRecordWithData(s.cloud.root, gate)); e != nil || gate["pendingOutbox"] != int64(15) {
		t.Fatal("durable pending accounting", gate, e)
	}
}

func TestFirestoreStoreDeploymentQuotaAcrossSpaces(t *testing.T) {
	stores, _, options := firestoreFixture(t)
	ctx := context.Background()
	db := stores[0].cloud.db
	options.AccountRoot = record.NewKeyWithID("datatugWatchControl", "accounts-"+stable(options.StorageRoot.String()))
	options.AccountNamespace = "hosted-deployment"
	options.Config.EmailDailyLimit = 1
	options.Config.EmailMinuteLimit = 1
	first, e := NewFirestoreStore(db, options)
	if e != nil {
		t.Fatal(e)
	}
	otherSpace := record.NewKeyWithID("spaces", fmt.Sprintf("other-%d", stores[0].now().UnixNano()))
	options.ProjectKey = record.NewKeyWithParentAndID(otherSpace, "projects", testScope.Project)
	options.StorageRoot = record.NewKeyWithParentAndID(otherSpace, "detectiveProjects", testScope.Project)
	options.Config.Namespace = "another-space"
	projectKey := options.ProjectKey
	options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error {
		return tx.Get(ctx, record.NewRecordWithData(projectKey, map[string]any{}))
	}
	if e = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(projectKey, map[string]any{"title": "authorized second Space"}))
	}); e != nil {
		t.Fatal(e)
	}
	second, e := NewFirestoreStore(db, options)
	if e != nil {
		t.Fatal(e)
	}
	if first.cloud.quotaRoot.String() != second.cloud.quotaRoot.String() || first.cloud.coordinationRoot.String() == second.cloud.coordinationRoot.String() {
		t.Fatal("account quota and bot authority partitioning confused")
	}
	for i, store := range []*Store{first, second} {
		if e = store.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			delivery := Delivery{Channel: "email", AccountID: "same-billing-account"}
			allowed, e := store.reserveQuota(ctx, tx, &delivery)
			if e != nil {
				return e
			}
			if allowed != (i == 0) {
				return fmt.Errorf("Space quota partition bypass: space%d allowed%t", i, allowed)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestFirestoreStoreDeletedWatchReleasesLiveCapacityAndPreservesFence(t *testing.T) {
	stores, c, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	for i := 0; i < 100; i++ {
		create(t, s, a, definition(fmt.Sprintf("capacity-%03d", i)))
	}
	if _, e := s.PutDefinition(ctx, a, definition("over-cap"), "over-cap", 0); e != ErrCapacity {
		t.Fatal("live project cap", e)
	}
	r := register(t, s, "runner")
	cl := claim(t, s, r)
	target, e := s.GetDefinition(ctx, a, cl.Definition.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteDefinition(ctx, a, target.ID, "delete-claimed", target.Version); e != nil {
		t.Fatal(e)
	}
	create(t, s, a, definition("after-delete"))
	if _, e = s.FinishRun(ctx, r, cloudDone(cl, "pass")); e != ErrStaleLease {
		t.Fatal("deleted completion accepted", e)
	}
	c.advance(25 * 60 * 60 * 1e9)
	if e = s.Prune(ctx, 5); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PutDefinition(ctx, a, definition(target.ID), "recreate-deleted", 0); e != ErrForbidden {
		t.Fatal("deleted identity reused", e)
	}
	if _, e = s.FinishRun(ctx, r, cloudDone(cl, "pass")); e != ErrStaleLease {
		t.Fatal("compacted identity resurrected completion", e)
	}
}
