package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/spf13/cobra"
)

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

	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
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
	// A database file whose path has a "?" in it is read by the scan and not opened
	// again by the project: it is refused now, before the project is looked at. The path
	// that is looked at is the whole path of the file, which a relative --path is not.
	if v.Driver == dbconnection.DriverSQLite3 && v.Path != "" {
		absolutePath, absErr := scanFilepathAbs(v.Path)
		if absErr != nil {
			return fmt.Errorf("cannot tell where --path %q is: %w", v.Path, absErr)
		}
		if err := api.CheckSQLitePath(v.Path, absolutePath); err != nil {
			return err
		}
	}
	// A database the project already holds stays on its model: a scan without --dbmodel
	// keeps it, and one that names another is refused, naming both. The same database in
	// another environment is on the model the other environments record for it.
	var err error
	if v.DbModel, err = api.ResolveScanDbModel(v.ProjectDir, v.Environment, v.Database, v.DbModel); err != nil {
		return err
	}
	// A name that differs only by case from one the project has is one folder on some
	// file systems: it is refused now, before the database is read.
	if err = api.CheckScanNamesAgainstProject(v.ProjectDir, v.Environment, v.Database, v.DbModel); err != nil {
		return err
	}
	// A project folder that is not there is made, once the scan has read something: a
	// scan that fails makes nothing.
	projectDirIsNew, err := checkProjectDir(v.ProjectDir)
	if err != nil {
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
		return err
	}

	// The descriptor goes down before the project that points at it: a project
	// that names a missing one is worse than a descriptor with no project. But a
	// project that cannot be saved must leave no descriptor behind, in a directory
	// with no project, so the project is validated (as SaveProject does first)
	// before anything is written.
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
	if hasDescriptor {
		if err = descriptor.WriteDescriptor(v.ProjectDir); err != nil {
			return err
		}
	}

	log.Println("Saving project", datatugProject.ID, "...")
	saveStore, _ := filestore.NewSingleProjectStore(v.ProjectDir, datatugProject.ID)
	savedProject := saveStore.GetProjectStore(datatugProject.ID)
	scanned := api.ScannedCatalog{Driver: v.Driver, Environment: v.Environment, ID: v.Database, Server: api.ScannedServer(v.Driver, connParams)}
	return api.SaveScannedProject(ctx, savedProject, v.ProjectDir, datatugProject, scanned, stderr)
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

// descriptorWriter is implemented by connection parameters that need a file
// beside the project: PostgreSQL's connection descriptor.
type descriptorWriter interface {
	WriteDescriptor(projectDir string) error
}

// scanLookupEnv is the environment a PostgreSQL scan reads its connection URL
// from, a seam so tests never touch the process environment. Always
// os.LookupEnv in production.
var scanLookupEnv = os.LookupEnv

// scanPostgresAvailable is the answer to whether this release can scan
// PostgreSQL, a seam so a test can stand in for a datatug-core whose project
// model records postgres servers. Always api.CheckPostgresScanAvailable in
// production, which lifts itself on the day datatug-core accepts the driver.
var scanPostgresAvailable = api.CheckPostgresScanAvailable

// scanUpdateDbSchema is a seam over api.UpdateDbSchema so tests can drive the
// save step with a project the scanner itself does not produce (an invalid
// one, or one that lacks the scanned catalog). Always api.UpdateDbSchema in
// production.
var scanUpdateDbSchema = api.UpdateDbSchema

// scanNewConnectionString is a seam over dbconnection.NewConnectionString,
// whose error return the fixed, always-valid options connectionParams passes
// can never trip. Always dbconnection.NewConnectionString in production.
var scanNewConnectionString = dbconnection.NewConnectionString

// connectionParams builds DB connection parameters from the scan flags.
func (v *scanDbCommand) connectionParams() (dbconnection.Params, error) {
	if v.Driver == "" {
		return nil, fmt.Errorf("--driver (-D) is required: the database driver to scan, sqlite3 or sqlserver")
	}
	if v.Driver == api.DriverPostgres {
		return v.postgresConnectionParams()
	}
	if v.DSNEnv != "" {
		return nil, fmt.Errorf("--dsn-env applies only to -D %s", api.DriverPostgres)
	}
	if v.Driver == dbconnection.DriverSQLite3 {
		if v.Path == "" {
			return nil, fmt.Errorf("scanning a sqlite3 database requires --path to the database file")
		}
		return dbconnection.NewSQLite3ConnectionParams(v.Path, v.Database, dbconnection.ModeReadOnly), nil
	}

	if v.Host == "" {
		// Deriving the server/host from the project's environment config is not implemented yet.
		return nil, fmt.Errorf("deriving the DB server from environment config is not implemented yet — pass -s/--server")
	}

	options := []string{"mode=" + dbconnection.ModeReadOnly}
	if v.Port != 0 {
		options = append(options, "port="+strconv.Itoa(v.Port))
	}

	connParams, err := scanNewConnectionString(v.Driver, v.Host, v.User, v.Password, v.Database, options...)
	if err != nil {
		return nil, fmt.Errorf("invalid connection string: %w", err)
	}
	return connParams, nil
}

// postgresConnectionParams builds the parameters of a PostgreSQL scan. The only
// way to connect is a connection URL held in an environment variable named by
// --dsn-env: the URL carries the host, port, user and password together, so a
// password is never on a command line (the process list and the shell history
// would keep it) or in a project file, and each of those flags is refused
// instead of being quietly ignored.
func (v *scanDbCommand) postgresConnectionParams() (dbconnection.Params, error) {
	// Say that the scan does not exist before asking for anything: a user must not
	// be sent to put a production password in a variable, one refusal after
	// another, for a scan that cannot run, nor have the host and user of that
	// database logged.
	if err := scanPostgresAvailable(); err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("%s not accepted for a postgres scan: the host, port, user and password come from the connection URL in an environment variable, and --path is for sqlite3 files. Put the URL there and pass --dsn-env NAME, so a password never appears on a command line or in a project file", strings.Join(refused, ", "))
	}
	if v.DSNEnv == "" {
		return nil, fmt.Errorf("scanning a postgres database requires --dsn-env NAME, the environment variable that holds the connection URL (postgres://user:password@host/database)")
	}
	if !dbcopy.ValidEnvName(v.DSNEnv) {
		return nil, fmt.Errorf("--dsn-env must be an environment variable name: upper-case letters, digits and underscores, starting with a letter")
	}
	// The project will name this variable in its descriptor, and a project is
	// only allowed to name some variables: say so now, not when it is opened.
	if err := dbcopy.CheckDescriptorEnvName(v.DSNEnv, scanLookupEnv); err != nil {
		return nil, fmt.Errorf("--dsn-env: %w", err)
	}
	params, err := api.NewPostgresScanParams(scanLookupEnv, v.DSNEnv, v.Environment, v.Database)
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
			"Scanning PostgreSQL (-D postgres --dsn-env NAME) is not available in this release: a DataTug project cannot record a postgres server yet, " +
			"so the scan stops before it connects.",
		RunE: scanCommandAction,
	}
	flags := cmd.Flags()
	flags.StringP("project", "p", "", "Registered project id/name to scan into; with --directory, the id of a new project (default: the name of the folder)")
	flags.StringP("directory", "d", "", "Path to the project directory (alternative to --project); made if it does not exist")
	flags.StringP("driver", "D", "", "DB driver: sqlserver or sqlite3 (postgres is not available in this release: a project cannot record a postgres server yet)")
	flags.StringP("server", "s", "", "Network server / host name")
	flags.Int("port", 0, "Server network port (default if omitted)")
	flags.StringP("user", "U", "", "DB login user")
	flags.StringP("password", "P", "", "DB login password")
	flags.String("db", "", "ID of database to scan: a plain name, as it is the name of a folder of the project (for sqlserver also the name of the database on the server)")
	flags.String("dbmodel", "", "ID of DB model: a plain name (default: the model the project already records for the database in this environment, else the model the other environments record for it when they agree, else the ID of the database)")
	flags.String("env", "", "Environment the DB belongs to: a plain name. E.g.: LOCAL, DEV, SIT, UAT, PERF, PROD.")
	flags.String("path", "", "Path to the SQLite database file (required for -D sqlite3); it must exist, and its whole path (from the working directory, for a relative one) must have no \"?\" in it")
	flags.String("dsn-env", "", "Environment variable that holds the PostgreSQL connection URL, for -D postgres (not available in this release: a project cannot record a postgres server yet). The password stays in the variable and is never written to the project. The name must start with "+dbcopy.DescriptorEnvPrefix+" or be listed in "+dbcopy.DescriptorEnvAllowList)
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
