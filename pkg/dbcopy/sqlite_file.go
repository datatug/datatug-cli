package dbcopy

import (
	"net/url"
	"path/filepath"
	"strings"
)

// The modes a SQLite file is opened in (SQLite's own names for them).
const (
	// SQLiteReadOnly opens the file for reading only.
	SQLiteReadOnly = "ro"
	// SQLiteReadWrite opens the file for reading and writing, and never creates it.
	SQLiteReadWrite = "rw"
)

// SQLiteFileURI is the "file:" URI of the SQLite file at path, the form in which a
// SQLite driver is handed a file name that may hold any character a file name can.
//
// A driver that is given a bare path reads everything after the first "?" in it as its
// own parameters (modernc's does), so a file named what?mode=rw.db is opened as what,
// which it creates, empty, beside the file that was named. In a URI the "?", "#" and
// "%" of a name are percent-encoded, which the driver reads back as the characters of
// the name, as it does a space or any byte that is not ASCII.
//
// The URI opens the file read-write and never creates it (mode=rw: SQLite opens a file
// that is write-protected read-only, as it always did): the source of a path that is
// not there is refused before it is opened (see CheckSourceFile), and a file that goes
// between that check and the open is not made again, empty, by the open. The file name
// ends where the query starts, so the path is never read as anything but a name.
//
// An absolute path follows an empty host ("file:///data/x.db"), and a relative one the
// scheme alone ("file:data/x.db"). A path with a drive letter is written after a slash
// ("file:///C:/x.db"), and a network path ("//host/share/x.db") is kept as it is after
// the empty host ("file:////host/share/x.db"): the start of the name is never read as the
// host of the URI, which SQLite refuses.
func SQLiteFileURI(path string) string {
	return SQLiteFileURIMode(path, SQLiteReadWrite)
}

// SQLiteFileURIMode is the one builder of the "file:" URI of a SQLite file: SQLiteFileURI is
// the read-write form of it, and every other place that hands a driver the path of a file
// (a read-only open of a scan, of chat, of a viewer) builds the URI with it, in the mode it
// needs, so that the name of the file is read back as the file it is whatever it holds.
// mode is one of SQLiteReadOnly and SQLiteReadWrite; options are further "name=value"
// parameters of the URI that SQLite knows (such as "immutable=1"), written after the mode as
// they are given, so they are never a part of a path or of anything a client sent.
func SQLiteFileURIMode(path, mode string, options ...string) string {
	return sqliteFileURIMode(filepath.ToSlash(path), filepath.VolumeName(path), mode, options)
}

// sqliteFileURI is SQLiteFileURI for the path name in the slash form and the volume of
// the path (empty on a system with no drive letters), so that the forms of a system
// that is not the one running can be told.
func sqliteFileURI(name, volume string) string {
	return sqliteFileURIMode(name, volume, SQLiteReadWrite, nil)
}

// sqliteFileURIMode is SQLiteFileURIMode for the path name in the slash form and the volume
// of the path.
func sqliteFileURIMode(name, volume, mode string, options []string) string {
	if volume != "" && !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	// Only the escaping of url.URL is used: its String() rewrites a path that starts with
	// two slashes, which is what a network path does.
	escaped := (&url.URL{Path: name}).EscapedPath()
	scheme := "file:"
	if strings.HasPrefix(escaped, "/") {
		scheme = "file://" // an empty host, then the path from the root
	}
	query := "mode=" + mode
	for _, option := range options {
		query += "&" + option
	}
	return scheme + escaped + "?" + query
}
