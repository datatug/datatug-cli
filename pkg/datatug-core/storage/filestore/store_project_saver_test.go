package filestore

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/stretchr/testify/assert"
)

func TestSaveProject(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "datatug_test_save_project")
	assert.NoError(t, err)
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	projectID := "test_save_project"
	projectPath := path.Join(tmpDir, projectID)

	store := newFsProjectStore(projectID, projectPath)

	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{
			Access: "public",
			ProjItemBrief: datatug.ProjItemBrief{
				ID:    projectID,
				Title: "Test Project",
			},
		},
		Created: &datatug.ProjectCreated{
			At: time.Now(),
		},
		DbModels: datatug.DbModels{
			{
				ProjectItem: datatug.ProjectItem{
					ProjItemBrief: datatug.ProjItemBrief{
						ID:    "model1",
						Title: "Model 1",
					},
				},
			},
		},
		Environments: datatug.Environments{
			{
				ProjectItem: datatug.ProjectItem{
					ProjItemBrief: datatug.ProjItemBrief{
						ID:    "env1",
						Title: "Env 1",
					},
				},
			},
		},
		Entities: datatug.Entities{
			{
				ProjectItem: datatug.ProjectItem{
					ProjItemBrief: datatug.ProjItemBrief{
						ID:    "entity1",
						Title: "Entity 1",
					},
				},
			},
		},
		Boards: datatug.Boards{
			{
				ProjectItem: datatug.ProjectItem{
					ProjItemBrief: datatug.ProjItemBrief{
						ID:    "board1",
						Title: "Board 1",
					},
				},
			},
		},
	}

	t.Run("SaveProject_Full", func(t *testing.T) {
		err := store.SaveProject(context.Background(), project)
		assert.NoError(t, err)

		// Verify project file exists
		assert.FileExists(t, path.Join(projectPath, storage.ProjectSummaryFileName))
	})

	t.Run("SaveProject_MissingProjectID", func(t *testing.T) {
		err := store.SaveProject(context.Background(), &datatug.Project{})
		assert.Error(t, err)
	})
}

func TestSaveProject_PersistsDbModels(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "datatug_test_save_dbmodels")
	assert.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	const projectID = "p1"
	projectPath := path.Join(tmpDir, projectID)
	store := newFsProjectStore(projectID, projectPath)

	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{
			Access:        "private",
			ProjItemBrief: datatug.ProjItemBrief{ID: projectID},
		},
		Created: &datatug.ProjectCreated{At: time.Now()},
		DbModels: datatug.DbModels{
			{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "m1"}},
				Schemas: datatug.SchemaModels{
					{
						ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "main"}},
						Tables: datatug.TableModels{
							{
								DBCollectionKey: datatug.NewCollectionKey(datatug.CollectionTypeTable, "widgets", "main", "", nil),
								DbType:          "BASE TABLE",
								Columns: datatug.ColumnModels{
									{ColumnInfo: datatug.ColumnInfo{DbColumnProps: datatug.DbColumnProps{Name: "id", DbType: "INTEGER"}}},
								},
								PrimaryKey: &datatug.UniqueKey{Name: "PK_widgets", Columns: []string{"id"}},
								ForeignKeys: datatug.ForeignKeys{
									{Name: "FK_widgets_0", Columns: []string{"id"}, RefTable: datatug.NewCollectionKey(datatug.CollectionTypeTable, "gadgets", "main", "", nil)},
								},
								Indexes: []*datatug.Index{
									{Name: "idx_widgets_id", Type: "BTREE", Columns: []*datatug.IndexColumn{{Name: "id"}}},
								},
							},
						},
					},
				},
			},
		},
	}

	if err := store.SaveProject(context.Background(), project); err != nil {
		t.Fatalf("SaveProject failed: %v", err)
	}

	// The DB-model file is now written (previously "NOT IMPLEMENTED YET").
	modelFile := path.Join(projectPath, storage.DbModelsFolder, storage.JsonFileName("m1", storage.DbModelFileSuffix))
	assert.FileExists(t, modelFile)

	// Round-trip: load the DB models back and confirm the table and column survived.
	loaded, err := newFsDbModelsStore(projectPath).LoadDbModels(context.Background())
	assert.NoError(t, err)
	if assert.Len(t, loaded, 1) &&
		assert.Len(t, loaded[0].Schemas, 1) &&
		assert.Len(t, loaded[0].Schemas[0].Tables, 1) {
		table := loaded[0].Schemas[0].Tables[0]
		assert.Equal(t, "widgets", table.Name)
		if assert.Len(t, table.Columns, 1) {
			assert.Equal(t, "id", table.Columns[0].Name)
		}
		// Primary key, foreign keys and indexes must round-trip too.
		if assert.NotNil(t, table.PrimaryKey) {
			assert.Equal(t, []string{"id"}, table.PrimaryKey.Columns)
		}
		if assert.Len(t, table.ForeignKeys, 1) {
			assert.Equal(t, "gadgets", table.ForeignKeys[0].RefTable.Name)
		}
		if assert.Len(t, table.Indexes, 1) {
			assert.Equal(t, "idx_widgets_id", table.Indexes[0].Name)
		}
	}
}
