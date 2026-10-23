package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

const (
	definitions   = "dt_watch_definitions"
	observations  = "dt_watch_observations"
	runs          = "dt_watch_runs"
	runners       = "dt_watch_runners"
	subscriptions = "dt_watch_subscriptions"
	inbox         = "dt_watch_inbox"
	outbox        = "dt_watch_outbox"
	audit         = "dt_watch_audit"
	operations    = "dt_watch_operations"
)

var tables = []string{definitions, observations, runs, runners, subscriptions, inbox, outbox, audit, operations, telegramLinks, telegramRequests, quotas}

// Store separates durable project definitions from bounded runtime tables. Every
// transition uses the same DALGO read/write transaction, with SQLite IMMEDIATE
// locking established before the callback. No DALGO precondition is assumed.
type Store struct {
	db           *dalgo2sqlite.Database
	config       Config
	beforeCommit func() error
}

var _ DefinitionRepository = (*Store)(nil)

// NewSQLiteStore accepts an absolute local file path or sqlite:///absolute/path.
// Storage configuration belongs to daemon deployment. Arbitrary driver DSNs are
// refused so a caller cannot override transaction locking or durability pragmas.
func NewSQLiteStore(connection string, c Config) (*Store, error) {
	if c.Namespace == "" || len(c.Namespace) > 128 {
		return nil, ErrInvalid
	}
	path := connection
	if strings.HasPrefix(connection, "sqlite:") {
		u, e := url.Parse(connection)
		if e != nil || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, ErrInvalid
		}
		path = u.Path
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "?#") || strings.HasPrefix(path, "//") {
		return nil, fmt.Errorf("%w: SQLite storage requires an absolute local file path", ErrInvalid)
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0) {
		return nil, ErrInvalid
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = f.Close(); e != nil {
		return nil, e
	}
	if c.MaxWatchesPerUser <= 0 {
		c.MaxWatchesPerUser = 100
	}
	if c.MinIntervalSeconds <= 0 {
		c.MinIntervalSeconds = 5
	}
	if c.MaxWatchesPerUser > 1000 || c.MinIntervalSeconds < 5 || c.MinIntervalSeconds > 3600 {
		return nil, ErrInvalid
	}
	if c.EmailDailyLimit <= 0 {
		c.EmailDailyLimit = 50
	}
	if c.SMSDailyLimit <= 0 {
		c.SMSDailyLimit = 10
	}
	if c.EmailMinuteLimit <= 0 {
		c.EmailMinuteLimit = 5
	}
	if c.SMSMinuteLimit <= 0 {
		c.SMSMinuteLimit = 2
	}
	if c.TelegramDailyLimit <= 0 {
		c.TelegramDailyLimit = 100
	}
	if c.TelegramMinuteLimit <= 0 {
		c.TelegramMinuteLimit = 5
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.LeaseDuration <= 0 {
		c.LeaseDuration = 30 * time.Second
	}
	if c.RunnerTTL <= 0 {
		c.RunnerTTL = 90 * time.Second
	}
	if c.ExecutionTimeout <= 0 {
		c.ExecutionTimeout = 2 * time.Minute
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.HistoryLimit <= 0 {
		c.HistoryLimit = 1000
	}
	if c.LeaseDuration > time.Minute || c.ExecutionTimeout > 15*time.Minute || c.HistoryLimit > 10000 || c.MaxAttempts > 10 {
		return nil, ErrInvalid
	}
	opts := dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite", Recordsets: map[string]*dalgo2sql.Recordset{}}
	for _, table := range tables {
		opts.Recordsets[table] = dalgo2sql.NewRecordset(table, dalgo2sql.Table, []dal.FieldRef{dal.Field("id")})
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_txlock=immediate&_pragma=busy_timeout(50)&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)"
	db, e := dalgo2sqlite.NewDatabaseWithOptions(dsn, dal.NewSchema(nil, nil), opts)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fields := []dbschema.FieldDef{{Name: "id", Type: dbschema.String}, {Name: "namespace", Type: dbschema.String}, {Name: "project", Type: dbschema.String}, {Name: "environment", Type: dbschema.String}, {Name: "watch", Type: dbschema.String}, {Name: "owner", Type: dbschema.String}, {Name: "visibility", Type: dbschema.String}, {Name: "updated", Type: dbschema.Int}, {Name: "payload", Type: dbschema.String}}
	for _, table := range tables {
		e = db.CreateCollection(ctx, dbschema.CollectionDef{Name: table, Fields: fields, PrimaryKey: []dal.FieldName{"id"}, Indexes: []dbschema.IndexDef{{Name: table + "_scope", Fields: []dal.FieldName{"namespace", "project", "environment", "watch"}}}}, ddl.IfNotExists())
		if e != nil {
			_ = db.Close()
			return nil, e
		}
	}
	return &Store{db: db, config: c}, nil
}
func (s *Store) Close() error   { return s.db.Close() }
func (s *Store) now() time.Time { return s.config.Now().UTC() }
func stable(parts ...string) string {
	b, _ := json.Marshal(parts)
	v := sha256.Sum256(b)
	return hex.EncodeToString(v[:])
}
func key(table string, scope Scope, id string) *record.Key {
	return record.NewKeyWithID(table, stable(scope.Namespace, scope.Project, scope.Environment, id))
}
func (s *Store) validScope(scope Scope) bool {
	return scope.Namespace == s.config.Namespace && scope.Project != "" && scope.Environment != "" && len(scope.Project) <= 200 && len(scope.Environment) <= 100
}
func (s *Store) actor(a Actor) error {
	if a.ID == "" || !s.validScope(a.Scope) {
		return ErrForbidden
	}
	return nil
}
func (s *Store) transaction(ctx context.Context, fn dal.RWTxWorker) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		err := s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			if err := fn(ctx, tx); err != nil {
				return err
			}
			if s.beforeCommit != nil {
				return s.beforeCommit()
			}
			return nil
		})
		var code interface{ Code() int }
		if !errors.As(err, &code) || (code.Code()&255 != 5 && code.Code()&255 != 6) {
			return err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func read[T any](ctx context.Context, r dal.Getter, table string, scope Scope, id string) (T, error) {
	var data T
	m := map[string]any{}
	err := r.Get(ctx, record.NewRecordWithData(key(table, scope, id), m))
	if record.IsNotFound(err) {
		return data, ErrNotFound
	}
	if err != nil {
		return data, err
	}
	p, ok := m["payload"].(string)
	if !ok {
		return data, fmt.Errorf("watch storage payload type %T", m["payload"])
	}
	err = json.Unmarshal([]byte(p), &data)
	return data, err
}
func (s *Store) save(ctx context.Context, tx dal.ReadwriteTransaction, table string, scope Scope, id, watchID, owner, visibility string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) > 128*1024 {
		return fmt.Errorf("%w: record exceeds 128 KiB", ErrInvalid)
	}
	m := map[string]any{"namespace": scope.Namespace, "project": scope.Project, "environment": scope.Environment, "watch": watchID, "owner": owner, "visibility": visibility, "updated": s.now().UnixNano(), "payload": string(payload)}
	k := key(table, scope, id)
	exists, err := tx.Exists(ctx, k)
	if err != nil {
		return err
	}
	if !exists {
		return tx.Insert(ctx, record.NewRecordWithData(k, m))
	}
	u := make([]update.Update, 0, len(m))
	for field, value := range m {
		u = append(u, update.ByFieldName(field, value))
	}
	return tx.Update(ctx, k, u)
}
func list[T any](ctx context.Context, r dal.QueryExecutor, table string, scope Scope, watchID string) ([]T, error) {
	conditions := []dal.Condition{dal.WhereField("namespace", dal.Equal, scope.Namespace)}
	if scope.Project != "" {
		conditions = append(conditions, dal.WhereField("project", dal.Equal, scope.Project))
	}
	if scope.Environment != "" {
		conditions = append(conditions, dal.WhereField("environment", dal.Equal, scope.Environment))
	}
	if watchID != "" {
		conditions = append(conditions, dal.WhereField("watch", dal.Equal, watchID))
	}
	q := dal.From(dal.NewRootCollectionRef(table, "")).NewQuery().Where(dal.NewGroupCondition(dal.And, conditions...)).SelectColumns()
	reader, err := r.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	items := []T{}
	for {
		rec, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) {
			break
		}
		if err != nil {
			return nil, err
		}
		m, ok := rec.Data().(map[string]any)
		if !ok {
			return nil, fmt.Errorf("watch storage row type %T", rec.Data())
		}
		p, ok := m["payload"].(string)
		if !ok {
			return nil, fmt.Errorf("watch storage payload type %T", m["payload"])
		}
		var item T
		if err = json.Unmarshal([]byte(p), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
func (s *Store) log(ctx context.Context, tx dal.ReadwriteTransaction, scope Scope, actor, action, id string) error {
	now := s.now()
	e := AuditEvent{ID: stable(actor, action, id, now.String()), Scope: scope, Actor: actor, Action: action, ResourceID: id, At: now}
	return s.save(ctx, tx, audit, scope, e.ID, id, actor, "", e)
}
func visible(a Actor, d Definition) bool {
	return a.Scope == d.Scope && d.Lifecycle != "deleted" && (d.Visibility == "shared" || d.Owner == a.ID)
}
func editable(a Actor, d Definition) bool {
	return visible(a, d) && (d.Owner == a.ID || d.Visibility == "shared" && a.SharedEditor)
}
