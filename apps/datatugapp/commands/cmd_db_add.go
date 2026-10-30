package commands

// specscore: feature/cli/db/add

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

// dbAddLookupEnv is the environment `db add` looks an env:NAME source up in, a seam so tests never
// touch the process environment. Always os.LookupEnv in production.
var dbAddLookupEnv = os.LookupEnv

// defaultEnvironmentID is the environment `db add` picks, without --env, in a project of several.
const defaultEnvironmentID = "local"

// dbAddResult is the JSON document `db add` prints. The field order is the order of the keys.
type dbAddResult struct {
	Added  bool   `json:"added"`
	ID     string `json:"id"`
	Env    string `json:"env"`
	Driver string `json:"driver"`
	// DBRef is the exact --db value that addresses the source in `db schema`, `db table`, `db column`,
	// `query run` and `db copy`: datatug://<project>/<id>, with ?env=<env> when --env was given or when
	// another environment of the project lists a source of the same id.
	DBRef string `json:"dbRef"`
}

// dbAddCommand wires `datatug db add --project <id> --db <path|url|env:NAME> [--env <ENV>] [--name <id>]`.
func dbAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a database as a source of a project, without reading it",
		Long: "Registers a database as a source of an environment of a registered DataTug project, in the files a scan writes, " +
			"and does not open the database: no metadata is scanned and no row is read (`datatug scan` describes it later).\n\n" +
			"--db is a SQLite file path (or a sqlite:// URL), stored as a scan stores it, or env:NAME, the name of an environment variable that " +
			"holds a PostgreSQL URL: the project then holds only the name of the variable in a connection descriptor, never the URL. " +
			"A connection URL with credentials is refused: put it in an environment variable and pass env:NAME.\n\n" +
			"When the same database is already a source of the environment nothing is written and the existing id is printed with added false.",
		Example: "  datatug db add --project shop --db ./shop.sqlite --env local\n" +
			"  DATATUG_SHOP_PG=postgres://user:secret@host/shop datatug db add --project shop --db env:DATATUG_SHOP_PG --name shop-pg --env prod",
		Args: cobra.NoArgs,
		RunE: dbAddAction,
	}
	flags := cmd.Flags()
	flags.StringP("project", "p", "", "Registered project id/name to add the source to (required)")
	flags.String("db", "", "The database: a SQLite file path or sqlite:// URL, or env:NAME for a PostgreSQL URL held in an environment variable (required)")
	flags.String("env", "", "Environment of the project the source belongs to: a plain name; made when the project has none of that id (default: the project's only environment, or \"local\" when it has several)")
	flags.String("name", "", "Id of the source: a plain name (default for a SQLite file: its file name without the extension; required for env:NAME)")
	flags.String("format", "json", "Output format: json or text (json carries dbRef, the --db value that addresses the source)")
	return cmd
}

// dbAddSource is the database `db add` was given, resolved and checked without opening it.
type dbAddSource struct {
	sqlitePath string // the absolute path of a SQLite file
	dsnEnv     string // the variable that holds a PostgreSQL URL
}

func dbAddAction(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	project, _ := flags.GetString("project")
	dbArg, _ := flags.GetString("db")
	env, _ := flags.GetString("env")
	envGiven := env != ""
	name, _ := flags.GetString("name")
	format, _ := flags.GetString("format")
	if format != "json" && format != "text" {
		return Exit(fmt.Sprintf("unsupported --format %q: want one of json, text", format), exitCodeUsage)
	}
	if project == "" {
		return Exit("--project is required: the registered project to add the source to (see `datatug projects`)", exitCodeUsage)
	}
	if dbArg == "" {
		return Exit("--db is required: a SQLite file path, or env:NAME for a PostgreSQL URL held in an environment variable", exitCodeUsage)
	}
	source, err := resolveDbAddSource(dbArg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if name == "" {
		if name, err = defaultSourceName(source); err != nil {
			return err
		}
	}
	base, err := openProjectForRead(project, "")
	if err != nil {
		return err
	}
	ctx := cmd.Context() // set by cobra's Execute
	store := base.store.GetProjectStore(base.projectID)
	if env == "" {
		if env, err = onlyEnvironmentOf(ctx, store); err != nil {
			return err
		}
	}
	added, err := api.AddProjectSource(ctx, store, base.ProjectDir, api.ProjectSourceRequest{
		Environment: env, ID: name, SQLitePath: source.sqlitePath, DSNEnv: source.dsnEnv,
	})
	if err != nil {
		var refusal *api.ProjectSourceRefusal
		if errors.As(err, &refusal) {
			return Exit(err.Error(), exitCodeUsage)
		}
		return err
	}
	reference := dbcopy.DatatugRef{Project: base.projectID, DB: added.ID}
	// The environments were read a moment ago: a failure now only leaves the reference without ?env=.
	listing, _ := api.EnvironmentsListingSource(ctx, store, added.ID)
	if envGiven || len(listing) > 1 {
		reference.Env = added.Environment
	}
	return writeDbAddResult(cmd.OutOrStdout(), format, dbAddResult{Added: added.Added, ID: added.ID, Env: added.Environment, Driver: added.Driver, DBRef: reference.String()})
}

// resolveDbAddSource turns --db into the database to register, and refuses what cannot be one
// without ever opening a database: an env:NAME that names a variable a project may not name, one
// that holds something other than a PostgreSQL URL, a connection URL (which carries credentials a
// project must not hold), another provider, or a SQLite file that is not there. No message holds the
// value of --db or of a variable when it is, or may be, a connection string. A note about a variable
// that is not set goes to stderr.
func resolveDbAddSource(dbArg string, stderr io.Writer) (dbAddSource, error) {
	if strings.HasPrefix(dbArg, "env:") {
		return resolveDbAddEnv(strings.TrimPrefix(dbArg, "env:"), stderr)
	}
	resolved, _, err := resolveSchemaSource(dbArg)
	if err != nil {
		return dbAddSource{}, Exit(err.Error(), exitCodeUsage)
	}
	ref, err := dbcopy.Parse(resolved)
	if err != nil {
		return dbAddSource{}, Exit(err.Error(), exitCodeUsage)
	}
	switch ref.Scheme {
	case "sqlite":
	case "postgres":
		// The URL is not echoed, and neither is any part of it: it may hold a password.
		return dbAddSource{}, Exit("--db is a PostgreSQL connection URL, which carries credentials a project must not hold: put it in an environment variable and pass --db env:NAME", exitCodeUsage)
	default:
		return dbAddSource{}, Exit(fmt.Sprintf("--db: a %s source cannot be registered: give a SQLite file, or env:NAME naming an environment variable that holds a PostgreSQL URL", ref.Scheme), exitCodeUsage)
	}
	path, err := absolutePath(ref.Path)
	if err != nil {
		return dbAddSource{}, Exit(fmt.Sprintf("cannot resolve the --db path: %v", err), exitCodeUsage)
	}
	// The file is looked at, never opened as a database: a path that is not there is a typing mistake.
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return dbAddSource{}, Exit(fmt.Sprintf("the SQLite file %s cannot be found", dbcopy.PathDisplay(path)), exitCodeUsage)
	case info.IsDir():
		return dbAddSource{}, Exit(fmt.Sprintf("%s is a folder, not a SQLite file", dbcopy.PathDisplay(path)), exitCodeUsage)
	}
	return dbAddSource{sqlitePath: path}, nil
}

// resolveDbAddEnv checks the variable name of an env:NAME source. The variable is read only to tell
// that it holds a PostgreSQL URL when it is set; the value is never shown, and never opened.
func resolveDbAddEnv(variable string, stderr io.Writer) (dbAddSource, error) {
	if !dbcopy.ValidEnvName(variable) {
		return dbAddSource{}, Exit("--db env:NAME: NAME must be an environment variable name: upper-case letters, digits and underscores, starting with a letter", exitCodeUsage)
	}
	if err := dbcopy.CheckDescriptorEnvName(variable, dbAddLookupEnv); err != nil {
		return dbAddSource{}, Exit("--db: "+err.Error(), exitCodeUsage)
	}
	if _, set := dbAddLookupEnv(variable); !set {
		_, _ = fmt.Fprintf(stderr, "note: environment variable %s is not set here, so what it holds was not checked; the project records its name only\n", variable)
		return dbAddSource{dsnEnv: variable}, nil
	}
	ref, err := dbcopy.ParseWithEnv("env:"+variable, dbAddLookupEnv)
	if err != nil {
		return dbAddSource{}, Exit("--db: "+err.Error(), exitCodeUsage)
	}
	if ref.Scheme != api.DriverPostgres {
		return dbAddSource{}, Exit(fmt.Sprintf("--db: environment variable %s must hold a postgres:// URL, not a %s source: a SQLite file is given as its path", variable, ref.Scheme), exitCodeUsage)
	}
	return dbAddSource{dsnEnv: variable}, nil
}

// defaultSourceName is the id of a source given no --name: for a SQLite file its file name without
// the extension, with every character a plain name does not allow written as "-". An env:NAME has
// no file name, and a file name that makes no plain name must be named with --name.
func defaultSourceName(source dbAddSource) (string, error) {
	if source.sqlitePath == "" {
		return "", Exit("--name is required for an env:NAME source: the id the source is listed under", exitCodeUsage)
	}
	base := filepath.Base(source.sqlitePath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, base)
	name = strings.TrimLeftFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if api.CheckScanName("--name", name) != nil {
		return "", Exit("--name is required: the file name does not make a plain name (letters, digits, \".\", \"_\" and \"-\", starting with a letter or a digit)", exitCodeUsage)
	}
	return name, nil
}

// onlyEnvironmentOf is the default of --env: the id of the one environment the project has, or, when
// it has several, the one whose id is "local"; with none, or several and no "local", --env is required.
// It never makes an environment.
func onlyEnvironmentOf(ctx context.Context, store datatug.ProjectStore) (string, error) {
	envs, err := store.LoadEnvironments(ctx)
	if err != nil {
		return "", errors.New("the environments of the project cannot be read")
	}
	switch len(envs) {
	case 0:
		return "", Exit("--env is required: the project has no environment yet, so name the one to make", exitCodeUsage)
	case 1:
		return envs[0].GetID(), nil
	}
	shown := make([]string, len(envs))
	for i, env := range envs {
		if env.GetID() == defaultEnvironmentID {
			return defaultEnvironmentID, nil
		}
		shown[i] = dbcopy.SourceIDDisplay(env.GetID())
	}
	return "", Exit(fmt.Sprintf("--env is required: the project has %d environments (%s)", len(envs), strings.Join(shown, ", ")), exitCodeUsage)
}

func writeDbAddResult(w io.Writer, format string, result dbAddResult) error {
	if format == "text" {
		verb := "added"
		if !result.Added {
			verb = "already registered as"
		}
		_, err := fmt.Fprintf(w, "%s %s (%s) in environment %s: --db %s\n", verb, result.ID, result.Driver, result.Env, result.DBRef)
		return err
	}
	data, _ := json.Marshal(result) // a struct of a bool and three strings cannot fail to marshal
	_, err := fmt.Fprintf(w, "%s\n", data)
	return err
}
