package watch

import (
	"context"
	"reflect"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// CloudRetainedSource is private typed resolver input, not a lifecycle response.
// Scope is preserved independently for every owner/environment. The host must
// join Definition/Run/Event pins to its immutable source manifests and classify
// other project sources before it can claim a complete reference inventory.
// Expiry and coordinator retention cutoffs deliberately do not remove rows.
type CloudRetainedSource struct {
	Collection, RecordID    string
	Owner                   string
	Scope                   Scope
	WatchID, RunID, EventID string
	DeliveryID              string
	Active                  bool
	Definition              *Definition
	Run                     *Run
	Event                   *Event
	Observation             *Observation
}

// ProjectCloudRetainedSources validates the closed table schemas and their
// physical envelope joins. It does not infer dependencies from a notification
// URL, human text, inputs, opaque CheckRunRef or the current mutable Check.
// A legacy payload, unexpected table/action, or incomplete scan returns no rows.
func ProjectCloudRetainedSources(ctx context.Context, inventory CloudRetainedInventory) ([]CloudRetainedSource, error) {
	collections, err := projectRetainedCollections()
	if ctx == nil || inventory.Namespace == "" || inventory.Project == "" || err != nil || !reflect.DeepEqual(inventory.Collections, collections) || len(inventory.Documents) > 10000 {
		return nil, ErrInvalid
	}
	out := make([]CloudRetainedSource, 0, len(inventory.Documents))
	seen := map[string]bool{}
	remaining := 16 << 20
	for _, document := range inventory.Documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cost := len(document.Payload) + len(document.ID) + len(document.Environment) + len(document.Owner) + len(document.WatchID) + len(document.Collection) + 64
		identity := document.Collection + "/" + document.ID
		if cost > remaining || len(document.Payload) > 128<<10 || !nonceHashPattern.MatchString(document.ID) || seen[identity] || document.Environment == "" || len(document.Environment) > 100 {
			return nil, ErrInvalid
		}
		remaining -= cost
		seen[identity] = true
		source := CloudRetainedSource{Collection: document.Collection, RecordID: document.ID, Owner: document.Owner, Scope: Scope{Namespace: inventory.Namespace, Project: inventory.Project, Environment: document.Environment}}
		var id string
		switch document.Collection {
		case definitions:
			value, err := retainedCloudValue[Definition](document)
			if err != nil || !validRetainedDefinition(value) || value.Scope != source.Scope || value.ID != document.WatchID || value.Owner != document.Owner {
				return nil, ErrInvalid
			}
			id, source.WatchID, source.Active, source.Definition = value.ID, value.ID, value.Lifecycle != "deleted", &value
		case operations:
			value, err := retainedCloudValue[operation](document)
			if err != nil || !nonceHashPattern.MatchString(value.Digest) || !validRetainedDefinition(value.Definition) || value.Definition.Scope != source.Scope || value.Definition.ID != document.WatchID || document.Owner == "" {
				return nil, ErrInvalid
			}
			// The operation key binds an actor+operation ID that is intentionally
			// absent from its response payload. Its owning envelope and immutable
			// definition binding provide the available identity joins.
			source.WatchID, source.Definition = value.Definition.ID, &value.Definition
		case runs:
			value, err := retainedCloudValue[Run](document)
			if err != nil || value.Scope != source.Scope || value.ID == "" || value.WatchID != document.WatchID || value.DefinitionVersion < 1 || document.Owner != "" {
				return nil, ErrInvalid
			}
			switch value.State {
			case "due", "running", "retry":
				source.Active = true
			case "completed", "failed", "cancelled":
			default:
				return nil, ErrInvalid
			}
			id, source.WatchID, source.RunID, source.EventID, source.Run = value.ID, value.WatchID, value.ID, value.EventID, &value
		case events:
			value, err := retainedCloudValue[Event](document)
			if err != nil || value.Scope != source.Scope || value.ID == "" || value.WatchID != document.WatchID || value.Owner != document.Owner || value.Owner == "" || value.RunID == "" || value.DefinitionVersion < 1 || value.CheckID == "" || value.CheckVersion == "" {
				return nil, ErrInvalid
			}
			id, source.WatchID, source.RunID, source.EventID, source.Event = value.ID, value.WatchID, value.RunID, value.ID, &value
		case observations:
			value, err := retainedCloudValue[Observation](document)
			if err != nil || value.WatchID == "" || value.WatchID != document.WatchID || document.Owner == "" {
				return nil, ErrInvalid
			}
			id, source.WatchID, source.RunID, source.EventID, source.Active = value.WatchID, value.WatchID, value.LastRunID, value.LastEventID, true
			source.Observation = &value
		case subscriptions:
			value, err := retainedCloudValue[Subscription](document)
			if err != nil || value.WatchID == "" || value.WatchID != document.WatchID || value.UserID == "" || value.UserID != document.Owner {
				return nil, ErrInvalid
			}
			id, source.WatchID, source.Active = stable(value.WatchID, value.UserID), value.WatchID, !value.Unsubscribed
		case inbox:
			value, err := retainedCloudValue[Notification](document)
			if err != nil || value.Scope != source.Scope || value.ID == "" || value.WatchID != document.WatchID || value.UserID != document.Owner || value.UserID == "" {
				return nil, ErrInvalid
			}
			id, source.WatchID, source.RunID, source.EventID = value.ID, value.WatchID, value.RunID, value.EventID
		case outbox:
			value, err := retainedCloudValue[Delivery](document)
			if err != nil || value.Scope != source.Scope || value.ID == "" || value.WatchID != document.WatchID || value.UserID != document.Owner || value.UserID == "" {
				return nil, ErrInvalid
			}
			switch value.State {
			case "pending", "sending", "retry", "rate_limited":
				source.Active = true
			case "delivered", "failed", "cancelled":
			default:
				return nil, ErrInvalid
			}
			id, source.WatchID, source.RunID, source.EventID = value.ID, value.WatchID, value.RunID, value.EventID
			source.DeliveryID = value.ID
		case audit:
			value, err := retainedCloudValue[AuditEvent](document)
			if err != nil || value.Scope != source.Scope || value.ID == "" || value.Actor != document.Owner || value.ResourceID != document.WatchID {
				return nil, ErrInvalid
			}
			switch value.Action {
			case "definition.put", "definition.enabled", "definition.paused", "definition.deleted", "definition.reset-baseline", "subscription.put":
				source.WatchID = value.ResourceID
			case "run.claim", "run.finish":
				source.RunID = value.ResourceID
			case "notification.retry":
				source.DeliveryID = value.ResourceID
			case "runner.register", "telegram.disconnect":
			default:
				return nil, ErrInvalid
			}
			id = value.ID
		case runners:
			value, err := retainedCloudValue[Runner](document)
			if err != nil || value.Scope != source.Scope || value.ID == "" || value.ID != document.Owner || document.WatchID != "" {
				return nil, ErrInvalid
			}
			id = value.ID
		case telegramLinks:
			value, err := retainedCloudValue[telegramLink](document)
			if err != nil || value.Scope != source.Scope || value.UserID == "" || value.UserID != document.Owner || document.WatchID != "" {
				return nil, ErrInvalid
			}
			id = value.UserID
		case telegramRequests:
			value, err := retainedCloudValue[telegramRequest](document)
			if err != nil || value.Scope != source.Scope || !nonceHashPattern.MatchString(value.Hash) || value.UserID != document.Owner || document.WatchID != "" {
				return nil, ErrInvalid
			}
			id = value.Hash
		case telegramProviders:
			value, err := retainedCloudValue[telegramProvider](document)
			if err != nil || value.Scope != source.Scope || document.WatchID != "" || document.Owner != "" {
				return nil, ErrInvalid
			}
			id = "active"
		default:
			return nil, ErrInvalid
		}
		if id != "" && key(document.Collection, source.Scope, id).ID != document.ID {
			return nil, ErrInvalid
		}
		out = append(out, source)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func retainedCloudValue[T any](document CloudRetainedDocument) (value T, err error) {
	err = apicontract.DecodeStrict(document.Payload, &value)
	return
}
func validRetainedDefinition(value Definition) bool {
	if value.Version < 1 || value.ID == "" || value.Owner == "" {
		return false
	}
	if value.Lifecycle == "deleted" && value.CheckID == "" {
		return reflect.DeepEqual(value, Definition{Scope: value.Scope, ID: value.ID, Owner: value.Owner, Version: value.Version, Lifecycle: "deleted", UpdatedAt: value.UpdatedAt})
	}
	if validateDefinition(value) != nil {
		return false
	}
	switch value.Lifecycle {
	case "enabled":
		return value.Enabled
	case "paused", "completed", "deleted":
		return !value.Enabled
	default:
		return false
	}
}
