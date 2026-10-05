package api

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/datatug/datatug-cli/pkg/schemers/mssqlschema"
	"github.com/datatug/datatug-cli/pkg/schemers/sqliteschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/parallel"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/strongo/random"
	"github.com/strongo/slice"
	"github.com/strongo/validation"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver sqliteScanDriver names
)

// ProjectLoader defines an interface to load project info
type ProjectLoader interface {
	LoadProjectFile(ctx context.Context) (projectFile datatug.ProjectFile, err error)
	LoadProject(ctx context.Context, o ...datatug.StoreOption) (project *datatug.Project, err error)
}

var _ ProjectLoader = (datatug.ProjectStore)(nil)

// UpdateDbSchema updates DB schema
func UpdateDbSchema(ctx context.Context, projectLoader ProjectLoader, projectID, environment, driver, dbModelID string, dbConnParams dbconnection.Params) (project *datatug.Project, err error) {
	// dbConnParams.String() is the connection string, password included: log
	// the parts that identify the target instead.
	log.Printf("Updating DB info for project=%v, env=%v, driver=%v, dbModelID=%v, dbCatalog=%v, server=%v, port=%v, user=%v",
		projectID, environment, driver, dbModelID, dbConnParams.Catalog(), dbConnParams.Server(), dbConnParams.Port(), dbConnParams.User())

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
	var projFileErr error
	getProjectSummaryWorker := func() error {
		_, projFileErr = projectLoader.LoadProjectFile(ctx)
		if projFileErr != nil {
			if datatug.ProjectDoesNotExist(projFileErr) {
				return nil
			}
			return fmt.Errorf("failed to load project summary: %w", projFileErr)
		}
		return nil
	}
	// SQLite is file-based: the project model forbids host/port for sqlite3
	// (the file path is carried on the catalog instead).
	dbServer := datatug.ServerRef{Driver: driver}
	if driver != dbconnection.DriverSQLite3 {
		dbServer.Host = dbConnParams.Server()
		dbServer.Port = dbConnParams.Port()
	}
	scanDbWorker := func() error {
		var scanErr error
		if dbCatalog, scanErr = scanDbCatalogSeam(dbServer, dbConnParams); scanErr != nil {
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
	if datatug.ProjectDoesNotExist(projFileErr) {
		log.Println("Creating a new DataTug project...")
		if project, err = newProjectWithDatabaseSeam(environment, dbServer, dbCatalog); err != nil {
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
		// A project that already holds another database has no model for this one.
		dbModel = &datatug.DbModel{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: dbCatalog.DbModel}}}
		project.DbModels = append(project.DbModels, dbModel)
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
	if dbServerRef.Driver != dbconnection.DriverSQLite3 && dbServerRef.Host == "" {
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
				db.Title = driverTitle(dbServerRef.Driver)
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
	if dbCatalog.Driver == "" {
		dbCatalog.Driver = dbServer.Driver
	}
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
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: dbServer.Driver, Title: driverTitle(dbServer.Driver)}},
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

// sqliteScanDriver is the database/sql driver a SQLite scan opens: modernc.org/sqlite,
// which is pure Go and is the driver the rest of the CLI opens SQLite files with. A
// release is built with cgo off, where the cgo driver registered as "sqlite3" is a
// stub that fails on its first use.
const sqliteScanDriver = "sqlite"

// checkSQLiteFile is nil when connectionParams name an existing SQLite file. A
// scan must not open a path that is not one: SQLite creates an empty database
// there, and the scan of a mistyped path would succeed with no tables.
func checkSQLiteFile(connectionParams dbconnection.Params) error {
	withPath, ok := connectionParams.(interface{ Path() string })
	if !ok || withPath.Path() == "" {
		return fmt.Errorf("a SQLite scan needs connection parameters that name the database file")
	}
	path := withPath.Path()
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot scan SQLite database: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("cannot scan SQLite database: %s is a folder, not a database file", path)
	}
	return nil
}

// driverTitle is the title of a driver item in a project. datatug-core refuses
// to save a driver item without one, so every place that builds one gives it this.
func driverTitle(driver string) string {
	switch driver {
	case dbconnection.DriverSQLite3:
		return "SQLite"
	case "sqlserver":
		return "SQL Server"
	case DriverPostgres:
		return "PostgreSQL"
	default:
		return driver
	}
}

func scanDbCatalog(server datatug.ServerRef, connectionParams dbconnection.Params) (dbCatalog *datatug.DbCatalog, err error) {
	if server.Driver == DriverPostgres {
		// Ask whether the project can record the server before opening anything:
		// a scan that cannot be saved must not connect and read a schema first.
		if err = checkPostgresServer(server); err != nil {
			return nil, err
		}
		// PostgreSQL is read through DALgo's schema reader, and opened from an
		// environment variable: there is no connection string to give database/sql.
		return scanPostgresCatalog(context.Background(), connectionParams)
	}
	var db *sql.DB

	driverName := server.Driver
	if server.Driver == dbconnection.DriverSQLite3 {
		if err = checkSQLiteFile(connectionParams); err != nil {
			return nil, err
		}
		driverName = sqliteScanDriver
	}
	if db, err = sql.Open(driverName, connectionParams.ConnectionString()); err != nil {
		return nil, fmt.Errorf("failed to open SQL db: %w", err)
	}

	// Close the database connection pool after command executes
	defer func() { _ = db.Close() }()

	//informationSchema := schemer.NewInformationSchema(server, db)

	var scanner schemer.Scanner
	switch server.Driver {
	case "sqlserver":
		scanner = schemer.NewScanner(mssqlschema.NewSchemaProvider(db))
	case dbconnection.DriverSQLite3:
		scanner = schemer.NewScanner(sqliteschema.NewSchemaProvider(func() (*sql.DB, error) { return db, nil }))
	default:
		return nil, fmt.Errorf("unsupported DB driver: %v", server.Driver)
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
		if err = updateSchemaModelSeam(envID, schemaModel, schema); err != nil {
			return fmt.Errorf("faild to update DB schema model: %w", err)
		}
	}
	return nil
}

func updateSchemaModel(envID string, schema *datatug.Schema, dbSchema *datatug.DbSchema) (err error) {
	updateTables := func(tables []*datatug.CollectionInfo) (result datatug.TableModels, err error) {
		for _, table := range tables {
			tableModel := schema.Tables.GetByKey(table.DBCollectionKey)
			if tableModel == nil {
				// datatug-core's datatug.TableModel (the git-tracked schema type)
				// carries no keys, indexes or references, only the live-scanned
				// datatug.CollectionInfo (table, above) does, so they are not in
				// the model. Nor is anything else a scan found: SaveScannedProject
				// saves the model as an id and its environments and writes the
				// columns of each table as files of their own, in the layout
				// that spec/features/cli/scan/README.md describes; keys are not
				// stored in a project yet.
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
				return nil, fmt.Errorf("updating the table %q the db model already holds in schema %q: %w", table.Name(), dbSchema.ID, errNotImplementedYet)
			}
		}
		return
	}
	if schema.Tables, err = updateTables(dbSchema.Tables); err != nil {
		return err
	}
	schema.Views, err = updateTables(dbSchema.Views)
	return err
}
