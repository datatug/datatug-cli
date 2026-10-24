package watch

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
)

// RecipientAuthority is fresh server-owned identity and entitlement evidence.
// Paid is never populated from a browser request or an unverified token claim.
type RecipientAuthority struct {
	AccountID string
	Email     string
	Phone     string
	Paid      bool
}

// FirestoreOptions binds one Store to an existing trusted project and store/Space
// namespace. AccountRoot must be shared by projects in that same namespace so
// creating another project cannot evade account/channel quotas. Both keys and
// all callbacks are constructed by the authenticated server, never request JSON.
type FirestoreOptions struct {
	ProjectKey *record.Key
	// StorageRoot is a separately protected server-only project container. It
	// must not be a descendant of a project subtree readable by Space members.
	StorageRoot *record.Key
	AccountRoot *record.Key
	// AccountNamespace is a server-fixed deployment quota namespace. Empty uses Config.Namespace.
	AccountNamespace string
	ProjectID        string
	Config           Config
	Authorize        func(context.Context, dal.ReadTransaction) error
	AdmitDefinition  func(context.Context, dal.ReadTransaction, Definition, string) (string, error)
	AdmitRunner      func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error)
	AdmitRecipient   func(context.Context, dal.ReadTransaction, Definition, string) (RecipientAuthority, error)
	// DefinitionChanged stages exactly one <=1KiB deterministic metadata event.
	// It runs after every read, must not read/publish/network, and shares commit.
	DefinitionChanged func(context.Context, dal.ReadwriteTransaction, Definition) error
	// WorkReady stages one deterministic metadata wakeup for new pending work.
	// It shares DefinitionChanged's no-read/no-network/<=1KiB contract.
	WorkReady func(context.Context, dal.ReadwriteTransaction, WorkEvent) error
}

// These are conservative deployment budgets, not claimed Firestore API maxima.
const cloudQueryRows = 256
const cloudReadRows = 2000
const cloudWriteRows = 400
const cloudWriteBytes = 4 * 1024 * 1024

var ErrCapacity = errors.New("watch operation exceeds its bounded storage budget")

type firestoreDatabase struct {
	db               dal.DB
	options          FirestoreOptions
	root             *record.Key
	quotaRoot        *record.Key
	coordinationRoot *record.Key
}

// NewFirestoreStore reuses the pinned DALGO Firestore adapter. It neither creates
// a database nor owns its shared client; Store.Close leaves that client open.
// Authorization executes inside the same transaction as protected reads/writes.
// Callback errors abort the transaction, and callbacks may run more than once.
func NewFirestoreStore(db dal.DB, options FirestoreOptions) (*Store, error) {
	if db == nil || db.Adapter().Name() != "firestore" || options.ProjectKey == nil || options.StorageRoot == nil || options.AccountRoot == nil || options.Authorize == nil || options.AdmitDefinition == nil || options.AdmitRunner == nil || options.AdmitRecipient == nil || options.DefinitionChanged == nil || options.WorkReady == nil {
		return nil, ErrInvalid
	}
	if options.ProjectID == "" || options.ProjectKey.ID != options.ProjectID || options.ProjectKey.Validate() != nil || options.StorageRoot.Validate() != nil || options.AccountRoot.Validate() != nil || strings.Contains(options.ProjectID, "/") || options.Config.Namespace == "" || len(options.Config.Namespace) > 128 {
		return nil, ErrInvalid
	}
	if watchKeyContains(options.StorageRoot, options.ProjectKey) || watchKeyContains(options.AccountRoot, options.ProjectKey) {
		return nil, ErrInvalid
	}
	// Cloud ingestion and UI reads are bounded; larger deployments need explicit
	// pagination rather than an accidental unbounded project snapshot.
	if options.Config.HistoryLimit == 0 {
		options.Config.HistoryLimit = 100
	}
	if options.Config.HistoryLimit > 100 {
		return nil, ErrCapacity
	}
	c, err := normalizeConfig(options.Config)
	if err != nil {
		return nil, err
	}
	if options.AccountNamespace == "" {
		options.AccountNamespace = c.Namespace
	}
	if len(options.AccountNamespace) > 128 {
		return nil, ErrInvalid
	}
	options.Config = c
	f := &firestoreDatabase{db: db, options: options,
		root:             record.NewKeyWithParentAndID(cloneWatchKey(options.StorageRoot), "watchCoordinators", stable(c.Namespace)),
		quotaRoot:        record.NewKeyWithParentAndID(cloneWatchKey(options.AccountRoot), "watchAccountNamespaces", stable(options.AccountNamespace)),
		coordinationRoot: record.NewKeyWithParentAndID(cloneWatchKey(options.AccountRoot), "watchAccountNamespaces", stable(c.Namespace))}
	return &Store{db: f, config: c, cloud: f}, nil
}

func cloneWatchKey(k *record.Key) *record.Key {
	if k == nil {
		return nil
	}
	return record.NewKeyWithParentAndID(cloneWatchKey(k.Parent()), k.Collection(), k.ID)
}

func (*firestoreDatabase) Close() error { return nil }

type firestoreReadContext struct{}

// withRead keeps compound views (e.g. inbox plus current Watch visibility) in
// one authorized snapshot. Nested Store reads reuse its transaction-local view.
func (f *firestoreDatabase) withRead(ctx context.Context, fn func(context.Context, *firestoreSession) error) error {
	if current, ok := ctx.Value(firestoreReadContext{}).(*firestoreSession); ok && current.database == f {
		return fn(ctx, current)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return f.db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		if err := f.options.Authorize(ctx, tx); err != nil {
			return err
		}
		session := newFirestoreSession(f, tx)
		return fn(context.WithValue(ctx, firestoreReadContext{}, session), session)
	}, dal.TxWithAttempts(5))
}

func (f *firestoreDatabase) Get(ctx context.Context, r record.Record) error {
	return f.withRead(ctx, func(ctx context.Context, tx *firestoreSession) error { return tx.Get(ctx, r) })
}

func (f *firestoreDatabase) Exists(ctx context.Context, key *record.Key) (bool, error) {
	var result bool
	err := f.withRead(ctx, func(ctx context.Context, tx *firestoreSession) error {
		result = false
		var err error
		result, err = tx.Exists(ctx, key)
		return err
	})
	return result, err
}

func (f *firestoreDatabase) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	var result dal.RecordsReader
	err := f.withRead(ctx, func(ctx context.Context, tx *firestoreSession) error {
		result = nil
		var err error
		result, err = tx.ExecuteQueryToRecordsReader(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (f *firestoreDatabase) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, dal.ErrNotSupported
}

func (f *firestoreDatabase) RunReadwriteTransaction(ctx context.Context, fn dal.RWTxWorker, _ ...dal.TransactionOption) error {
	return f.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := f.options.Authorize(ctx, tx); err != nil {
			return err
		}
		// A small revision document is a serialization fence, not a snapshot of
		// project state. It also protects empty query/capacity decisions.
		gate := map[string]any{}
		err := tx.Get(ctx, record.NewRecordWithData(f.root, gate))
		missingGate := record.IsNotFound(err)
		if err != nil && !record.IsNotFound(err) {
			return err
		}
		var revision int64
		if err == nil {
			var ok bool
			revision, ok = gate["revision"].(int64)
			if !ok || revision < 0 || revision == int64(^uint64(0)>>1) {
				return ErrInvalid
			}
		}
		session := newFirestoreSession(f, tx)
		session.ReadwriteTransaction = tx
		if value, exists := gate["pendingOutbox"]; exists {
			var ok bool
			session.pendingOutbox, ok = value.(int64)
			if !ok || session.pendingOutbox < 0 {
				return ErrInvalid
			}
		}
		if value, exists := gate["sweepCursor"]; exists {
			var ok bool
			session.sweepCursor, ok = value.(string)
			if !ok || session.sweepCursor != "" && !nonceHashPattern.MatchString(session.sweepCursor) {
				return ErrInvalid
			}
		}
		originalSweep := session.sweepCursor
		if err := fn(context.WithValue(ctx, firestoreReadContext{}, session), session); err != nil {
			return err
		}
		if err := session.flush(ctx); err != nil {
			return err
		}
		for _, d := range session.changed {
			if err := f.options.DefinitionChanged(ctx, tx, d); err != nil {
				return err
			}
		}
		for _, event := range session.ready {
			if err := f.options.WorkReady(ctx, tx, event); err != nil {
				return err
			}
		}
		if !missingGate && !session.needsProjectFence(originalSweep) {
			return nil
		}
		return tx.Set(ctx, record.NewRecordWithData(f.root, map[string]any{"revision": revision + 1, "pendingOutbox": session.backlog(), "sweepCursor": session.sweepCursor}))
	}, dal.TxWithAttempts(5))
}

func (s *Store) readView(ctx context.Context, fn func(context.Context) error) error {
	if s.cloud == nil {
		return fn(ctx)
	}
	return s.cloud.withRead(ctx, func(ctx context.Context, _ *firestoreSession) error { return fn(ctx) })
}

func (s *Store) sourceAdmission(ctx context.Context, tx dal.ReadwriteTransaction, d Definition, operation string) (string, error) {
	if s.cloud == nil {
		return d.SourceRevision, nil
	}
	session, ok := tx.(*firestoreSession)
	if !ok {
		return "", ErrForbidden
	}
	revision, err := s.cloud.options.AdmitDefinition(ctx, session.reader, d, operation)
	if err != nil {
		return "", err
	}
	if revision == "" || len(revision) > 128 {
		return "", ErrInvalid
	}
	if operation != "save" && operation != "reset" && revision != d.SourceRevision {
		return "", ErrConflict
	}
	return revision, nil
}

func (s *Store) recipientAdmission(ctx context.Context, tx dal.ReadwriteTransaction, d Definition, user string) (RecipientAuthority, error) {
	if s.cloud == nil {
		return RecipientAuthority{}, nil
	}
	session, ok := tx.(*firestoreSession)
	if !ok {
		return RecipientAuthority{}, ErrForbidden
	}
	authority, err := s.cloud.options.AdmitRecipient(ctx, session.reader, d, user)
	if err != nil {
		return RecipientAuthority{}, err
	}
	if authority.AccountID == "" {
		return RecipientAuthority{}, ErrForbidden
	}
	return authority, nil
}

func (s *Store) runnerAdmission(ctx context.Context, tx dal.ReadwriteTransaction, d Definition, id RunnerIdentity) (bool, error) {
	if s.cloud == nil {
		return true, nil
	}
	session, ok := tx.(*firestoreSession)
	if !ok {
		return false, ErrForbidden
	}
	return s.cloud.options.AdmitRunner(ctx, session.reader, d, id)
}

func watchKeyContains(child, parent *record.Key) bool {
	for key := child; key != nil; key = key.Parent() {
		if key.String() == parent.String() {
			return true
		}
	}
	return false
}
func (s *Store) inReadView(ctx context.Context) bool {
	current, ok := ctx.Value(firestoreReadContext{}).(*firestoreSession)
	return ok && current.database == s.cloud
}
