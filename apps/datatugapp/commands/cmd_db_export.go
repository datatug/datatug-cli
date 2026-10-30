package commands

import (
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/spf13/cobra"
)

// dbExportCommand is a shortcut for `datatug db copy --from <url> --to ingitdb://<folder>`: a local
// backup of any source adapter available through DataTug's DALgo URL dispatcher, as a native inGitDB
// project. It shares the copy engine (see cmd_db_copy_ingitdb.go); it only adds a plain folder path and a
// default folder name.
func dbExportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "export", Short: "Back up a database to a local folder as a native inGitDB project (a shortcut for db copy --to ingitdb://)",
		Long: "A shortcut for `datatug db copy --from URL --to ingitdb://FOLDER`: writes all source collections (or those --include/--exclude/--where/--limit select) as one native inGitDB records file per collection, for a local backup. --to is a folder path or an ingitdb:// URL (add ?records-format=jsonl to pick the format); without it the folder is ./<database name>.ingitdb, the name taken from --from (a SQLite file name without its extension, or a PostgreSQL database name; never a host or a credential). The folder must not exist; it is published only after every collection succeeds, and a failed export leaves nothing behind. --dry-run checks and counts everything and writes nothing; --json prints the same report as db copy; --fail-on-warning exits 3 when anything was not carried exactly. Supported source URLs: sqlite://, ingitdb://, postgres://, http://, https://, openvaultdb://, or env:NAME. A backup to the cloud or another server is not this command: use `db copy --to`, or the planned `backup` family.",
		RunE: dbExportAction,
	}
	flags := cmd.Flags()
	flags.String("from", "", "Source DALgo URL")
	flags.String("from-schema", "", "PostgreSQL source schema to read; defaults to public.")
	flags.String("to", "", "Destination folder path or ingitdb:// local directory URL (must not exist); default ./<database name>.ingitdb")
	flags.String("records-format", "json", "Records file format for every table: json, jsonl, ingr, csv, or yaml (same as ingitdb://DIR?records-format=...)")
	flags.Bool("json", false, "Print a machine-readable summary (tables, rows, warnings, exit code) on stdout, as db copy --json.")
	flags.Bool("dry-run", false, "Check and count everything and write nothing.")
	flags.Bool("fail-on-warning", false, "Exit 3 when anything could not be carried exactly (the export itself is still made).")
	flags.String("include", "", "Comma-separated list of source tables to export. Mutually exclusive with --exclude.")
	flags.String("exclude", "", "Comma-separated list of source tables to skip. Mutually exclusive with --include.")
	flags.StringArray("where", nil, "Row predicate: <table>:<field>:<op>:<value>. Repeatable; multiple on the same table AND-compose. Operators: =, <, <=, >, >=, in.")
	flags.StringArray("limit", nil, "Per-table row limit: <table>:<N> (positive integer). Repeatable; one per table.")
	flags.String("filter-config", "", "Path to a YAML filter config file. Mutually exclusive with any other filter flag.")
	// The copy flags that mean nothing for a folder backup are known, and refused with exit 2 and the reason.
	flags.String("overwrite", "", "")
	flags.String("to-schema", "", "")
	flags.String("decimals", "", "")
	flags.Int("parallel-streams", 0, "")
	flags.Bool("progress", false, "")
	flags.String("recover-job-ref", "", "")
	for _, flag := range nativeInGitDBRefusedFlags {
		_ = flags.MarkHidden(flag.name)
	}
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

func dbExportAction(cmd *cobra.Command, _ []string) error {
	from, _ := cmd.Flags().GetString("from")
	fromSchema, _ := cmd.Flags().GetString("from-schema")
	to, _ := cmd.Flags().GetString("to")
	recordsFormat, _ := cmd.Flags().GetString("records-format")
	var srcRef, tgtRef dbcopy.BackendRef
	fail := nativeFailer(cmd, "db export", func() (faithful.EndpointInfo, faithful.EndpointInfo) { return inGitDBEndpoints(srcRef, tgtRef)() })
	srcRef, _, err := parseSourceArg(cmd.Context(), from)
	if err != nil {
		return keepExit(err, func(err error) error {
			return fail(2, "--from: %v", dbcopy.RedactErrorWithSecrets(err, from))
		})
	}
	if fromSchema != "" && srcRef.Scheme != "postgres" {
		return fail(2, "--from-schema is supported only for PostgreSQL sources")
	}
	if !cmd.Flags().Changed("to") {
		if to, err = defaultExportFolder(srcRef); err != nil {
			return fail(2, "--to is required here: %v; pass --to <folder>", err)
		}
	}
	tgtRef, urlFormat, err := parseExportDestination(cmd, to)
	if err != nil {
		return keepExit(err, func(err error) error { return fail(2, "--to: %v", dbcopy.RedactErrorWithSecrets(err, to)) })
	}
	if tgtRef.Scheme != "ingitdb" {
		return fail(2, "--to must be an ingitdb:// local directory or a folder path")
	}
	if urlFormat != "" {
		if cmd.Flags().Changed("records-format") && !strings.EqualFold(recordsFormat, urlFormat) {
			return fail(2, "--records-format and ?records-format= of --to name different formats")
		}
		recordsFormat = urlFormat
	}
	if err := refuseNativeInGitDBFlags(cmd, fail); err != nil {
		return err
	}
	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return fail(2, "%v", err)
	}
	return runNativeInGitDB(cmd, nativeInGitDBRequest{command: "db export", fromRaw: from, src: srcRef, tgt: tgtRef, fromSchema: fromSchema,
		recordsFormat: recordsFormat, directives: directives, fail: fail})
}

// urlLike is the start of a URL: a scheme and a colon. A Windows drive (C:\dir) is a path.
var urlLike = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
var windowsDrive = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// parseExportDestination is the --to of `db export`: an ingitdb:// URL (as in db copy, with its
// ?records-format= option), or a plain folder path, which is the same as ingitdb:// in front of it.
func parseExportDestination(cmd *cobra.Command, to string) (dbcopy.BackendRef, string, error) {
	if to == "" {
		return dbcopy.BackendRef{}, "", errors.New("the folder is empty")
	}
	if urlLike.MatchString(to) && !windowsDrive.MatchString(to) {
		base, format, err := splitInGitDBOptions(to)
		if err != nil {
			return dbcopy.BackendRef{}, "", err
		}
		ref, _, err := parseSourceArg(cmd.Context(), base)
		return ref, format, err
	}
	if strings.Contains(strings.TrimPrefix(to, `\\?\`), "?") { // `\\?\C:\dir` is a Windows path, not a query
		return dbcopy.BackendRef{}, "", errors.New(`a "?" in a plain folder path is not an option: write ingitdb://FOLDER?records-format=jsonl to pick a format, or --records-format`)
	}
	ref, err := dbcopy.Parse(dbcopy.LocalSourceURL("ingitdb", to))
	return ref, "", err
}

// exportFolderMaxLen is the most characters of a folder name derived from a source name, and exportFolderMaxBytes the
// most bytes: with the ".ingitdb" that follows, a file system takes at most 255 bytes in a name (100 CJK characters are 300).
const (
	exportFolderMaxLen   = 100
	exportFolderMaxBytes = 255 - len(".ingitdb")
)

// absFolder makes the path of a local source absolute; a test replaces it to fail.
var absFolder = filepath.Abs

// defaultExportFolder is the folder `db export` writes to without --to: <database name>.ingitdb in the
// working folder. The name is the SQLite file name without its extension, the PostgreSQL database name, or
// the name of the local folder or file of another source, made of letters, digits, "." "_" and "-" only,
// so it is the same on every run and never holds a host, a user or a password. A source with no such name
// (an in-memory SQLite, a PostgreSQL URL without a database) is an error.
func defaultExportFolder(src dbcopy.BackendRef) (string, error) {
	var stem string
	switch src.Scheme {
	case "postgres":
		u, err := url.Parse(src.Path)
		if err != nil {
			return "", errors.New("the source URL names no database")
		}
		stem = strings.Trim(u.Path, "/")
	case "sqlite":
		stem = fileStem(src.Path)
	case "ingitdb":
		// The folder is named by its own name, also for "." or ".." (run inside the project).
		path := src.Path
		if abs, err := absFolder(path); err == nil {
			path = abs
		}
		stem = strings.TrimSuffix(fileStem(path), ".ingitdb")
	case "openvaultdb", "http", "https":
		stem = strings.TrimSuffix(fileStem(src.Path), ".ingitdb")
	}
	name := folderSafe(stem)
	if name == "" || strings.Contains(strings.ToLower(src.Path), ":memory:") || strings.Contains(strings.ToLower(src.Path), "mode=memory") {
		return "", errors.New("no folder name can be derived from --from")
	}
	return name + ".ingitdb", nil
}

func fileStem(path string) string {
	base := filepath.Base(filepath.FromSlash(strings.ReplaceAll(path, `\`, "/")))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// folderSafe keeps letters, digits, ".", "_" and "-" of name, writes "_" for any other character, strips
// "." "_" and "-" from both ends, puts "_" before a name Windows reserves for a device (CON, NUL, COM1, ... with or
// without an extension), and cuts the result to exportFolderMaxLen characters and exportFolderMaxBytes bytes, on a
// character boundary.
func folderSafe(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	safe := strings.Trim(b.String(), "._-")
	if windowsDeviceName(safe) {
		safe = "_" + safe
	}
	runes := []rune(safe)
	if len(runes) > exportFolderMaxLen {
		runes = runes[:exportFolderMaxLen]
	}
	safe = string(runes)
	for len(safe) > exportFolderMaxBytes {
		_, size := utf8.DecodeLastRuneInString(safe)
		safe = safe[:len(safe)-size]
	}
	return strings.TrimRight(safe, "._-")
}

// windowsDeviceName reports a name Windows reserves for a device: CON, PRN, AUX, NUL, COM1-9 and LPT1-9, in any case,
// also when an extension follows ("nul.txt", "COM1.ingitdb").
func windowsDeviceName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	upper := strings.ToUpper(base)
	return len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9'
}

// An env:NAME source keeps only its variable name in the CLI argument. The
// parsed ref retains the resolved locator, so scrub secrets from both forms
// before returning an arbitrary provider introspection or row error.
func redactExportFailure(err error, from string, ref dbcopy.BackendRef) error {
	return dbcopy.RedactErrorWithSecrets(dbcopy.RedactErrorWithSecrets(err, ref.Path), from)
}
