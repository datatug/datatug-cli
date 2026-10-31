package commands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// This first native checkpoint runs only in the existing owned CI PostgreSQL
// service. It proves the native mode guard and selected effects, not the full
// Task2 matrix, attestation or a tier promotion. Each SQL attempt uses a fresh
// transaction, so an earlier aborted transaction cannot manufacture a refusal.
func TestPostgresNativeReadonlyGuardAndMutationEffects(t *testing.T) {
	requireNativeReadonlyCI(t)
	server := newRealPgServer(t)
	require.Equal(t, os.Getenv(realPgMajorVar), strconv.Itoa(realPgServerMajor(t, server.scanURL)), "CI must name the actual server major")
	execAll(t, server.scanURL,
		`CREATE TABLE native_guard_probe(id integer PRIMARY KEY, note text)`,
		`INSERT INTO native_guard_probe VALUES(1,'original')`,
		`CREATE SEQUENCE native_guard_seq START 10`,
		`CREATE FUNCTION native_guard_write() RETURNS integer LANGUAGE plpgsql VOLATILE AS $$ BEGIN INSERT INTO native_guard_probe VALUES(3,'function'); RETURN 3; END $$`,
		`CREATE FUNCTION native_guard_definer_write() RETURNS integer LANGUAGE plpgsql VOLATILE SECURITY DEFINER AS $$ BEGIN INSERT INTO native_guard_probe VALUES(4,'definer'); RETURN 4; END $$`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ref, err := dbcopy.Parse(server.scanURL)
	require.NoError(t, err)
	db, err := ref.Open(ctx)
	require.NoError(t, err)
	attempts := []struct{ name, text string }{
		{"N-PG1-insert", `INSERT INTO native_guard_probe VALUES(2,'insert') RETURNING id`},
		{"N-PG1-update", `UPDATE native_guard_probe SET note='updated' WHERE id=1 RETURNING id`},
		{"N-PG1-delete", `DELETE FROM native_guard_probe WHERE id=1 RETURNING id`},
		{"N-PG1-truncate", `TRUNCATE native_guard_probe`},
		{"N-PG1-merge", `MERGE INTO native_guard_probe p USING(VALUES(1,'merge')) s(id,note) ON p.id=s.id WHEN MATCHED THEN UPDATE SET note=s.note`},
		{"N-PG2-writable-cte", `WITH changed AS(INSERT INTO native_guard_probe VALUES(2,'cte') RETURNING id) SELECT * FROM changed`},
		{"N-PG3-volatile-function", `SELECT native_guard_write()`},
		{"N-PG3-security-definer", `SELECT native_guard_definer_write()`},
		{"N-PG4-nextval", `SELECT nextval('native_guard_seq')`},
		{"N-PG4-setval", `SELECT setval('native_guard_seq',100)`},
		{"N-PG7-create", `CREATE TABLE native_guard_created(id integer)`},
		{"N-PG7-alter", `ALTER TABLE native_guard_probe ADD COLUMN injected integer`},
		{"N-PG7-drop", `DROP TABLE native_guard_probe`},
		{"N-PG8-do-write", `DO $$ BEGIN INSERT INTO native_guard_probe VALUES(5,'do'); END $$`},
	}
	for _, attempt := range attempts {
		t.Run(attempt.name, func(t *testing.T) {
			err := dbcopy.RunVerifiedPostgresReadonly(ctx, db, func(ctx context.Context, tx dal.ReadTransaction) error {
				reader, err := tx.ExecuteQueryToRecordsReader(ctx, dal.NewTextQuery(attempt.text, nil))
				if err != nil {
					return err
				}
				_, err = dal.ReadAllToRecords(ctx, reader)
				return err
			})
			var native *pgconn.PgError
			require.True(t, errors.As(err, &native), "must observe the native error, not lexical refusal/aborted predecessor: %v", err)
			require.Equal(t, "25006", native.Code)
			assertNativeGuardFixtureUnchanged(t, server.scanURL)
		})
	}
	// This role owns and can write the fixture. The transaction is the tested
	// restriction; this is not a claim that its reachable credential is read-only.
	execAll(t, server.scanURL, `INSERT INTO native_guard_probe VALUES(2,'baseline')`, `DELETE FROM native_guard_probe WHERE id=2`)
}

func assertNativeGuardFixtureUnchanged(t *testing.T, rawURL string) {
	t.Helper()
	observer, err := sql.Open("pgx", rawURL)
	require.NoError(t, err)
	defer observer.Close()
	var count int
	var note string
	require.NoError(t, observer.QueryRow(`SELECT count(*) FROM native_guard_probe`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, observer.QueryRow(`SELECT note FROM native_guard_probe WHERE id=1`).Scan(&note))
	require.Equal(t, "original", note)
	var last int
	var called bool
	require.NoError(t, observer.QueryRow(`SELECT last_value,is_called FROM native_guard_seq`).Scan(&last, &called))
	require.Equal(t, 10, last)
	require.False(t, called)
	var relation sql.NullString
	require.NoError(t, observer.QueryRow(`SELECT to_regclass('native_guard_created')::text`).Scan(&relation))
	require.False(t, relation.Valid)
	var injected bool
	require.NoError(t, observer.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='native_guard_probe' AND column_name='injected')`).Scan(&injected))
	require.False(t, injected)
}

// A reachable SELECT-only login is a separate native capability fact. Neither
// writable credentials in a read-only transaction nor superuser mocks prove it.
func TestPostgresNativeReadonlyRoleCannotWriteOutsideDataTug(t *testing.T) {
	requireNativeReadonlyCI(t)
	server := newRealPgServer(t)
	adminURL := os.Getenv(realPgAdminVar)
	execAll(t, server.scanURL, `CREATE TABLE native_role_probe(id integer PRIMARY KEY)`, `INSERT INTO native_role_probe VALUES(1)`)
	role := "dt_ro_" + randomHex(t, 6)
	password := "Pw" + randomHex(t, 12)
	execAll(t, adminURL, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, role, password))
	t.Cleanup(func() {
		execAll(t, server.scanURL, fmt.Sprintf(`REVOKE ALL ON native_role_probe FROM %s`, role), fmt.Sprintf(`REVOKE USAGE ON SCHEMA public FROM %s`, role))
		execAll(t, adminURL, fmt.Sprintf(`DROP ROLE %s`, role))
	})
	execAll(t, server.scanURL, fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, role), fmt.Sprintf(`GRANT SELECT ON native_role_probe TO %s`, role))
	parsed, err := url.Parse(server.scanURL)
	require.NoError(t, err)
	parsed.User = url.UserPassword(role, password)
	raw, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	defer raw.Close()
	var count int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM native_role_probe`).Scan(&count))
	require.Equal(t, 1, count)
	_, err = raw.Exec(`INSERT INTO native_role_probe VALUES(2)`)
	var native *pgconn.PgError
	require.True(t, errors.As(err, &native))
	require.Equal(t, "42501", native.Code)
	var privileged bool
	require.NoError(t, raw.QueryRow(`SELECT rolsuper OR rolcreaterole OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged))
	require.False(t, privileged)
	var canWrite bool
	require.NoError(t, raw.QueryRow(`SELECT has_table_privilege(current_user,'native_role_probe','INSERT,UPDATE,DELETE,TRUNCATE')`).Scan(&canWrite))
	require.False(t, canWrite)
}

func requireNativeReadonlyCI(t *testing.T) {
	t.Helper()
	if os.Getenv("DATATUG_NATIVE_ASSURANCE_CI") != "1" {
		t.Skip("native read-only proof requires the owned CI fixture")
	}
}
