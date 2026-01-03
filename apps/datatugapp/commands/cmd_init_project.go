package commands

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/strongo/logus"
	"github.com/urfave/cli/v3"
)

var projectIdArg = &cli.StringArg{Name: "project"}
var projectPathArg = &cli.StringArg{Name: "projectPath"}

func initCommand() *cli.Command {
	return &cli.Command{
		Name:        "init",
		Usage:       "Creates a new datatug project",
		Description: "Creates a new datatug project in specified directory using a connection to some database",
		Action:      initCommandAction,
		Arguments: []cli.Argument{
			projectIdArg,
			projectPathArg,
		},
	}
}

func initCommandAction(ctx context.Context, c *cli.Command) (err error) {
	logus.Infof(ctx, "Initiating project...")

	projectDir := c.Arguments[1].Get().(string)
	if err = os.MkdirAll(projectDir, 0777); err != nil {
		return err
	}
	dataTugDirPath := path.Join(projectDir, "datatug")
	var fileInfo os.FileInfo
	if fileInfo, err = os.Stat(dataTugDirPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to get info about %v: %w", dataTugDirPath, err)
		}
	} else if fileInfo.IsDir() {
		return fmt.Errorf("the folder already contains datatug project: %v", dataTugDirPath)
	}
	//return fmt.Errorf("the folder  contains a `datatug` file, this name is reserver for DataTug directory: %v", dataTugDirPath)

	//var port int
	//if v.Port != "" {
	//	if port, err = strconv.Atoi(v.Port); err != nil {
	//		return err
	//	}
	//}
	//
	//connString := updateUrlConfig.NewConnectionString(v.Host, v.User, v.Password, v.Database, port)
	//
	//var db *sql.DB
	//
	//if db, err = sql.Open(v.driver, connString.String()); err != nil {
	//	log.Fatal("Error creating DB connection: " + err.Error())
	//}
	//
	//// Close the database connection pool after consoleCommand executes
	//defer func() { _ = db.Close() }()
	//
	//server := datatug.ServerRef{driver: v.driver, Host: v.Host, Port: port}
	//informationSchema := schemer.NewInformationSchema(server, db)
	//
	//var database *datatug.Database
	//if database, err = informationSchema.GetDatabase(v.Database); err != nil {
	//	return fmt.Errorf("failed to get database metadata: %w", err)
	//}

	projectID := projectIdArg.Get().(string)

	storage.Current, projectID = filestore.NewSingleProjectStore(projectDir, projectID)
	datatugProject := datatug.Project{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: projectID},
			Access:        "private",
		},
		//Environments: []*datatug.Environment{
		//	{
		//		ProjectItem: datatug.ProjectItem{ID: v.Environment},
		//		DbServers: []*datatug.EnvDbServer{
		//			{
		//				driver:    v.driver,
		//				Host:      server.Host,
		//				Port:      server.Port,
		//				DatabaseDiffs: []string{database.ID},
		//			},
		//		},
		//		DatabaseDiffs: []*datatug.Database{
		//			database,
		//		},
		//	},
		//},
	}
	var currentUser *user.User
	if currentUser, err = user.Current(); err != nil {
		return err
	}
	if currentUser != nil {
		datatugProject.Created = &datatug.ProjectCreated{
			//ByName:     currentUser.ID,
			//ByUsername: currentUser.Username,
			At: time.Now(),
		}
	}

	var dal storage.Store
	if dal, err = storage.NewDatatugStore(""); err != nil {
		return err
	}
	store := dal.GetProjectStore(projectID)
	if err = store.SaveProject(context.Background(), &datatugProject); err != nil {
		return err
	}
	return err
}
