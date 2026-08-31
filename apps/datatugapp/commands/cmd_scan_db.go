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
	flags.StringP("driver", "D", "", "DB driver, e.g. sqlserver")
	flags.StringP("server", "s", "", "Network server / host name")
	flags.Int("port", 0, "Server network port (default if omitted)")
	flags.StringP("user", "U", "", "DB login user")
	flags.StringP("password", "P", "", "DB login password")
	flags.String("db", "", "ID of database to scan")
	flags.String("dbmodel", "", "ID of DB model (required for newly scanned databases)")
	flags.String("env", "", "Environment the DB belongs to. E.g.: LOCAL, DEV, SIT, UAT, PERF, PROD.")
	flags.String("path", "", "Path to the SQLite database file (required for -D sqlite3)")
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
}
