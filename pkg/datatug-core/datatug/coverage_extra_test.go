package datatug

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// constants.go — DirPath()
// ---------------------------------------------------------------------------

func TestDirPath_success(t *testing.T) {
	orig := homedirDir
	defer func() { homedirDir = orig }()

	homedirDir = func() (string, error) { return "/tmp", nil }
	p := DirPath()
	assert.Contains(t, p, "datatug")
}

func TestDirPath_panic(t *testing.T) {
	orig := homedirDir
	defer func() { homedirDir = orig }()

	homedirDir = func() (string, error) { return "", errors.New("no home") }
	require.Panics(t, func() { DirPath() })
}

// ---------------------------------------------------------------------------
// store_options.go — ToSlice and Next
// ---------------------------------------------------------------------------

func TestStoreOptions_ToSlice(t *testing.T) {
	t.Run("non_zero_depth", func(t *testing.T) {
		opts := GetStoreOptions(Depth(3))
		slice := opts.ToSlice()
		require.Len(t, slice, 1)
		// verify the option sets depth correctly
		var verify StoreOptions
		slice[0](&verify)
		assert.Equal(t, 3, verify.depth)
	})
	t.Run("zero_depth", func(t *testing.T) {
		opts := GetStoreOptions()
		slice := opts.ToSlice()
		assert.Nil(t, slice)
	})
}

func TestStoreOptions_Next(t *testing.T) {
	t.Run("depth_positive_decrements", func(t *testing.T) {
		o := StoreOptions{depth: 2}
		n := o.Next()
		assert.Equal(t, 1, n.Depth())
	})
	t.Run("depth_zero_stays_zero", func(t *testing.T) {
		o := StoreOptions{depth: 0}
		n := o.Next()
		assert.Equal(t, 0, n.Depth())
	})
}

// ---------------------------------------------------------------------------
// server.go — ProjDbServer.Validate inner error paths
// ---------------------------------------------------------------------------

func TestProjDbServer_Validate_innerErrors(t *testing.T) {
	t.Run("invalid_project_item_via_validate_with_options", func(t *testing.T) {
		// ID matches GetID but ProjItemBrief validation fails because of invalid access
		v := ProjDbServer{
			ProjectItem: ProjectItem{
				ProjItemBrief: ProjItemBrief{ID: "mysql:localhost"},
				Access:        "bogus", // invalid access — triggers ValidateWithOptions error
			},
			Server: ServerRef{Driver: "mysql", Host: "localhost"},
		}
		assert.Error(t, v.Validate())
	})
	t.Run("invalid_server_ref", func(t *testing.T) {
		// ID matches GetID but server.Validate() returns error
		v := ProjDbServer{
			ProjectItem: ProjectItem{
				ProjItemBrief: ProjItemBrief{ID: "baddriver:localhost"},
			},
			Server: ServerRef{Driver: "baddriver", Host: "localhost"},
		}
		assert.Error(t, v.Validate())
	})
	t.Run("invalid_catalog", func(t *testing.T) {
		// ID matches, server valid, catalog has empty ID which fails Validate
		v := ProjDbServer{
			ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql:localhost"}},
			Server:      ServerRef{Driver: "mysql", Host: "localhost"},
			Catalogs: DbCatalogs{{
				DbCatalogBase: DbCatalogBase{
					ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: ""}},
					Driver:      "mysql",
				},
			}},
		}
		assert.Error(t, v.Validate())
	})
}

// ---------------------------------------------------------------------------
// server.go — ProjDbDrivers.IDs and GetByID
// ---------------------------------------------------------------------------

func TestProjDbDrivers_IDs(t *testing.T) {
	drivers := ProjDbDrivers{
		{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql"}}},
		{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "sqlite3"}}},
	}
	assert.Equal(t, []string{"mysql", "sqlite3"}, drivers.IDs())
}

func TestProjDbDrivers_GetByID(t *testing.T) {
	d := &ProjDbDriver{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql"}}}
	drivers := ProjDbDrivers{d}
	assert.Equal(t, d, drivers.GetByID("mysql"))
	assert.Nil(t, drivers.GetByID("other"))
}

// ---------------------------------------------------------------------------
// server.go — ProjDbDriver.Validate error path (invalid tags via ProjItemBrief.Validate)
// ---------------------------------------------------------------------------

func TestProjDbDriver_Validate_invalidTags(t *testing.T) {
	// v.ProjItemBrief.Validate() delegates to ListOfTags.Validate()
	// An empty tag triggers the error in that path (line 143-145)
	v := ProjDbDriver{
		ProjectItem: ProjectItem{
			ProjItemBrief: ProjItemBrief{
				ID:         "mysql",
				ListOfTags: ListOfTags{Tags: []string{""}}, // empty tag → error
			},
		},
	}
	assert.Error(t, v.Validate())
}

// ---------------------------------------------------------------------------
// project.go — NewProjectWithStore
// ---------------------------------------------------------------------------

func TestNewProjectWithStore(t *testing.T) {
	store := mockProjectLoader{}
	p := NewProjectWithStore("p1", store)
	require.NotNil(t, p)
	assert.Equal(t, "p1", p.ID)
	assert.NotNil(t, p.store)
}

// ---------------------------------------------------------------------------
// project.go — GetDBs, GetProjDbServer, AddProjDbServer
// ---------------------------------------------------------------------------

type mockProjDbDriversLoader struct {
	ProjectStore
	drivers ProjDbDrivers
	err     error
}

func (m mockProjDbDriversLoader) LoadProjDbDrivers(_ context.Context, _ ...StoreOption) (ProjDbDrivers, error) {
	return m.drivers, m.err
}

func TestProject_GetDBs(t *testing.T) {
	ctx := context.Background()
	t.Run("loads_from_store", func(t *testing.T) {
		drivers := ProjDbDrivers{{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql"}}}}
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{drivers: drivers})
		dbs, err := p.GetDBs(ctx)
		require.NoError(t, err)
		assert.Equal(t, drivers, dbs)
	})
	t.Run("uses_cached_drivers", func(t *testing.T) {
		drivers := ProjDbDrivers{{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "cached"}}}}
		p := Project{
			DbDrivers: drivers,
			store:     mockProjDbDriversLoader{},
		}
		dbs, err := p.GetDBs(ctx)
		require.NoError(t, err)
		assert.Equal(t, drivers, dbs)
	})
	t.Run("store_error", func(t *testing.T) {
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{err: errors.New("load error")})
		_, err := p.GetDBs(ctx)
		assert.Error(t, err)
	})
}

func TestProject_GetProjDbServer(t *testing.T) {
	ctx := context.Background()
	ref := ServerRef{Driver: "mysql", Host: "localhost", Port: 3306}
	server := &ProjDbServer{
		ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql:localhost:3306"}},
		Server:      ref,
	}
	drivers := ProjDbDrivers{{
		ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql"}},
		Servers:     ProjDbServers{server},
	}}

	t.Run("found", func(t *testing.T) {
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{drivers: drivers})
		s, err := p.GetProjDbServer(ctx, ref)
		require.NoError(t, err)
		assert.Equal(t, server, s)
	})
	t.Run("not_found_no_match", func(t *testing.T) {
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{drivers: drivers})
		s, err := p.GetProjDbServer(ctx, ServerRef{Driver: "sqlite3"})
		require.NoError(t, err)
		assert.Nil(t, s)
	})
	t.Run("store_error", func(t *testing.T) {
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{err: errors.New("fail")})
		_, err := p.GetProjDbServer(ctx, ref)
		assert.Error(t, err)
	})
}

func TestProject_AddProjDbServer(t *testing.T) {
	ctx := context.Background()
	ref := ServerRef{Driver: "mysql", Host: "localhost", Port: 3306}
	dbServer := &ProjDbServer{
		ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql:localhost:3306"}},
		Server:      ref,
	}

	t.Run("store_error", func(t *testing.T) {
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{err: errors.New("fail")})
		err := p.AddProjDbServer(ctx, dbServer)
		assert.Error(t, err)
	})
	t.Run("new_driver", func(t *testing.T) {
		// No existing drivers; creates a new ProjDbDriver
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{drivers: ProjDbDrivers{}})
		err := p.AddProjDbServer(ctx, dbServer)
		require.NoError(t, err)
		// The driver should have been added
		assert.NotEmpty(t, p.DbDrivers)
	})
	t.Run("existing_driver", func(t *testing.T) {
		existingDriver := &ProjDbDriver{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "mysql"}}}
		p := NewProjectWithStore("p1", mockProjDbDriversLoader{drivers: ProjDbDrivers{existingDriver}})
		err := p.AddProjDbServer(ctx, dbServer)
		require.NoError(t, err)
		// Server was appended to existing driver
		assert.NotEmpty(t, existingDriver.Servers)
	})
}

// ---------------------------------------------------------------------------
// db_model.go — DbModels.GetByID and TableModel.Validate invalid key
// ---------------------------------------------------------------------------

func TestDbModels_GetByID(t *testing.T) {
	m := &DbModel{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "m1"}}}
	models := DbModels{m}
	assert.Equal(t, m, models.GetByID("m1"))
	assert.Nil(t, models.GetByID("missing"))
}

// Note: DBCollectionKey.Validate() always returns nil per current implementation,
// so the TableModel.Validate() error branch for invalid DBCollectionKey cannot be triggered
// without modifying production code. Documented as a gap.

// ---------------------------------------------------------------------------
// db_objects.go — TableKeys.Validate error path
// ---------------------------------------------------------------------------

// Note: DBCollectionKey.Validate() returns nil unconditionally, so the
// TableKeys.Validate() per-element error branch cannot be triggered without
// modifying production code. Documented as a gap.

// ---------------------------------------------------------------------------
// db_objects.go — ColumnInfo.Validate wrap branch
// ---------------------------------------------------------------------------

func TestColumnInfo_Validate_namedColumnWithInvalidProps(t *testing.T) {
	// DbColumnProps.Validate fails AND Name is non-empty → fmt.Errorf wrap at line 504
	// DbColumnProps.Name="" triggers "missing required field: name" error
	// ColumnInfo embeds DbColumnProps, so Name is in DbColumnProps
	v := ColumnInfo{
		DbColumnProps: DbColumnProps{
			Name:            "mycol",
			OrdinalPosition: -1, // negative → triggers Validate error
		},
	}
	err := v.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mycol")
}

// ---------------------------------------------------------------------------
// dbcatalog.go — DbCatalogs.IDs
// ---------------------------------------------------------------------------

func TestDbCatalogs_IDs(t *testing.T) {
	catalogs := DbCatalogs{
		{DbCatalogBase: DbCatalogBase{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "cat1"}}}},
		{DbCatalogBase: DbCatalogBase{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "cat2"}}}},
	}
	assert.Equal(t, []string{"cat1", "cat2"}, catalogs.IDs())
}

// ---------------------------------------------------------------------------
// entities.go — Entity.Validate Tables error branch
// ---------------------------------------------------------------------------

// Note: DBCollectionKey.Validate() returns nil unconditionally, so
// TableKeys.Validate() never returns an error, making the Entity.Validate()
// Tables error branch unreachable without changing production code.
// Documented as a gap.

// ---------------------------------------------------------------------------
// env_db_server.go — EnvDbServer.GetID and SetID
// ---------------------------------------------------------------------------

func TestEnvDbServer_GetID(t *testing.T) {
	v := &EnvDbServer{ServerRef: ServerRef{Host: "h", Port: 5432}}
	assert.Equal(t, "h:5432", v.GetID())
}

func TestEnvDbServer_SetID(t *testing.T) {
	var v EnvDbServer
	v.SetID("h:5432")
	assert.Equal(t, "h", v.Host)
	assert.Equal(t, 5432, v.Port)
}

// ---------------------------------------------------------------------------
// environment.go — Environments.IDs
// ---------------------------------------------------------------------------

func TestEnvironments_IDs(t *testing.T) {
	envs := Environments{
		{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "prod"}}},
		{ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "staging"}}},
	}
	assert.Equal(t, []string{"prod", "staging"}, envs.IDs())
}

// ---------------------------------------------------------------------------
// folder.go — Folder.GetID and SetID
// ---------------------------------------------------------------------------

func TestFolder_GetID(t *testing.T) {
	f := &Folder{Name: "myFolder"}
	assert.Equal(t, "myFolder", f.GetID())
}

func TestFolder_SetID(t *testing.T) {
	var f Folder
	f.SetID("myFolder")
	assert.Equal(t, "myFolder", f.Name)
}

// ---------------------------------------------------------------------------
// proj_item.go — ProjectItem.GetProjectItem
// ---------------------------------------------------------------------------

func TestProjectItem_GetProjectItem(t *testing.T) {
	pi := ProjectItem{
		ProjItemBrief: ProjItemBrief{ID: "item1", Title: "Item One"},
		Access:        "public",
	}
	assert.Equal(t, pi, pi.GetProjectItem())
}

// ---------------------------------------------------------------------------
// query.go — QueryDef.Validate unsupported type
// ---------------------------------------------------------------------------

func TestQueryDef_Validate_unsupportedType(t *testing.T) {
	v := newQueryDef("UNKNOWN_TYPE", "")
	err := v.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported value")
}

// ---------------------------------------------------------------------------
// recordset_def.go — validateKeyColumnNames reaches return nil
// ---------------------------------------------------------------------------

func TestRecordsetDefinition_Validate_validPrimaryKey(t *testing.T) {
	// PrimaryKey references a valid, non-duplicate existing column → closure returns nil
	v := RecordsetDefinition{
		ProjectItem: ProjectItem{ProjItemBrief: ProjItemBrief{ID: "r1", Title: "T"}},
		Type:        "recordset",
		Columns:     RecordsetColumnDefs{{Name: "id", Type: "string"}, {Name: "name", Type: "string"}},
		RecordsetBaseDef: RecordsetBaseDef{
			PrimaryKey: &UniqueKey{Columns: []string{"id"}},
		},
	}
	assert.NoError(t, v.Validate())
}

// ---------------------------------------------------------------------------
// db_collection.go — CollectionInfo.Validate DBCollectionKey error path
// ---------------------------------------------------------------------------

// Note: DBCollectionKey.Validate() returns nil unconditionally per current
// implementation. Therefore CollectionInfo.Validate's DBCollectionKey.Validate()
// error branch cannot be triggered without refactoring. Documented as a gap.

// ---------------------------------------------------------------------------
// boards.go — BoardWidget.Validate widget.Validate() error wrap
// ---------------------------------------------------------------------------

func TestBoardWidget_Validate_widgetValidateError(t *testing.T) {
	// SQL widget resolves correctly but its SQL is missing → widget.Validate() returns error
	// This triggers the "failed to test widget of type" error wrap at line 174-176.
	v := BoardWidget{
		Name: "SQL",
		Data: &SQLWidgetDef{SQL: SQLWidgetSettings{Query: ""}}, // missing query
	}
	err := v.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to test widget of type")
}
