package watch

import (
	"context"
	"encoding/json"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"time"
)

type cloudClaimProgress struct {
	advanced       bool
	admissionError error
}

// One candidate per real transaction bounds source admission. A denied or
// oversized candidate still commits only the fair cursor/runner heartbeat; its
// job and observation are untouched. Four attempts share the caller deadline.
func (s *Store) claimCloudDue(ctx context.Context, id RunnerIdentity) (*Claim, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var lastError error
	for i := 0; i < 4; i++ {
		progress := &cloudClaimProgress{}
		claim, e := s.claimDueAttempt(ctx, id, progress)
		if e != nil {
			return nil, e
		}
		if claim != nil {
			return claim, nil
		}
		if progress.admissionError != nil {
			lastError = progress.admissionError
		}
		if !progress.advanced {
			return nil, lastError
		}
	}
	return nil, lastError
}
func (s *Store) cloudClaimJobs(ctx context.Context, tx dal.ReadwriteTransaction, id RunnerIdentity, progress *cloudClaimProgress) ([]Run, error) {
	data := map[string]any{}
	runnerKey := key(runners, id.Scope, id.ID)
	if e := tx.Get(ctx, record.NewRecordWithData(runnerKey, data)); e != nil {
		return nil, e
	}
	cursor := ""
	if value, exists := data["claimCursor"]; exists {
		var ok bool
		cursor, ok = value.(string)
		if !ok || cursor != "" && !nonceHashPattern.MatchString(cursor) {
			return nil, ErrInvalid
		}
	}
	query := cloudQuery(runs, id.Scope, "", []dal.Condition{dal.WhereField("active", dal.Equal, "yes")}, 1, dal.Ascending(dal.DocumentID())).(dal.StructuredQuery)
	builder := dal.From(dal.NewRootCollectionRef(runs, "")).NewQuery().Where(query.Where()).Limit(1).OrderBy(dal.Ascending(dal.DocumentID()))
	if cursor != "" {
		builder = builder.StartAfter(dal.Cursor(cursor))
	}
	rows, e := cloudRecords(ctx, tx, builder.SelectColumns())
	if e != nil {
		return nil, e
	}
	next := ""
	if len(rows) > 0 {
		next = rows[0].Key().ID.(string)
	}
	progress.advanced = len(rows) > 0 || cursor != ""
	if e = tx.Update(ctx, runnerKey, []update.Update{update.ByFieldName("claimCursor", next)}); e != nil {
		return nil, e
	}
	jobs := make([]Run, 0, len(rows))
	for _, row := range rows {
		var run Run
		if e = json.Unmarshal([]byte(row.Data().(map[string]any)["payload"].(string)), &run); e != nil {
			return nil, e
		}
		jobs = append(jobs, run)
	}
	return jobs, nil
}
