package commands

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dbconnection"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/urfave/cli/v3"
)

var (
	scanProjectFlag  = cli.StringFlag{Name: "project", Aliases: []string{"p"}, Usage: "Registered project id/name to scan into"}
	scanDirFlag      = cli.StringFlag{Name: "directory", Aliases: []string{"d"}, Usage: "Path to the project directory (alternative to --project)"}
	scanDriverFlag   = cli.StringFlag{Name: "driver", Aliases: []string{"D"}, Usage: "DB driver, e.g. sqlserver"}
	scanServerFlag   = cli.StringFlag{Name: "server", Aliases: []string{"s"}, Usage: "Network server / host name"}
	scanPortFlag     = cli.IntFlag{Name: "port", Usage: "Server network port (default if omitted)"}
	scanUserFlag     = cli.StringFlag{Name: "user", Aliases: []string{"U"}, Usage: "DB login user"}
	scanPasswordFlag = cli.StringFlag{Name: "password", Aliases: []string{"P"}, Usage: "DB login password"}
	scanDbFlag       = cli.StringFlag{Name: "db", Usage: "ID of database to scan", Required: true}
	scanDbModelFlag  = cli.StringFlag{Name: "dbmodel", Usage: "ID of DB model (required for newly scanned databases)"}
	scanEnvFlag      = cli.StringFlag{Name: "env", Usage: "Environment the DB belongs to. E.g.: LOCAL, DEV, SIT, UAT, PERF, PROD.", Required: true}
	scanPathFlag     = cli.StringFlag{Name: "path", Usage: "Path to the SQLite database file (required for -D sqlite3)"}
)

func scanCommandAction(_ context.Context, c *cli.Command) error {
	v := &scanDbCommand{}
	v.ProjectName = c.String(scanProjectFlag.Name)
	v.ProjectDir = c.String(scanDirFlag.Name)
	v.Driver = c.String(scanDriverFlag.Name)
	v.Host = c.String(scanServerFlag.Name)
	v.Port = c.Int(scanPortFlag.Name)
	v.User = c.String(scanUserFlag.Name)
	v.Password = c.String(scanPasswordFlag.Name)
	v.Database = c.String(scanDbFlag.Name)
	v.DbModel = c.String(scanDbModelFlag.Name)
	v.Environment = c.String(scanEnvFlag.Name)
	v.Path = c.String(scanPathFlag.Name)

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
	datatugProject, err := api.UpdateDbSchema(context.Background(), projectStore, v.projectID, v.Environment, v.Driver, v.DbModel, connParams)
	if err != nil {
		return err
	}

	log.Println("Saving project", datatugProject.ID, "...")
	saveStore, _ := filestore.NewSingleProjectStore(v.ProjectDir, datatugProject.ID)
	if err = saveStore.GetProjectStore(datatugProject.ID).SaveProject(context.Background(), datatugProject); err != nil {
		return fmt.Errorf("failed to save datatug project [%v]: %w", datatugProject.ID, err)
	}

	return nil
}

// connectionParams builds DB connection parameters from the scan flags.
func (v *scanDbCommand) connectionParams() (dbconnection.Params, error) {
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

	connParams, err := dbconnection.NewConnectionString(v.Driver, v.Host, v.User, v.Password, v.Database, options...)
	if err != nil {
		return nil, fmt.Errorf("invalid connection string: %w", err)
	}
	return connParams, nil
}

func scanCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "scan",
		Usage:       "Adds or updates DB metadata",
		Description: "Adds or updates DB metadata from a specific server in a specific environment",
		Flags: []cli.Flag{
			&scanProjectFlag, &scanDirFlag, &scanDriverFlag, &scanServerFlag, &scanPortFlag,
			&scanUserFlag, &scanPasswordFlag, &scanDbFlag, &scanDbModelFlag, &scanEnvFlag, &scanPathFlag,
		},
		Action: scanCommandAction,
	}
}

// scanDbCommand defines parameters for scan consoleCommand
type scanDbCommand struct {
	projectBaseCommand
	Driver      string `short:"D" long:"driver" description:"Supported values: sqlserver."`
	Host        string `short:"s" long:"server" description:"Network server name."`
	Port        int    `long:"port" description:"ServerReference network port, if not specified default is used."`
	User        string `short:"U" long:"user" description:"User name to login to DB."`
	Password    string `short:"P" long:"password" description:"Password to login to DB."`
	Database    string `long:"db" required:"true" description:"ID of database to be scanned."`
	DbModel     string `long:"dbmodel" required:"false" description:"ID of DB model, is required for newly scanned databases."`
	Environment string `long:"env" required:"true" description:"Specify environment the DB belongs to. E.g.: LOCAL, DEV, SIT, UAT, PERF, PROD."`
	Path        string `long:"path" description:"Path to the SQLite database file (required for sqlite3)."`
}
