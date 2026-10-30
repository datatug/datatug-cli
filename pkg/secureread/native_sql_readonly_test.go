package secureread

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"modernc.org/sqlite"
)

func TestNativeSQLCannotDisableReadOnlyOrAttachWritableDatabase(t *testing.T) {
	source := newSQLiteFixture(t)
	target := newSQLiteFixture(t)
	targetRef, err := dbcopy.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(session)
	copyPath := filepath.Join(t.TempDir(), "forbidden-copy.sqlite")
	for _, sql := range []string{
		"PRAGMA query_only=OFF; DELETE FROM products; SELECT 1",
		"PRAGMA query_only=0; DELETE FROM products; SELECT 1",
		"ATTACH DATABASE '" + strings.ReplaceAll(targetRef.Path, "'", "''") + "' AS other; DELETE FROM other.products; SELECT 1",
		"VACUUM INTO '" + strings.ReplaceAll(copyPath, "'", "''") + "'",
	} {
		if _, err := executor.RunNativeSQL(context.Background(), source, sql); err == nil {
			t.Fatalf("write escape succeeded: %s", sql)
		}
	}
	for _, url := range []string{source, target} {
		result, err := executor.RunNativeSQL(context.Background(), url, "SELECT name FROM products ORDER BY name")
		if err != nil || len(result.Rows) != 3 {
			t.Fatalf("database changed: rows=%d err=%v", len(result.Rows), err)
		}
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("VACUUM wrote an output file: %v", err)
	}
}

func TestNativeSQLAuthorizerPolicy(t *testing.T) {
	for _, test := range []struct {
		action     sqlite.AuthorizerActionCode
		arg1, arg2 string
		want       sqlite.AuthorizerReturnCode
	}{
		{sqlite.AuthSelect, "", "", sqlite.AuthorizerOK},
		{sqlite.AuthFunction, "", "count", sqlite.AuthorizerOK},
		{sqlite.AuthFunction, "", "load_extension", sqlite.AuthorizerDeny},
		{sqlite.AuthFunction, "", "writefile", sqlite.AuthorizerDeny},
		{sqlite.AuthPragma, "table_info", "products", sqlite.AuthorizerOK},
		{sqlite.AuthPragma, "query_only", "ON", sqlite.AuthorizerOK},
		{sqlite.AuthPragma, "query_only", "OFF", sqlite.AuthorizerDeny},
		{sqlite.AuthPragma, "writable_schema", "ON", sqlite.AuthorizerDeny},
		{sqlite.AuthAttach, "", "", sqlite.AuthorizerDeny},
		{sqlite.AuthUpdate, "", "", sqlite.AuthorizerDeny},
	} {
		if got := authorizeReadOnlySQL(test.action, test.arg1, test.arg2, "", ""); got != test.want {
			t.Errorf("%+v returned %v", test, got)
		}
	}
	if err := installReadOnlyAuthorizer(nil, ""); err == nil {
		t.Fatal("missing driver authorizer accepted")
	}
}

func TestNativeReadOnlyAuthorizerSurvivesConnectionReplacement(t *testing.T) {
	source := newSQLiteFixture(t)
	ref, err := dbcopy.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	_, db, closeDB, err := openReadOnlySQLiteDB(context.Background(), ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// database/sql discards this physical connection and opens a replacement
	// for the next query. A one-off Raw authorizer would be lost here.
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, err := db.Exec("PRAGMA query_only=OFF; DELETE FROM products"); err == nil {
		t.Fatal("replacement connection lost its authorizer")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM products").Scan(&count); err != nil || count != 3 {
		t.Fatalf("replacement read: count=%d error=%v", count, err)
	}
}
