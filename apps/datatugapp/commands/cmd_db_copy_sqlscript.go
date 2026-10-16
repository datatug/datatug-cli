package commands

import (
	"context"
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

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/spf13/cobra"
)

// sqlScriptScheme is the --to scheme that writes a SQL script instead of copying into a database:
// sqlscript:///path/out.sql?dialect=mysql (an absolute path), sqlscript:out.sql?dialect=mysql (a relative one).
const sqlScriptScheme = "sqlscript"

// sqlScriptQuery are the query parameters of a sqlscript URL.
var sqlScriptQuery = map[string]bool{"dialect": true, "name": true, "edition": true, "license": true, "attribution": true}

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
	q := u.Query()
	for k := range q {
		if !sqlScriptQuery[k] {
			return t, fmt.Errorf("unknown parameter %q (known: dialect, name, edition, license, attribution)", k)
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
	return t, nil
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
	if _, statErr := os.Stat(target.path); statErr == nil && overwrite != "recreate" && !dryRun {
		return fail(1, "%s already exists; use --overwrite=recreate to replace it", filepath.Base(target.path))
	}
	if same, err := sameFile(srcRef.Path, target.path); err == nil && same {
		return fail(2, "--from and --to are the same file")
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
		return fail(1, "%s", dbcopy.RedactErrorWithSecrets(err, srcRef.Path).Error())
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

// writeSQLScript writes the script to a temporary file beside path and puts it in place when it is complete, so
// a failed export leaves no half-written script.
func writeSQLScript(ctx context.Context, req faithful.ScriptRequest, path string, replace bool) (*faithful.Report, error) {
	if req.DryRun {
		return faithful.ExportScript(ctx, req, io.Discard)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the folder of the script: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".datatug-sqlscript-*")
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
	_ = os.Chmod(tmp.Name(), 0o644) // a script is for reading; a failure leaves the owner-only mode CreateTemp gave
	if replace {
		err = os.Rename(tmp.Name(), path)
	} else {
		err = os.Link(tmp.Name(), path) // fails when the file appeared meanwhile
	}
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return rep, nil
}
