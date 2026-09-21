package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Bookmark is project-owned; its snapshot has no live session relationship.
type Bookmark struct {
	ID         string
	ProjectID  string
	SourceID   string
	TargetKind string
	Title      string
	Tags       []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Snapshot   BookmarkSnapshot
}

// BookmarkSnapshot contains copied identifiers only. They must never be
// resolved via the originating session.
type BookmarkSnapshot struct {
	SourceID  string
	RecordSet RecordSet
	View      *RecordSetView
	Selection *Selection
}

type storedBookmarkSnapshot struct {
	SourceID  string                  `json:"sourceId,omitempty"`
	RecordSet storedBookmarkRecordSet `json:"recordSet"`
	View      *RecordSetView          `json:"view,omitempty"`
	Selection *Selection              `json:"selection,omitempty"`
}

type storedBookmarkRecordSet struct {
	ID              string          `json:"id"`
	QueryID         string          `json:"queryId"`
	OriginMessageID string          `json:"originMessageId"`
	Title           string          `json:"title"`
	DTQL            string          `json:"dtql"`
	Source          string          `json:"source"`
	Environment     string          `json:"environment"`
	Database        string          `json:"database"`
	Parameters      map[string]any  `json:"parameters"`
	CreatedAt       time.Time       `json:"createdAt"`
	Result          json.RawMessage `json:"result"`
}

type bookmarkSQL interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SessionStore) CreateBookmark(ctx context.Context, sessionID string, ref ContextReference, title string) (Bookmark, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bookmark{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sessionExists(ctx, tx, sessionID, s.scope); err != nil {
		return Bookmark{}, err
	}
	kind, snapshot, fallback, err := s.bookmarkTarget(ctx, tx, sessionID, ref)
	if err != nil {
		return Bookmark{}, err
	}
	snapshot.SourceID, err = s.bookmarkSourceID(snapshot)
	if err != nil {
		return Bookmark{}, err
	}
	payload, err := encodeBookmarkSnapshot(snapshot)
	if err != nil {
		return Bookmark{}, fmt.Errorf("encode bookmark snapshot: %w", err)
	}
	now := time.Now().UTC()
	bookmark := Bookmark{ID: uuid.NewString(), ProjectID: s.info.ProjectID, SourceID: snapshot.SourceID, TargetKind: kind, Title: normalizeBookmarkTitle(title, fallback, ref.Title), CreatedAt: now, UpdatedAt: now, Snapshot: snapshot}
	tagsJSON, _ := json.Marshal(bookmark.Tags)
	if _, err := tx.ExecContext(ctx, `INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, bookmark.ID, bookmark.ProjectID, s.scope, bookmark.Title, string(tagsJSON), bookmark.TargetKind, stamp(now), stamp(now), payload); err != nil {
		return Bookmark{}, fmt.Errorf("store bookmark: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Bookmark{}, err
	}
	return bookmark, nil
}

func (s *SessionStore) ListBookmarks(ctx context.Context) ([]Bookmark, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, title, tags_json, target_kind, created_at, updated_at, snapshot_json FROM bookmarks WHERE scope = ? AND project_id = ? ORDER BY updated_at DESC, id DESC`, s.scope, s.info.ProjectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var bookmarks []Bookmark
	for rows.Next() {
		bookmark, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		bookmark.SourceID, err = s.bookmarkSourceID(bookmark.Snapshot)
		if err != nil {
			return nil, err
		}
		bookmarks = append(bookmarks, bookmark)
	}
	return bookmarks, rows.Err()
}

func (s *SessionStore) loadBookmarks(ctx context.Context, item *ChatSession) error {
	bookmarks, err := s.ListBookmarks(ctx)
	if err != nil {
		return err
	}
	if item.Bookmarks == nil {
		item.Bookmarks = map[string]Bookmark{}
	}
	for _, bookmark := range bookmarks {
		item.Bookmarks[bookmark.ID] = bookmark
	}
	return nil
}

// FindBookmarks matches title and tags case-insensitively; required tags use AND.
func (s *SessionStore) FindBookmarks(ctx context.Context, search string, tags []string) ([]Bookmark, error) {
	bookmarks, err := s.ListBookmarks(ctx)
	if err != nil {
		return nil, err
	}
	search = strings.ToLower(strings.TrimSpace(search))
	required, err := normalizedTags(tags)
	if err != nil {
		return nil, err
	}
	filtered := bookmarks[:0]
	for _, bookmark := range bookmarks {
		if search != "" && !strings.Contains(strings.ToLower(bookmark.Title), search) && !tagsContainText(bookmark.Tags, search) {
			continue
		}
		if hasAllTags(bookmark.Tags, required) {
			filtered = append(filtered, bookmark)
		}
	}
	return filtered, nil
}

func (s *SessionStore) RenameBookmark(ctx context.Context, id, title string) (Bookmark, error) {
	title = strings.Join(strings.Fields(sanitizeTerminalText(title)), " ")
	if title == "" {
		return Bookmark{}, errors.New("bookmark title is empty")
	}
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE bookmarks SET title = ?, updated_at = ? WHERE id = ? AND scope = ? AND project_id = ?`, title, stamp(time.Now().UTC()), id, s.scope, s.info.ProjectID); err != nil {
		return Bookmark{}, err
	}
	return s.bookmark(ctx, s.db, id)
}

func (s *SessionStore) AddBookmarkTag(ctx context.Context, id, tag string) (Bookmark, error) {
	return s.changeBookmarkTags(ctx, id, func(tags []string) ([]string, error) {
		tag, err := normalizeTag(tag)
		if err != nil {
			return nil, err
		}
		for _, existing := range tags {
			if strings.EqualFold(existing, tag) {
				return tags, nil
			}
		}
		return append(tags, tag), nil
	})
}

func (s *SessionStore) RemoveBookmarkTag(ctx context.Context, id, tag string) (Bookmark, error) {
	return s.changeBookmarkTags(ctx, id, func(tags []string) ([]string, error) {
		tag, err := normalizeTag(tag)
		if err != nil {
			return nil, err
		}
		filtered := tags[:0]
		for _, existing := range tags {
			if !strings.EqualFold(existing, tag) {
				filtered = append(filtered, existing)
			}
		}
		return filtered, nil
	})
}

func (s *SessionStore) changeBookmarkTags(ctx context.Context, id string, mutate func([]string) ([]string, error)) (Bookmark, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bookmark{}, err
	}
	defer func() { _ = tx.Rollback() }()
	bookmark, err := s.bookmark(ctx, tx, id)
	if err != nil {
		return Bookmark{}, err
	}
	tags, err := mutate(append([]string(nil), bookmark.Tags...))
	if err != nil {
		return Bookmark{}, err
	}
	tags, err = normalizedTags(tags)
	if err != nil {
		return Bookmark{}, err
	}
	if !sameTags(bookmark.Tags, tags) {
		now := time.Now().UTC()
		payload, _ := json.Marshal(tags)
		if _, err := tx.ExecContext(ctx, `UPDATE bookmarks SET tags_json = ?, updated_at = ? WHERE id = ? AND scope = ? AND project_id = ?`, string(payload), stamp(now), id, s.scope, s.info.ProjectID); err != nil {
			return Bookmark{}, err
		}
		bookmark.Tags, bookmark.UpdatedAt = tags, now
	}
	if err := tx.Commit(); err != nil {
		return Bookmark{}, err
	}
	return bookmark, nil
}

// DeleteBookmark scans decoded workspaces (fail-closed), then repeats the
// reverse-reference test in its DELETE to serialize concurrent SaveWorkspace.
func (s *SessionStore) DeleteBookmark(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.bookmark(ctx, tx, id); err != nil {
		return err
	}
	if err := s.ensureBookmarkUnreferenced(ctx, tx, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ? AND scope = ? AND project_id = ? AND NOT EXISTS (
		SELECT 1 FROM session_workspace w JOIN sessions se ON se.id = w.session_id WHERE se.scope = ? AND (
			EXISTS (SELECT 1 FROM json_each(w.state_json, '$.attachments') r WHERE lower(json_extract(r.value, '$.kind')) = 'bookmark' AND json_extract(r.value, '$.objectId') = ?)
			OR EXISTS (SELECT 1 FROM json_each(w.state_json, '$.docks') d WHERE lower(json_extract(d.value, '$.reference.kind')) = 'bookmark' AND json_extract(d.value, '$.reference.objectId') = ?)
		))`, id, s.scope, s.info.ProjectID, s.scope, id, id)
	if err != nil {
		return fmt.Errorf("delete bookmark: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("cannot delete bookmark because it is still referenced")
	}
	return tx.Commit()
}

func (s *SessionStore) bookmark(ctx context.Context, q bookmarkSQL, id string) (Bookmark, error) {
	row := q.QueryRowContext(ctx, `SELECT id, project_id, title, tags_json, target_kind, created_at, updated_at, snapshot_json FROM bookmarks WHERE id = ? AND scope = ? AND project_id = ?`, id, s.scope, s.info.ProjectID)
	bookmark, err := scanBookmark(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bookmark{}, fmt.Errorf("bookmark %q not found in this project and access scope", id)
	}
	if err != nil {
		return Bookmark{}, err
	}
	bookmark.SourceID, err = s.bookmarkSourceID(bookmark.Snapshot)
	return bookmark, err
}

func (s *SessionStore) bookmarkSourceID(snapshot BookmarkSnapshot) (string, error) {
	id := snapshot.SourceID
	if id == "" {
		id = snapshot.RecordSet.Database
	}
	if id == "" || id != snapshot.RecordSet.Database {
		return "", errors.New("bookmark source does not match this project scope")
	}
	if _, ok := s.info.Sources[id]; !ok {
		return "", errors.New("bookmark source does not match this project scope")
	}
	return id, nil
}

type bookmarkScanner interface{ Scan(...any) error }

func scanBookmark(scanner bookmarkScanner) (Bookmark, error) {
	var bookmark Bookmark
	var tagsJSON, created, updated string
	var payload []byte
	if err := scanner.Scan(&bookmark.ID, &bookmark.ProjectID, &bookmark.Title, &tagsJSON, &bookmark.TargetKind, &created, &updated, &payload); err != nil {
		return Bookmark{}, err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &bookmark.Tags); err != nil {
		return Bookmark{}, fmt.Errorf("corrupt bookmark %q tags: %w", bookmark.ID, err)
	}
	if normalized, err := normalizedTags(bookmark.Tags); err != nil || !sameTags(bookmark.Tags, normalized) {
		return Bookmark{}, fmt.Errorf("corrupt bookmark %q tags", bookmark.ID)
	}
	var err error
	if bookmark.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Bookmark{}, fmt.Errorf("corrupt bookmark %q timestamp: %w", bookmark.ID, err)
	}
	if bookmark.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return Bookmark{}, fmt.Errorf("corrupt bookmark %q timestamp: %w", bookmark.ID, err)
	}
	if bookmark.Snapshot, err = decodeBookmarkSnapshot(bookmark.TargetKind, payload); err != nil {
		return Bookmark{}, fmt.Errorf("corrupt bookmark %q snapshot: %w", bookmark.ID, err)
	}
	return bookmark, nil
}

func (s *SessionStore) bookmarkTarget(ctx context.Context, tx *sql.Tx, sessionID string, ref ContextReference) (string, BookmarkSnapshot, string, error) {
	recordSets, err := recordSetsForSession(ctx, tx, sessionID)
	if err != nil {
		return "", BookmarkSnapshot{}, "", err
	}
	workspace, err := workspaceForSession(ctx, tx, sessionID, recordSets)
	if err != nil {
		return "", BookmarkSnapshot{}, "", err
	}
	kind := strings.ToLower(strings.TrimSpace(ref.Kind))
	var snapshot BookmarkSnapshot
	var fallback string
	switch kind {
	case "recordset":
		record, ok := recordSets[ref.ObjectID]
		if !ok {
			return "", BookmarkSnapshot{}, "", errors.New("bookmark target is unavailable")
		}
		snapshot, fallback = BookmarkSnapshot{RecordSet: record}, record.Title
	case "view":
		view, ok := workspace.Views[ref.ObjectID]
		if !ok {
			return "", BookmarkSnapshot{}, "", errors.New("bookmark target is unavailable")
		}
		record, ok := recordSets[view.RecordSetID]
		if !ok {
			return "", BookmarkSnapshot{}, "", errors.New("bookmark target is unavailable")
		}
		viewCopy := view
		snapshot, fallback = BookmarkSnapshot{RecordSet: record, View: &viewCopy}, view.Title
	case "selection":
		selection, ok := workspace.Selections[ref.ObjectID]
		if !ok {
			return "", BookmarkSnapshot{}, "", errors.New("bookmark target is unavailable")
		}
		view, ok := workspace.Views[selection.ViewID]
		if !ok {
			return "", BookmarkSnapshot{}, "", errors.New("bookmark target is unavailable")
		}
		record, ok := recordSets[view.RecordSetID]
		if !ok {
			return "", BookmarkSnapshot{}, "", errors.New("bookmark target is unavailable")
		}
		viewCopy, selectionCopy := view, selection
		snapshot, fallback = BookmarkSnapshot{RecordSet: record, View: &viewCopy, Selection: &selectionCopy}, selection.Title
	default:
		return "", BookmarkSnapshot{}, "", errors.New("only a RecordSet, view, or selection can be bookmarked")
	}
	snapshot.RecordSet.SessionID = ""
	if err := validateBookmarkSnapshot(kind, snapshot); err != nil {
		return "", BookmarkSnapshot{}, "", err
	}
	return kind, snapshot, fallback, nil
}

func recordSetsForSession(ctx context.Context, q bookmarkSQL, sessionID string) (map[string]RecordSet, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json FROM recordsets WHERE session_id = ? ORDER BY rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	recordSets := map[string]RecordSet{}
	for rows.Next() {
		var record RecordSet
		var params, created string
		var payload []byte
		if err := rows.Scan(&record.ID, &record.QueryID, &record.OriginMessageID, &record.Title, &record.DTQL, &record.Source, &record.Environment, &record.Database, &params, &created, &payload); err != nil {
			return nil, err
		}
		record.SessionID = sessionID
		if err := json.Unmarshal([]byte(params), &record.Parameters); err != nil {
			return nil, fmt.Errorf("corrupt RecordSet parameters %q: %w", record.ID, err)
		}
		if record.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("corrupt RecordSet timestamp %q: %w", record.ID, err)
		}
		if record.Result, err = decodeResult(payload); err != nil {
			return nil, fmt.Errorf("corrupt RecordSet %q: %w", record.ID, err)
		}
		recordSets[record.ID] = record
	}
	return recordSets, rows.Err()
}

func workspaceForSession(ctx context.Context, q bookmarkSQL, sessionID string, records map[string]RecordSet) (WorkspaceState, error) {
	var payload string
	err := q.QueryRowContext(ctx, `SELECT state_json FROM session_workspace WHERE session_id = ?`, sessionID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceState{Views: map[string]RecordSetView{}, Selections: map[string]Selection{}, ActiveTab: "Project"}, nil
	}
	if err != nil {
		return WorkspaceState{}, err
	}
	var workspace WorkspaceState
	if err := json.Unmarshal([]byte(payload), &workspace); err != nil {
		return WorkspaceState{}, fmt.Errorf("corrupt workspace: %w", err)
	}
	if workspace.Views == nil {
		workspace.Views = map[string]RecordSetView{}
	}
	if workspace.Selections == nil {
		workspace.Selections = map[string]Selection{}
	}
	if err := validateLoadedWorkspace(ChatSession{RecordSets: records, Workspace: workspace}); err != nil {
		return WorkspaceState{}, err
	}
	return workspace, nil
}

func encodeBookmarkSnapshot(snapshot BookmarkSnapshot) ([]byte, error) {
	result, err := encodeResult(snapshot.RecordSet.Result)
	if err != nil {
		return nil, err
	}
	stored := storedBookmarkSnapshot{SourceID: snapshot.SourceID, RecordSet: storedBookmarkRecordSet{ID: snapshot.RecordSet.ID, QueryID: snapshot.RecordSet.QueryID, OriginMessageID: snapshot.RecordSet.OriginMessageID, Title: snapshot.RecordSet.Title, DTQL: snapshot.RecordSet.DTQL, Source: snapshot.RecordSet.Source, Environment: snapshot.RecordSet.Environment, Database: snapshot.RecordSet.Database, Parameters: snapshot.RecordSet.Parameters, CreatedAt: snapshot.RecordSet.CreatedAt, Result: result}, View: snapshot.View, Selection: snapshot.Selection}
	return json.Marshal(stored)
}

func decodeBookmarkSnapshot(targetKind string, payload []byte) (BookmarkSnapshot, error) {
	var stored storedBookmarkSnapshot
	if err := json.Unmarshal(payload, &stored); err != nil {
		return BookmarkSnapshot{}, err
	}
	if len(stored.RecordSet.Result) == 0 {
		return BookmarkSnapshot{}, errors.New("missing RecordSet result")
	}
	result, err := decodeResult(stored.RecordSet.Result)
	if err != nil {
		return BookmarkSnapshot{}, err
	}
	snapshot := BookmarkSnapshot{SourceID: stored.SourceID, RecordSet: RecordSet{ID: stored.RecordSet.ID, QueryID: stored.RecordSet.QueryID, OriginMessageID: stored.RecordSet.OriginMessageID, Title: stored.RecordSet.Title, DTQL: stored.RecordSet.DTQL, Source: stored.RecordSet.Source, Environment: stored.RecordSet.Environment, Database: stored.RecordSet.Database, Parameters: stored.RecordSet.Parameters, CreatedAt: stored.RecordSet.CreatedAt, Result: result}, View: stored.View, Selection: stored.Selection}
	if err := validateBookmarkSnapshot(targetKind, snapshot); err != nil {
		return BookmarkSnapshot{}, err
	}
	return snapshot, nil
}

func validateBookmarkSnapshot(targetKind string, snapshot BookmarkSnapshot) error {
	if snapshot.RecordSet.ID == "" {
		return errors.New("missing RecordSet identity")
	}
	workspace := WorkspaceState{Views: map[string]RecordSetView{}, Selections: map[string]Selection{}}
	switch targetKind {
	case "recordset":
		if snapshot.View != nil || snapshot.Selection != nil {
			return errors.New("RecordSet bookmark has unexpected workspace state")
		}
	case "view":
		if snapshot.View == nil || snapshot.Selection != nil {
			return errors.New("view bookmark has invalid workspace state")
		}
		workspace.Views[snapshot.View.ID] = *snapshot.View
	case "selection":
		if snapshot.View == nil || snapshot.Selection == nil {
			return errors.New("selection bookmark has incomplete workspace state")
		}
		workspace.Views[snapshot.View.ID] = *snapshot.View
		workspace.Selections[snapshot.Selection.ID] = *snapshot.Selection
	default:
		return fmt.Errorf("unsupported bookmark target kind %q", targetKind)
	}
	return validateLoadedWorkspace(ChatSession{RecordSets: map[string]RecordSet{snapshot.RecordSet.ID: snapshot.RecordSet}, Workspace: workspace})
}

func (s *SessionStore) ensureBookmarkUnreferenced(ctx context.Context, q bookmarkSQL, id string) error {
	rows, err := q.QueryContext(ctx, `SELECT w.state_json FROM session_workspace w JOIN sessions se ON se.id = w.session_id WHERE se.scope = ?`, s.scope)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var workspace WorkspaceState
		if err := json.Unmarshal([]byte(payload), &workspace); err != nil {
			return fmt.Errorf("corrupt workspace while checking bookmark references: %w", err)
		}
		if workspaceReferencesBookmark(workspace, id) {
			return errors.New("cannot delete bookmark because it is still referenced")
		}
	}
	return rows.Err()
}

func workspaceReferencesBookmark(workspace WorkspaceState, id string) bool {
	for _, ref := range workspace.Attachments {
		if strings.EqualFold(ref.Kind, "bookmark") && ref.ObjectID == id {
			return true
		}
	}
	for _, dock := range workspace.Docks {
		if strings.EqualFold(dock.Reference.Kind, "bookmark") && dock.Reference.ObjectID == id {
			return true
		}
	}
	return false
}

func (s *SessionStore) validateBookmarkReferences(ctx context.Context, q bookmarkSQL, workspace WorkspaceState) error {
	seen := map[string]bool{}
	refs := append([]ContextReference(nil), workspace.Attachments...)
	for _, dock := range workspace.Docks {
		refs = append(refs, dock.Reference)
	}
	for _, ref := range refs {
		if !strings.EqualFold(ref.Kind, "bookmark") {
			continue
		}
		if ref.ObjectID == "" {
			return errors.New("bookmark context identity is empty")
		}
		if ref.ProjectID != "" && ref.ProjectID != s.info.ProjectID {
			return errors.New("bookmark context belongs to a different project")
		}
		if seen[ref.ObjectID] {
			continue
		}
		seen[ref.ObjectID] = true
		bookmark, err := s.bookmark(ctx, q, ref.ObjectID)
		if err != nil {
			return errors.New("bookmark context is unavailable")
		}
		if ref.SourceID != "" && ref.SourceID != bookmark.SourceID {
			return errors.New("bookmark context belongs to a different data source")
		}
	}
	return nil
}

func normalizeBookmarkTitle(title, preferred, fallback string) string {
	for _, candidate := range []string{title, preferred, fallback, "Bookmarked result"} {
		candidate = strings.Join(strings.Fields(sanitizeTerminalText(candidate)), " ")
		if candidate != "" {
			if len([]rune(candidate)) > 120 {
				return string([]rune(candidate)[:120])
			}
			return candidate
		}
	}
	return "Bookmarked result"
}
func normalizeTag(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", errors.New("bookmark tag is empty")
	}
	if len([]rune(tag)) > 80 {
		return "", errors.New("bookmark tag is too long")
	}
	for _, r := range tag {
		if unicode.IsControl(r) {
			return "", errors.New("bookmark tag contains a control character")
		}
	}
	return tag, nil
}
func normalizedTags(tags []string) ([]string, error) {
	normalized := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag, err := normalizeTag(tag)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(tag)
		if !seen[key] {
			normalized, seen[key] = append(normalized, tag), true
		}
	}
	return normalized, nil
}
func tagsContainText(tags []string, text string) bool {
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), text) {
			return true
		}
	}
	return false
}
func hasAllTags(tags, required []string) bool {
	for _, wanted := range required {
		found := false
		for _, tag := range tags {
			if strings.EqualFold(tag, wanted) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func sameTags(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
