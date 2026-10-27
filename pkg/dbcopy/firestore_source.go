package dbcopy

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"cloud.google.com/go/firestore"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2firestore"
	dalrecord "github.com/dal-go/record"
)

var firestoreID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Firestore URLs select the user's Google Cloud project and database. Credentials
// come from Application Default Credentials, never from the URL or project files.
func parseFirestoreSource(rest, display string) (BackendRef, error) {
	u, err := url.Parse("firestore://" + rest)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return BackendRef{}, fmt.Errorf("Firestore URL must be firestore://PROJECT/DATABASE without credentials or options")
	}
	project, database := u.Host, strings.TrimPrefix(u.Path, "/")
	if database == "" {
		database = "(default)"
	}
	if !firestoreID.MatchString(project) || (database != "(default)" && !firestoreID.MatchString(database)) {
		return BackendRef{}, fmt.Errorf("Firestore URL requires a project ID and one database ID")
	}
	return BackendRef{Scheme: "firestore", ProjectID: project, DatasetID: database, Path: project + "/" + database, Raw: display}, nil
}

// Cache clients for the process lifetime: DALgo's DB does not expose Close.
var firestoreSources = struct {
	sync.Mutex
	dbs map[string]dal.DB
}{dbs: make(map[string]dal.DB)}

func (r BackendRef) openFirestore(ctx context.Context, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, fmt.Errorf("Firestore query sources cannot be opened as copy destinations")
	}
	firestoreSources.Lock()
	defer firestoreSources.Unlock()
	cacheKey := r.Path + "@" + os.Getenv("FIRESTORE_EMULATOR_HOST")
	if db := firestoreSources.dbs[cacheKey]; db != nil {
		return db, nil
	}
	client, err := firestore.NewClientWithDatabase(ctx, r.ProjectID, r.DatasetID)
	if err != nil {
		return nil, err
	}
	db := firestoreQuerySource{dalgo2firestore.NewDatabase(r.DatasetID, client)}
	firestoreSources.dbs[cacheKey] = db
	return db, nil
}

// TugQL emits map-shaped queries without a Go record factory. Supply one at
// the source boundary while preserving the original filters and query window.
type firestoreQuerySource struct{ dal.DB }
type firestoreMapQuery struct{ dal.StructuredQuery }

func (q firestoreMapQuery) IntoRecord() dalrecord.Record {
	return dalrecord.NewRecordWithIncompleteKey(q.From().Base().Name(), reflect.String, map[string]any{})
}
func (s firestoreQuerySource) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	structured, ok := query.(dal.StructuredQuery)
	if !ok {
		return nil, fmt.Errorf("%w: Firestore requires a structured query", dal.ErrNotSupported)
	}
	if len(structured.From().Joins()) > 0 {
		return nil, fmt.Errorf("%w: Firestore joins are unsupported", dal.ErrNotSupported)
	}
	for _, column := range structured.Columns() {
		if column.Alias != "" {
			return nil, fmt.Errorf("%w: Firestore projection aliases are unsupported", dal.ErrNotSupported)
		}
		if _, ok := column.Expression.(dal.FieldRef); !ok {
			return nil, fmt.Errorf("%w: Firestore projections require plain fields", dal.ErrNotSupported)
		}
	}
	return s.DB.ExecuteQueryToRecordsReader(ctx, firestoreMapQuery{structured})
}

// Query sources expose no write transaction even when ADC has broader IAM rights.
func (s firestoreQuerySource) RunReadwriteTransaction(context.Context, dal.RWTxWorker, ...dal.TransactionOption) error {
	return fmt.Errorf("%w: Firestore query sources are read-only", dal.ErrNotSupported)
}
