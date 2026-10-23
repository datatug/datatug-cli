package watch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

// Runtime retention is a per-namespace cap on terminal history. Definitions,
// subscriptions and account links are excluded. Undelivered outbox records and
// active executions are never pruned; ingestion applies backpressure at its cap.
func (s *Store) prune(ctx context.Context, tx dal.ReadwriteTransaction) error {
	type row struct {
		id      string
		updated int64
		payload string
	}
	for _, table := range []string{runs, inbox, outbox, audit, operations, runners, telegramRequests, quotas} {
		q := dal.From(dal.NewRootCollectionRef(table, "")).NewQuery().Where(dal.WhereField("namespace", dal.Equal, s.config.Namespace)).SelectColumns()
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, q)
		if err != nil {
			return err
		}
		rows := []row{}
		for {
			r, e := reader.Next()
			if errors.Is(e, dal.ErrNoMoreRecords) {
				break
			}
			if e != nil {
				_ = reader.Close()
				return e
			}
			m := r.Data().(map[string]any)
			candidate := row{id: m["id"].(string), updated: m["updated"].(int64), payload: m["payload"].(string)}
			switch table {
			case runs:
				var run Run
				if e = json.Unmarshal([]byte(candidate.payload), &run); e != nil {
					_ = reader.Close()
					return e
				}
				if run.State != "completed" && run.State != "cancelled" && run.State != "failed" {
					continue
				}
			case outbox:
				var delivery Delivery
				if e = json.Unmarshal([]byte(candidate.payload), &delivery); e != nil {
					_ = reader.Close()
					return e
				}
				if delivery.State != "delivered" && delivery.State != "cancelled" && delivery.State != "failed" {
					continue
				}
			case runners:
				if time.Unix(0, candidate.updated).Add(24 * time.Hour).After(s.now()) {
					continue
				}
			case telegramRequests:
				var request telegramRequest
				if e = json.Unmarshal([]byte(candidate.payload), &request); e != nil {
					_ = reader.Close()
					return e
				}
				if request.ExpiresAt.After(s.now()) {
					continue
				}
			case quotas:
				if time.Unix(0, candidate.updated).Add(48 * time.Hour).After(s.now()) {
					continue
				}
			}
			rows = append(rows, candidate)
		}
		if err = reader.Close(); err != nil {
			return err
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].updated > rows[j].updated })
		keep := s.config.HistoryLimit
		if table == runners || table == telegramRequests || table == quotas {
			keep = 0
		}
		for i := keep; i < len(rows); i++ {
			if err = tx.Delete(ctx, record.NewKeyWithID(table, rows[i].id)); err != nil {
				return err
			}
		}
	}
	return nil
}
