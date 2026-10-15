package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// How `db schema` learned the provider of its --db value.
const (
	detectedFromScheme = "scheme"
	detectedFromName   = "file name"
	detectedFromData   = "file content"
	// detectedFromProject is a database that is a source of a project (a datatug:// reference).
	detectedFromProject = "project"
)

// sqliteMagic is the first 16 bytes of every SQLite database file.
const sqliteMagic = "SQLite format 3\x00"

// sqliteExtensions are the file extensions that name a SQLite database.
var sqliteExtensions = map[string]bool{".sqlite": true, ".sqlite3": true, ".db": true, ".db3": true, ".s3db": true, ".sl3": true}

// schemeForm is the start of a URL source: two or more characters then a
// colon, so a Windows drive letter is not taken for a scheme.
var schemeForm = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+:`)

// userHomeDir and absolutePath are seams for the ~/ expansion and the
// resolution against the working directory.
var (
	userHomeDir  = os.UserHomeDir
	absolutePath = filepath.Abs
)

// resolveSchemaSource turns the --db value into a source dbcopy.Parse takes. A
// value in URL form (or env:NAME) is passed on untouched; anything else is a
// file path, which is a SQLite database when its extension says so or, failing
// that, when it starts with the SQLite header.
func resolveSchemaSource(arg string) (source, detectedFrom string, err error) {
	if arg == "" || schemeForm.MatchString(arg) {
		return arg, detectedFromScheme, nil
	}
	path := arg
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, homeErr := userHomeDir()
		if homeErr != nil {
			return "", "", fmt.Errorf("cannot expand ~ in --db: %w", homeErr)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	absolute, err := absolutePath(path)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve --db path: %w", err)
	}
	if strings.ContainsAny(absolute, "?#@%") {
		return "", "", fmt.Errorf("--db path not supported: a path holding ?, #, @ or %% would be read as part of a URL; rename the file or its directory")
	}
	if sqliteExtensions[strings.ToLower(filepath.Ext(absolute))] {
		return "sqlite://" + absolute, detectedFromName, nil
	}
	if !hasSQLiteHeader(absolute) {
		return "", "", fmt.Errorf("%s is not a recognised database file: expected a SQLite file (.sqlite, .sqlite3, .db, .db3, .s3db, .sl3 or an SQLite header) or a URL", dbcopy.PathDisplay(arg))
	}
	return "sqlite://" + absolute, detectedFromData, nil
}

// hasSQLiteHeader reports whether the file starts with the SQLite header.
func hasSQLiteHeader(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, len(sqliteMagic))
	if _, err = io.ReadFull(file, header); err != nil {
		return false
	}
	return string(header) == sqliteMagic
}
