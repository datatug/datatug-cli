package chat

import (
	"context"
	"fmt"
	"time"
)

// Archives preserve durable session data while closing its terminal tab.
// List deliberately retains its old all-session semantics for other consumers.
func (s *SessionStore) ArchivedSessionIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.session_id FROM session_archives a JOIN sessions s ON s.id=a.session_id WHERE s.scope=?`, s.scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
func (s *SessionStore) Archive(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sessionExists(ctx, tx, id, s.scope); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_archives(session_id,archived_at) VALUES(?,?) ON CONFLICT(session_id) DO UPDATE SET archived_at=excluded.archived_at`, id, stamp(time.Now().UTC())); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SessionStore) RestoreArchive(ctx context.Context, id string) error {
	if _, err := s.Load(ctx, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_archives WHERE session_id=?`, id)
	return err
}
func (c *SessionChat) ArchiveActive(ctx context.Context, expectedID string) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
	if c.activeID != expectedID {
		return ChatSession{}, ErrActiveSessionChanged
	}
	if err := c.store.Archive(ctx, expectedID); err != nil {
		return ChatSession{}, err
	}
	list, err := c.store.List(ctx)
	if err != nil {
		return ChatSession{}, err
	}
	archived, err := c.store.ArchivedSessionIDs(ctx)
	if err != nil {
		return ChatSession{}, err
	}
	var next ChatSession
	for _, item := range list {
		if !archived[item.ID] {
			next, err = c.store.Load(ctx, item.ID)
			break
		}
	}
	if next.ID == "" && err == nil {
		next, err = c.store.Create(ctx, "New chat")
	}
	if err == nil {
		c.activeID = next.ID
		c.lastBookmarkID = ""
		err = c.store.Activate(ctx, next.ID)
	}
	return next, err
}
func (c *SessionChat) DeleteActive(ctx context.Context, expectedID string) (ChatSession, error) {
	// Delete owns the same mutex as Ask. A canceled turn finishes before the
	// cascade, so no late writer can repopulate the deleted session.
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
	if c.activeID != expectedID {
		return ChatSession{}, ErrActiveSessionChanged
	}
	if err := c.store.Delete(ctx, expectedID); err != nil {
		return ChatSession{}, err
	}
	next, err := c.store.LatestOrCreate(ctx)
	if err != nil {
		return ChatSession{}, fmt.Errorf("open next chat: %w", err)
	}
	c.activeID = next.ID
	c.lastBookmarkID = ""
	return next, nil
}
