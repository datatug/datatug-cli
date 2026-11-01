package dbcopy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/stretchr/testify/require"
)

func TestSQLiteReadSourcesRefuseNativeWrites(t *testing.T) {
	t.Parallel()
	for name, open := range map[string]func(BackendRef, context.Context) (dal.DB, error){
		"Open": BackendRef.Open, "OpenProtected": BackendRef.OpenProtected,
		"OpenForTest": BackendRef.OpenForTest, "OpenProtectedForTest": BackendRef.OpenProtectedForTest,
		"OpenForExport": BackendRef.OpenForExport,
		"OpenForCopy":   func(ref BackendRef, ctx context.Context) (dal.DB, error) { return ref.OpenForCopy(ctx, "") },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "owned.sqlite")
			moveSQLiteFile(t, path, "original")
			ref := BackendRef{Scheme: "sqlite", Path: path}
			insert := func(db dal.DB, id int64) error {
				query := dal.NewTextQuery(fmt.Sprintf("INSERT INTO marker (id,name) VALUES (%d,'explicit write') RETURNING name", id), nil)
				reader, err := db.ExecuteQueryToRecordsetReader(ctx, query)
				if err != nil {
					return err
				}
				_, _, err = reader.Next()
				return errors.Join(err, reader.Close())
			}
			// The exact same native operation succeeds through the explicit copy
			// target opener; a lexical or unsupported-operation refusal cannot pass.
			writer, err := ref.OpenForWrite(ctx)
			require.NoError(t, err)
			require.NoError(t, insert(writer, 2))
			require.NoError(t, writer.(io.Closer).Close())
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			db, err := open(ref, ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.(io.Closer).Close()) })
			query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("marker", ""))).SelectIntoRecord(nil)
			reader, err := db.ExecuteQueryToRecordsReader(ctx, query)
			require.NoError(t, err)
			names := map[string]bool{}
			for {
				row, readErr := reader.Next()
				if errors.Is(readErr, io.EOF) {
					break
				}
				require.NoError(t, readErr)
				names[row.Data().(map[string]any)["name"].(string)] = true
			}
			require.NoError(t, reader.Close())
			require.Equal(t, map[string]bool{"original": true, "explicit write": true}, names)
			err = insert(db, 3)
			require.Error(t, err)
			var native interface{ Code() int }
			require.ErrorAs(t, err, &native)
			require.Equal(t, 8, native.Code()&0xff, "SQLite SQLITE_READONLY must refuse the actual native write")
			observer, err := sql.Open("sqlite", SQLiteFileURIMode(path, SQLiteReadOnly))
			require.NoError(t, err)
			var count int
			require.NoError(t, observer.QueryRowContext(ctx, "SELECT count(*) FROM marker").Scan(&count))
			require.Equal(t, 2, count)
			require.NoError(t, observer.Close())
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, sha256.Sum256(before), sha256.Sum256(after), "denied native write must leave the owned file unchanged")
		})
	}
}
