package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/spf13/cobra"
)

// sqlScriptScheme is the --to scheme that writes a SQL script instead of copying into a database:
// sqlscript:///path/out.sql?dialect=mysql (an absolute path), sqlscript:out.sql?dialect=mysql (a relative one).
const sqlScriptScheme = "sqlscript"

// sqlScriptQuery are the query parameters of a sqlscript URL.
var sqlScriptQuery = map[string]bool{"dialect": true, "name": true, "edition": true, "license": true, "attribution": true, "notice-file": true}

// sqlScriptVersion is the version the script header names.
var sqlScriptVersion = func() string { return versionOf(debug.ReadBuildInfo()) }

// versionOf is the module version of a build, or "dev" for a build that has none.
func versionOf(bi *debug.BuildInfo, ok bool) string {
	if ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func isSQLScriptTarget(to string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(to)), sqlScriptScheme+":")
}

type sqlScriptTarget struct {
	path    string
	dialect string
	header  faithful.Header
}

// parseSQLScriptTarget parses sqlscript:///path?dialect=mysql.
func parseSQLScriptTarget(raw string) (sqlScriptTarget, error) {
	var t sqlScriptTarget
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return t, errors.New("not a valid URL")
	}
	if u.Host != "" || u.User != nil || u.Fragment != "" {
		return t, errors.New("a sqlscript URL has a path and a query only, as in sqlscript:///tmp/out.sql?dialect=mysql")
	}
	t.path = u.Path
	if t.path == "" {
		t.path = u.Opaque
	}
	if t.path == "" {
		return t, errors.New("the file path is missing, as in sqlscript:///tmp/out.sql?dialect=mysql")
	}
	// ParseQuery, not u.Query(): Query drops what it cannot parse, so a ";" in an attribution would make it vanish
	// from the header and the licence line with it.
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return t, fmt.Errorf("the query is not valid (%v); write a \";\" inside a value as %%3B and a \"&\" as %%26", err)
	}
	for k, vs := range q {
		if !sqlScriptQuery[k] {
			return t, fmt.Errorf("unknown parameter %q (known: dialect, name, edition, license, attribution, notice-file)", k)
		}
		if len(vs) > 1 {
			return t, fmt.Errorf("parameter %q is given %d times; give it once", k, len(vs))
		}
	}
	t.dialect = q.Get("dialect")
	if t.dialect == "" {
		return t, errors.New("the dialect is missing: add ?dialect=mysql")
	}
	if _, err := faithful.DialectByName(t.dialect); err != nil {
		return t, err
	}
	t.header = faithful.Header{Name: q.Get("name"), Edition: q.Get("edition"), License: q.Get("license"), Attribution: q.Get("attribution")}
	if q.Has("notice-file") {
		if t.header.Notice, err = readNoticeFile(q.Get("notice-file")); err != nil {
			return t, err
		}
	}
	return t, nil
}

// readNoticeBytes reads at most limit bytes of r; a test replaces it to fail the read.
var readNoticeBytes = func(r io.Reader, limit int64) ([]byte, error) { return io.ReadAll(io.LimitReader(r, limit)) }

// openNoticeFile opens the notice file without blocking: a path that was a regular file a moment ago and is a FIFO now
// opens at once (O_NONBLOCK) and is refused by the stat of the open handle. A test replaces it to swap the path at that
// moment.
var openNoticeFile = func(path string) (*os.File, error) { return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) }

// statNoticeFile stats the open notice file; a test replaces it to fail the stat.
var statNoticeFile = func(f *os.File) (os.FileInfo, error) { return f.Stat() }

// readNoticeFile reads the multi-line licence notice of a script header from a regular file (a relative path is
// the current folder's): UTF-8, no NUL, at most faithful.MaxNoticeBytes, with text in it. One leading byte order
// mark, which editors add, is dropped. The file is opened first and the open handle is checked (a regular file), so a
// path swapped for a FIFO between a check and the open cannot hang the command. Every failure is a usage error of the
// request (exit 2); the text itself is never repeated in the error.
func readNoticeFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("notice-file is empty: give the path of the notice, as in notice-file=/path/to/NOTICE.txt")
	}
	f, err := openNoticeFile(path)
	if err != nil {
		return "", fmt.Errorf("notice-file: %v", err)
	}
	defer func() { _ = f.Close() }()
	info, err := statNoticeFile(f)
	if err != nil {
		return "", fmt.Errorf("notice-file %s: %v", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("notice-file %s is not a regular file", filepath.Base(path))
	}
	b, err := readNoticeBytes(f, faithful.MaxNoticeBytes+4) // room for a byte order mark and one byte over
	if err != nil {
		return "", fmt.Errorf("notice-file %s: %v", filepath.Base(path), err)
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if len(b) > faithful.MaxNoticeBytes {
		return "", fmt.Errorf("notice-file %s is larger than %d bytes", filepath.Base(path), faithful.MaxNoticeBytes)
	}
	if len(b) == 0 {
		return "", fmt.Errorf("notice-file %s is empty", filepath.Base(path))
	}
	// ValidateNotice judges what the header would carry: a file of blanks, byte order marks and control characters holds no
	// text, and would be written as nothing but a bare comment line.
	if err := faithful.ValidateNotice(string(b)); err != nil {
		return "", fmt.Errorf("notice-file %s: %v", filepath.Base(path), err)
	}
	return string(b), nil
}

// sqlScriptRefusedFlags are the copy flags that mean nothing for a script file.
var sqlScriptRefusedFlags = map[string]string{
	"from-schema": "a script is written from a SQLite file", "to-schema": "a script has no schema",
	"decimals": "it applies to PostgreSQL targets", "where": "a script holds every row of the tables it writes",
	"limit": "a script holds every row of the tables it writes", "filter-config": "a script holds every row of the tables it writes",
	"parallel-streams": "a script is written one table after the other", "progress": "a script is written in one pass",
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dbCopySQLScript runs `db copy --to sqlscript:...`: it writes the SQL script of a SQLite database.
func dbCopySQLScript(cmd *cobra.Command, fromURL, toURL, overwrite string) error {
	ctx := cmd.Context()
	asJSON, _ := cmd.Flags().GetBool("json")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	failOnWarning, _ := cmd.Flags().GetBool("fail-on-warning")
	fail := func(code int, format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		if asJSON {
			rep := &faithful.Report{Command: "db copy", DryRun: dryRun, Status: "failed", ExitCode: code, Error: msg, Tables: []faithful.TableReport{}, Warnings: []faithful.Warning{}}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			_ = enc.Encode(rep)
		}
		return Exit(msg, code)
	}
	target, err := parseSQLScriptTarget(toURL)
	if err != nil {
		return fail(2, "--to: %v", err)
	}
	for name, why := range sqlScriptRefusedFlags {
		if cmd.Flags().Changed(name) {
			return fail(2, "--%s is not supported for a sqlscript destination: %s", name, why)
		}
	}
	if overwrite == "reload" {
		return fail(2, "--overwrite=reload does not apply to a script file; use --overwrite=recreate to replace an existing file")
	}
	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return fail(2, "%v", err)
	}
	srcRef, _, err := parseSourceArg(ctx, fromURL)
	if err != nil {
		return keepExit(err, func(err error) error { return fail(2, "--from: %v", err) })
	}
	if srcRef.Scheme != "sqlite" {
		return fail(2, "a sqlscript destination is written from a sqlite:// source, not %s://", srcRef.Scheme)
	}
	if same, err := sameFile(srcRef.Path, target.path); err == nil && same {
		return fail(2, "--from and --to are the same file")
	}
	if _, statErr := os.Stat(target.path); statErr == nil && overwrite != "recreate" && !dryRun {
		return fail(1, "%s already exists; use --overwrite=recreate to replace it", filepath.Base(target.path))
	}
	sum, err := fileSHA256(srcRef.Path)
	if err != nil {
		return fail(4, "open --from: %v", dbcopy.RedactErrorWithSecrets(err, srcRef.Path))
	}
	src, err := srcRef.OpenSQL(ctx, false)
	if err != nil {
		return fail(4, "open --from: %v", err)
	}
	defer func() { _ = src.Close() }()
	target.header.SourceSHA256 = sum
	target.header.Version = sqlScriptVersion()
	if target.header.Name == "" {
		target.header.Name = strings.TrimSuffix(filepath.Base(srcRef.Path), filepath.Ext(srcRef.Path))
	}
	req := faithful.ScriptRequest{Dialect: target.dialect, DB: src, Display: srcRef.Display(), Header: target.header, DryRun: dryRun,
		Include: directives.IncludeTables, Exclude: directives.ExcludeTables}
	rep, err := writeSQLScript(ctx, req, target.path, overwrite == "recreate")
	if err != nil {
		code := 1
		var usage *faithful.ScriptUsageError
		if errors.As(err, &usage) {
			code = 2 // the request can be fixed: a table that does not exist, a name the script cannot carry
		}
		return fail(code, "%s", dbcopy.RedactErrorWithSecrets(err, srcRef.Path).Error())
	}
	code := 0
	if failOnWarning && len(rep.Warnings) > 0 {
		code = exitCodeWarnings
		rep.ExitCode = code
	}
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		rep.WriteText(cmd.ErrOrStderr())
	}
	if code != 0 {
		return Exit(fmt.Sprintf("%d warning(s) and --fail-on-warning is set", len(rep.Warnings)), code)
	}
	return nil
}

// linkFile puts the finished temporary file in place without replacing anything; a test replaces it to play a
// file system without hard links.
var linkFile = os.Link

// writeSQLScript writes the script to a temporary file beside path and puts it in place when it is complete, so
// a failed export leaves no half-written script.
//
// The file gets the mode of a new file under the user's umask (0666 minus the umask: 0644 with the usual 022), and
// a file that is replaced keeps its own mode. Without --overwrite=recreate the file is put in place with a hard
// link, which fails when the name is taken; on a file system with no hard links (FAT, exFAT, some network
// shares) it is created exclusively (O_EXCL) and the script is copied into it, and a copy that fails is removed
// again. Either way nothing that was there is replaced.
func writeSQLScript(ctx context.Context, req faithful.ScriptRequest, path string, replace bool) (*faithful.Report, error) {
	if req.DryRun {
		return faithful.ExportScript(ctx, req, io.Discard)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the folder of the script: %w", err)
	}
	tmp, err := createExclusive(filepath.Dir(path), ".datatug-sqlscript-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	rep, err := faithful.ExportScript(ctx, req, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if replace {
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			_ = os.Chmod(tmp.Name(), info.Mode().Perm()) // the replaced file's mode is kept
		}
		err = os.Rename(tmp.Name(), path)
	} else {
		err = putInPlace(tmp.Name(), path)
	}
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return rep, nil
}

// randomBytes fills b with random bytes; a test replaces it to make the name of a temporary file impossible.
var randomBytes = func(b []byte) error { _, err := rand.Read(b); return err }

// createExclusive creates a new file in dir under a name that starts with prefix, with the mode of a new file
// under the umask (what a plain create gives; os.CreateTemp would force 0600). A name that is taken is an
// error: a six-byte random name does not meet one.
func createExclusive(dir, prefix string) (*os.File, error) {
	var rnd [6]byte
	if err := randomBytes(rnd[:]); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, prefix+hex.EncodeToString(rnd[:])), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
}

// copyContents copies the script into its destination; a test replaces it to fail the copy or to move a file onto the
// destination in the middle of it.
var copyContents = io.Copy

// putInPlace gives the finished file at from the name to, failing when to exists.
func putInPlace(from, to string) error {
	err := linkFile(from, to)
	if err == nil || errors.Is(err, os.ErrExist) {
		return err
	}
	// No hard links here: create the destination exclusively and copy the script into it.
	src, openErr := os.Open(from)
	if openErr != nil {
		return openErr
	}
	defer func() { _ = src.Close() }()
	dst, createErr := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if createErr != nil {
		return createErr
	}
	_, copyErr := copyContents(dst, src)
	made, statErr := dst.Stat()
	if closeErr := dst.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		// Remove the file this call made, and only that one: if somebody renamed another file onto the name
		// meanwhile, it stays.
		if now, err := os.Lstat(to); err == nil && statErr == nil && os.SameFile(made, now) {
			_ = os.Remove(to)
		}
	}
	return copyErr
}
