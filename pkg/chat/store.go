package chat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// ChatScope prevents a cached, policy-redacted result from being reopened
// under another database or principal/policy configuration.
type ChatScope struct {
	// ProjectID partitions retained project artefacts. It deliberately does not
	// participate in the existing session scope hash: Phase 2 sessions retain
	// their exact historic scope identity during the Phase 4 migration.
	ProjectID         string
	Environment       string
	Database          string
	AccessFingerprint string
	Sources           map[string]string
}

type ChatSession struct {
	ID         string
	Title      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Messages   []ChatMessage
	Queries    []ExecutedQuery
	RecordSets map[string]RecordSet
	Bookmarks  map[string]Bookmark
	Workspace  WorkspaceState
}

type ChatMessage struct {
	ID          string
	Role        string
	Kind        string
	Text        string
	QueryID     string
	RecordSetID string
	CreatedAt   time.Time
}

type ExecutedQuery struct {
	ID              string
	OriginMessageID string
	Title           string
	DTQL            string
	Source          string
	Parameters      map[string]any
	ExecutedAt      time.Time
	Error           string
}

// RecordSet is a session-owned, immutable result snapshot. Re-execution must
// insert another ID, never update one of these rows.
type RecordSet struct {
	ID              string
	SessionID       string
	QueryID         string
	OriginMessageID string
	Title           string
	DTQL            string
	Source          string
	Environment     string
	Database        string
	Parameters      map[string]any
	CreatedAt       time.Time
	Result          secureread.Result
	Lineage         *JoinLineage
}

// JoinLineage preserves the exact user-selected edge without making a saved
// RecordSet depend on live schema metadata.
type JoinLineage struct {
	ParentRecordSetID string            `json:"parentRecordSetId"`
	CandidateID       JoinCandidateID   `json:"candidateId"`
	AppliedEdges      []AppliedJoinEdge `json:"appliedEdges,omitempty"`
}

// AppliedJoinEdge ties an FK constraint to the exact joined relation path in
// immutable DTQL. It distinguishes duplicate constraints with identical ON
// field pairs; older RecordSets without it use conservative ON matching.
type AppliedJoinEdge struct {
	JoinPath     RelationInstanceID `json:"joinPath"`
	SourcePath   RelationInstanceID `json:"sourcePath"`
	ConstraintID string             `json:"constraintId"`
	Direction    string             `json:"direction"`
	CandidateID  JoinCandidateID    `json:"candidateId"`
	Fields       []JoinFieldPair    `json:"fields"`
}

type SessionStore struct {
	db    *sql.DB
	scope string
	info  ChatScope
	path  string
}

// DefaultChatStorePath keeps snapshots outside the project repository. The
// hash distinguishes projects without exposing their path in a filename.
func DefaultChatStorePath(projectDir string) (string, error) {
	root, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find chat home: %w", err)
	}
	canonical, err := canonicalProjectPath(projectDir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return filepath.Join(root, ".datatug", "chat", hex.EncodeToString(sum[:12])+".sqlite"), nil
}

func canonicalProjectPath(projectDir string) (string, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func OpenSessionStore(path string, scope ChatScope) (*SessionStore, error) {
	if scope.ProjectID == "" || scope.Environment == "" || scope.Database == "" || scope.AccessFingerprint == "" || scope.Sources[scope.Database] == "" {
		return nil, errors.New("chat session scope requires project ID, environment, database, access fingerprint, and selected source")
	}
	if path == "" {
		return nil, errors.New("chat session store path is empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create private chat directory: %w", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("chat directory %q must be private (mode 0700)", dir)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("chat database %q must be a private regular file", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("create chat database: %w", createErr)
		}
		_ = file.Close()
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize chat database: %w", err)
		}
	}
	if err = initChatSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Keep the persisted scope identity byte-for-byte compatible with Phase 3.
	// ProjectID is an additional bookmark boundary, not a session-scope change.
	encoded, _ := json.Marshal(struct {
		Environment       string
		Database          string
		AccessFingerprint string
		Sources           map[string]string
	}{scope.Environment, scope.Database, scope.AccessFingerprint, scope.Sources})
	sum := sha256.Sum256(encoded)
	newScope := hex.EncodeToString(sum[:])
	legacy, _ := json.Marshal(struct {
		Environment       string
		Database          string
		AccessFingerprint string
	}{scope.Environment, scope.Database, scope.AccessFingerprint})
	legacySum := sha256.Sum256(legacy)
	if err := migrateLegacyChatScopes(db, hex.EncodeToString(legacySum[:]), newScope, scope.Sources[scope.Database]); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SessionStore{db: db, scope: newScope, info: scope, path: path}, nil
}

// Phase 2 scope hashes omitted source identity. Reopen those sessions only if
// every saved execution used the currently selected source URL. Old sessions
// with no queries are safe to migrate. Mismatched snapshots remain untouched
// and inaccessible rather than being relabeled as data from a new source.
func migrateLegacyChatScopes(db *sql.DB, oldScope, newScope, selectedURL string) error {
	if oldScope == newScope {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	sessions, err := tx.Query(`SELECT id FROM sessions WHERE scope = ?`, oldScope)
	if err != nil {
		return fmt.Errorf("read legacy chat sessions: %w", err)
	}
	var ids []string
	for sessions.Next() {
		var id string
		if err := sessions.Scan(&id); err != nil {
			_ = sessions.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := sessions.Err(); err != nil {
		_ = sessions.Close()
		return err
	}
	_ = sessions.Close()
	for _, id := range ids {
		rows, err := tx.Query(`SELECT source FROM queries WHERE session_id = ? UNION SELECT source FROM recordsets WHERE session_id = ?`, id, id)
		if err != nil {
			return fmt.Errorf("read legacy chat sources: %w", err)
		}
		compatible := true
		for rows.Next() {
			var source string
			if err := rows.Scan(&source); err != nil {
				_ = rows.Close()
				return err
			}
			if source != selectedURL {
				compatible = false
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		if compatible {
			if _, err := tx.Exec(`UPDATE sessions SET scope = ? WHERE id = ?`, newScope, id); err != nil {
				return fmt.Errorf("migrate legacy chat session: %w", err)
			}
		}
	}
	return tx.Commit()
}

func initChatSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read chat schema version: %w", err)
	}
	if version != 0 && version != 1 && version != 2 && version != 3 && version != 4 && version != 5 {
		return fmt.Errorf("unsupported chat database schema version %d", version)
	}
	if version >= 1 {
		for _, table := range []string{"sessions", "messages", "queries", "recordsets"} {
			var name string
			if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
				return fmt.Errorf("chat database is missing %s metadata: %w", table, err)
			}
		}
		var integrity string
		if err := db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
			return fmt.Errorf("chat database integrity check failed: %s: %v", integrity, err)
		}
		if version >= 2 {
			var name string
			if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'session_workspace'`).Scan(&name); err != nil {
				return fmt.Errorf("chat database is missing workspace metadata: %w", err)
			}
		}
		if version >= 3 {
			var name string
			if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'bookmarks'`).Scan(&name); err != nil {
				return fmt.Errorf("chat database is missing bookmark metadata: %w", err)
			}
			if version == 5 {
				return nil
			}
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, scope TEXT NOT NULL, title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS sessions_scope_recent ON sessions(scope, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS messages (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, role TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, query_id TEXT NOT NULL DEFAULT '', recordset_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS messages_session_order ON messages(session_id)`,
		`CREATE TABLE IF NOT EXISTS queries (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, origin_message_id TEXT NOT NULL, title TEXT NOT NULL, dtql TEXT NOT NULL, source TEXT NOT NULL, parameters_json TEXT NOT NULL, executed_at TEXT NOT NULL, error TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS recordsets (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, query_id TEXT NOT NULL REFERENCES queries(id) ON DELETE CASCADE, origin_message_id TEXT NOT NULL, title TEXT NOT NULL, dtql TEXT NOT NULL, source TEXT NOT NULL, environment TEXT NOT NULL, database_id TEXT NOT NULL, parameters_json TEXT NOT NULL, created_at TEXT NOT NULL, result_json BLOB NOT NULL, parent_recordset_id TEXT NOT NULL DEFAULT '', join_candidate_id TEXT NOT NULL DEFAULT '', join_applied_edges_json TEXT NOT NULL DEFAULT '[]')`,
		`CREATE TABLE IF NOT EXISTS session_workspace (session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE, state_json TEXT NOT NULL)`,
		// A bookmark snapshot has no foreign key to the transient session rows.
		// It owns its encoded result, view, and selection after creation.
		`CREATE TABLE IF NOT EXISTS bookmarks (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, scope TEXT NOT NULL, title TEXT NOT NULL, tags_json TEXT NOT NULL, target_kind TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, snapshot_json BLOB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS bookmarks_scope_project_recent ON bookmarks(scope, project_id, updated_at DESC, id DESC)`,
		`PRAGMA user_version = 5`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("initialize chat schema: %w", err)
		}
	}
	if version >= 1 && version < 4 {
		if err := addRecordsetColumnIfMissing(tx, "parent_recordset_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("migrate chat lineage parent: %w", err)
		}
		if err := addRecordsetColumnIfMissing(tx, "join_candidate_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("migrate chat lineage candidate: %w", err)
		}
	}
	if version >= 1 && version < 5 {
		if err := addRecordsetColumnIfMissing(tx, "join_applied_edges_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return fmt.Errorf("migrate chat lineage edges: %w", err)
		}
	}
	return tx.Commit()
}

func addRecordsetColumnIfMissing(tx *sql.Tx, name, definition string) error {
	rows, err := tx.Query(`PRAGMA table_info(recordsets)`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var ordinal int
		var column, kind string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&ordinal, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if column == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(`ALTER TABLE recordsets ADD COLUMN ` + name + ` ` + definition)
	return err
}

func (s *SessionStore) Close() error { return s.db.Close() }

func (s *SessionStore) List(ctx context.Context) ([]ChatSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, created_at, updated_at FROM sessions WHERE scope = ? ORDER BY updated_at DESC, id DESC`, s.scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var sessions []ChatSession
	for rows.Next() {
		var item ChatSession
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Title, &created, &updated); err != nil {
			return nil, err
		}
		if item.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("corrupt chat session %q timestamp: %w", item.ID, err)
		}
		if item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
			return nil, fmt.Errorf("corrupt chat session %q timestamp: %w", item.ID, err)
		}
		sessions = append(sessions, item)
	}
	return sessions, rows.Err()
}

func (s *SessionStore) Create(ctx context.Context, title string) (ChatSession, error) {
	now := time.Now().UTC()
	item := ChatSession{ID: uuid.NewString(), Title: normalizeSessionTitle(title), CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, scope, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, item.ID, s.scope, item.Title, stamp(now), stamp(now))
	return item, err
}

func normalizeSessionTitle(title string) string {
	title = strings.Join(strings.Fields(sanitizeTerminalText(title)), " ")
	if title == "" {
		return "New chat"
	}
	if len([]rune(title)) > 80 {
		return string([]rune(title)[:80])
	}
	return title
}

func (s *SessionStore) LatestOrCreate(ctx context.Context) (ChatSession, error) {
	list, err := s.List(ctx)
	if err != nil {
		return ChatSession{}, err
	}
	if len(list) > 0 {
		return s.Load(ctx, list[0].ID)
	}
	return s.Create(ctx, "New chat")
}

func (s *SessionStore) Load(ctx context.Context, id string) (ChatSession, error) {
	var item ChatSession
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM sessions WHERE id = ? AND scope = ?`, id, s.scope).Scan(&item.ID, &item.Title, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return item, fmt.Errorf("chat session %q not found in this access scope", id)
	}
	if err != nil {
		return item, err
	}
	if item.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return item, fmt.Errorf("corrupt chat session timestamp: %w", err)
	}
	if item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return item, fmt.Errorf("corrupt chat session timestamp: %w", err)
	}
	item.RecordSets = make(map[string]RecordSet)
	item.Bookmarks = make(map[string]Bookmark)
	if err := s.loadMessages(ctx, &item); err != nil {
		return ChatSession{}, err
	}
	if err := s.loadQueries(ctx, &item); err != nil {
		return ChatSession{}, err
	}
	if err := s.loadRecordSets(ctx, &item); err != nil {
		return ChatSession{}, err
	}
	if err := s.loadBookmarks(ctx, &item); err != nil {
		return ChatSession{}, err
	}
	if err := s.loadWorkspace(ctx, &item); err != nil {
		return ChatSession{}, err
	}
	if err := s.validateBookmarkReferences(ctx, s.db, item.Workspace); err != nil {
		return ChatSession{}, err
	}
	for _, message := range item.Messages {
		if message.Kind == "grid" {
			if _, ok := item.RecordSets[message.RecordSetID]; !ok {
				return ChatSession{}, fmt.Errorf("chat session %q has a missing RecordSet %q", id, message.RecordSetID)
			}
		}
	}
	return item, nil
}

func (s *SessionStore) loadWorkspace(ctx context.Context, item *ChatSession) error {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT state_json FROM session_workspace WHERE session_id = ?`, item.ID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		item.Workspace = WorkspaceState{Views: map[string]RecordSetView{}, Selections: map[string]Selection{}, ActiveTab: "Project"}
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(payload), &item.Workspace); err != nil {
		return fmt.Errorf("corrupt workspace for chat session %q: %w", item.ID, err)
	}
	if item.Workspace.Views == nil {
		item.Workspace.Views = map[string]RecordSetView{}
	}
	if item.Workspace.Selections == nil {
		item.Workspace.Selections = map[string]Selection{}
	}
	return validateLoadedWorkspace(*item)
}

func validateLoadedWorkspace(item ChatSession) error {
	for id, view := range item.Workspace.Views {
		if view.ID != id {
			return fmt.Errorf("workspace view %q has inconsistent identity", id)
		}
		record, ok := item.RecordSets[view.RecordSetID]
		if !ok {
			return fmt.Errorf("workspace view %q references a missing RecordSet", view.ID)
		}
		for _, column := range append(append([]string{}, view.Columns...), view.OrderBy) {
			if column != "" && !containsColumn(record.Result.Columns, column) {
				return fmt.Errorf("workspace view %q has invalid column %q", view.ID, column)
			}
		}
		for _, row := range view.RowIndices {
			if row < 0 || row >= len(record.Result.Rows) {
				return fmt.Errorf("workspace view %q has invalid row index %d", view.ID, row)
			}
		}
	}
	for id, selection := range item.Workspace.Selections {
		if selection.ID != id {
			return fmt.Errorf("workspace selection %q has inconsistent identity", id)
		}
		view, ok := item.Workspace.Views[selection.ViewID]
		if !ok {
			return fmt.Errorf("workspace selection %q references a missing view", selection.ID)
		}
		record := item.RecordSets[view.RecordSetID]
		allowedRows := make(map[int]bool, len(view.RowIndices))
		for _, row := range view.RowIndices {
			allowedRows[row] = true
		}
		viewColumns := view.Columns
		if len(viewColumns) == 0 {
			viewColumns = record.Result.Columns
		}
		for _, row := range selection.Rows {
			if !allowedRows[row] {
				return fmt.Errorf("workspace selection %q has a row outside its view", selection.ID)
			}
		}
		for _, column := range selection.Columns {
			if !containsColumn(viewColumns, column) {
				return fmt.Errorf("workspace selection %q has a column outside its view", selection.ID)
			}
		}
		for _, span := range selection.Ranges {
			if span.FirstRow < 0 || span.LastRow < span.FirstRow || span.LastRow >= len(record.Result.Rows) || span.FirstCol < 0 || span.LastCol < span.FirstCol || span.LastCol >= len(record.Result.Columns) {
				return fmt.Errorf("workspace selection %q has an invalid cell range", selection.ID)
			}
			for row := span.FirstRow; row <= span.LastRow; row++ {
				if !allowedRows[row] {
					return fmt.Errorf("workspace selection %q has a cell range outside its view", selection.ID)
				}
			}
		}
		if err := validateSelectionRangeProjection(record.Result.Columns, selection.Rows, selection.Columns, selection.Ranges); err != nil {
			return fmt.Errorf("workspace selection %q: %w", selection.ID, err)
		}
	}
	if item.Workspace.CurrentSelectionID != "" {
		if _, ok := item.Workspace.Selections[item.Workspace.CurrentSelectionID]; !ok {
			return fmt.Errorf("workspace current selection is missing")
		}
	}
	return nil
}

// SaveWorkspace replaces only session-scoped presentation/context state; it
// never modifies an immutable RecordSet or reruns a query.
func (s *SessionStore) SaveWorkspace(ctx context.Context, sessionID string, state WorkspaceState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode workspace: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sessionExists(ctx, tx, sessionID, s.scope); err != nil {
		return err
	}
	if err := s.validateBookmarkReferences(ctx, tx, state); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_workspace (session_id, state_json) VALUES (?, ?) ON CONFLICT(session_id) DO UPDATE SET state_json = excluded.state_json`, sessionID, string(payload)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, stamp(time.Now().UTC()), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SessionStore) loadMessages(ctx context.Context, item *ChatSession) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, kind, text, query_id, recordset_id, created_at FROM messages WHERE session_id = ? ORDER BY rowid`, item.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var message ChatMessage
		var created string
		if err := rows.Scan(&message.ID, &message.Role, &message.Kind, &message.Text, &message.QueryID, &message.RecordSetID, &created); err != nil {
			return err
		}
		if message.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return fmt.Errorf("corrupt chat message timestamp: %w", err)
		}
		item.Messages = append(item.Messages, message)
	}
	return rows.Err()
}

func (s *SessionStore) loadQueries(ctx context.Context, item *ChatSession) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, origin_message_id, title, dtql, source, parameters_json, executed_at, error FROM queries WHERE session_id = ? ORDER BY rowid`, item.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var query ExecutedQuery
		var params, executed string
		if err := rows.Scan(&query.ID, &query.OriginMessageID, &query.Title, &query.DTQL, &query.Source, &params, &executed, &query.Error); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(params), &query.Parameters); err != nil {
			return fmt.Errorf("corrupt chat query parameters %q: %w", query.ID, err)
		}
		if query.ExecutedAt, err = time.Parse(time.RFC3339Nano, executed); err != nil {
			return fmt.Errorf("corrupt chat query timestamp: %w", err)
		}
		item.Queries = append(item.Queries, query)
	}
	return rows.Err()
}

func (s *SessionStore) loadRecordSets(ctx context.Context, item *ChatSession) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json, parent_recordset_id, join_candidate_id, join_applied_edges_json FROM recordsets WHERE session_id = ? ORDER BY rowid`, item.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var record RecordSet
		var params, created string
		var payload []byte
		var parentID, candidateID, appliedJSON string
		if err := rows.Scan(&record.ID, &record.QueryID, &record.OriginMessageID, &record.Title, &record.DTQL, &record.Source, &record.Environment, &record.Database, &params, &created, &payload, &parentID, &candidateID, &appliedJSON); err != nil {
			return err
		}
		record.SessionID = item.ID
		if err := json.Unmarshal([]byte(params), &record.Parameters); err != nil {
			return fmt.Errorf("corrupt RecordSet parameters %q: %w", record.ID, err)
		}
		if record.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return fmt.Errorf("corrupt RecordSet timestamp %q: %w", record.ID, err)
		}
		if record.Result, err = decodeResult(payload); err != nil {
			return fmt.Errorf("corrupt RecordSet %q: %w", record.ID, err)
		}
		if parentID != "" || candidateID != "" {
			record.Lineage = &JoinLineage{ParentRecordSetID: parentID, CandidateID: JoinCandidateID(candidateID)}
			if err := json.Unmarshal([]byte(appliedJSON), &record.Lineage.AppliedEdges); err != nil {
				return fmt.Errorf("corrupt RecordSet JOIN lineage %q: %w", record.ID, err)
			}
		}
		item.RecordSets[record.ID] = record
	}
	return rows.Err()
}

func (s *SessionStore) Rename(ctx context.Context, id, title string) error {
	return s.updateSession(ctx, id, `UPDATE sessions SET title = ?, updated_at = ? WHERE id = ? AND scope = ?`, normalizeSessionTitle(title), stamp(time.Now().UTC()), id, s.scope)
}

// Activate makes the selected session the one reopened by the next CLI run.
func (s *SessionStore) Activate(ctx context.Context, id string) error {
	return s.updateSession(ctx, id, `UPDATE sessions SET updated_at = ? WHERE id = ? AND scope = ?`, stamp(time.Now().UTC()), id, s.scope)
}

func (s *SessionStore) Clear(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sessionExists(ctx, tx, id, s.scope); err != nil {
		return err
	}
	for _, table := range []string{"session_workspace", "recordsets", "queries", "messages"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE session_id = ?", id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET title = 'New chat', updated_at = ? WHERE id = ?`, stamp(time.Now().UTC()), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SessionStore) Delete(ctx context.Context, id string) error {
	return s.updateSession(ctx, id, `DELETE FROM sessions WHERE id = ? AND scope = ?`, id, s.scope)
}

func (s *SessionStore) updateSession(ctx context.Context, id, statement string, args ...any) error {
	result, err := s.db.ExecContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("chat session %q not found in this access scope", id)
	}
	return nil
}

func sessionExists(ctx context.Context, tx *sql.Tx, id, scope string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ? AND scope = ?`, id, scope).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat session %q not found in this access scope", id)
		}
		return err
	}
	return nil
}

func (s *SessionStore) AppendUser(ctx context.Context, sessionID, prompt string) (ChatMessage, error) {
	message := ChatMessage{ID: uuid.NewString(), Role: "You", Kind: "text", Text: prompt, CreatedAt: time.Now().UTC()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChatMessage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sessionExists(ctx, tx, sessionID, s.scope); err != nil {
		return ChatMessage{}, err
	}
	if err := insertMessage(ctx, tx, sessionID, message); err != nil {
		return ChatMessage{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, stamp(message.CreatedAt), sessionID); err != nil {
		return ChatMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return ChatMessage{}, err
	}
	return message, nil
}

func insertMessage(ctx context.Context, tx *sql.Tx, sessionID string, message ChatMessage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO messages (id, session_id, role, kind, text, query_id, recordset_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, message.ID, sessionID, message.Role, message.Kind, message.Text, message.QueryID, message.RecordSetID, stamp(message.CreatedAt))
	return err
}

// AppendQuery commits a successful execution at the tool boundary, before
// the model's final response. Each invocation gets a new immutable snapshot,
// including repeated executions of identical DTQL.
func (s *SessionStore) AppendQuery(ctx context.Context, sessionID, originID, source string, query QueryResult) (QueryResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueryResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkOrigin(ctx, tx, sessionID, originID); err != nil {
		return QueryResult{}, err
	}
	now := time.Now().UTC()
	if err := s.appendQueryTx(ctx, tx, sessionID, originID, source, &query, now); err != nil {
		return QueryResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, stamp(now), sessionID); err != nil {
		return QueryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return QueryResult{}, err
	}
	return query, nil
}

func (s *SessionStore) checkOrigin(ctx context.Context, tx *sql.Tx, sessionID, originID string) error {
	if err := sessionExists(ctx, tx, sessionID, s.scope); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE id = ? AND session_id = ? AND role = 'You'`, originID, sessionID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("originating user message %q is missing from this session", originID)
		}
		return err
	}
	return nil
}

func (s *SessionStore) appendQueryTx(ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
	query.QueryID = uuid.NewString()
	parameters := query.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		return fmt.Errorf("encode query parameters: %w", err)
	}
	errText := ""
	if query.Err != nil {
		errText = publicQueryError(query.Err, query.Parameters)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO queries (id, session_id, origin_message_id, title, dtql, source, parameters_json, executed_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, query.QueryID, sessionID, originID, query.Title, query.DTQL, source, string(paramsJSON), stamp(now), errText); err != nil {
		return err
	}
	message := ChatMessage{ID: uuid.NewString(), Role: "DataTug", QueryID: query.QueryID, CreatedAt: now}
	if query.Err != nil {
		message.Kind, message.Text = "error", errText
	} else {
		query.RecordSetID = uuid.NewString()
		databaseID := s.info.Database
		if query.SourceID != "" {
			databaseID = query.SourceID
		}
		payload, err := encodeResult(query.Result)
		if err != nil {
			return fmt.Errorf("encode query result: %w", err)
		}
		parentID, candidateID := "", ""
		appliedJSON := []byte("[]")
		if query.Lineage != nil {
			parentID, candidateID = query.Lineage.ParentRecordSetID, string(query.Lineage.CandidateID)
			if appliedJSON, err = json.Marshal(query.Lineage.AppliedEdges); err != nil {
				return fmt.Errorf("encode JOIN lineage: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recordsets (id, session_id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json, parent_recordset_id, join_candidate_id, join_applied_edges_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, query.RecordSetID, sessionID, query.QueryID, originID, query.Title, query.DTQL, source, s.info.Environment, databaseID, string(paramsJSON), stamp(now), payload, parentID, candidateID, string(appliedJSON)); err != nil {
			return err
		}
		message.Kind, message.RecordSetID = "grid", query.RecordSetID
	}
	return insertMessage(ctx, tx, sessionID, message)
}

func (s *SessionStore) AppendTurn(ctx context.Context, sessionID, originID, source string, turn Turn) (Turn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Turn{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkOrigin(ctx, tx, sessionID, originID); err != nil {
		return Turn{}, err
	}
	now := time.Now().UTC()
	if turn.Text != "" {
		if err := insertMessage(ctx, tx, sessionID, ChatMessage{ID: uuid.NewString(), Role: "DataTug", Kind: "text", Text: turn.Text, CreatedAt: now}); err != nil {
			return Turn{}, err
		}
	}
	for i := range turn.Queries {
		query := &turn.Queries[i]
		if query.QueryID != "" {
			continue
		} // already committed by run_dtql
		if err := s.appendQueryTx(ctx, tx, sessionID, originID, source, query, now); err != nil {
			return Turn{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, stamp(now), sessionID); err != nil {
		return Turn{}, err
	}
	if err := tx.Commit(); err != nil {
		return Turn{}, err
	}
	return turn, nil
}

func stamp(t time.Time) string { return t.Format(time.RFC3339Nano) }

type storedResult struct {
	Columns     []string                `json:"columns"`
	Rows        []storedRow             `json:"rows"`
	Limitations []secureread.Limitation `json:"limitations,omitempty"`
	Collection  string                  `json:"collection,omitempty"`
	Provenance  *dalgo2http.Provenance  `json:"provenance,omitempty"`
}
type storedRow struct {
	Key  string                 `json:"key,omitempty"`
	Data map[string]storedValue `json:"data"`
}
type storedValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value,omitempty"`
}

func encodeResult(result secureread.Result) ([]byte, error) {
	stored := storedResult{Columns: result.Columns, Limitations: result.Limitations, Collection: result.Collection, Provenance: result.Provenance, Rows: make([]storedRow, len(result.Rows))}
	for i, row := range result.Rows {
		stored.Rows[i] = storedRow{Key: row.Key, Data: make(map[string]storedValue, len(row.Data))}
		for name, value := range row.Data {
			encoded, err := encodeValue(value)
			if err != nil {
				return nil, fmt.Errorf("row %d column %q: %w", i, name, err)
			}
			stored.Rows[i].Data[name] = encoded
		}
	}
	return json.Marshal(stored)
}

func encodeValue(value any) (storedValue, error) {
	if value == nil {
		return storedValue{Type: "null"}, nil
	}
	var kind string
	switch value.(type) {
	case time.Time:
		kind = "time"
	case []byte:
		kind = "bytes"
	case int, int8, int16, int32, int64:
		kind = "int"
	case uint, uint8, uint16, uint32, uint64:
		kind = "uint"
	case float32, float64:
		kind = "float"
	case string:
		kind = "string"
	case bool:
		kind = "bool"
	default:
		kind = "json"
	}
	bytes, err := json.Marshal(value)
	return storedValue{Type: kind, Value: bytes}, err
}

func decodeResult(payload []byte) (secureread.Result, error) {
	var stored storedResult
	if err := json.Unmarshal(payload, &stored); err != nil {
		return secureread.Result{}, err
	}
	// A real empty DALgo result can have nil Columns. The rows field, however,
	// is always an explicit JSON array in a valid snapshot.
	if stored.Rows == nil {
		return secureread.Result{}, errors.New("missing rows")
	}
	result := secureread.Result{Columns: stored.Columns, Limitations: stored.Limitations, Collection: stored.Collection, Provenance: stored.Provenance, Rows: make([]secureread.Row, len(stored.Rows))}
	for i, row := range stored.Rows {
		result.Rows[i] = secureread.Row{Key: row.Key, Data: make(map[string]any, len(row.Data))}
		for name, value := range row.Data {
			decoded, err := decodeValue(value)
			if err != nil {
				return secureread.Result{}, fmt.Errorf("row %d column %q: %w", i, name, err)
			}
			result.Rows[i].Data[name] = decoded
		}
	}
	return result, nil
}

func decodeValue(value storedValue) (any, error) {
	switch value.Type {
	case "null":
		return nil, nil
	case "time":
		var v time.Time
		return v, json.Unmarshal(value.Value, &v)
	case "bytes":
		var v []byte
		return v, json.Unmarshal(value.Value, &v)
	case "int":
		var number json.Number
		if err := json.Unmarshal(value.Value, &number); err != nil {
			return nil, err
		}
		return number.Int64()
	case "uint":
		return strconv.ParseUint(string(value.Value), 10, 64)
	case "float":
		var v float64
		return v, json.Unmarshal(value.Value, &v)
	case "string":
		var v string
		return v, json.Unmarshal(value.Value, &v)
	case "bool":
		var v bool
		return v, json.Unmarshal(value.Value, &v)
	case "json":
		var v any
		decoder := json.NewDecoder(strings.NewReader(string(value.Value)))
		decoder.UseNumber()
		return v, decoder.Decode(&v)
	default:
		return nil, fmt.Errorf("unknown cell type %q", value.Type)
	}
}
