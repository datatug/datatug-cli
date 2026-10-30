package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
)

// XOR the sign bit so hexadecimal lexical ordering matches signed Unix nanos,
// including dates before 1970. The physical ID provides a unique stable tie.
func cloudEventOrder(e Event) string {
	return fmt.Sprintf("%016x:%s", uint64(e.OccurredAt.UnixNano())^(uint64(1)<<63), key(events, e.Scope, e.ID).ID)
}

// Two disjoint ACL lanes avoid OR filters and hidden-row pagination gaps. Each
// physical query stays within the provider's bounded AND/string-range contract.
func (s *Store) listCloudEventPage(ctx context.Context, a Actor, watchID, before string, limit int) ([]Event, string, error) {
	if limit > 100 {
		return nil, "", ErrCapacity
	}
	result := []Event{}
	next := ""
	err := s.eventRead(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		result = []Event{}
		next = ""
		d, err := read[Definition](ctx, tx, definitions, a.Scope, watchID)
		if err != nil {
			return err
		}
		if !visible(a, d) {
			return ErrNotFound
		}
		if _, err = s.sourceAdmission(ctx, tx, d, "event"); err != nil {
			return err
		}
		cursor := ""
		if before != "" {
			e, err := read[Event](ctx, tx, events, a.Scope, before)
			if err != nil {
				return err
			}
			if e.WatchID != watchID || !visibleEvent(a, e) {
				return ErrNotFound
			}
			cursor = cloudEventOrder(e)
		}
		for _, lane := range [][]dal.Condition{
			{dal.WhereField("visibility", dal.Equal, "shared")},
			{dal.WhereField("visibility", dal.Equal, "personal"), dal.WhereField("owner", dal.Equal, a.ID)},
		} {
			if cursor != "" {
				lane = append(lane, dal.WhereField("eventOrder", dal.LessThen, cursor))
			}
			rows, err := cloudRecords(ctx, tx, cloudQuery(events, a.Scope, watchID, lane, limit+1, dal.Descending(dal.Field("eventOrder"))))
			if err != nil {
				return err
			}
			for _, row := range rows {
				data, ok := row.Data().(map[string]any)
				if !ok {
					return ErrInvalid
				}
				payload, ok := data["payload"].(string)
				if !ok {
					return ErrInvalid
				}
				var e Event
				if err = json.Unmarshal([]byte(payload), &e); err != nil {
					return err
				}
				if e.WatchID != watchID || !visibleEvent(a, e) || data["visibility"] != e.Visibility || data["owner"] != e.Owner || data["eventOrder"] != cloudEventOrder(e) || row.Key().ID != key(events, e.Scope, e.ID).ID {
					return ErrInvalid
				}
				result = append(result, e)
			}
		}
		sort.Slice(result, func(i, j int) bool { return cloudEventOrder(result[i]) > cloudEventOrder(result[j]) })
		if len(result) > limit {
			result = result[:limit]
			next = result[len(result)-1].ID
		}
		return nil
	})
	return result, next, err
}
