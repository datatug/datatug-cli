package commands

import (
	"context"
	"fmt"
	"log"
	"os"
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
	if _, err := os.Stat(v.ProjectDir); os.IsNotExist(err) {
		return fmt.Errorf("ProjectDir=[%v] not found: %w", v.ProjectDir, err)
	}

	connParams, err := v.connectionParams()
	if err != nil {
		return err
	}

	if v.DbModel == "" {
		v.DbModel = v.Database
	}

	projectStore := v.store.GetProjectStore(v.projectID)
	datatugProject, err := scanUpdateDbSchema(context.Background(), projectStore, v.projectID, v.Environment, v.Driver, v.DbModel, connParams)
	if err != nil {
		return err
	}

	// The descriptor goes down before the project that points at it: an orphan
	// descriptor is harmless, a project that names a missing one is not.
	if descriptor, ok := connParams.(descriptorWriter); ok {
		if err = descriptor.WriteDescriptor(v.ProjectDir); err != nil {
			return err
		}
	}

	log.Println("Saving project", datatugProject.ID, "...")
	saveStore, _ := filestore.NewSingleProjectStore(v.ProjectDir, datatugProject.ID)
	if err = saveStore.GetProjectStore(datatugProject.ID).SaveProject(context.Background(), datatugProject); err != nil {
		return fmt.Errorf("failed to save datatug project [%v]: %w", datatugProject.ID, err)
	}

	return nil
}

// descriptorWriter is implemented by connection parameters that need a file
// beside the project: PostgreSQL's connection descriptor.
type descriptorWriter interface {
	WriteDescriptor(projectDir string) error
}

// scanLookupEnv is the environment a PostgreSQL scan reads its connection URL
// from, a seam so tests never touch the process environment. Always
// os.LookupEnv in production.
var scanLookupEnv = os.LookupEnv

// scanUpdateDbSchema is a seam over api.UpdateDbSchema so tests can drive the
// save step with a project the scanner itself does not produce (a project
// freshly built from a scan does not pass save-time validation). Always
// api.UpdateDbSchema in production.
var scanUpdateDbSchema = api.UpdateDbSchema

// scanNewConnectionString is a seam over dbconnection.NewConnectionString,
// whose error return the fixed, always-valid options connectionParams passes
// can never trip. Always dbconnection.NewConnectionString in production.
var scanNewConnectionString = dbconnection.NewConnectionString

// connectionParams builds DB connection parameters from the scan flags.
func (v *scanDbCommand) connectionParams() (dbconnection.Params, error) {
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
	var refused []string
	for _, flag := range []struct {
		name  string
		given bool
	}{{"--server", v.Host != ""}, {"--port", v.Port != 0}, {"--user", v.User != ""}, {"--password", v.Password != ""}} {
		if flag.given {
			refused = append(refused, flag.name)
		}
	}
	if len(refused) > 0 {
		return nil, fmt.Errorf("%s not accepted for a postgres scan: the host, port, user and password come from the connection URL in an environment variable. Put the URL there and pass --dsn-env NAME, so a password never appears on a command line or in a project file", strings.Join(refused, ", "))
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
		Long:  "Adds or updates DB metadata from a specific server in a specific environment",
		RunE:  scanCommandAction,
	}
	flags := cmd.Flags()
	flags.StringP("project", "p", "", "Registered project id/name to scan into")
	flags.StringP("directory", "d", "", "Path to the project directory (alternative to --project)")
	flags.StringP("driver", "D", "", "DB driver: sqlserver, sqlite3 or postgres")
	flags.StringP("server", "s", "", "Network server / host name")
	flags.Int("port", 0, "Server network port (default if omitted)")
	flags.StringP("user", "U", "", "DB login user")
	flags.StringP("password", "P", "", "DB login password")
	flags.String("db", "", "ID of database to scan")
	flags.String("dbmodel", "", "ID of DB model (required for newly scanned databases)")
	flags.String("env", "", "Environment the DB belongs to. E.g.: LOCAL, DEV, SIT, UAT, PERF, PROD.")
	flags.String("path", "", "Path to the SQLite database file (required for -D sqlite3)")
	flags.String("dsn-env", "", "Environment variable that holds the PostgreSQL connection URL (required for -D postgres; the password stays in the variable and is never written to the project). The name must start with "+dbcopy.DescriptorEnvPrefix+" or be listed in "+dbcopy.DescriptorEnvAllowList)
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
