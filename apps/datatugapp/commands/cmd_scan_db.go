package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/datatug/datatug-cli/internal/plainfs"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

// scanExitCodeConnection is the exit code of a scan whose database cannot be connected to (spec/features/cli/scan).
const scanExitCodeConnection = 4

func scanCommandAction(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	v := &scanDbCommand{}
	v.ProjectName, _ = flags.GetString("project")
	v.ProjectDir, _ = flags.GetString("directory")
	v.Driver, _ = flags.GetString("driver")
	v.Host, _ = flags.GetString("server")
	v.Port, _ = flags.GetInt("port")
	v.User, _ = flags.GetString("user")
	v.Password, _ = flags.GetString("password")
	v.Database, _ = flags.GetString("db")
	v.DbModel, _ = flags.GetString("dbmodel")
	v.Environment, _ = flags.GetString("env")
	v.Path, _ = flags.GetString("path")
	v.DSNEnv, _ = flags.GetString("dsn-env")
	v.FollowProjectLink, _ = flags.GetBool(followProjectLinkFlagName)

	// The driver is the first thing looked at: a scan without one the scan reads cannot go on,
	// whatever the project holds.
	if err := checkScanDriver(v.Driver); err != nil {
		return err
	}
	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true, writesProject: true}); err != nil {
		return err
	}
	// Validate the supplied root before normalization: an address is not a
	// local directory, and a regular file keeps its existing useful diagnostic.
	projectDirIsNew, err := checkProjectDir(v.ProjectDir)
	if err != nil {
		return err
	}
	// The root-link policy runs above. Abs preserves that policy while giving
	// Core one stable root identity for relative scans and absolute reopens.
	if !filepath.IsAbs(v.ProjectDir) {
		absoluteDir, err := scanDirectoryAbs(v.ProjectDir)
		if err != nil {
			return fmt.Errorf("cannot locate the project folder: %w", err)
		}
		v.ProjectDir = absoluteDir
		v.store, v.projectID = filestore.NewSingleProjectStore(v.ProjectDir, v.projectID)
	}
	// Refuse unsafe summary entries before Core's generic recovery error can
	// hide their project-relative path. The shared walker never follows links.
	if _, err := plainfs.New(v.ProjectDir, 0o755).ReadFile(filepath.Join(v.ProjectDir, storage.ProjectSummaryFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	log.Println("Initiating project...")

	// A database, a model and an environment are names of folders of the project: a
	// name that cannot be one is refused before anything is read or written.
	for _, name := range []struct{ flag, value string }{{"--db", v.Database}, {"--env", v.Environment}} {
		if err := api.CheckScanName(name.flag, name.value); err != nil {
			return err
		}
	}
	if v.DbModel != "" {
		if err := api.CheckScanName("--dbmodel", v.DbModel); err != nil {
			return err
		}
	}
	// A database the project already holds stays on its model: a scan without --dbmodel
	// keeps it, and one that names another is refused, naming both. The same database in
	// another environment is on the model the other environments record for it.
	var modelNote string
	if v.DbModel, modelNote, err = api.ResolveScanDbModelNoted(v.ProjectDir, v.Environment, v.Database, v.DbModel); err != nil {
		return err
	}
	if modelNote != "" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), modelNote)
	}
	// A name that differs only by case from one the project has is one folder on some
	// file systems: it is refused now, before the database is read.
	if err = api.CheckScanNamesAgainstProject(v.ProjectDir, v.Environment, v.Database, v.DbModel); err != nil {
		return err
	}
	// A database id the environment already records under another driver is refused now, before
	// the database is read: a catalog file is kept by environment and id, whatever the driver.
	if err = api.CheckScanDriverAgainstProject(v.ProjectDir, v.Environment, v.Database, v.Driver); err != nil {
		return err
	}
	connParams, err := v.connectionParams()
	if err != nil {
		return err
	}

	// The id of a new project is --project, or else the name of its folder, and is
	// checked when the project is made: the project of a folder that exists has its
	// own.
	newProjectID := v.ProjectName
	if newProjectID == "" {
		absoluteDir, absErr := scanFilepathAbs(v.ProjectDir)
		if absErr != nil {
			return fmt.Errorf("cannot tell the name of the project folder %q: %w", v.ProjectDir, absErr)
		}
		newProjectID = filepath.Base(absoluteDir)
	}

	projectStore := v.store.GetProjectStore(v.projectID)
	// What the scan leaves out of the database it reads is named on stderr, as is what
	// it leaves out of the project.
	stderr := cmd.ErrOrStderr()
	ctx := api.WithScanWarnings(context.Background(), stderr)
	datatugProject, err := scanUpdateDbSchema(ctx, projectStore, newProjectID, v.Environment, v.Driver, v.DbModel, connParams)
	if err != nil {
		if dbcopy.IsServerConnectionFailure(err) {
			// The specification of `scan` names 4 for a database that cannot be connected to. The text is the
			// error's own: it names no part of the connection.
			return Exit(err.Error(), scanExitCodeConnection)
		}
		return err
	}

	// The descriptor goes down before the project that points at it: a project
	// that names a missing one is worse than a descriptor with no project. But a
	// project that cannot be saved must leave no descriptor behind, in a directory
	// with no project, so the project is validated (as SaveProject does first)
	// before anything is written, and a save that fails takes back the descriptor
	// this scan wrote: it removes one the scan made and puts back one that was there.
	descriptor, hasDescriptor := connParams.(descriptorWriter)
	if hasDescriptor {
		if err = datatugProject.Validate(); err != nil {
			return fmt.Errorf("failed to save datatug project [%v]: project validation failed: %w", datatugProject.ID, err)
		}
	}
	if projectDirIsNew {
		if err = scanMkdirAll(v.ProjectDir, 0o755); err != nil {
			return fmt.Errorf("failed to create the project folder %q: %w", v.ProjectDir, err)
		}
	}
	undoDescriptor := func() error { return nil }
	if hasDescriptor {
		if undoDescriptor, err = descriptor.WriteDescriptor(v.ProjectDir); err != nil {
			return err
		}
	}

	log.Println("Saving project", datatugProject.ID, "...")
	saveStore, _ := filestore.NewSingleProjectStore(v.ProjectDir, datatugProject.ID)
	savedProject := saveStore.GetProjectStore(datatugProject.ID)
	scanned := api.ScannedCatalog{Driver: v.Driver, Environment: v.Environment, ID: v.Database, Server: api.ScannedServer(v.Driver, connParams)}
	return saveScanned(func() error {
		return api.SaveScannedProject(ctx, savedProject, v.ProjectDir, datatugProject, scanned, stderr)
	}, undoDescriptor)
}

// saveScanned runs save, and when it fails takes back what the scan wrote beside the
// project (undo): a descriptor that points at a project that was not saved is worse than
// none. An undo that fails is said with the failure of the save, which is what the person
// needs to know first.
func saveScanned(save, undo func() error) error {
	err := save()
	if err == nil {
		return nil
	}
	if undoErr := undo(); undoErr != nil {
		return errors.Join(err, fmt.Errorf("and the connection descriptor this scan wrote could not be taken back: %w", undoErr))
	}
	return err
}

// checkProjectDir is whether the project folder dir is not there yet, and an error
// when it cannot be one: it is a file, an address, or it cannot be looked at.
func checkProjectDir(dir string) (isNew bool, err error) {
	if strings.Contains(dir, "://") {
		// A registered project that lives at an address, not on this machine: there is no
		// folder to scan into, and none to make.
		return false, fmt.Errorf("project folder %q is not a folder on this machine", dir)
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("cannot use project folder %q: %w", dir, err)
	case !info.IsDir():
		return false, fmt.Errorf("project folder %q is a file, not a folder", dir)
	}
	return false, nil
}

// scanMkdirAll is a seam over os.MkdirAll, which checkProjectDir has already shown
// can make the folder a scan makes. Always os.MkdirAll in production.
var scanMkdirAll = os.MkdirAll

// scanFilepathAbs is a seam over filepath.Abs, which fails only when the working
// directory is gone. Always filepath.Abs in production.
var scanFilepathAbs = filepath.Abs

// scanDirectoryAbs permits testing a missing working directory without changing
// the separate optional project-name inference seam.
var scanDirectoryAbs = filepath.Abs

// descriptorWriter is implemented by connection parameters that need a file
// beside the project: PostgreSQL's connection descriptor. It returns the function
// that takes the write back, for a scan whose save fails.
type descriptorWriter interface {
	WriteDescriptor(projectDir string) (undo func() error, err error)
}

// scanLookupEnv is the environment a PostgreSQL scan reads its connection URL
// from, a seam so tests never touch the process environment. Always
// os.LookupEnv in production.
var scanLookupEnv = os.LookupEnv

// scanUpdateDbSchema is a seam over api.UpdateDbSchema so tests can drive the
// save step with a project the scanner itself does not produce (an invalid
// one, or one that lacks the scanned catalog). Always api.UpdateDbSchema in
// production.
var scanUpdateDbSchema = api.UpdateDbSchema

// scanDrivers are the drivers a scan reads, in the order the messages name them.
var scanDrivers = []string{dbconnection.DriverSQLite3, api.DriverPostgres, api.DriverMySQL, api.DriverOracle, api.DriverSQLServer, api.DriverCouchDB, api.DriverPostgREST, api.DriverLibSQL}

// checkScanDriver is an error, naming --driver and the drivers a scan reads, when driver is not
// one of them. The value is said as it was typed only when it is a plain name: the flag may have
// been given a connection string.
func checkScanDriver(driver string) error {
	if driver == "" {
		return fmt.Errorf("--driver (-D) is required: the database driver to scan, %s", scanDriverNames())
	}
	if !slices.Contains(scanDrivers, driver) {
		return fmt.Errorf("--driver (-D) %q is not a driver a scan reads: use %s", dbcopy.SourceIDDisplay(driver), scanDriverNames())
	}
	return nil
}

// scanDriverNames is "sqlite3, postgres, mysql, oracle, sqlserver or couchdb".
func scanDriverNames() string {
	return strings.Join(scanDrivers[:len(scanDrivers)-1], ", ") + " or " + scanDrivers[len(scanDrivers)-1]
}

// connectionParams builds DB connection parameters from the scan flags.
func (v *scanDbCommand) connectionParams() (dbconnection.Params, error) {
	if api.IsServerDriver(v.Driver) || v.Driver == api.DriverCouchDB || v.Driver == api.DriverPostgREST || v.Driver == api.DriverLibSQL {
		return v.descriptorConnectionParams()
	}
	if v.DSNEnv != "" {
		return nil, fmt.Errorf("--dsn-env applies only to -D %s, %s, %s, %s, %s, %s or %s", api.DriverPostgres, api.DriverMySQL, api.DriverOracle, api.DriverSQLServer, api.DriverCouchDB, api.DriverPostgREST, api.DriverLibSQL)
	}
	// sqlite3 is the one driver a scan reads that is not a server (checkScanDriver has refused any other).
	if v.Path == "" {
		return nil, fmt.Errorf("scanning a sqlite3 database requires --path to the database file")
	}
	return dbconnection.NewSQLite3ConnectionParams(v.Path, v.Database, dbconnection.ModeReadOnly), nil
}

// scanArticle is driver with the article a sentence puts in front of it: "a postgres", "an oracle".
func scanArticle(driver string) string {
	if driver == api.DriverOracle {
		return "an " + driver
	}
	return "a " + driver
}

// scanURLExamples is the shape of the connection URL of each driver a scan reads from --dsn-env.
var scanURLExamples = map[string]string{
	api.DriverPostgres:  "postgres://user:password@host/database",
	api.DriverMySQL:     "mysql://user:password@host:3306/database",
	api.DriverOracle:    "oracle://user:password@host:1521/service",
	api.DriverSQLServer: "sqlserver://user:password@host:1433/database",
	api.DriverCouchDB:   "couchdb://host:6984/database (couchdb+http://127.0.0.1:5984/database for a loopback development server)",
	api.DriverPostgREST: "postgrest://host:3000/rest/v1 (postgrest+http://127.0.0.1:3000 for a loopback development server)",
	api.DriverLibSQL:    "libsql://host[:port][/base-path] (libsql+http://127.0.0.1:8080 for a loopback development server); the token is never in the URL but in DATATUG_LIBSQL_BEARER",
}

// descriptorConnectionParams builds the parameters of a PostgreSQL, MySQL, Oracle, SQL Server or CouchDB
// scan. The only way to connect is a connection URL held in an environment variable named by --dsn-env:
// the URL carries the host, port, user and password together, so a password is never on a command line
// (the process list and the shell history would keep it) or in a project file, and each of those flags is
// refused instead of being quietly ignored. The project records only a descriptor that names the variable.
func (v *scanDbCommand) descriptorConnectionParams() (dbconnection.Params, error) {
	var refused []string
	for _, flag := range []struct {
		name  string
		given bool
	}{{"--server", v.Host != ""}, {"--port", v.Port != 0}, {"--user", v.User != ""}, {"--password", v.Password != ""}, {"--path", v.Path != ""}} {
		if flag.given {
			refused = append(refused, flag.name)
		}
	}
	if len(refused) > 0 {
		return nil, fmt.Errorf("%s not accepted for %s scan: the host, port, user and password come from the connection URL in an environment variable, and --path is for sqlite3 files. Put the URL there and pass --dsn-env NAME, so a password never appears on a command line or in a project file", strings.Join(refused, ", "), scanArticle(v.Driver))
	}
	if v.DSNEnv == "" {
		return nil, fmt.Errorf("scanning a %s database requires --dsn-env NAME, the environment variable that holds the connection URL (%s)", v.Driver, scanURLExamples[v.Driver])
	}
	if !dbcopy.ValidEnvName(v.DSNEnv) {
		return nil, fmt.Errorf("--dsn-env must be an environment variable name: upper-case letters, digits and underscores, starting with a letter")
	}
	// The project will name this variable in its descriptor, and a project is
	// only allowed to name some variables: say so now, not when it is opened.
	if err := dbcopy.CheckDescriptorEnvName(v.DSNEnv, scanLookupEnv); err != nil {
		return nil, fmt.Errorf("--dsn-env: %w", err)
	}
	if v.Driver == api.DriverCouchDB {
		params, err := api.NewCouchDBScanParams(scanLookupEnv, v.DSNEnv, v.Environment, v.Database)
		if err != nil {
			return nil, err // not params: a nil *CouchDBScanParams would be a non-nil Params
		}
		return params, nil
	}
	if v.Driver == api.DriverPostgREST {
		params, err := api.NewPostgRESTScanParams(scanLookupEnv, v.DSNEnv, v.Environment, v.Database)
		if err != nil {
			return nil, err // not params: a nil *PostgRESTScanParams would be a non-nil Params
		}
		return params, nil
	}
	if v.Driver == api.DriverLibSQL {
		params, err := api.NewLibSQLScanParams(scanLookupEnv, v.DSNEnv, v.Environment, v.Database)
		if err != nil {
			return nil, err // not params: a nil *LibSQLScanParams would be a non-nil Params
		}
		return params, nil
	}
	params, err := api.NewServerScanParams(v.Driver, scanLookupEnv, v.DSNEnv, v.Environment, v.Database)
	if err != nil {
		return nil, err // not params: a nil *PostgresScanParams would be a non-nil Params
	}
	return params, nil
}

func scanCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Adds or updates DB metadata",
		Long: "Adds or updates DB metadata from a specific server in a specific environment.\n\n" +
			"Scanning PostgreSQL (-D postgres --dsn-env NAME) reads every schema the role can use, the system schemas apart: its tables, with views and materialized views as views, " +
			"the columns of each with their defaults (the text of the SQL expression) and the primary keys, and saves them like any other scan, a folder for each schema. " +
			"The connection URL (host, port, user and password) stays in the environment variable: the project holds the name of the variable in a connection descriptor, " +
			"connections/<env>/<db>.json, and the driver and the database id in its environment file, and never a host, a port, a user or a password. " +
			"The scan does not save foreign keys or indexes.\n\n" +
			"Scanning MySQL (-D mysql --dsn-env NAME, mysql://user:password@host:3306/database) reads the tables of the database of the URL; scanning Oracle " +
			"(-D oracle --dsn-env NAME, oracle://user:password@host:1521/service) reads the tables and views of the session's schema; scanning SQL Server " +
			"(-D sqlserver --dsn-env NAME, sqlserver://user:password@host:1433/database) reads the tables and views of the login's default schema. The project records them " +
			"under the schema \"default\" and, as for PostgreSQL, holds the name of the variable and never the URL or any part of it. " +
			"Scanning CouchDB (-D couchdb --dsn-env NAME, couchdb://host:6984/database) reads the one configured collection, its native _id key and its Mango indexes; " +
			"the project likewise records a connection descriptor that names the operator's variable, never the endpoint. " +
			"Scanning PostgREST (-D postgrest --dsn-env NAME, postgrest://host:3000/rest/v1) reads the relations the role can see from the API root's OpenAPI document; " +
			"the project likewise records a connection descriptor that names the operator's variable, never the endpoint. " +
			"-s/--server, --port, -U/--user and -P/--password are not accepted: a password on a command line stays in the shell history and the process list.",
		RunE: scanCommandAction,
	}
	flags := cmd.Flags()
	flags.StringP("project", "p", "", "Registered project id/name to scan into; with --directory, the id of a new project (default: the name of the folder)")
	flags.StringP("directory", "d", "", "Path to the project directory (alternative to --project); made if it does not exist")
	flags.StringP("driver", "D", "", "DB driver: sqlite3, postgres, mysql, oracle, sqlserver, couchdb, postgrest or libsql (postgres reads every schema; for every driver but sqlite3 the connection URL comes from the environment variable of --dsn-env)")
	flags.StringP("server", "s", "", "Not accepted: a server's host is in the connection URL of --dsn-env")
	flags.Int("port", 0, "Not accepted: a server's port is in the connection URL of --dsn-env")
	flags.StringP("user", "U", "", "Not accepted: the user is in the connection URL of --dsn-env")
	flags.StringP("password", "P", "", "Not accepted: the password is in the connection URL of --dsn-env")
	flags.String("db", "", "ID of database to scan: a plain name, as it is the name of a folder of the project (the database on a server is the one of the connection URL)")
	flags.String("dbmodel", "", "ID of DB model: a plain name (default: the model the project already records for the database in this environment, else the model the other environments record for it when they agree, else the ID of the database)")
	flags.String("env", "", "Environment the DB belongs to: a plain name. E.g.: LOCAL, DEV, SIT, UAT, PERF, PROD.")
	flags.String("path", "", "Path to the SQLite database file (required for -D sqlite3); it must exist")
	flags.String("dsn-env", "", "Environment variable that holds the connection URL, for -D postgres (postgres://user:password@host/database), -D mysql (mysql://user:password@host:3306/database), -D oracle (oracle://user:password@host:1521/service), -D sqlserver (sqlserver://user:password@host:1433/database), -D couchdb (couchdb://host:6984/database) -D postgrest (postgrest://host:3000/rest/v1) or -D libsql (libsql://host:8080). The host, port, user and password stay in the variable and are never written to the project, which holds only the name of the variable. The name must start with "+dbcopy.DescriptorEnvPrefix+" or be listed in "+dbcopy.DescriptorEnvAllowList)
	registerFollowProjectLinkFlag(cmd)
	_ = cmd.MarkFlagRequired("db")
	_ = cmd.MarkFlagRequired("env")
	return cmd
}

// scanDbCommand defines parameters for scan consoleCommand
type scanDbCommand struct {
	projectBaseCommand
	Driver      string
	Host        string
	Port        int
	User        string
	Password    string
	Database    string
	DbModel     string
	Environment string
	Path        string
	DSNEnv      string
}
