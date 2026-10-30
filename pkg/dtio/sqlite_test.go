package dtio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSQLite(t *testing.T) {
	// 1. Non-existent file
	if IsSQLite("/non/existent/file.db") {
		t.Errorf("expected false for non-existent file")
	}

	tmpDir := t.TempDir()

	// 2. Short file (< 16 bytes)
	shortFile := filepath.Join(tmpDir, "short.db")
	if err := os.WriteFile(shortFile, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if IsSQLite(shortFile) {
		t.Errorf("expected false for short file")
	}

	// 3. 16 bytes but not SQLite header
	nonSqliteFile := filepath.Join(tmpDir, "not_sqlite.db")
	if err := os.WriteFile(nonSqliteFile, []byte("1234567890123456"), 0600); err != nil {
		t.Fatal(err)
	}
	if IsSQLite(nonSqliteFile) {
		t.Errorf("expected false for non-sqlite file")
	}

	// 4. Valid SQLite header
	sqliteFile := filepath.Join(tmpDir, "valid.db")
	validHeader := append([]byte("SQLite format 3\x00"), []byte("extra data")...)
	if err := os.WriteFile(sqliteFile, validHeader, 0600); err != nil {
		t.Fatal(err)
	}
	if !IsSQLite(sqliteFile) {
		t.Errorf("expected true for valid sqlite file")
	}
}
