package api

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/parallel"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/datatug/datatug/pkg/schemers/mssqlschema"
	"github.com/strongo/random"
	"github.com/strongo/slice"
	"github.com/strongo/validation"
)

// ProjectLoader defines an interface to load project info
type ProjectLoader interface {
	// LoadProjectSummary loads project summary
	LoadProjectSummary(ctx context.Context) (projectSummary datatug.ProjectSummary, err error)
	// LoadProject loads the whole project
	LoadProject(ctx context.Context, o ...datatug.StoreOption) (project *datatug.Project, err error)
}

var _ ProjectLoader = (datatug.ProjectStore)(nil)

// UpdateDbSchema updates DB schema
func UpdateDbSchema(ctx context.Context, projectLoader ProjectLoader, projectID, environment, driver, dbModelID string, dbConnParams dbconnection.Params) (project *datatug.Project, err error) {
	log.Printf("Updating DB info for project=%v, env=%v, driver=%v, dbModelID=%v, dbCatalog=%v, connStr=%v",
		projectID, environment, driver, dbModelID, dbConnParams.Catalog(), dbConnParams.String())

	if dbConnParams.Catalog() == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("dbConnParams.catalog")
	}

	if projectID == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("projectID")
	}
	if environment == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("environment")
	}
	if driver == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("driver")
	}
	if dbModelID == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("dbModelId")
	}
	var (
		dbCatalog *datatug.DbCatalog
	)
	var projSummaryErr error
	getProjectSummaryWorker := func() error {
		_, projSummaryErr = projectLoader.LoadProjectSummary(ctx)
		if err != nil {
			if datatug.ProjectDoesNotExist(projSummaryErr) {
				return nil
			}
			return fmt.Errorf("failed to load project summary: %w", err)
		}
		return nil
	}
	dbServer := datatug.ServerRef{
		Driver: driver,
		Host:   dbConnParams.Server(),
		Port:   dbConnParams.Port(),
	}
	scanDbWorker := func() error {
		var scanErr error
		if dbCatalog, scanErr = scanDbCatalog(dbServer, dbConnParams); err != nil {
			return scanErr
		}
		return scanErr
	}
	if err = parallel.Run(
		getProjectSummaryWorker,
		scanDbWorker,
	); err != nil {
		return project, err
	}

	if dbCatalog.Path == "" {
		if connWithPath, ok := dbConnParams.(interface{ Path() string }); ok {
			dbCatalog.Path = connWithPath.Path()
		}
	}

	if dbCatalog.DbModel != "" {
		dbCatalog.DbModel = dbModelID
	} else if dbCatalog.DbModel == "" {
		dbCatalog.DbModel = dbCatalog.ID
	}
	if datatug.ProjectDoesNotExist(projSummaryErr) {
		log.Println("Creating a new DataTug project...")
		if project, err = newProjectWithDatabase(environment, dbServer, dbCatalog); err != nil {
			return project, err
		}
	} else {
		log.Printf("Loading existing project...")
		if project, err = projectLoader.LoadProject(ctx); err != nil {
			err = fmt.Errorf("failed to load DataTug project: %w", err)
			return
		}
		log.Println("Updating project with latest database info...", environment)
		if err = updateProjectWithDbCatalog(project, environment, dbServer, dbCatalog); err != nil {
			return project, fmt.Errorf("failed in updateProjectWithDbCatalog(): %w", err)
		}
	}
	dbModel := project.DbModels.GetByID(dbCatalog.DbModel)
	if dbModel == nil {
		err = fmt.Errorf("db model not found by ID: %v. there is %v db models in the project: %v", dbCatalog.DbModel, len(project.DbModels), strings.Join(project.DbModels.IDs(), ", "))
		return
	}
	if err = updateDbModelWithDbCatalog(environment, dbModel, dbCatalog); err != nil {
		err = fmt.Errorf("failed to update dbModel with database: %w", err)
		return
	}
	return project, err
}

func updateProjectWithDbCatalog(project *datatug.Project, envID string, dbServerRef datatug.ServerRef, dbCatalog *datatug.DbCatalog) (err error) {
	if envID == "" {
		return validation.NewErrRequestIsMissingRequiredField("envID")
	}
	if dbServerRef.Host == "" {
		return validation.NewErrRequestIsMissingRequiredField("dbServerRef.Host")
	}
	if dbCatalog == nil {
		return validation.NewErrRequestIsMissingRequiredField("dbCatalog")
	}
	if dbCatalog.ID == "" {
		return validation.NewErrRequestIsMissingRequiredField("dbCatalog.ID")
	}
	if dbCatalog.Driver == "" {
		dbCatalog.Driver = dbServerRef.Driver
	} else if dbCatalog.Driver != dbServerRef.Driver {
		return validation.NewErrBadRecordFieldValue("driver", "dbCatalog.driver != dbServerRef.driver")
	}
	if err = dbServerRef.Validate(); err != nil {
		return fmt.Errorf("db server ref is invalid: %w", err)
	}
	if err = dbCatalog.Validate(); err != nil {
		return fmt.Errorf("new db catalog is invalid: %w", err)
	}
	// Update environment
	{
		if environment := project.Environments.GetByID(envID); environment == nil {
			environment = &datatug.Environment{
				ProjectItem: datatug.ProjectItem{
					ProjItemBrief: datatug.ProjItemBrief{ID: envID},
				},
				DbServers: datatug.EnvDbServers{
					{
						ServerRef: dbServerRef,
						Catalogs:  []string{dbCatalog.ID},
					},
				},
			}
			project.Environments = append(project.Environments, environment)
		} else if envDbServer := environment.DbServers.GetByServerRef(dbServerRef); envDbServer == nil {
			environment.DbServers = append(environment.DbServers, &datatug.EnvDbServer{
				ServerRef: dbServerRef,
				Catalogs:  []string{dbCatalog.ID},
			})
		} else if i := slice.Index(envDbServer.Catalogs, dbCatalog.ID); i < 0 {
			envDbServer.Catalogs = append(envDbServer.Catalogs, dbCatalog.ID)
		}
	}
	// Update DB server
	{
		ctx := context.Background()
		projDbServer, err := project.GetProjDbServer(ctx, dbServerRef)
		if err != nil {
			return err
		}
		if projDbServer == nil {
			db := project.DbDrivers.GetByID(dbServerRef.Driver)
			if db == nil {
				db = new(datatug.ProjDbDriver)
				db.ID = dbServerRef.Driver
				project.DbDrivers = append(project.DbDrivers, db)
			}
			projDbServer = &datatug.ProjDbServer{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: dbServerRef.GetID()}},
				Server:      dbServerRef,
				Catalogs:    datatug.DbCatalogs{dbCatalog},
			}
			db.Servers = append(db.Servers, projDbServer)
		}
		updated := false
		for j, db := range projDbServer.Catalogs {
			if db.ID == dbCatalog.ID {
				// restore path as path might be normalized if pointing to user's directory
				dbCatalog.Path = projDbServer.Catalogs[j].Path
				// TODO: currently replaces catalog with new info, should merge to preserver comments
				projDbServer.Catalogs[j] = dbCatalog
				updated = true
				break
			}
		}
		if !updated {
			projDbServer.Catalogs = append(projDbServer.Catalogs, dbCatalog)
		}
	}
	return nil
}

func newProjectWithDatabase(environment string, dbServer datatug.ServerRef, dbCatalog *datatug.DbCatalog) (project *datatug.Project, err error) {
	//var currentUser *user.User
	//if currentUser, err = user.Current(); err != nil {
	//	err = fmt.Errorf("failed to get current OS user")
	//	return
	//}
	project = &datatug.Project{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: random.ID(9)},
			Access:        "private",
		},
		Created: &datatug.ProjectCreated{
			//ByUsername: currentUser.Username,
			//ByName:     currentUser.Name,
			At: time.Now(),
		},
		DbModels: datatug.DbModels{
			&datatug.DbModel{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: dbCatalog.DbModel}},
			},
		},
		Environments: datatug.Environments{
			{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: environment}},
				DbServers: []*datatug.EnvDbServer{
					{
						ServerRef: dbServer,
						Catalogs:  []string{dbCatalog.ID},
					},
				},
			},
		},
		DbDrivers: datatug.ProjDbDrivers{
			{
				Servers: datatug.ProjDbServers{
					{
						ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: dbServer.GetID()}},
						Server:      dbServer,
						Catalogs: datatug.DbCatalogs{
							dbCatalog,
						},
					},
				},
			},
		},
	}
	log.Println("project.ID:", project.ID)
	return project, err
}

func scanDbCatalog(server datatug.ServerRef, connectionParams dbconnection.Params) (dbCatalog *datatug.DbCatalog, err error) {
	var db *sql.DB

	if db, err = sql.Open(server.Driver, connectionParams.ConnectionString()); err != nil {
		return nil, fmt.Errorf("failed to open SQL db: %w", err)
	}

	// Close the database connection pool after command executes
	defer func() { _ = db.Close() }()

	//informationSchema := schemer.NewInformationSchema(server, db)

	var scanner schemer.Scanner
	switch server.Driver {
	case "sqlserver":
		scanner = schemer.NewScanner(mssqlschema.NewSchemaProvider())
	// TODO: Disabled until figured out how to exclude it from Google Appengine Build
	//case "sqlite3":
	//	scanner = schemer.NewScanner(sqlite.NewSchemaProvider())
	default:
		return nil, fmt.Errorf("unsupported DB driver: %v", err)
	}

	dbCatalog, err = scanner.ScanCatalog(context.Background(), connectionParams.Catalog())
	if err != nil {
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", err)
	}
	dbCatalog.ID = connectionParams.Catalog()
	//if database, err = informationSchema.GetDatabase(connectionParams.Database()); err != nil {
	//	return nil, fmt.Errorf("failed to get database metadata: %w", err)
	//}
	return
}

func updateDbModelWithDbCatalog(envID string, dbModel *datatug.DbModel, dbCatalog *datatug.DbCatalog) (err error) {
	{ // Update dbmodel environments
		environment := dbModel.Environments.GetByID(envID)
		if environment == nil {
			environment = &datatug.DbModelEnv{ID: envID}
			dbModel.Environments = append(dbModel.Environments, environment)
		}
		dbModelDb := environment.DbCatalogs.GetByID(dbCatalog.ID)
		if dbModelDb == nil {
			environment.DbCatalogs = append(environment.DbCatalogs, &datatug.DbModelDbCatalog{
				ID: dbCatalog.ID,
			})
		}
	}

	for _, schema := range dbCatalog.Schemas {
		var schemaModel *datatug.Schema
		for _, sm := range dbModel.Schemas {
			if sm.ID == schema.ID {
				schemaModel = sm
				goto UpdateSchemaModel
			}
		}
		schemaModel = &datatug.Schema{
			ProjectItem: schema.ProjectItem,
		}
		dbModel.Schemas = append(dbModel.Schemas, schemaModel)
	UpdateSchemaModel:
		if err = updateSchemaModel(envID, schemaModel, schema); err != nil {
			return fmt.Errorf("faild to update DB schema model: %w", err)
		}
	}
	return nil
}

func updateSchemaModel(envID string, schema *datatug.Schema, dbSchema *datatug.DbSchema) (err error) {
	updateTables := func(tables []*datatug.CollectionInfo) (result datatug.TableModels) {
		for _, table := range tables {
			tableModel := schema.Tables.GetByKey(table.DBCollectionKey)
			if tableModel == nil {
				tableModel = &datatug.TableModel{
					DBCollectionKey: table.DBCollectionKey,
					ByEnv:           make(datatug.StateByEnv),
				}
				tableModel.ByEnv[envID] = &datatug.EnvState{
					Status: "exists",
				}
				tableModel.Columns = make(datatug.ColumnModels, len(table.Columns))
				for i, c := range table.Columns {
					tableModel.Columns[i] = &datatug.ColumnModel{
						ColumnInfo: *c,
						ByEnv:      make(datatug.StateByEnv),
					}
					tableModel.Columns[i].ByEnv[envID] = &datatug.EnvState{
						Status: "exists",
					}
				}
				result = append(result, tableModel)
			} else {
				panic(errNotImplementedYet)
			}
		}
		return
	}
	schema.Tables = updateTables(dbSchema.Tables)
	schema.Views = updateTables(dbSchema.Views)
	return nil
}
