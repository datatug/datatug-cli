package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"database/sql/driver"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/mattn/go-sqlite3"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/sqlexecute"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSQLDriver struct{}

func (mockSQLDriver) Open(name string) (driver.Conn, error) { return nil, nil }

func init() {
	sql.Register("unsupported_driver", mockSQLDriver{})
}

type mockStore struct {
	storage.Store
	getProjectStoreFunc func(projectID string) datatug.ProjectStore
	createProjectFunc   func(ctx context.Context, req dto.CreateProjectRequest) (*datatug.ProjectSummary, error)
	getProjectsFunc     func(ctx context.Context) ([]datatug.ProjectBrief, error)
}

func (m mockStore) GetProjectStore(projectID string) datatug.ProjectStore {
	if m.getProjectStoreFunc != nil {
		return m.getProjectStoreFunc(projectID)
	}
	return nil
}

func (m mockStore) CreateProject(ctx context.Context, req dto.CreateProjectRequest) (*datatug.ProjectSummary, error) {
	if m.createProjectFunc != nil {
		return m.createProjectFunc(ctx, req)
	}
	return nil, nil
}

func (m mockStore) GetProjects(ctx context.Context) ([]datatug.ProjectBrief, error) {
	if m.getProjectsFunc != nil {
		return m.getProjectsFunc(ctx)
	}
	return nil, nil
}

type mockProjectStore struct {
	datatug.ProjectStore
	loadProjectFileFunc          func(ctx context.Context) (datatug.ProjectFile, error)
	loadProjectFunc              func(ctx context.Context, o ...datatug.StoreOption) (*datatug.Project, error)
	saveBoardFunc                func(ctx context.Context, board *datatug.Board) error
	loadBoardFunc                func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Board, error)
	deleteBoardFunc              func(ctx context.Context, id string) error
	dbServersStoreFunc           func(driver string) datatug.ProjDbServersStore
	loadEntityFunc               func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Entity, error)
	loadEntitiesFunc             func(ctx context.Context, o ...datatug.StoreOption) (datatug.Entities, error)
	deleteEntityFunc             func(ctx context.Context, id string) error
	saveEntityFunc               func(ctx context.Context, entity *datatug.Entity) error
	loadEnvironmentSummaryFunc   func(ctx context.Context, id string) (*datatug.EnvironmentSummary, error)
	saveFolderFunc               func(ctx context.Context, path string, folder *datatug.Folder) error
	deleteFolderFunc             func(ctx context.Context, id string) error
	loadRecordsetDefinitionsFunc func(ctx context.Context, o ...datatug.StoreOption) ([]*datatug.RecordsetDefinition, error)
	loadRecordsetDefinitionFunc  func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.RecordsetDefinition, error)
	loadEnvironmentFunc          func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error)
	loadEnvironmentsFunc         func(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error)
	loadEnvDbCatalogFunc         func(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error)
	loadEnvDbCatalogsFunc        func(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error)
	loadQueryFunc                func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.QueryDef, error)
	deleteQueryFunc              func(ctx context.Context, id string) error
	loadProjDbDriversFunc        func(ctx context.Context, o ...datatug.StoreOption) (datatug.ProjDbDrivers, error)
}

func (m mockProjectStore) LoadProjDbDrivers(ctx context.Context, o ...datatug.StoreOption) (datatug.ProjDbDrivers, error) {
	if m.loadProjDbDriversFunc != nil {
		return m.loadProjDbDriversFunc(ctx, o...)
	}
	return nil, nil
}

func (m mockProjectStore) LoadProjectFile(ctx context.Context) (datatug.ProjectFile, error) {
	if m.loadProjectFileFunc != nil {
		return m.loadProjectFileFunc(ctx)
	}
	return datatug.ProjectFile{}, nil
}

func (m mockProjectStore) LoadProject(ctx context.Context, o ...datatug.StoreOption) (*datatug.Project, error) {
	if m.loadProjectFunc != nil {
		return m.loadProjectFunc(ctx, o...)
	}
	return &datatug.Project{}, nil
}

func (m mockProjectStore) SaveBoard(ctx context.Context, board *datatug.Board) error {
	if m.saveBoardFunc != nil {
		return m.saveBoardFunc(ctx, board)
	}
	return nil
}

func (m mockProjectStore) LoadBoard(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Board, error) {
	if m.loadBoardFunc != nil {
		return m.loadBoardFunc(ctx, id, o...)
	}
	return nil, nil
}

func (m mockProjectStore) DeleteBoard(ctx context.Context, id string) error {
	if m.deleteBoardFunc != nil {
		return m.deleteBoardFunc(ctx, id)
	}
	return nil
}

func (m mockProjectStore) DbServersStore(driver string) datatug.ProjDbServersStore {
	if m.dbServersStoreFunc != nil {
		return m.dbServersStoreFunc(driver)
	}
	return mockDbServersStore{}
}

func (m mockProjectStore) LoadEntity(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Entity, error) {
	if m.loadEntityFunc != nil {
		return m.loadEntityFunc(ctx, id, o...)
	}
	return nil, nil
}

func (m mockProjectStore) LoadEntities(ctx context.Context, o ...datatug.StoreOption) (datatug.Entities, error) {
	if m.loadEntitiesFunc != nil {
		return m.loadEntitiesFunc(ctx, o...)
	}
	return nil, nil
}

func (m mockProjectStore) DeleteEntity(ctx context.Context, id string) error {
	if m.deleteEntityFunc != nil {
		return m.deleteEntityFunc(ctx, id)
	}
	return nil
}

func (m mockProjectStore) SaveEntity(ctx context.Context, entity *datatug.Entity) error {
	if m.saveEntityFunc != nil {
		return m.saveEntityFunc(ctx, entity)
	}
	return nil
}

func (m mockProjectStore) LoadEnvironmentSummary(ctx context.Context, id string) (*datatug.EnvironmentSummary, error) {
	if m.loadEnvironmentSummaryFunc != nil {
		return m.loadEnvironmentSummaryFunc(ctx, id)
	}
	return nil, nil
}

func (m mockProjectStore) SaveFolder(ctx context.Context, path string, folder *datatug.Folder) error {
	if m.saveFolderFunc != nil {
		return m.saveFolderFunc(ctx, path, folder)
	}
	return nil
}

func (m mockProjectStore) DeleteFolder(ctx context.Context, id string) error {
	if m.deleteFolderFunc != nil {
		return m.deleteFolderFunc(ctx, id)
	}
	return nil
}

func (m mockProjectStore) LoadRecordsetDefinitions(ctx context.Context, o ...datatug.StoreOption) ([]*datatug.RecordsetDefinition, error) {
	if m.loadRecordsetDefinitionsFunc != nil {
		return m.loadRecordsetDefinitionsFunc(ctx, o...)
	}
	return nil, nil
}

func (m mockProjectStore) LoadRecordsetDefinition(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.RecordsetDefinition, error) {
	if m.loadRecordsetDefinitionFunc != nil {
		return m.loadRecordsetDefinitionFunc(ctx, id, o...)
	}
	return nil, nil
}

func (m mockProjectStore) LoadEnvironment(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
	if m.loadEnvironmentFunc != nil {
		return m.loadEnvironmentFunc(ctx, id, o...)
	}
	return nil, nil
}

func (m mockProjectStore) LoadEnvironments(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error) {
	if m.loadEnvironmentsFunc != nil {
		return m.loadEnvironmentsFunc(ctx, o...)
	}
	return nil, nil
}

func (m mockProjectStore) LoadEnvDbCatalog(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
	if m.loadEnvDbCatalogFunc != nil {
		return m.loadEnvDbCatalogFunc(ctx, envID, serverID, dbID, o...)
	}
	return datatug.DbCatalog{}, nil
}

func (m mockProjectStore) LoadEnvDbCatalogs(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	if m.loadEnvDbCatalogsFunc != nil {
		return m.loadEnvDbCatalogsFunc(ctx, envID, o...)
	}
	return nil, nil
}

func (m mockProjectStore) LoadQuery(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.QueryDef, error) {
	if m.loadQueryFunc != nil {
		return m.loadQueryFunc(ctx, id, o...)
	}
	return nil, nil
}

func (m mockProjectStore) DeleteQuery(ctx context.Context, id string) error {
	if m.deleteQueryFunc != nil {
		return m.deleteQueryFunc(ctx, id)
	}
	return nil
}

type mockDbServersStore struct {
	datatug.ProjDbServersStore
	saveProjDbServerFunc   func(ctx context.Context, server *datatug.ProjDbServer, o ...datatug.StoreOption) error
	deleteProjDbServerFunc func(ctx context.Context, id string) error
	loadProjDbServerFunc   func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.ProjDbServer, error)
}

func (m mockDbServersStore) SaveProjDbServer(ctx context.Context, server *datatug.ProjDbServer, o ...datatug.StoreOption) error {
	if m.saveProjDbServerFunc != nil {
		return m.saveProjDbServerFunc(ctx, server, o...)
	}
	return nil
}

func (m mockDbServersStore) DeleteProjDbServer(ctx context.Context, id string) error {
	if m.deleteProjDbServerFunc != nil {
		return m.deleteProjDbServerFunc(ctx, id)
	}
	return nil
}

func (m mockDbServersStore) LoadProjDbServer(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.ProjDbServer, error) {
	if m.loadProjDbServerFunc != nil {
		return m.loadProjDbServerFunc(ctx, id, o...)
	}
	return nil, nil
}

func TestDatabaseAPI_GetServerDatabases(t *testing.T) {
	// 1. Validation error
	_, err := GetServerDatabases(dto.GetServerDatabasesRequest{})
	require.Error(t, err)

	// 2. Execution error via executeSingleSeam
	origSeam := executeSingleSeam
	defer func() { executeSingleSeam = origSeam }()

	executeSingleSeam = func(e sqlexecute.Executor, command sqlexecute.RequestCommand) (sqlexecute.Response, error) {
		return sqlexecute.Response{}, errors.New("exec error")
	}

	validReq := dto.GetServerDatabasesRequest{
		Project:   "proj1",
		ServerRef: datatug.ServerRef{Driver: "sqlserver", Host: "localhost"},
	}
	_, err = GetServerDatabases(validReq)
	require.Error(t, err)

	// 3. Success
	executeSingleSeam = func(e sqlexecute.Executor, command sqlexecute.RequestCommand) (sqlexecute.Response, error) {
		rs := datatug.Recordset{
			Rows: [][]interface{}{
				{"db1"},
				{"db2"},
			},
		}
		return sqlexecute.Response{
			Commands: []*sqlexecute.CommandResponse{
				{
					Items: []sqlexecute.CommandResponseItem{
						{Value: rs},
					},
				},
			},
		}, nil
	}

	dbs, err := GetServerDatabases(validReq)
	require.NoError(t, err)
	require.Len(t, dbs, 2)
	assert.Equal(t, "db1", dbs[0].ID)
	assert.Equal(t, "db2", dbs[1].ID)
}

func TestDbServerAPI(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	ctx := context.Background()
	ref := dto.ProjectRef{ProjectID: "proj1"}
	serverRef := datatug.ServerRef{Driver: "sqlserver", Host: "localhost"}
	projServer := datatug.ProjDbServer{Server: serverRef}

	// 1. Store error
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	assert.Error(t, AddDbServer(ctx, ref, projServer))
	assert.Error(t, UpdateDbServer(ctx, ref, projServer))
	assert.Error(t, DeleteDbServer(ctx, ref, serverRef))
	_, err := GetDbServerSummary(ctx, ref, serverRef)
	assert.Error(t, err)

	// 2. GetDbServerSummary invalid server ref
	_, err = GetDbServerSummary(ctx, ref, datatug.ServerRef{})
	assert.Error(t, err)

	// 3. Success paths
	saveCalled := false
	deleteCalled := false
	loadCalled := false

	ms := mockDbServersStore{
		saveProjDbServerFunc: func(ctx context.Context, server *datatug.ProjDbServer, o ...datatug.StoreOption) error {
			saveCalled = true
			return nil
		},
		deleteProjDbServerFunc: func(ctx context.Context, id string) error {
			deleteCalled = true
			return nil
		},
		loadProjDbServerFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.ProjDbServer, error) {
			loadCalled = true
			return &projServer, nil
		},
	}
	mps := mockProjectStore{
		dbServersStoreFunc: func(driver string) datatug.ProjDbServersStore {
			return ms
		},
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mps
			},
		}, nil
	}

	assert.NoError(t, AddDbServer(ctx, ref, projServer))
	assert.True(t, saveCalled)

	saveCalled = false
	assert.NoError(t, UpdateDbServer(ctx, ref, projServer))
	assert.True(t, saveCalled)

	assert.NoError(t, DeleteDbServer(ctx, ref, serverRef))
	assert.True(t, deleteCalled)

	sum, err := GetDbServerSummary(ctx, ref, serverRef)
	assert.NoError(t, err)
	assert.NotNil(t, sum)
	assert.True(t, loadCalled)
}

func TestEntityAPI(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	ctx := context.Background()

	// 1. validateEntityInput
	assert.Error(t, validateEntityInput("", "ent1"))
	assert.Error(t, validateEntityInput("proj1", ""))
	assert.NoError(t, validateEntityInput("proj1", "ent1"))

	// 2. Validation errors
	_, err := GetEntity(ctx, dto.ProjectItemRef{})
	assert.Error(t, err)
	_, err = GetAllEntities(ctx, dto.ProjectRef{})
	assert.Error(t, err)
	assert.Error(t, DeleteEntity(ctx, dto.ProjectItemRef{}))

	// 3. storeFor errors
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	ref := dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "proj1"}, ID: "ent1"}
	projRef := dto.ProjectRef{ProjectID: "proj1"}

	_, err = GetEntity(ctx, ref)
	assert.Error(t, err)
	_, err = GetAllEntities(ctx, projRef)
	assert.Error(t, err)
	assert.Error(t, DeleteEntity(ctx, ref))

	// 4. SaveEntity validations
	assert.Error(t, SaveEntity(ctx, dto.ProjectRef{}, &datatug.Entity{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "e"}}}))
	// invalid entity validation
	invalidEnt := &datatug.Entity{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: "inv"},
		},
		Fields: datatug.EntityFields{
			{ID: ""}, // triggers validation error
		},
	}
	assert.Error(t, SaveEntity(ctx, projRef, invalidEnt))
	// SaveEntity with store error
	validEnt := &datatug.Entity{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: "e1"},
		},
	}
	assert.Error(t, SaveEntity(ctx, projRef, validEnt))

	// 5. Success paths
	var loadedID, deletedID, savedID string
	mps := mockProjectStore{
		loadEntityFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Entity, error) {
			loadedID = id
			return validEnt, nil
		},
		loadEntitiesFunc: func(ctx context.Context, o ...datatug.StoreOption) (datatug.Entities, error) {
			return datatug.Entities{validEnt}, nil
		},
		deleteEntityFunc: func(ctx context.Context, id string) error {
			deletedID = id
			return nil
		},
		saveEntityFunc: func(ctx context.Context, entity *datatug.Entity) error {
			savedID = entity.ID
			return nil
		},
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mps
			},
		}, nil
	}

	ent, err := GetEntity(ctx, ref)
	assert.NoError(t, err)
	assert.Equal(t, "ent1", loadedID)
	assert.NotNil(t, ent)

	ents, err := GetAllEntities(ctx, projRef)
	assert.NoError(t, err)
	assert.Len(t, ents, 1)

	assert.NoError(t, DeleteEntity(ctx, ref))
	assert.Equal(t, "ent1", deletedID)

	// Save with entity.ID == "" -> takes Title
	entTitle := &datatug.Entity{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{Title: "title1"},
		},
	}
	assert.NoError(t, SaveEntity(ctx, projRef, entTitle))
	assert.Equal(t, "title1", savedID)

	// Save with entity.Title == entity.ID -> sets Title to ""
	entSame := &datatug.Entity{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: "same", Title: "same"},
		},
	}
	assert.NoError(t, SaveEntity(ctx, projRef, entSame))
	assert.Equal(t, "same", savedID)
	assert.Equal(t, "", entSame.Title)
}

func TestEnvironmentAPI(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	ctx := context.Background()

	// Missing projectID
	_, err := GetEnvironmentSummary(ctx, dto.ProjectItemRef{})
	assert.Error(t, err)

	// Missing ID
	_, err = GetEnvironmentSummary(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}})
	assert.Error(t, err)

	// Store error
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	ref := dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}, ID: "e1"}
	_, err = GetEnvironmentSummary(ctx, ref)
	assert.Error(t, err)

	// Success
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadEnvironmentSummaryFunc: func(ctx context.Context, id string) (*datatug.EnvironmentSummary, error) {
						return &datatug.EnvironmentSummary{}, nil
					},
				}
			},
		}, nil
	}
	res, err := GetEnvironmentSummary(ctx, ref)
	assert.NoError(t, err)
	assert.NotNil(t, res)
}

func TestFoldersAPI(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	ctx := context.Background()

	// CreateFolder validate error
	_, err := CreateFolder(ctx, dto.CreateFolder{})
	assert.Error(t, err)

	// DeleteFolder missing projectID
	assert.Error(t, DeleteFolder(ctx, dto.ProjectItemRef{}))

	// Store error
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	createReq := dto.CreateFolder{
		ProjectRef: dto.ProjectRef{ProjectID: "p1", StoreID: "store1"},
		Path:       "queries/f1",
		Name:       "f1",
	}
	_, err = CreateFolder(ctx, createReq)
	assert.Error(t, err)

	delRef := dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}, ID: "f1"}
	assert.Error(t, DeleteFolder(ctx, delRef))

	// Success
	var savedPath string
	var deletedID string
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					saveFolderFunc: func(ctx context.Context, path string, folder *datatug.Folder) error {
						savedPath = path
						return nil
					},
					deleteFolderFunc: func(ctx context.Context, id string) error {
						deletedID = id
						return nil
					},
				}
			},
		}, nil
	}

	f, err := CreateFolder(ctx, createReq)
	assert.NoError(t, err)
	assert.NotNil(t, f)
	assert.Equal(t, "queries/f1", savedPath)

	assert.NoError(t, DeleteFolder(ctx, delRef))
	assert.Equal(t, "f1", deletedID)
}

func TestBoardAPI(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	ctx := context.Background()
	ref := dto.ProjectRef{ProjectID: "p1"}
	itemRef := dto.ProjectItemRef{ProjectRef: ref, ID: "b1"}
	board := datatug.Board{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "b1"}}}

	// Store error
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err := CreateBoard(ctx, ref, board)
	assert.Error(t, err)
	_, err = GetBoard(ctx, itemRef)
	assert.Error(t, err)
	assert.Error(t, DeleteBoard(ctx, itemRef))
	_, err = SaveBoard(ctx, ref, board)
	assert.Error(t, err)

	// Success
	var loadedID, deletedID string
	var savedBoard *datatug.Board
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					saveBoardFunc: func(ctx context.Context, b *datatug.Board) error {
						savedBoard = b
						return nil
					},
					loadBoardFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Board, error) {
						loadedID = id
						return &board, nil
					},
					deleteBoardFunc: func(ctx context.Context, id string) error {
						deletedID = id
						return nil
					},
				}
			},
		}, nil
	}

	b, err := CreateBoard(ctx, ref, board)
	assert.NoError(t, err)
	assert.NotNil(t, b)
	assert.Equal(t, "b1", savedBoard.ID)

	b, err = GetBoard(ctx, itemRef)
	assert.NoError(t, err)
	assert.Equal(t, "b1", loadedID)

	assert.NoError(t, DeleteBoard(ctx, itemRef))
	assert.Equal(t, "b1", deletedID)

	b, err = SaveBoard(ctx, ref, board)
	assert.NoError(t, err)
	assert.NotNil(t, b)
}

func TestProjectAPI(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	ctx := context.Background()

	// validateProjectInput
	assert.Error(t, validateProjectInput(""))
	assert.NoError(t, validateProjectInput("p1"))

	// GetProjects store error and success
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err := GetProjects(ctx, "store1")
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectsFunc: func(ctx context.Context) ([]datatug.ProjectBrief, error) {
				return []datatug.ProjectBrief{{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "p1"}}}}, nil
			},
		}, nil
	}
	projects, err := GetProjects(ctx, "store1")
	assert.NoError(t, err)
	assert.Len(t, projects, 1)

	// GetProjectSummary
	_, err = GetProjectSummary(ctx, dto.ProjectRef{})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = GetProjectSummary(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
						return datatug.ProjectFile{}, errors.New("file err")
					},
				}
			},
		}, nil
	}
	_, err = GetProjectSummary(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
						return datatug.ProjectFile{}, nil
					},
				}
			},
		}, nil
	}
	summary, err := GetProjectSummary(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.NoError(t, err)
	assert.NotNil(t, summary)

	// CreateProject
	_, err = CreateProject(ctx, dto.CreateProjectRequest{})
	assert.Error(t, err)

	validCreate := dto.CreateProjectRequest{
		StoreID: "store1",
		ID:      "proj_valid",
		Title:   "Valid Proj",
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = CreateProject(ctx, validCreate)
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, nil // store == nil
	}
	_, err = CreateProject(ctx, validCreate)
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			createProjectFunc: func(ctx context.Context, req dto.CreateProjectRequest) (*datatug.ProjectSummary, error) {
				return &datatug.ProjectSummary{}, nil
			},
		}, nil
	}
	created, err := CreateProject(ctx, validCreate)
	assert.NoError(t, err)
	assert.NotNil(t, created)

	// GetProjectFull
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = GetProjectFull(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadProjectFunc: func(ctx context.Context, o ...datatug.StoreOption) (*datatug.Project, error) {
						return &datatug.Project{}, nil
					},
				}
			},
		}, nil
	}
	full, err := GetProjectFull(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.NoError(t, err)
	assert.NotNil(t, full)
}

func TestRecordsetAPI(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	ctx := context.Background()

	// 1. RecordsetRequestParams.Validate
	assert.Error(t, RecordsetRequestParams{}.Validate())
	assert.Error(t, RecordsetRequestParams{Project: "p"}.Validate())
	assert.NoError(t, RecordsetRequestParams{Project: "p", Recordset: "r"}.Validate())

	// 2. RecordsetDataRequestParams.Validate
	assert.Error(t, RecordsetDataRequestParams{}.Validate())
	assert.Error(t, RecordsetDataRequestParams{RecordsetRequestParams: RecordsetRequestParams{Project: "p", Recordset: "r"}}.Validate())
	assert.NoError(t, RecordsetDataRequestParams{RecordsetRequestParams: RecordsetRequestParams{Project: "p", Recordset: "r"}, Data: "d"}.Validate())

	// 3. RowWithIndex.Validate
	assert.Error(t, RowWithIndex{Index: -1}.Validate())
	assert.NoError(t, RowWithIndex{Index: 0}.Validate())

	// 4. AddRowsToRecordset
	_, err := AddRowsToRecordset(RecordsetDataRequestParams{}, nil)
	assert.Error(t, err)
	validParams := RecordsetDataRequestParams{RecordsetRequestParams: RecordsetRequestParams{Project: "p", Recordset: "r"}, Data: "d"}
	_, err = AddRowsToRecordset(validParams, nil)
	assert.ErrorIs(t, err, errNotImplementedYet)

	// 5. RemoveRowsFromRecordset
	_, err = RemoveRowsFromRecordset(RecordsetDataRequestParams{}, nil)
	assert.Error(t, err)
	_, err = RemoveRowsFromRecordset(validParams, []RowWithIndex{{Index: -1}})
	assert.Error(t, err)
	_, err = RemoveRowsFromRecordset(validParams, []RowWithIndex{{Index: 1}})
	assert.ErrorIs(t, err, errNotImplementedYet)

	// 6. UpdateRowsInRecordset
	_, err = UpdateRowsInRecordset(RecordsetDataRequestParams{}, nil)
	assert.Error(t, err)
	_, err = UpdateRowsInRecordset(validParams, []RowWithIndexAndNewValues{{RowWithIndex: RowWithIndex{Index: -1}}})
	assert.Error(t, err)
	_, err = UpdateRowsInRecordset(validParams, []RowWithIndexAndNewValues{{RowWithIndex: RowWithIndex{Index: 1}}})
	assert.ErrorIs(t, err, errNotImplementedYet)

	// 7. GetRecordset panics
	assert.Panics(t, func() {
		_, _ = GetRecordset(ctx, dto.ProjectItemRef{})
	})

	// 8. GetDatasetDefinition
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = GetDatasetDefinition(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}, ID: "d1"})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadRecordsetDefinitionFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.RecordsetDefinition, error) {
						return &datatug.RecordsetDefinition{}, nil
					},
				}
			},
		}, nil
	}
	def, err := GetDatasetDefinition(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}, ID: "d1"})
	assert.NoError(t, err)
	assert.NotNil(t, def)

	// 9. GetRecordsetsSummary
	_, err = GetRecordsetsSummary(ctx, dto.ProjectRef{})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = GetRecordsetsSummary(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadRecordsetDefinitionsFunc: func(ctx context.Context, o ...datatug.StoreOption) ([]*datatug.RecordsetDefinition, error) {
						return nil, errors.New("load defs err")
					},
				}
			},
		}, nil
	}
	_, err = GetRecordsetsSummary(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadRecordsetDefinitionsFunc: func(ctx context.Context, o ...datatug.StoreOption) ([]*datatug.RecordsetDefinition, error) {
						return []*datatug.RecordsetDefinition{
							{
								ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "root_ds", Title: "Root"}},
								Columns:     datatug.RecordsetColumnDefs{{Name: "c1"}},
							},
							{
								ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "sub/ds1", Title: "Sub 1"}},
								Columns:     datatug.RecordsetColumnDefs{{Name: "c2"}},
							},
							{
								ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "sub/ds2", Title: "Sub 2"}},
							},
						}, nil
					},
				}
			},
		}, nil
	}
	summary, err := GetRecordsetsSummary(ctx, dto.ProjectRef{ProjectID: "p1"})
	assert.NoError(t, err)
	assert.NotNil(t, summary)
	assert.Equal(t, "/", summary.ID)

	// 10. getRecordsetFolder empty paths
	assert.Equal(t, summary, getRecordsetFolder(summary, nil))
}

func TestSecureSession(t *testing.T) {
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()

	// Save and restore secure state
	secureMu.Lock()
	savedContextID := securityContextID
	savedSession := secureSession
	savedDirs := projectDirs
	savedExec := secureExecutor
	savedCaps := capabilities
	secureMu.Unlock()

	defer func() {
		secureMu.Lock()
		securityContextID = savedContextID
		secureSession = savedSession
		projectDirs = savedDirs
		secureExecutor = savedExec
		capabilities = savedCaps
		secureMu.Unlock()
	}()

	// ValidateSecurityContext when empty
	secureMu.Lock()
	securityContextID = ""
	secureSession = secureread.Session{}
	projectDirs = nil
	secureExecutor = nil
	capabilities = Capabilities{}
	secureMu.Unlock()

	assert.False(t, ValidateSecurityContext("any"))
	assert.Equal(t, "", SecurityContextID())
	_, ok := SecureExecutor()
	assert.False(t, ok)
	assert.Equal(t, "", SecurePrincipalID())
	r, g := SecurePrincipalRolesGroups()
	assert.Empty(t, r)
	assert.Empty(t, g)
	assert.Empty(t, SecureConfiguredProjectIDs())
	assert.False(t, SecureSessionUnrestricted())

	// Configure
	session, err := secureread.NewSession(secureread.SessionOptions{
		NoPolicies: true,
		As:         "user1",
		Roles:      []string{"admin"},
		Groups:     []string{"devs"},
	})
	require.NoError(t, err)
	caps := Capabilities{
		SnapshotPolicies: map[string]SnapshotProjectPolicy{
			"p1": {
				Sources: map[string]SnapshotSourcePolicy{
					"s1": {Allow: true, MaskedColumns: []string{"secret"}},
					"s2": {Allow: false},
				},
			},
		},
	}
	ConfigureSecureSession(session, map[string]string{"p1": "/tmp/p1"}, caps)

	assert.True(t, ValidateSecurityContext(SecurityContextID()))
	assert.False(t, ValidateSecurityContext("wrong"))
	_, ok = SecureExecutor()
	assert.True(t, ok)
	assert.Equal(t, "user1", SecurePrincipalID())
	r, g = SecurePrincipalRolesGroups()
	assert.Equal(t, []string{"admin"}, r)
	assert.Equal(t, []string{"devs"}, g)
	assert.Equal(t, []string{"p1"}, SecureConfiguredProjectIDs())
	assert.True(t, SecureSessionUnrestricted())
	assert.Equal(t, caps, GetCapabilities())

	// SecurePrincipalID with non-string ID
	secureMu.Lock()
	secureSession.Principal = &access.Principal{ID: 12345}
	secureMu.Unlock()
	assert.Equal(t, "12345", SecurePrincipalID())

	// SecurePrincipalRolesGroups with nil slices
	secureMu.Lock()
	secureSession.Principal = &access.Principal{}
	secureMu.Unlock()
	r, g = SecurePrincipalRolesGroups()
	assert.NotNil(t, r)
	assert.NotNil(t, g)

	// SnapshotPolicy tests
	pol, ok := SnapshotPolicy("p1", "s1")
	assert.True(t, ok)
	assert.True(t, pol.Allow)
	assert.Equal(t, []string{"secret"}, pol.MaskedColumns)

	_, ok = SnapshotPolicy("p1", "s2") // allow == false
	assert.False(t, ok)

	_, ok = SnapshotPolicy("p1", "unknown_source")
	assert.False(t, ok)

	_, ok = SnapshotPolicy("unknown_proj", "s1")
	assert.False(t, ok)

	// ProjectStoreFor
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = ProjectStoreFor("p1")
	assert.Error(t, err)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{}
			},
		}, nil
	}
	ps, err := ProjectStoreFor("p1")
	assert.NoError(t, err)
	assert.NotNil(t, ps)
}

func TestExecutionEvidence(t *testing.T) {
	// Close any previous
	_ = CloseExecutionEvidence()

	// Manager is nil: all lookups fail
	_, err := ExecutionEvidenceStore("p1", nil)
	assert.Error(t, err)

	_, err = ExecutionEvidenceStoreByID("p1", "s1")
	assert.Error(t, err)

	_, err = IncidentStoreByID("p1", "s1")
	assert.Error(t, err)

	assert.NoError(t, CloseExecutionEvidence())

	// Configure with invalid routing (e.g. invalid store config)
	badConfig := []incidentstore.ConfiguredStore{
		{StoreID: "s1", Kind: incidents.StoreLocationDedicatedRepository}, // missing repository
	}
	err = ConfigureExecutionEvidence(map[string]string{"p1": t.TempDir()}, badConfig, executionstore.Options{})
	assert.Error(t, err)

	// Configure valid
	tmpDir := t.TempDir()
	validConfig := []incidentstore.ConfiguredStore{
		{StoreID: "s1", Kind: incidents.StoreLocationDedicatedRepository, Repository: tmpDir},
	}
	err = ConfigureExecutionEvidence(map[string]string{"p1": tmpDir}, validConfig, executionstore.Options{})
	require.NoError(t, err)

	// Lookups when manager is non-nil
	store, err := ExecutionEvidenceStore("p1", nil)
	assert.NoError(t, err)
	assert.NotNil(t, store)

	store, err = ExecutionEvidenceStoreByID("p1", "s1")
	assert.NoError(t, err)
	assert.NotNil(t, store)

	incStore, err := IncidentStoreByID("p1", "s1")
	assert.NoError(t, err)
	assert.NotNil(t, incStore)

	// Reconfigure to exercise previous != nil
	err = ConfigureExecutionEvidence(map[string]string{"p1": tmpDir}, validConfig, executionstore.Options{})
	require.NoError(t, err)

	assert.NoError(t, CloseExecutionEvidence())
}

func TestExecuteCommandsAndSelect(t *testing.T) {
	ctx := context.Background()

	// 1. Validation errors
	assert.Error(t, ExecuteCommandsRequest{}.Validate())
	assert.Error(t, ExecuteCommandsRequest{Project: "p"}.Validate())
	assert.Error(t, ExecuteCommandsRequest{Project: "p", Commands: []ExecuteCommandRequest{{}}}.Validate())

	assert.Error(t, ExecuteCommandRequest{}.Validate())
	assert.Error(t, ExecuteCommandRequest{Type: "OTHER"}.Validate())
	assert.Error(t, ExecuteCommandRequest{Type: "SQL"}.Validate())
	assert.Error(t, ExecuteCommandRequest{Type: "SQL", Text: "SELECT 1"}.Validate())
	assert.Error(t, ExecuteCommandRequest{Type: "SQL", Text: "SELECT 1", Env: "e"}.Validate())
	assert.NoError(t, ExecuteCommandRequest{Type: "SQL", Text: "SELECT 1", Env: "e", DB: "d"}.Validate())

	assert.Error(t, SelectRequest{}.Validate())
	assert.Error(t, SelectRequest{SQL: "SELECT 1", From: "t"}.Validate())
	assert.Error(t, SelectRequest{SQL: "SELECT 1", Where: "a:1"}.Validate())
	assert.Error(t, SelectRequest{SQL: "SELECT 1", Columns: []string{"c"}}.Validate())
	assert.Error(t, SelectRequest{SQL: "SELECT 1"}.Validate())
	assert.Error(t, SelectRequest{SQL: "SELECT 1", Project: "p"}.Validate())
	assert.Error(t, SelectRequest{SQL: "SELECT 1", Project: "p", Environment: "e"}.Validate())
	assert.Error(t, SelectRequest{From: "t", Project: "p", Environment: "e", Database: "d", Where: "invalid"}.Validate())
	assert.Error(t, SelectRequest{From: "t", Project: "p", Environment: "e", Database: "d", Limit: -2}.Validate())
	assert.NoError(t, SelectRequest{From: "t", Project: "p", Environment: "e", Database: "d", Where: "id:1", Limit: 5}.Validate())

	// 2. SecureExecutor not configured
	secureMu.Lock()
	savedExec := secureExecutor
	secureExecutor = nil
	secureMu.Unlock()
	defer func() {
		secureMu.Lock()
		secureExecutor = savedExec
		secureMu.Unlock()
	}()

	validCmdReq := ExecuteCommandsRequest{
		Project: "p1",
		Commands: []ExecuteCommandRequest{
			{Type: "SQL", Text: "SELECT 1", Env: "dev", DB: "db1"},
		},
	}
	_, err := ExecuteCommands(ctx, "store1", validCmdReq)
	assert.Error(t, err)

	validSelReq := SelectRequest{
		Project: "p1", Environment: "dev", Database: "db1",
		SQL: "SELECT 1",
	}
	_, err = ExecuteSelect(ctx, "store1", validSelReq)
	assert.Error(t, err)

	// Configure real executor and sqlite DB
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE items (id INT, name TEXT); INSERT INTO items VALUES (1, 'apple');")
	require.NoError(t, err)
	_ = db.Close()

	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, map[string]string{"p1": tmpDir}, Capabilities{})

	// Store error
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = ExecuteCommands(ctx, "store1", validCmdReq)
	assert.Error(t, err)
	_, err = ExecuteSelect(ctx, "store1", validSelReq)
	assert.Error(t, err)

	// NamedParams error in ExecuteCommands
	mps := mockProjectStore{
		loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
			return &datatug.Environment{
				DbServers: []*datatug.EnvDbServer{
					{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
				},
			}, nil
		},
		loadEnvDbCatalogFunc: func(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
			return datatug.DbCatalog{
				ID:     "db1",
				Driver: "sqlite3",
				Path:   dbPath,
			}, nil
		},
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mps
			},
		}, nil
	}

	namedParamReq := ExecuteCommandsRequest{
		Project: "p1",
		Commands: []ExecuteCommandRequest{
			{Type: "SQL", Text: "SELECT 1", Env: "dev", DB: "db1", NamedParams: map[string]any{"x": 1}},
		},
	}
	_, err = ExecuteCommands(ctx, "store1", namedParamReq)
	assert.Error(t, err)

	// resolveSourceURL error
	resolveErrReq := ExecuteCommandsRequest{
		Project: "p1",
		Commands: []ExecuteCommandRequest{
			{Type: "SQL", Text: "SELECT 1", Env: "bad_env", DB: "db1"},
		},
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
						return nil, errors.New("env not found")
					},
				}
			},
		}, nil
	}
	_, err = ExecuteCommands(ctx, "store1", resolveErrReq)
	assert.Error(t, err)

	_, err = ExecuteSelect(ctx, "store1", validSelReq)
	assert.Error(t, err)

	// Successful execution of ExecuteCommands (with ID and without ID)
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mps
			},
		}, nil
	}
	cmdResp, err := ExecuteCommands(ctx, "store1", validCmdReq)
	require.NoError(t, err)
	assert.Len(t, cmdResp.Commands, 1)

	cmdReqWithID := ExecuteCommandsRequest{
		ID:      "batch1",
		Project: "p1",
		Commands: []ExecuteCommandRequest{
			{ID: "c1", Type: "SQL", Text: "SELECT 1", Env: "dev", DB: "db1"},
		},
	}
	cmdResp, err = ExecuteCommands(ctx, "store1", cmdReqWithID)
	require.NoError(t, err)
	assert.Equal(t, "c1", cmdResp.Commands[0].CommandID)

	// Execution error in ExecuteCommands
	badSQLReq := ExecuteCommandsRequest{
		Project: "p1",
		Commands: []ExecuteCommandRequest{
			{Type: "SQL", Text: "SELECT * FROM nonexistent_table", Env: "dev", DB: "db1"},
		},
	}
	_, err = ExecuteCommands(ctx, "store1", badSQLReq)
	assert.Error(t, err)

	// Successful ExecuteSelect with SQL
	selResp, err := ExecuteSelect(ctx, "store1", validSelReq)
	require.NoError(t, err)
	assert.NotEmpty(t, selResp.Columns)

	// ExecuteSelect with SQL error
	badSelReq := SelectRequest{
		Project: "p1", Environment: "dev", Database: "db1",
		SQL: "SELECT * FROM nonexistent_table",
	}
	_, err = ExecuteSelect(ctx, "store1", badSelReq)
	assert.Error(t, err)

	// ExecuteSelect with From
	fromSelReq := SelectRequest{
		Project: "p1", Environment: "dev", Database: "db1",
		From:    "items",
		Where:   "name:apple",
		Limit:   10,
		Columns: []string{"id", "name"},
	}
	selResp, err = ExecuteSelect(ctx, "store1", fromSelReq)
	require.NoError(t, err)
	assert.NotEmpty(t, selResp.Columns)

	// ExecuteSelect with From invalid where
	badWhereReq := SelectRequest{
		Project: "p1", Environment: "dev", Database: "db1",
		From:    "items",
		Where:   ":value",
	}
	_, err = ExecuteSelect(ctx, "store1", badWhereReq)
	assert.Error(t, err)
}

func TestIncidentsAPI(t *testing.T) {
	ctx := context.Background()

	// 1. ResolveIncidentProject
	_, err := ResolveIncidentProject("unserved_p", "dev")
	assert.Error(t, err)

	secureMu.Lock()
	savedDirs := projectDirs
	projectDirs = map[string]string{"p1": t.TempDir()}
	secureMu.Unlock()
	defer func() {
		secureMu.Lock()
		projectDirs = savedDirs
		secureMu.Unlock()
	}()

	_, err = ResolveIncidentProject("p1", "")
	assert.Error(t, err)

	ref, err := ResolveIncidentProject("p1", "prod")
	require.NoError(t, err)
	assert.Equal(t, "p1", ref.ProjectID)

	// 2. SecureIncidentActor
	secureMu.Lock()
	savedCtxID := securityContextID
	savedSess := secureSession
	securityContextID = ""
	secureSession = secureread.Session{}
	secureMu.Unlock()
	defer func() {
		secureMu.Lock()
		securityContextID = savedCtxID
		secureSession = savedSess
		secureMu.Unlock()
	}()

	_, err = SecureIncidentActor(incidents.ActorViaAPI)
	assert.Error(t, err)

	secureMu.Lock()
	securityContextID = "ctx1"
	secureSession = secureread.Session{}
	secureMu.Unlock()

	_, err = SecureIncidentActor(incidents.ActorViaAPI)
	assert.Error(t, err)

	secureMu.Lock()
	secureSession = secureread.Session{Principal: &access.Principal{}}
	secureMu.Unlock()
	_, err = SecureIncidentActor(incidents.ActorViaAPI)
	assert.Error(t, err)

	secureMu.Lock()
	secureSession = secureread.Session{Principal: &access.Principal{ID: "alice"}}
	secureMu.Unlock()
	_, err = SecureIncidentActor("invalid_via")
	assert.Error(t, err)

	actor, err := SecureIncidentActor(incidents.ActorViaAPI)
	require.NoError(t, err)
	assert.Equal(t, "alice", actor.ID)

	// 3. IncidentView when not configured
	secureMu.Lock()
	securityContextID = ""
	secureMu.Unlock()
	_, _, err = IncidentView(ctx, incidents.Incident{}, nil)
	assert.Error(t, err)

	// 4. IncidentView and incidentViewPolicy when configured
	secureMu.Lock()
	securityContextID = "ctx1"
	secureSession = secureread.Session{
		Unrestricted: true,
		Principal:    &access.Principal{ID: "alice"},
	}
	secureMu.Unlock()

	visibleScope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "p1", Environment: "prod"}
	badScope := investigation.ProjectScope{StoreID: "wrong_store", ProjectID: "p1", Environment: "prod"}
	unservedScope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "unserved", Environment: "prod"}

	facts := []investigation.Fact{
		{ID: "f1", Entity: "User", Field: "Email", Value: investigation.NewStringValue("a@b.com"), Enabled: true, Scope: &visibleScope},
		{ID: "f2", Entity: "User", Field: "Email", Value: investigation.NewStringValue("c@d.com"), Enabled: true, Scope: &badScope},
		{ID: "f3", Entity: "User", Field: "Email", Value: investigation.NewStringValue("e@f.com"), Enabled: true, Scope: &unservedScope},
		{ID: "f4", Entity: "User", Field: "Email", Value: investigation.NewStringValue("g@h.com"), Enabled: true, Scope: nil},
		{ID: "f5", Entity: "User", Field: "Email", Value: investigation.NewStringValue("i@j.com"), Enabled: true, Scope: &visibleScope, Physical: &investigation.PhysicalRef{Collection: "users", Column: "email"}},
	}
	incRef := incidents.IncidentRef{StoreID: "s1", IncidentID: "INC-1"}
	mergeTarget := incidents.IncidentRef{StoreID: "s1", IncidentID: "INC-2"}
	stored := incidents.Incident{
		Ref:              incRef,
		MergedInto:       &mergeTarget,
		CanonicalContext: incidents.CanonicalContext{Facts: facts},
	}

	goodCreatedPayload, _ := json.Marshal(incidents.CreatedPayload{
		CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{facts[0]}},
	})
	goodFactPayload, _ := json.Marshal(incidents.ContextFactAddedPayload{
		Fact: facts[4],
	})

	events := []incidents.Event{
		{ID: "e_created_bad", Type: incidents.EventIncidentCreated, Payload: []byte("{invalid")},
		{ID: "e_created_ok", Type: incidents.EventIncidentCreated, Payload: goodCreatedPayload},
		{ID: "e_fact_bad", Type: incidents.EventContextFactAdded, Payload: []byte("{invalid")},
		{ID: "e_fact_ok", Type: incidents.EventContextFactAdded, Payload: goodFactPayload},
		{
			ID:   "e_refs",
			Type: incidents.EventIncidentMerged,
			Refs: []incidents.ArtifactRef{
				{Kind: incidents.RefFact, Artifact: &incidents.ProjectArtifactRef{StoreID: LocalStoreID, ProjectID: "p1", Environment: "prod"}},
				{Kind: incidents.RefCheck, Artifact: &incidents.ProjectArtifactRef{StoreID: LocalStoreID, ProjectID: "p1", Environment: "prod"}},
				{Kind: incidents.RefCheck, Project: &visibleScope},
				{Kind: incidents.RefCheck, Execution: &incidents.ExecutionRef{ProjectID: "p1"}},
				{Kind: incidents.RefCheck, Comparison: &incidents.ComparisonRef{Left: incidents.ExecutionRef{ProjectID: "p1"}, Right: incidents.ExecutionRef{ProjectID: "p1"}}},
				{Kind: incidents.RefIncident, Incident: &mergeTarget},
				{ID: "custom_id"},
			},
			Incident: incRef,
		},
		{ID: "e_note", Type: incidents.EventNoteAdded},
	}

	view, pol, err := IncidentView(ctx, stored, events)
	require.NoError(t, err)
	assert.NotNil(t, view)
	assert.True(t, pol.WithheldEvents["e_created_bad"])
	assert.True(t, pol.WithheldEvents["e_fact_bad"])
}

func TestSourceResolverAPI(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. sourceURLFromCatalog
	_, err := sourceURLFromCatalog(datatug.DbCatalog{Driver: "openvaultdb", Path: ""}, tmpDir)
	assert.Error(t, err)
	ovURL, err := sourceURLFromCatalog(datatug.DbCatalog{Driver: "openvaultdb", Path: "/a/b.json"}, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "openvaultdb:///a/b.json", ovURL)

	_, err = sourceURLFromCatalog(datatug.DbCatalog{Driver: "sqlite3", Path: ""}, tmpDir)
	assert.Error(t, err)
	sqlURL, err := sourceURLFromCatalog(datatug.DbCatalog{Driver: "sqlite3", Path: "/a/b.db"}, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "sqlite:///a/b.db", sqlURL)

	_, err = sourceURLFromCatalog(datatug.DbCatalog{Driver: "ingitdb", Path: ""}, tmpDir)
	assert.Error(t, err)
	ingURL, err := sourceURLFromCatalog(datatug.DbCatalog{Driver: "ingitdb", Path: "/a/b"}, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "ingitdb:///a/b", ingURL)

	_, err = sourceURLFromCatalog(datatug.DbCatalog{Driver: "oracle", Path: "/a"}, tmpDir)
	assert.Error(t, err)

	// 2. resolveSourceURL
	mps := mockProjectStore{
		loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
			return nil, errors.New("env load err")
		},
	}
	_, _, err = resolveSourceURL(ctx, mps, "dev", "db1", tmpDir)
	assert.Error(t, err)

	mps = mockProjectStore{
		loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
			return &datatug.Environment{DbServers: []*datatug.EnvDbServer{nil}}, nil
		},
	}
	_, _, err = resolveSourceURL(ctx, mps, "dev", "db1", tmpDir)
	assert.Error(t, err)

	mps = mockProjectStore{
		loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
			return &datatug.Environment{
				DbServers: []*datatug.EnvDbServer{
					{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
				},
			}, nil
		},
		loadEnvDbCatalogFunc: func(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
			return datatug.DbCatalog{}, errors.New("cat not found")
		},
	}
	_, _, err = resolveSourceURL(ctx, mps, "dev", "db1", tmpDir)
	assert.Error(t, err)

	mps = mockProjectStore{
		loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
			return &datatug.Environment{
				DbServers: []*datatug.EnvDbServer{
					{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
				},
			}, nil
		},
		loadEnvDbCatalogFunc: func(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
			return datatug.DbCatalog{ID: "db1", Driver: "sqlite3", Path: "/a/b.db"}, nil
		},
	}
	u, d, err := resolveSourceURL(ctx, mps, "dev", "db1", tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "sqlite:///a/b.db", u)
	assert.Equal(t, "sqlite3", d)

	// 3. LoadQueryDocument
	_, err = LoadQueryDocument("unknown_proj", "q1", datatug.QueryTypeSQL)
	assert.Error(t, err)

	secureMu.Lock()
	savedDirs := projectDirs
	projectDirs = map[string]string{"p1": tmpDir}
	secureMu.Unlock()
	defer func() {
		secureMu.Lock()
		projectDirs = savedDirs
		secureMu.Unlock()
	}()

	_, err = LoadQueryDocument("p1", "q1", datatug.QueryTypeSQL)
	assert.Error(t, err)

	qDir := filepath.Join(tmpDir, storage.QueriesFolder, "sales")
	require.NoError(t, os.MkdirAll(qDir, 0755))
	qFile := filepath.Join(qDir, "top_customers.query.sql")
	require.NoError(t, os.WriteFile(qFile, []byte("SELECT * FROM customers"), 0644))

	doc, err := LoadQueryDocument("p1", "sales/top_customers", datatug.QueryTypeSQL)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM customers", doc)

	// 4. queryFolderAndID
	f, id := queryFolderAndID("q1")
	assert.Equal(t, "", f)
	assert.Equal(t, "q1", id)

	f, id = queryFolderAndID("a/b/c")
	assert.Equal(t, filepath.Join("a", "b"), f)
	assert.Equal(t, "c", id)
}

func TestSourceWarningsAPI(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. ProjectStoreFor error
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	WarnMissingSourceFiles(ctx, map[string]string{"p1": tmpDir})

	// 2. LoadEnvironments error
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadEnvironmentsFunc: func(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error) {
						return nil, errors.New("load envs err")
					},
				}
			},
		}, nil
	}
	WarnMissingSourceFiles(ctx, map[string]string{"p1": tmpDir})

	// 3. warnMissingSourceFilesForEnvironment with env == nil
	warnMissingSourceFilesForEnvironment(ctx, mockProjectStore{}, "p1", tmpDir, nil)

	// 4. warnMissingSourceFilesForEnvironment with ListSources error
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadEnvironmentsFunc: func(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error) {
						return datatug.Environments{
							{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}}},
						}, nil
					},
					loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
						return nil, errors.New("env err")
					},
				}
			},
		}, nil
	}
	WarnMissingSourceFiles(ctx, map[string]string{"p1": tmpDir})

	// 5. warnMissingSourceFilesForEnvironment with missing file warning
	dbMissing := filepath.Join(tmpDir, "missing.db")
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadEnvironmentsFunc: func(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error) {
						return datatug.Environments{
							{
								ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}},
								DbServers: []*datatug.EnvDbServer{
									{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
								},
							},
						}, nil
					},
					loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
						return &datatug.Environment{
							ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}},
							DbServers: []*datatug.EnvDbServer{
								{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
							},
						}, nil
					},
					loadEnvDbCatalogFunc: func(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
						return datatug.DbCatalog{
							ID:     "c1",
							Driver: "sqlite3",
							Path:   dbMissing,
						}, nil
					},
				}
			},
		}, nil
	}
	WarnMissingSourceFiles(ctx, map[string]string{"p1": tmpDir})
}

func TestResolverAPI(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, storage.QueriesFolder), 0755))

	// 1. ResolveSource
	_, err := ResolveSource(ctx, mockProjectStore{}, tmpDir, "dev", "")
	assert.Error(t, err)

	mpsErr := mockProjectStore{
		loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
			return nil, errors.New("env err")
		},
	}
	_, err = ResolveSource(ctx, mpsErr, tmpDir, "dev", "s1")
	assert.Error(t, err)

	mpsOk := mockProjectStore{
		loadEnvironmentFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
			return &datatug.Environment{
				DbServers: []*datatug.EnvDbServer{
					{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
				},
			}, nil
		},
		loadEnvDbCatalogFunc: func(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
			return datatug.DbCatalog{ID: "db1", Driver: "sqlite3", Path: "/a/b.db"}, nil
		},
		loadEnvDbCatalogsFunc: func(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return datatug.DbCatalogs{
				&datatug.DbCatalog{ID: "db1", Driver: "sqlite3", Path: "/a/b.db"},
			}, nil
		},
	}
	_, err = ResolveSource(ctx, mpsOk, tmpDir, "dev", "unknown_source")
	assert.ErrorIs(t, err, ErrSourceUnavailable)

	res, err := ResolveSource(ctx, mpsOk, tmpDir, "dev", "db1")
	require.NoError(t, err)
	assert.Equal(t, "db1", res.ID)

	// 2. EligibleTargets
	httpQuery := &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "http1"}},
		Type:        datatug.QueryTypeHTTP,
	}
	hDir := filepath.Join(tmpDir, storage.QueriesFolder)
	require.NoError(t, os.MkdirAll(hDir, 0755))
	hDef := datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "http1", Title: "HTTP 1"}},
		Type:        datatug.QueryTypeHTTP,
	}
	hBytes, _ := json.Marshal(hDef)
	require.NoError(t, os.WriteFile(filepath.Join(hDir, "http1.query.json"), hBytes, 0644))

	targets, err := EligibleTargets(ctx, mpsOk, tmpDir, "dev", httpQuery)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "http1", targets[0].ID)

	httpQuery2 := &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "http_not_found"}},
		Type:        datatug.QueryTypeHTTP,
	}
	targets, err = EligibleTargets(ctx, mpsOk, tmpDir, "dev", httpQuery2)
	require.NoError(t, err)
	assert.Nil(t, targets)

	sqlQuery := &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "sql1"}},
		Type:        datatug.QueryTypeSQL,
	}
	targets, err = EligibleTargets(ctx, mpsOk, tmpDir, "dev", sqlQuery)
	require.NoError(t, err)
	assert.NotEmpty(t, targets)

	sqlQueryTargeted := &datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "sql1"}},
		Type:        datatug.QueryTypeSQL,
		Targets: []datatug.QueryDefTarget{
			{Driver: "sqlite3", Catalog: "db1"},
			{Driver: "ingitdb"},
		},
	}
	targets, err = EligibleTargets(ctx, mpsOk, tmpDir, "dev", sqlQueryTargeted)
	require.NoError(t, err)
	assert.NotEmpty(t, targets)

	// 3. driverMatches helper
	assert.True(t, driverMatches("sqlite", SourceKindSQL))
	assert.True(t, driverMatches("sqlite3", SourceKindSQL))
	assert.True(t, driverMatches("openvaultdb", SourceKindSQL))
	assert.True(t, driverMatches("ingitdb", SourceKindInGitDB))
	assert.False(t, driverMatches("ingitdb", SourceKindSQL))
	assert.False(t, driverMatches("unknown", SourceKindSQL))

	// 4. dedupeNonEmpty
	deduped := dedupeNonEmpty("a", "", "b", "a", "c", "")
	assert.Equal(t, []string{"a", "b", "c"}, deduped)

	// 5. semanticIngitdbPath
	assert.Equal(t, "ingitdb://"+filepath.Join(tmpDir, storage.DataFolder, "ingitdb"), semanticIngitdbPath(tmpDir))
}

func TestCatalogTablesAPIEdgeCases(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. catalogDbModel
	_, err := catalogDbModel(tmpDir, "dev", "c1")
	assert.ErrorIs(t, err, ErrCatalogNotFound)

	catDir := filepath.Join(tmpDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c1")
	require.NoError(t, os.MkdirAll(catDir, 0755))
	catFile := filepath.Join(catDir, "c1.db.json")
	require.NoError(t, os.WriteFile(catFile, []byte("{malformed"), 0644))
	_, err = catalogDbModel(tmpDir, "dev", "c1")
	assert.Error(t, err)

	require.NoError(t, os.WriteFile(catFile, []byte(`{"id":"c1"}`), 0644))
	_, err = catalogDbModel(tmpDir, "dev", "c1")
	assert.Error(t, err)

	require.NoError(t, os.WriteFile(catFile, []byte(`{"id":"c1","dbModel":"m1"}`), 0644))
	model, err := catalogDbModel(tmpDir, "dev", "c1")
	require.NoError(t, err)
	assert.Equal(t, "m1", model)

	// 2. listCatalogTables
	tables, err := listCatalogTables(filepath.Join(tmpDir, "nonexistent"), "tables", "sqlite3")
	require.NoError(t, err)
	assert.Empty(t, tables)

	modelDir := filepath.Join(tmpDir, "dbmodels", "m1")
	require.NoError(t, os.MkdirAll(modelDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modelDir, "ignore.txt"), []byte("hi"), 0644))

	schemaDir := filepath.Join(modelDir, "main")
	require.NoError(t, os.MkdirAll(schemaDir, 0755))
	tables, err = listCatalogTables(modelDir, "tables", "sqlite3")
	require.NoError(t, err)
	assert.Empty(t, tables)

	tableDir := filepath.Join(schemaDir, "tables", "users")
	require.NoError(t, os.MkdirAll(tableDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(schemaDir, "tables", "ignore_file"), []byte("x"), 0644))

	tables, err = listCatalogTables(modelDir, "tables", "sqlite3")
	require.NoError(t, err)
	require.Len(t, tables, 1)
	assert.Equal(t, "users", tables[0].Name)
}

func TestQueriesAPIEdgeCases(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. CreateQuery validation error
	_, err := CreateQuery(ctx, dto.CreateQuery{})
	assert.Error(t, err)

	// 2. UpdateQuery validation error
	_, err = UpdateQuery(ctx, dto.UpdateQuery{})
	assert.Error(t, err)

	// 3. DeleteQuery validation error
	assert.Error(t, DeleteQuery(ctx, dto.ProjectItemRef{}))

	// 4. DeleteQuery unsafe path
	assert.Error(t, DeleteQuery(ctx, dto.ProjectItemRef{
		ProjectRef: dto.ProjectRef{ProjectID: "p1", StoreID: "store1"},
		ID:         "../unsafe",
	}))

	// 5. requireFolderSupport when legacyStoreResolvesFolders is false
	origResolvesFolders := legacyStoreResolvesFolders
	defer func() { legacyStoreResolvesFolders = origResolvesFolders }()

	legacyStoreResolvesFolders = false
	assert.NoError(t, requireFolderSupport("root_query"))
	assert.Error(t, requireFolderSupport("folder/nested_query"))
	legacyStoreResolvesFolders = true

	// 6. GetQuery error paths
	_, err = GetQuery(ctx, dto.ProjectItemRef{})
	assert.Error(t, err)

	_, err = GetQuery(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "unknown"}, ID: "q1"})
	assert.Error(t, err)

	secureMu.Lock()
	savedDirs := projectDirs
	projectDirs = map[string]string{"p1": tmpDir}
	secureMu.Unlock()
	defer func() {
		secureMu.Lock()
		projectDirs = savedDirs
		secureMu.Unlock()
	}()

	_, err = GetQuery(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1", StoreID: "store1"}, ID: "nonexistent_query"})
	assert.Error(t, err)
}

func TestQueryIDEdgeCases(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ResolveQueryID(tmpDir, "q1")
	assert.ErrorIs(t, err, ErrQueryNotFound)

	qDir1 := filepath.Join(tmpDir, storage.QueriesFolder, "f1")
	qDir2 := filepath.Join(tmpDir, storage.QueriesFolder, "f2")
	require.NoError(t, os.MkdirAll(qDir1, 0755))
	require.NoError(t, os.MkdirAll(qDir2, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(qDir1, "dup.query.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(qDir2, "dup.query.json"), []byte("{}"), 0644))

	_, err = ResolveQueryID(tmpDir, "dup")
	assert.ErrorIs(t, err, ErrAmbiguousQueryID)

	resolved, err := ResolveQueryID(tmpDir, "f1/dup")
	require.NoError(t, err)
	assert.Equal(t, "f1/dup", resolved)
}

func TestScanDbSchemaAPI(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT);")
	require.NoError(t, err)
	_ = db.Close()

	params := dbconnection.NewSQLite3ConnectionParams(dbPath, "main", dbconnection.ModeReadOnly)

	badParams := dbconnection.NewSQLite3ConnectionParams(dbPath, "", dbconnection.ModeReadOnly)
	_, err = UpdateDbSchema(ctx, nil, "p1", "dev", "sqlite3", "m1", badParams)
	assert.Error(t, err)

	_, err = UpdateDbSchema(ctx, nil, "", "dev", "sqlite3", "m1", params)
	assert.Error(t, err)
	_, err = UpdateDbSchema(ctx, nil, "p1", "", "sqlite3", "m1", params)
	assert.Error(t, err)
	_, err = UpdateDbSchema(ctx, nil, "p1", "dev", "", "m1", params)
	assert.Error(t, err)
	_, err = UpdateDbSchema(ctx, nil, "p1", "dev", "sqlite3", "", params)
	assert.Error(t, err)

	mpsErr := mockProjectStore{
		loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
			return datatug.ProjectFile{}, errors.New("load file err")
		},
	}
	_, err = UpdateDbSchema(ctx, mpsErr, "p1", "dev", "sqlite3", "m1", params)
	assert.Error(t, err)

	mpsNew := mockProjectStore{
		loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
			return datatug.ProjectFile{}, datatug.ErrProjectDoesNotExist
		},
	}
	proj, err := UpdateDbSchema(ctx, mpsNew, "p1", "dev", "sqlite3", "m1", params)
	require.NoError(t, err)
	assert.NotNil(t, proj)

	mpsLoadErr := mockProjectStore{
		loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
			return datatug.ProjectFile{}, nil
		},
		loadProjectFunc: func(ctx context.Context, o ...datatug.StoreOption) (*datatug.Project, error) {
			return nil, errors.New("load proj err")
		},
	}
	_, err = UpdateDbSchema(ctx, mpsLoadErr, "p1", "dev", "sqlite3", "m1", params)
	assert.Error(t, err)

	existingProj := &datatug.Project{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "p1"}},
		DbDrivers:   datatug.ProjDbDrivers{},
		DbModels: datatug.DbModels{
			&datatug.DbModel{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "m1"}}},
		},
	}

	assert.Error(t, updateProjectWithDbCatalog(existingProj, "", datatug.ServerRef{}, nil))
	assert.Error(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{}, nil))
	assert.Error(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{Host: "localhost"}, nil))
	assert.Error(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{Host: "localhost"}, &datatug.DbCatalog{}))
	assert.Error(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{Host: "localhost", Driver: "sqlserver"}, &datatug.DbCatalog{ID: "c1", Driver: "postgres"}))
	assert.Error(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{Host: "localhost", Driver: "invalid_driver"}, &datatug.DbCatalog{ID: "c1"}))

	validCat := &datatug.DbCatalog{ID: "c1", Driver: "sqlserver"}
	assert.NoError(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{Host: "localhost", Driver: "sqlserver"}, validCat))
	assert.NoError(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{Host: "localhost", Driver: "sqlserver"}, validCat))

	existingSchema := &datatug.Schema{
		Tables: datatug.TableModels{
			{DBCollectionKey: datatug.NewTableKey("widgets", "dbo", "catalog", nil)},
		},
	}
	dbSchema := &datatug.DbSchema{
		Tables: []*datatug.CollectionInfo{
			{DBCollectionKey: datatug.NewTableKey("widgets", "dbo", "catalog", nil)},
		},
	}
	assert.Panics(t, func() {
		_ = updateSchemaModel("dev", existingSchema, dbSchema)
	})

	_, err = scanDbCatalog(datatug.ServerRef{Driver: "unsupported"}, params)
	assert.Error(t, err)
}

func TestCoverageRemaining100(t *testing.T) {
	ctx := context.Background()

	// 1. AuthTokenFromHTTPRequest & principalUID
	secureMu.Lock()
	secureSession.Principal = nil
	secureSession.Unrestricted = true
	secureMu.Unlock()
	tok, err := AuthTokenFromHTTPRequest(nil, false)
	assert.NoError(t, err)
	assert.NotNil(t, tok)

	secureMu.Lock()
	secureSession.Principal = nil
	secureSession.Unrestricted = false
	secureMu.Unlock()
	tok, err = AuthTokenFromHTTPRequest(nil, false)
	assert.NoError(t, err)
	assert.Nil(t, tok)

	p1 := &access.Principal{ID: nil}
	assert.Equal(t, "", principalUID(p1))
	p2 := &access.Principal{ID: "alice"}
	assert.Equal(t, "alice", principalUID(p2))
	p3 := &access.Principal{ID: 42}
	assert.Equal(t, "42", principalUID(p3))

	secureMu.Lock()
	secureSession.Principal = p2
	secureMu.Unlock()
	tok, err = AuthTokenFromHTTPRequest(nil, false)
	assert.NoError(t, err)
	assert.Equal(t, "alice", tok.UID)

	// 2. ResolveCatalogPath errors
	origExpand := homedirExpand
	origDir := homedirDir
	defer func() {
		homedirExpand = origExpand
		homedirDir = origDir
	}()
	homedirExpand = func(string) (string, error) { return "", errors.New("expand fail") }
	_, err = ResolveCatalogPath("/p", "~/cat")
	assert.Error(t, err)

	homedirDir = func() (string, error) { return "", errors.New("dir fail") }
	_, err = ResolveCatalogPath("/p", "$HOME/cat")
	assert.Error(t, err)

	// sourceURLFromCatalog errors (lines 66, 75, 84)
	homedirExpand = func(string) (string, error) { return "", errors.New("expand fail") }
	_, err = sourceURLFromCatalog(datatug.DbCatalog{Driver: "openvaultdb", Path: "~/a"}, "/p")
	assert.Error(t, err)
	_, err = sourceURLFromCatalog(datatug.DbCatalog{Driver: "sqlite3", Path: "~/a"}, "/p")
	assert.Error(t, err)
	_, err = sourceURLFromCatalog(datatug.DbCatalog{Driver: "ingitdb", Path: "~/a"}, "/p")
	assert.Error(t, err)
	homedirExpand = origExpand
	homedirDir = origDir

	// 3. newSecurityContextID error
	origRand := randRead
	defer func() { randRead = origRand }()
	randRead = func([]byte) (int, error) { return 0, errors.New("rand fail") }
	assert.Equal(t, "securitycontext-unavailable", newSecurityContextID())
	randRead = origRand

	// 4. ConfigureExecutionEvidence error
	origMgr := executionstoreNewManager
	defer func() { executionstoreNewManager = origMgr }()
	executionstoreNewManager = func(incidentstore.RepositoryRoots, map[string]incidents.StoreLocation, executionstore.Options) (*executionstore.Manager, error) {
		return nil, errors.New("manager fail")
	}
	assert.Error(t, ConfigureExecutionEvidence(map[string]string{"p1": t.TempDir()}, nil, executionstore.Options{}))
	executionstoreNewManager = origMgr

	// 5. ExecuteCommands and ExecuteSelect validation errors
	_, err = ExecuteCommands(ctx, "store1", ExecuteCommandsRequest{})
	assert.Error(t, err)
	_, err = ExecuteSelect(ctx, "store1", SelectRequest{})
	assert.Error(t, err)

	// 6. default executeSingleSeam
	_, _ = executeSingleSeam(sqlexecute.Executor{}, sqlexecute.RequestCommand{})

	// 7. getRecordsetFolder match
	rootFolder := &dto.ProjRecordsetSummary{
		Recordsets: []*dto.ProjRecordsetSummary{
			{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "sub"}}},
		},
	}
	resFolder := getRecordsetFolder(rootFolder, []string{"sub", "leaf"})
	assert.NotNil(t, resFolder)

	// 8. incidents.go branches
	paths := map[string]string{"p1": "/p1", "p2": "/p2"}
	scope := &investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "p1", Environment: "dev"}

	sessionNoPol := secureread.Session{}
	assert.Equal(t, incidents.FactHidden, incidentFactVisibility(ctx, investigation.Fact{Scope: scope, Entity: "e1", Field: "f1"}, sessionNoPol, paths))

	factPhys := investigation.Fact{
		Scope:  scope,
		Entity: "e1", Field: "f1",
		Physical: &investigation.PhysicalRef{Collection: "c1", Column: "col1"},
	}
	decode := func(name, fields, condition string, allow bool) accesspolicies.Loaded {
		eff := "allow"
		if !allow {
			eff = "deny"
		}
		condStr := ""
		if condition != "" {
			condStr = "\n        where:\n          op: '=='\n          left: {field: id}\n          right: {value: 1}"
		}
		fieldStr := ""
		if fields != "" {
			fieldStr = "\n        fields: [" + fields + "]"
		}
		doc := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: " + name + "}\ndefault: deny\nscopes:\n  - path: /c1\n    rules:\n      - id: read\n        effect: " + eff + "\n        operations: [query]" + condStr + fieldStr + "\n"
		loaded, err := accesspolicies.DecodeLoaded([]byte(doc), access.YAMLCodec{}, name+".yaml")
		require.NoError(t, err)
		return loaded
	}

	sessionWithPol := secureread.Session{
		Policies: []accesspolicies.Loaded{decode("allow_col", "col1", "", true)},
	}
	assert.Equal(t, incidents.FactVisible, incidentFactVisibility(ctx, factPhys, sessionWithPol, paths))

	sessionDeny := secureread.Session{
		Policies: []accesspolicies.Loaded{decode("cond_pol", "", "x == 1", true)},
	}
	assert.Equal(t, incidents.FactHidden, incidentFactVisibility(ctx, factPhys, sessionDeny, paths))

	sessionEmpty := secureread.Session{
		Policies: []accesspolicies.Loaded{decode("deny_pol", "", "", false)},
	}
	assert.Equal(t, incidents.FactHidden, incidentFactVisibility(ctx, factPhys, sessionEmpty, paths))

	sessionRedact := secureread.Session{
		Policies: []accesspolicies.Loaded{decode("redact_pol", "other_col", "", true)},
	}
	assert.Equal(t, incidents.FactValueRedacted, incidentFactVisibility(ctx, factPhys, sessionRedact, paths))

	storedInc := incidents.Incident{}
	ev := incidents.Event{}

	refProj := incidents.ArtifactRef{Project: &investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "p1", Environment: "dev"}}
	assert.True(t, incidentRefVisible(refProj, ev, storedInc, true, paths))
	refProjBad := incidents.ArtifactRef{Project: &investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "unknown", Environment: "dev"}}
	assert.False(t, incidentRefVisible(refProjBad, ev, storedInc, true, paths))

	refExec := incidents.ArtifactRef{Execution: &incidents.ExecutionRef{ProjectID: "p1"}}
	assert.True(t, incidentRefVisible(refExec, ev, storedInc, true, paths))
	refExecBad := incidents.ArtifactRef{Execution: &incidents.ExecutionRef{ProjectID: "unknown"}}
	assert.False(t, incidentRefVisible(refExecBad, ev, storedInc, true, paths))

	refComp := incidents.ArtifactRef{Comparison: &incidents.ComparisonRef{Left: incidents.ExecutionRef{ProjectID: "p1"}, Right: incidents.ExecutionRef{ProjectID: "p2"}}}
	assert.True(t, incidentRefVisible(refComp, ev, storedInc, true, paths))
	refCompBad := incidents.ArtifactRef{Comparison: &incidents.ComparisonRef{Left: incidents.ExecutionRef{ProjectID: "p1"}, Right: incidents.ExecutionRef{ProjectID: "unknown"}}}
	assert.False(t, incidentRefVisible(refCompBad, ev, storedInc, true, paths))

	refID := incidents.ArtifactRef{ID: "custom-id"}
	assert.True(t, incidentRefVisible(refID, ev, storedInc, true, paths))
	refIDEmpty := incidents.ArtifactRef{}
	assert.False(t, incidentRefVisible(refIDEmpty, ev, storedInc, true, paths))

	// 9. queries_api.go branches
	origFolders := legacyStoreResolvesFolders
	defer func() { legacyStoreResolvesFolders = origFolders }()
	legacyStoreResolvesFolders = false

	_, err = CreateQuery(ctx, dto.CreateQuery{
		ProjectRef: dto.ProjectRef{StoreID: "store1", ProjectID: "p1"},
		Folder:     "folder1",
		Query: datatug.QueryDefWithFolderPath{
			FolderPath: "folder1",
			QueryDef: datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1"}},
			},
		},
	})
	assert.Error(t, err)

	err = DeleteQuery(ctx, dto.ProjectItemRef{
		ProjectRef: dto.ProjectRef{StoreID: "store1", ProjectID: "p1"},
		ID:         "folder1/q1",
	})
	assert.Error(t, err)

	legacyStoreResolvesFolders = true

	credDef := datatug.QueryDefWithFolderPath{
		FolderPath: "~",
		QueryDef: datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q_cred", Title: "Server=db;Password=hunter2"}},
			Targets: []datatug.QueryDefTarget{
				{Driver: "sqlite3", Catalog: "c1"},
			},
		},
	}
	_, err = CreateQuery(ctx, dto.CreateQuery{
		ProjectRef: dto.ProjectRef{StoreID: "store1", ProjectID: "p1"},
		Query:      credDef,
	})
	assert.Error(t, err)

	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return nil, errors.New("store err")
	}
	_, err = CreateQuery(ctx, dto.CreateQuery{
		ProjectRef: dto.ProjectRef{ProjectID: "p1", StoreID: "bad"},
		Query: datatug.QueryDefWithFolderPath{
			FolderPath: "~",
			QueryDef: datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1"}},
			},
		},
	})
	assert.Error(t, err)
	err = DeleteQuery(ctx, dto.ProjectItemRef{
		ProjectRef: dto.ProjectRef{ProjectID: "p1", StoreID: "bad"},
		ID:         "q1",
	})
	assert.Error(t, err)

	_, err = GetQuery(ctx, dto.ProjectItemRef{
		ProjectRef: dto.ProjectRef{ProjectID: "p1", StoreID: "bad"},
		ID:         "q1",
	})
	assert.Error(t, err)

	_, err = GetQuery(ctx, dto.ProjectItemRef{
		ProjectRef: dto.ProjectRef{StoreID: "store1", ProjectID: "unknown_proj"},
		ID:         "q1",
	})
	assert.ErrorIs(t, err, ErrQueryNotFound)

	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectStoreFunc: func(projectID string) datatug.ProjectStore {
				return mockProjectStore{
					loadQueryFunc: func(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.QueryDef, error) {
						return nil, errors.New("load query err")
					},
				}
			},
		}, nil
	}
	p1Dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(p1Dir, storage.QueriesFolder), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(p1Dir, storage.QueriesFolder, "q1.query.json"), []byte(`{"id":"q1"}`), 0644))
	ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"p1": p1Dir}, Capabilities{})
	_, err = GetQuery(ctx, dto.ProjectItemRef{
		ProjectRef: dto.ProjectRef{StoreID: "store1", ProjectID: "p1"},
		ID:         "q1",
	})
	assert.ErrorIs(t, err, ErrQueryNotFound)

	// 10. query_id.go branches
	origRel := filepathRel
	defer func() { filepathRel = origRel }()
	filepathRel = func(string, string) (string, error) { return "", errors.New("rel err") }
	_, err = QueryIDIndex(p1Dir)
	assert.Error(t, err)
	filepathRel = origRel

	badFileAsDir := filepath.Join(t.TempDir(), "bad")
	require.NoError(t, os.WriteFile(badFileAsDir, []byte("file"), 0644))
	_, err = ResolveQueryID(badFileAsDir, "q1")
	assert.Error(t, err)

	// 11. resolver.go branches
	sourcesWithDup := []ResolvedSource{
		{ID: "chinook-local", URL: "sqlite:///db"},
		{ID: "chinook", URL: "sqlite:///db"},
	}
	deduped := dedupeSourcesByID(sourcesWithDup)
	assert.Len(t, deduped, 1)
	assert.Equal(t, "chinook", deduped[0].ID)

	projDir := t.TempDir()
	rsDir := filepath.Join(projDir, storage.RecordsetsFolder)
	require.NoError(t, os.MkdirAll(rsDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(rsDir, "subdir"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(rsDir, "readme.txt"), []byte("hi"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(rsDir, "bad.recordset.json"), []byte("{malformed"), 0644))
	_, err = recordsetSources(projDir)
	assert.Error(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(rsDir, "bad.recordset.json"), []byte(`{"id":"r1"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(rsDir, "custom.recordset.json"), []byte(`{"id":"custom-id","title":"Custom"}`), 0644))
	rsSources, err := recordsetSources(projDir)
	require.NoError(t, err)
	assert.Len(t, rsSources, 2)

	catStore := mockProjectStore{
		loadEnvDbCatalogsFunc: func(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return datatug.DbCatalogs{
				nil,
				&datatug.DbCatalog{ID: "cat1", Driver: "ingitdb", Path: "/a/b"},
				&datatug.DbCatalog{ID: "cat_unsupp", Driver: "unknown_drv", Path: "/a/b"},
				&datatug.DbCatalog{ID: "cat1", DbModel: "cat1", Driver: "ingitdb", Path: "/a/b"},
			}, nil
		},
	}
	cSources, err := catalogSources(ctx, catStore, projDir, "dev")
	require.NoError(t, err)
	assert.NotEmpty(t, cSources)

	projDirHttpErr := t.TempDir()
	qDir := filepath.Join(projDirHttpErr, storage.QueriesFolder)
	require.NoError(t, os.MkdirAll(qDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(qDir, "bad.query.json"), []byte("{bad"), 0644))
	_, err = EligibleTargets(ctx, nil, projDirHttpErr, "dev", &datatug.QueryDef{Type: datatug.QueryTypeHTTP, ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1"}}})
	assert.Error(t, err)

	errCatStore := mockProjectStore{
		loadEnvDbCatalogsFunc: func(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return nil, errors.New("cat load err")
		},
	}
	_, err = EligibleTargets(ctx, errCatStore, projDir, "dev", &datatug.QueryDef{Type: datatug.QueryTypeSQL})
	assert.Error(t, err)

	// 12. catalog_tables_api.go branches
	emptyKindDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(emptyKindDir, "users"), 0755))
	_, err = readCatalogColumns(emptyKindDir, "main", "users")
	assert.Error(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(emptyKindDir, "users", "users.columns.json"), []byte("{bad"), 0644))
	_, err = readCatalogColumns(emptyKindDir, "main", "users")
	assert.Error(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(emptyKindDir, "users", "users.columns.json"), []byte(`{}`), 0644))
	cols, err := readCatalogColumns(emptyKindDir, "main", "users")
	require.NoError(t, err)
	assert.Empty(t, cols)

	catProjDir := t.TempDir()
	mDir := filepath.Join(catProjDir, storage.DbModelsFolder, "m1")
	require.NoError(t, os.MkdirAll(filepath.Join(mDir, "s2", "tables", "t1"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(mDir, "s1", "tables", "t1"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(catProjDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c1"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(catProjDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c1", "c1.db.json"), []byte(`{"dbModel":"m1"}`), 0644))

	ct, err := GetCatalogTables(catProjDir, "dev", "c1")
	require.NoError(t, err)
	assert.Len(t, ct.Tables, 2)
	assert.Equal(t, "s1", ct.Tables[0].Schema)
	assert.Equal(t, "s2", ct.Tables[1].Schema)

	_, err = GetCatalogSchema(catProjDir, "dev", "c1")
	assert.Error(t, err)
	csPartial, err := GetCatalogSchemaPartial(catProjDir, "dev", "c1")
	require.NoError(t, err)
	assert.Len(t, csPartial.Relations, 2)
	assert.Equal(t, "s1", csPartial.Relations[0].Schema)
	assert.Equal(t, "s2", csPartial.Relations[1].Schema)

	// 13. scan_db_schema_api.go branches
	testDbPath := filepath.Join(t.TempDir(), "test.db")
	params := dbconnection.NewSQLite3ConnectionParams(testDbPath, "main", dbconnection.ModeReadOnly)
	existingProj := &datatug.Project{DbDrivers: datatug.ProjDbDrivers{}}

	projErrLoader := mockProjectStore{
		loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
			return datatug.ProjectFile{}, errors.New("proj file read err")
		},
	}
	_, err = UpdateDbSchema(ctx, projErrLoader, "p1", "dev", "sqlite3", "m1", params)
	assert.Error(t, err)

	_, err = UpdateDbSchema(ctx, mockProjectStore{}, "p1", "dev", "unsupported_drv", "m1", params)
	assert.Error(t, err)

	invalidCat := &datatug.DbCatalog{Driver: "sqlite3"}
	assert.Error(t, updateProjectWithDbCatalog(existingProj, "dev", datatug.ServerRef{Driver: "sqlite3"}, invalidCat))

	testProj := &datatug.Project{
		Environments: datatug.Environments{
			&datatug.Environment{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}},
				DbServers: datatug.EnvDbServers{
					{
						ServerRef: datatug.ServerRef{Driver: "sqlite3"},
						Catalogs:  []string{"existing_cat"},
					},
				},
			},
		},
		DbDrivers: datatug.ProjDbDrivers{},
	}
	newCat := &datatug.DbCatalog{ID: "new_cat", Driver: "sqlite3", Path: "/a/b.db"}
	assert.NoError(t, updateProjectWithDbCatalog(testProj, "dev", datatug.ServerRef{Driver: "sqlite3"}, newCat))
	assert.Contains(t, testProj.Environments[0].DbServers[0].Catalogs, "new_cat")

	testProj2 := &datatug.Project{
		Environments: datatug.Environments{
			&datatug.Environment{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}},
				DbServers:   datatug.EnvDbServers{},
			},
		},
		DbDrivers: datatug.ProjDbDrivers{},
	}
	assert.NoError(t, updateProjectWithDbCatalog(testProj2, "dev", datatug.ServerRef{Driver: "sqlite3"}, newCat))
	assert.Len(t, testProj2.Environments[0].DbServers, 1)

	sqlParams, err := dbconnection.NewConnectionString("sqlserver", "host1", "user", "pass", "db1")
	require.NoError(t, err)
	_, _ = UpdateDbSchema(ctx, mockProjectStore{}, "p1", "dev", "sqlserver", "m1", sqlParams)
}

func TestCoverageFinal100_PkgApi(t *testing.T) {
	ctx := context.Background()

	// 1. source_warnings.go
	{
		origDbcopyParse := dbcopyParse
		origLoadHTTP := loadHTTPQueries
		t.Cleanup(func() {
			dbcopyParse = origDbcopyParse
			loadHTTPQueries = origLoadHTTP
		})

		loadHTTPQueries = func(projectDir string) ([]httpsource.LoadedQuery, error) {
			return []httpsource.LoadedQuery{
				{Def: &datatug.QueryDef{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "h1"}},
					Type:        "HTTP",
				}},
			}, nil
		}

		pDir := t.TempDir()
		warnMissingSourceFilesForEnvironment(ctx, mockProjectStore{}, "p1", pDir, &datatug.Environment{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}},
		})

		dbcopyParse = func(raw string) (dbcopy.BackendRef, error) {
			return dbcopy.BackendRef{}, errors.New("parse error")
		}
		rDir := filepath.Join(pDir, storage.RecordsetsFolder)
		require.NoError(t, os.MkdirAll(rDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(rDir, "r1.recordset.json"), []byte(`{"id":"r1"}`), 0644))
		warnMissingSourceFilesForEnvironment(ctx, mockProjectStore{}, "p1", pDir, &datatug.Environment{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}},
		})
	}

	// 2. catalog_tables_api.go
	{
		pDir := t.TempDir()
		_, err := GetCatalogSchema(pDir, "dev", "nonexistent")
		assert.Error(t, err)

		cDir := filepath.Join(pDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c_empty")
		require.NoError(t, os.MkdirAll(cDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(cDir, "c_empty.db.json"), []byte(`{"dbModel":"nonexistent_model"}`), 0644))
		cs, err := GetCatalogSchema(pDir, "dev", "c_empty")
		require.NoError(t, err)
		assert.Empty(t, cs.Relations)

		cDir2 := filepath.Join(pDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c_filemodel")
		require.NoError(t, os.MkdirAll(cDir2, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(cDir2, "c_filemodel.db.json"), []byte(`{"dbModel":"file_model"}`), 0644))
		fileModelPath := filepath.Join(pDir, storage.DbModelsFolder, "file_model")
		require.NoError(t, os.MkdirAll(filepath.Dir(fileModelPath), 0755))
		require.NoError(t, os.WriteFile(fileModelPath, []byte("not a dir"), 0644))
		_, err = GetCatalogSchema(pDir, "dev", "c_filemodel")
		assert.Error(t, err)
		_, err = GetCatalogTables(pDir, "dev", "c_filemodel")
		assert.Error(t, err)

		cDir3 := filepath.Join(pDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c_schemafile")
		require.NoError(t, os.MkdirAll(cDir3, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(cDir3, "c_schemafile.db.json"), []byte(`{"dbModel":"model_with_file"}`), 0644))
		dirWithFile := filepath.Join(pDir, storage.DbModelsFolder, "model_with_file")
		require.NoError(t, os.MkdirAll(dirWithFile, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dirWithFile, "regular_file.txt"), []byte("hi"), 0644))
		cs3, err := GetCatalogSchema(pDir, "dev", "c_schemafile")
		require.NoError(t, err)
		assert.Empty(t, cs3.Relations)
		ct3, err := GetCatalogTables(pDir, "dev", "c_schemafile")
		require.NoError(t, err)
		assert.Empty(t, ct3.Tables)
		assert.Empty(t, ct3.Views)

		cDir4 := filepath.Join(pDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c_kindfile")
		require.NoError(t, os.MkdirAll(cDir4, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(cDir4, "c_kindfile.db.json"), []byte(`{"dbModel":"model_kindfile"}`), 0644))
		kindModelDir := filepath.Join(pDir, storage.DbModelsFolder, "model_kindfile", "s1")
		require.NoError(t, os.MkdirAll(kindModelDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(kindModelDir, "tables"), []byte("file instead of dir"), 0644))
		_, err = GetCatalogSchema(pDir, "dev", "c_kindfile")
		assert.Error(t, err)
		_, err = GetCatalogTables(pDir, "dev", "c_kindfile")
		assert.Error(t, err)

		cDir4v := filepath.Join(pDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c_kindfile_v")
		require.NoError(t, os.MkdirAll(cDir4v, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(cDir4v, "c_kindfile_v.db.json"), []byte(`{"dbModel":"model_kindfile_v"}`), 0644))
		kindModelDirV := filepath.Join(pDir, storage.DbModelsFolder, "model_kindfile_v", "s1")
		require.NoError(t, os.MkdirAll(kindModelDirV, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(kindModelDirV, "views"), []byte("file"), 0644))
		_, err = GetCatalogTables(pDir, "dev", "c_kindfile_v")
		assert.Error(t, err)

		cDir5 := filepath.Join(pDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c_tabfile")
		require.NoError(t, os.MkdirAll(cDir5, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(cDir5, "c_tabfile.db.json"), []byte(`{"dbModel":"model_tabfile"}`), 0644))
		tabModelDir := filepath.Join(pDir, storage.DbModelsFolder, "model_tabfile", "s1", "tables")
		require.NoError(t, os.MkdirAll(tabModelDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(tabModelDir, "not_a_table_dir.txt"), []byte("hi"), 0644))
		cs5, err := GetCatalogSchema(pDir, "dev", "c_tabfile")
		require.NoError(t, err)
		assert.Empty(t, cs5.Relations)
		ct5, err := GetCatalogTables(pDir, "dev", "c_tabfile")
		require.NoError(t, err)
		assert.Empty(t, ct5.Tables)

		origGlob := filepathGlob
		t.Cleanup(func() { filepathGlob = origGlob })
		filepathGlob = func(pattern string) ([]string, error) {
			return nil, errors.New("glob failed")
		}
		_, err = readCatalogColumns(tabModelDir, "s1", "t1")
		assert.Error(t, err)
		filepathGlob = origGlob

		colDir := filepath.Join(tabModelDir, "t_badread")
		require.NoError(t, os.MkdirAll(colDir, 0755))
		require.NoError(t, os.MkdirAll(filepath.Join(colDir, "t.columns.json"), 0755))
		_, err = readCatalogColumns(tabModelDir, "s1", "t_badread")
		assert.Error(t, err)

		cDirDir := filepath.Join(pDir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "c_isdir")
		catalogFilePath := filepath.Join(cDirDir, "c_isdir.db.json")
		require.NoError(t, os.MkdirAll(catalogFilePath, 0755))
		_, err = catalogDbModel(pDir, "dev", "c_isdir")
		assert.Error(t, err)
	}

	// 3. incidents.go
	{
		origExplain := accesspoliciesExplain
		t.Cleanup(func() { accesspoliciesExplain = origExplain })
		accesspoliciesExplain = func(ctx context.Context, loaded []accesspolicies.Loaded, query dal.Query, bindings map[string]any) []accesspolicies.Line {
			return nil
		}

		scope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "demo", Environment: "prod"}
		fact := investigation.Fact{ID: "customer", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("5"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &scope}
		session := secureread.Session{Policies: []accesspolicies.Loaded{{}}}
		require.Equal(t, incidents.FactHidden, incidentFactVisibility(context.Background(), fact, session, map[string]string{"demo": "/demo"}))
	}

	// 4. queries_api.go
	{
		origStore := storage.NewDatatugStore
		origCred := querywriteQueryCredentialReason
		t.Cleanup(func() {
			storage.NewDatatugStore = origStore
			querywriteQueryCredentialReason = origCred
		})
		storage.NewDatatugStore = func(id string) (storage.Store, error) {
			if id == "unknown_store_xyz" {
				return nil, errors.New("unknown store")
			}
			return mockStore{}, nil
		}

		credProjDir := t.TempDir()
		ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"p_cred": credProjDir}, Capabilities{AllowWrites: true})

		querywriteQueryCredentialReason = func(q *datatug.QueryDef) (string, string, bool) {
			if q.ID == "q_cred" {
				return "text", "credentials refused", true
			}
			return origCred(q)
		}

		_, err := CreateQuery(ctx, dto.CreateQuery{
			ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: "p_cred"},
			Query: datatug.QueryDefWithFolderPath{
				FolderPath: datatug.RootSharedFolderName,
				QueryDef: datatug.QueryDef{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q_cred", Title: "Query Cred"}},
					Type:        "SQL",
				},
			},
		})
		assert.Error(t, err)

		_, err = CreateQuery(ctx, dto.CreateQuery{
			ProjectRef: dto.ProjectRef{StoreID: "unknown_store_xyz", ProjectID: "p_cred"},
			Query: datatug.QueryDefWithFolderPath{
				FolderPath: datatug.RootSharedFolderName,
				QueryDef: datatug.QueryDef{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q_good", Title: "Query Good"}},
					Type:        "SQL",
				},
			},
		})
		assert.Error(t, err)

		err = DeleteQuery(ctx, dto.ProjectItemRef{
			StoreID:   "unknown_store_xyz",
			ProjectID: "p_cred",
			ID:        "q_good",
		})
		assert.Error(t, err)

		qDir := filepath.Join(credProjDir, storage.QueriesFolder)
		require.NoError(t, os.MkdirAll(qDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(qDir, "q_good.query.json"), []byte(`{"id":"q_good"}`), 0644))
		_, err = GetQuery(ctx, dto.ProjectItemRef{
			StoreID:   "unknown_store_xyz",
			ProjectID: "p_cred",
			ID:        "q_good",
		})
		assert.Error(t, err)
	}

	// 5. resolver.go
	{
		badProjDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(badProjDir, storage.RecordsetsFolder), []byte("file"), 0644))
		_, err := ResolveSource(ctx, mockProjectStore{}, badProjDir, "dev", "s1")
		assert.Error(t, err)
		_, err = ListSources(ctx, mockProjectStore{}, badProjDir, "")
		assert.Error(t, err)

		errProjStore := mockProjectStore{
			loadEnvDbCatalogsFunc: func(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
				return nil, errors.New("load catalogs err")
			},
		}
		_, err = ListSources(ctx, errProjStore, t.TempDir(), "dev")
		assert.Error(t, err)

		badRecDir := t.TempDir()
		rDir := filepath.Join(badRecDir, storage.RecordsetsFolder)
		require.NoError(t, os.MkdirAll(rDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(rDir, "bad.recordset.json"), []byte("{}"), 0000))
		_, err = recordsetSources(badRecDir)
		assert.Error(t, err)

		origLoadHTTP := loadHTTPQueries
		t.Cleanup(func() { loadHTTPQueries = origLoadHTTP })
		loadHTTPQueries = func(projectDir string) ([]httpsource.LoadedQuery, error) {
			return []httpsource.LoadedQuery{
				{Def: nil},
				{Def: &datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "http1"}}}},
			}, nil
		}
		multiKindProjDir := t.TempDir()
		mrDir := filepath.Join(multiKindProjDir, storage.RecordsetsFolder)
		require.NoError(t, os.MkdirAll(mrDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(mrDir, "rec1.recordset.json"), []byte(`{"id":"rec1","title":"Rec 1"}`), 0644))
		sources, err := ListSources(ctx, mockProjectStore{}, multiKindProjDir, "")
		require.NoError(t, err)
		assert.NotEmpty(t, sources)
	}

	// 6. scan_db_schema_api.go
	{
		origScanDb := scanDbCatalogSeam
		origUpdateSchema := updateSchemaModelSeam
		origNewProj := newProjectWithDatabaseSeam
		t.Cleanup(func() {
			scanDbCatalogSeam = origScanDb
			updateSchemaModelSeam = origUpdateSchema
			newProjectWithDatabaseSeam = origNewProj
		})

		testDbPath := filepath.Join(t.TempDir(), "test.db")
		params := dbconnection.NewSQLite3ConnectionParams(testDbPath, "main", dbconnection.ModeReadOnly)

		scanDbCatalogSeam = func(server datatug.ServerRef, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
			return &datatug.DbCatalog{
				ID:      "c_model",
				Driver:  "sqlite3",
				DbModel: "orig_model",
				Path:    "/fake/path.db",
			}, nil
		}
		p, err := UpdateDbSchema(ctx, mockProjectStore{
			loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
				return datatug.ProjectFile{}, datatug.ErrProjectDoesNotExist
			},
		}, "p_dm", "dev", "sqlite3", "target_model", params)
		require.NoError(t, err)
		assert.Equal(t, "target_model", p.DbModels[0].ID)

		// hits line 100
		newProjectWithDatabaseSeam = func(environment string, dbServer datatug.ServerRef, dbCatalog *datatug.DbCatalog) (*datatug.Project, error) {
			return nil, errors.New("new proj error")
		}
		_, err = UpdateDbSchema(ctx, mockProjectStore{
			loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
				return datatug.ProjectFile{}, datatug.ErrProjectDoesNotExist
			},
		}, "p_dm", "dev", "sqlite3", "m1", params)
		assert.Error(t, err)
		newProjectWithDatabaseSeam = origNewProj

		scanDbCatalogSeam = func(server datatug.ServerRef, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
			return &datatug.DbCatalog{ID: ""}, nil
		}
		_, err = UpdateDbSchema(ctx, mockProjectStore{
			loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
				return datatug.ProjectFile{}, nil
			},
			loadProjectFunc: func(ctx context.Context, o ...datatug.StoreOption) (*datatug.Project, error) {
				return &datatug.Project{}, nil
			},
		}, "p_dm", "dev", "sqlite3", "m1", params)
		assert.Error(t, err)

		scanDbCatalogSeam = func(server datatug.ServerRef, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
			return &datatug.DbCatalog{ID: "c_missing", Driver: "sqlite3", Path: "/a.db"}, nil
		}
		_, err = UpdateDbSchema(ctx, mockProjectStore{
			loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
				return datatug.ProjectFile{}, nil
			},
			loadProjectFunc: func(ctx context.Context, o ...datatug.StoreOption) (*datatug.Project, error) {
				return &datatug.Project{
					DbDrivers: datatug.ProjDbDrivers{},
					DbModels:  datatug.DbModels{},
				}, nil
			},
		}, "p_dm", "dev", "sqlite3", "m_missing", params)
		assert.Error(t, err)

		// hits line 119 and line 323
		scanDbCatalogSeam = func(server datatug.ServerRef, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
			return &datatug.DbCatalog{
				ID:      "c_update_err",
				Driver:  "sqlite3",
				Path:    "/a.db",
				DbModel: "c_update_err",
				Schemas: datatug.DbSchemas{
					{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "s1"}}},
				},
			}, nil
		}
		updateSchemaModelSeam = func(envID string, schema *datatug.Schema, dbSchema *datatug.DbSchema) error {
			return errors.New("update schema seam error")
		}
		_, err = UpdateDbSchema(ctx, mockProjectStore{
			loadProjectFileFunc: func(ctx context.Context) (datatug.ProjectFile, error) {
				return datatug.ProjectFile{}, nil
			},
			loadProjectFunc: func(ctx context.Context, o ...datatug.StoreOption) (*datatug.Project, error) {
				return &datatug.Project{
					DbDrivers: datatug.ProjDbDrivers{},
					DbModels: datatug.DbModels{
						{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "c_update_err"}}},
					},
				}, nil
			},
		}, "p_dm", "dev", "sqlite3", "c_update_err", params)
		assert.Error(t, err)
		updateSchemaModelSeam = origUpdateSchema

		// hits line 147
		err = updateProjectWithDbCatalog(&datatug.Project{DbDrivers: datatug.ProjDbDrivers{}}, "dev", datatug.ServerRef{Driver: "sqlite3"}, &datatug.DbCatalog{ID: "c1", Driver: "sqlite3", Path: ""})
		assert.Error(t, err)

		// hits line 178
		storeWithDbErr := mockProjectStore{
			loadProjDbDriversFunc: func(ctx context.Context, o ...datatug.StoreOption) (datatug.ProjDbDrivers, error) {
				return nil, errors.New("load db drivers err")
			},
		}
		projWithErr := datatug.NewProjectWithStore("p1", storeWithDbErr)
		projWithErr.DbDrivers = nil
		err = updateProjectWithDbCatalog(projWithErr, "dev", datatug.ServerRef{Driver: "sqlite3"}, &datatug.DbCatalog{ID: "c1", Driver: "sqlite3", Path: "/a.db"})
		assert.Error(t, err)

		// hits line 206
		projWithServer := &datatug.Project{
			Environments: datatug.Environments{
				{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev"}},
					DbServers: datatug.EnvDbServers{
						{ServerRef: datatug.ServerRef{Driver: "sqlite3"}, Catalogs: []string{"existing_cat"}},
					},
				},
			},
			DbDrivers: datatug.ProjDbDrivers{
				{
					ID: "sqlite3",
					Servers: datatug.ProjDbServers{
						{
							Server: datatug.ServerRef{Driver: "sqlite3"},
							Catalogs: datatug.DbCatalogs{
								{ID: "existing_cat", Driver: "sqlite3", Path: "/existing.db"},
							},
						},
					},
				},
			},
		}
		err = updateProjectWithDbCatalog(projWithServer, "dev", datatug.ServerRef{Driver: "sqlite3"}, &datatug.DbCatalog{ID: "new_cat", Driver: "sqlite3", Path: "/new.db"})
		assert.NoError(t, err)

		dbModelWithSchema := &datatug.DbModel{
			Schemas: []*datatug.Schema{
				{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "existing_schema"}}},
			},
		}
		dbCatWithSchema := &datatug.DbCatalog{
			ID: "cat1",
			Schemas: datatug.DbSchemas{
				{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "existing_schema"}}},
			},
		}
		err = updateDbModelWithDbCatalog("dev", dbModelWithSchema, dbCatWithSchema)
		assert.NoError(t, err)

		sqlParams, err := dbconnection.NewConnectionString("sqlserver", "host1", "user", "pass", "db1")
		require.NoError(t, err)
		_, err = scanDbCatalog(datatug.ServerRef{Driver: "sqlserver"}, sqlParams)
		assert.Error(t, err)
		_, err = scanDbCatalog(datatug.ServerRef{Driver: "unsupported_driver"}, sqlParams)
		assert.Error(t, err)
	}
}


