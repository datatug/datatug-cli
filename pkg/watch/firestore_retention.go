package watch

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dal-go/dalgo/dal"
)

// A durable document-ID cursor visits active deliveries independently of lane.
// Replacing/unlinking a bot cannot strand its old pending rows forever behind
// valid current-lane work. Only definitive consent/provider revocation cancels;
// valid pending work is never deleted, re-dated or charged quota by maintenance.
func (s *Store) sweepRevokedDeliveries(ctx context.Context, tx dal.ReadwriteTransaction, maxRows int) error {
	session := tx.(*firestoreSession)
	query := cloudQuery(outbox, Scope{Namespace: s.config.Namespace}, "", []dal.Condition{dal.WhereField("active", dal.Equal, "yes")}, maxRows, dal.Ascending(dal.DocumentID())).(dal.StructuredQuery)
	builder := dal.From(dal.NewRootCollectionRef(outbox, "")).NewQuery().Where(query.Where()).Limit(maxRows).OrderBy(dal.Ascending(dal.DocumentID()))
	if session.sweepCursor != "" {
		builder = builder.StartAfter(dal.Cursor(session.sweepCursor))
	}
	rows, e := cloudRecords(ctx, tx, builder.SelectColumns())
	if e != nil {
		return e
	}
	for _, row := range rows {
		// Row keys are already scope-hashed. Decode the shared payload rather than
		// inventing a scope from a document ID.
		var item Delivery
		if e = json.Unmarshal([]byte(row.Data().(map[string]any)["payload"].(string)), &item); e != nil {
			return e
		}
		d, e := read[Definition](ctx, tx, definitions, item.Scope, item.WatchID)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		valid := e == nil && d.Lifecycle != "deleted" && (d.Visibility == "shared" || d.Owner == item.UserID)
		sub, e := read[Subscription](ctx, tx, subscriptions, item.Scope, stable(item.WatchID, item.UserID))
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		valid = valid && e == nil && !sub.Unsubscribed
		selected := false
		for _, ch := range sub.Channels {
			selected = selected || ch == item.Channel
		}
		valid = valid && selected
		if item.Channel == "telegram" {
			provider, e := read[telegramProvider](ctx, tx, telegramProviders, item.Scope, "active")
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			valid = valid && e == nil && provider.BotID == item.TelegramBotID
			link, e := read[telegramLink](ctx, tx, telegramLinks, item.Scope, item.UserID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			valid = valid && e == nil && link.FailureCode == "" && link.BotID == item.TelegramBotID && link.Generation == item.TelegramLinkGeneration && link.ChatID == item.TelegramChatID && (link.ExpiresAt.IsZero() || link.ExpiresAt.After(s.now()))
		}
		if !valid {
			item.State = "cancelled"
			item.FailureCode = "SUBSCRIPTION_REVOKED"
			item.Fence++
			if e = s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item); e != nil {
				return e
			}
		}
	}
	session.sweepCursor = ""
	if len(rows) == maxRows {
		session.sweepCursor = rows[len(rows)-1].Key().ID.(string)
	}
	return nil
}
