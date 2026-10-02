package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/google/uuid"
)

// StoredNarrowing is one persisted table-narrowing decision: the inspectable
// provenance of which tables the model was shown for one user message.
type StoredNarrowing struct {
	ID              string
	OriginMessageID string
	CreatedAt       time.Time
	Record          narrowing.Record
}

// narrowingSchema creates the table that holds narrowing decisions. It is not
// part of the versioned chat schema on purpose: the table is created when the
// store opens, whatever the schema version, so a CLI that predates it still
// opens the same database (it never reads the table, and a session delete
// cascades to it).
var narrowingSchema = []string{
	`CREATE TABLE IF NOT EXISTS narrowing_decisions (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, origin_message_id TEXT NOT NULL, decision_json TEXT NOT NULL, created_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS narrowing_decisions_session ON narrowing_decisions(session_id)`,
}

func ensureNarrowingSchema(db *sql.DB) error {
	for _, statement := range narrowingSchema {
		if _, err := dbExec(db, statement); err != nil {
			return fmt.Errorf("initialize narrowing decisions: %w", err)
		}
	}
	return nil
}

var (
	jsonMarshalNarrowing = json.Marshal
	insertNarrowingFn    = insertNarrowing
)

func insertNarrowing(ctx context.Context, tx *sql.Tx, sessionID, originID string, record narrowing.Record, now time.Time) error {
	payload, err := jsonMarshalNarrowing(record)
	if err != nil {
		return fmt.Errorf("encode narrowing decision: %w", err)
	}
	_, err = txExecContextFn(tx, ctx, `INSERT INTO narrowing_decisions (id, session_id, origin_message_id, decision_json, created_at) VALUES (?, ?, ?, ?, ?)`, uuid.NewString(), sessionID, originID, string(payload), stamp(now))
	return err
}

func (s *SessionStore) loadNarrowings(ctx context.Context, item *ChatSession) error {
	rows, err := dbQueryContextFn(s.db, ctx, `SELECT id, origin_message_id, decision_json, created_at FROM narrowing_decisions WHERE session_id = ? ORDER BY rowid`, item.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var stored StoredNarrowing
		var payload, created string
		if err := rows.Scan(&stored.ID, &stored.OriginMessageID, &payload, &created); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(payload), &stored.Record); err != nil {
			return fmt.Errorf("corrupt narrowing decision %q: %w", stored.ID, err)
		}
		if stored.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return fmt.Errorf("corrupt narrowing decision timestamp: %w", err)
		}
		item.Narrowings = append(item.Narrowings, stored)
	}
	return rowsErrFn(rows)
}

// NarrowingFor returns the decision stored for a user message, if any.
func (s ChatSession) NarrowingFor(originMessageID string) (StoredNarrowing, bool) {
	for _, stored := range s.Narrowings {
		if stored.OriginMessageID == originMessageID {
			return stored, true
		}
	}
	return StoredNarrowing{}, false
}
