package watch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

// CloudRetainedDocument is an internal storage envelope, not an API response.
// Payload may contain private inputs and delivery details. Reference projection
// must decode its table's concrete schema, including historical operation pins.
type CloudRetainedDocument struct {
	Collection  string
	ID          string
	Environment string
	WatchID     string
	Owner       string
	Payload     json.RawMessage
}
type CloudRetainedInventory struct {
	Namespace   string
	Project     string
	Collections []string
	Documents   []CloudRetainedDocument
}
type CloudRetainedInventoryLimits struct{ MaxDocuments, MaxBytes int }

// Stable source labels for trusted typed inventory/admission adapters.
const CloudDefinitionCollection = definitions
const CloudRunCollection = runs

// projectRetainedCollections is deliberately closed. Account quotas and bot/poll
// configuration have no project-definition references and live outside the
// private project root. Every other current Watch table is project-owned.
// Adding a table requires explicit classification before inventory can succeed.
func projectRetainedCollections() ([]string, error) {
	out := []string{}
	for _, table := range tables {
		switch table {
		case quotas, telegramBots, telegramPolls:
		case events, definitions, observations, runs, runners, subscriptions, inbox, outbox, audit, operations, telegramLinks, telegramRequests, telegramProviders:
			out = append(out, table)
		default:
			return nil, ErrInvalid
		}
	}
	return out, nil
}

// RetainedProjectInventoryInTransaction reads every owner/environment in this
// configured hosted coordinator, including deleted definitions, expired rows,
// completed runs and immutable receipts/events. It never uses visibility lanes,
// HistoryLimit, ready/live filters or retention cutoffs.
//
// The authenticated server must prove tx uses the SAME database as the Store.
// This internal method performs fresh project authorization but is not a user
// content-read API: caller must redact source identities separately. It starts
// no transaction, writes nothing, and grants no completeness for external/native
// coordinators or other project sources. Unknown schemas, bounds and read/close
// failures must disable lifecycle mutations; no partial result is returned.
func (s *Store) RetainedProjectInventoryInTransaction(ctx context.Context, tx dal.ReadTransaction, limits CloudRetainedInventoryLimits) (CloudRetainedInventory, error) {
	if s == nil || s.cloud == nil || ctx == nil || tx == nil || reflect.ValueOf(tx).Kind() == reflect.Pointer && reflect.ValueOf(tx).IsNil() ||
		limits.MaxDocuments < 1 || limits.MaxDocuments > 10000 || limits.MaxBytes < 1 || limits.MaxBytes > 16<<20 {
		return CloudRetainedInventory{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return CloudRetainedInventory{}, err
	}
	if err := s.cloud.options.Authorize(ctx, tx); err != nil {
		return CloudRetainedInventory{}, err
	}
	collections, err := projectRetainedCollections()
	if err != nil {
		return CloudRetainedInventory{}, err
	}
	out := CloudRetainedInventory{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID, Collections: collections, Documents: []CloudRetainedDocument{}}
	remaining := limits.MaxBytes
	for _, table := range collections {
		if err := ctx.Err(); err != nil {
			return CloudRetainedInventory{}, err
		}
		docs, err := s.readRetainedProjectTable(ctx, tx, table, limits.MaxDocuments-len(out.Documents), &remaining)
		if err != nil {
			return CloudRetainedInventory{}, err
		}
		out.Documents = append(out.Documents, docs...)
	}
	if err := ctx.Err(); err != nil {
		return CloudRetainedInventory{}, err
	}
	return out, nil
}

func (s *Store) readRetainedProjectTable(ctx context.Context, tx dal.ReadTransaction, table string, capacity int, remaining *int) (out []CloudRetainedDocument, retErr error) {
	query := dal.From(dal.NewCollectionRef(table, "", s.cloud.root)).NewQuery().OrderBy(dal.Ascending(dal.DocumentID())).Limit(capacity + 1).
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithData(record.NewIncompleteKey(table, reflect.String, s.cloud.root), map[string]any{})
		})
	reader, err := tx.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := reader.Close(); retErr == nil && err != nil {
			out, retErr = nil, err
		}
	}()
	validator := newFirestoreSession(s.cloud, tx)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := reader.Next()
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		if errors.Is(err, dal.ErrNoMoreRecords) || errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if len(out) >= capacity {
			return nil, ErrCapacity
		}
		if row == nil || row.Key() == nil || row.Key().Validate() != nil || row.Key().Collection() != table || row.Key().Parent() == nil || row.Key().Parent().String() != s.cloud.root.String() {
			return nil, ErrInvalid
		}
		id, ok := row.Key().ID.(string)
		if !ok || !nonceHashPattern.MatchString(id) {
			return nil, ErrInvalid
		}
		data, ok := row.Data().(map[string]any)
		if !ok {
			return nil, ErrInvalid
		}
		if err := validator.validateRow(table, data); err != nil {
			return nil, err
		}
		payload := data["payload"].(string)
		environment, envOK := data["environment"].(string)
		watchID, watchOK := data["watch"].(string)
		owner, ownerOK := data["owner"].(string)
		cost := len(payload) + len(id) + len(environment) + len(watchID) + len(owner) + len(table) + 64
		if cost > *remaining {
			return nil, ErrCapacity
		}
		if !envOK || !watchOK || !ownerOK || len(payload) > 128<<10 || !json.Valid([]byte(payload)) {
			return nil, ErrInvalid
		}
		*remaining -= cost
		out = append(out, CloudRetainedDocument{Collection: table, ID: id, Environment: environment, WatchID: watchID, Owner: owner, Payload: append(json.RawMessage(nil), payload...)})
	}
}
