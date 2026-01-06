package sqlinfoschema

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/parallel"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

// InformationSchema provides API to retrieve information about a database
type InformationSchema struct {
	db     *sql.DB
	server datatug.ServerRef
}

// NewInformationSchema creates new InformationSchema
func NewInformationSchema(server datatug.ServerRef /*, db *sql.DB*/) InformationSchema {
	return InformationSchema{server: server /*, db: db*/}
}

// GetDatabase returns complete information about a database
func (s InformationSchema) GetDatabase(name string) (database *datatug.DbCatalog, err error) {
	database = new(datatug.DbCatalog)
	database.ID = name
	var tables []*datatug.CollectionInfo
	if tables, err = s.getTables(name); err != nil {
		return database, fmt.Errorf("failed to retrieve tables metadata: %w", err)
	}
	for _, t := range tables {
		schema := database.Schemas.GetByID(t.Schema())
		if schema == nil {
			schema = new(datatug.DbSchema)
			schema.ID = t.Schema()
			database.Schemas = append(database.Schemas, schema)
		}
		switch t.Name() {
		case "BASE TABLE":
			schema.Tables = append(schema.Tables, t)
		case "VIEW":
			schema.Views = append(schema.Views, t)
		default:
			err = fmt.Errorf("object [%s] has unknown DB type: %v", t.Name(), t.DbType)
			return
		}
	}
	log.Printf("%v schemas.", len(database.Schemas))
	err = parallel.Run(
		func() error {
			if err = s.getColumns(name, schemer.SortedTables{Tables: tables}); err != nil {
				return fmt.Errorf("failed to retrieve columns metadata: %w", err)
			}
			return nil
		},
		func() error {
			if err = s.getConstraints(name, schemer.SortedTables{Tables: tables}); err != nil {
				return fmt.Errorf("failed to retrieve constraints metadata: %w", err)
			}
			return nil
		},
		func() error {
			if err = s.getIndexes(name, schemer.SortedTables{Tables: tables}); err != nil {
				return fmt.Errorf("failed to retrieve indexes metadata: %w", err)
			}
			return nil
		},
	)
	log.Println("GetDatabase completed")
	return
}

func (s InformationSchema) getTables(catalog string) (tables []*datatug.CollectionInfo, err error) {
	var rows *sql.Rows
	//goland:noinspection SqlNoDataSourceInspection
	rows, err = s.db.Query(`SELECT
       TABLE_SCHEMA,
       TABLE_NAME,
       TABLE_TYPE
FROM INFORMATION_SCHEMA.TABLES
ORDER BY TABLE_SCHEMA, TABLE_NAME`)
	defer func() {
		_ = rows.Close()
	}()
	if err != nil {
		return nil, fmt.Errorf("failed to query INFORMATION_SCHEMA.TABLES: %w", err)
	}
	tables = make([]*datatug.CollectionInfo, 0)
	for rows.Next() {
		var schema, name, dbType string
		if err = rows.Scan(&schema, &name, &dbType); err != nil {
			return nil, fmt.Errorf("failed to scan table row into CollectionInfo struct: %w", err)
		}
		var collectionType datatug.CollectionType
		switch strings.ToUpper(dbType) {
		case "BASE TABLE":
			collectionType = datatug.CollectionTypeTable
		case "VIEW":
			collectionType = datatug.CollectionTypeView
		default:
			err = fmt.Errorf("unsupported DB type: %s", dbType)
			return
		}
		table := datatug.CollectionInfo{
			DBCollectionKey: datatug.NewCollectionKey(
				collectionType,
				name,
				schema,
				catalog,
				nil,
			),
		}
		tables = append(tables, &table)
	}
	log.Printf("Retrieved %v tables.", len(tables))
	//for i, t := range tables {
	//	log.Printf("\t%v: %v: %+v", i, t.ID, t)
	//}
	return tables, err
}

func (s InformationSchema) getConstraints(catalog string, tablesFinder schemer.SortedTables) (err error) {
	log.Println("Getting constraints...")
	provider := ConstraintsProvider{DB: s.db, SQL: `SELECT
	tc.TABLE_SCHEMA, tc.TABLE_NAME,
    tc.CONSTRAINT_TYPE, kcu.CONSTRAINT_NAME,
    kcu.COLUMN_NAME,-- kcu.ORDINAL_POSITION,
	rc.UNIQUE_CONSTRAINT_CATALOG, rc.UNIQUE_CONSTRAINT_SCHEMA, rc.UNIQUE_CONSTRAINT_NAME,
    rc.MATCH_OPTION, rc.UPDATE_RULE, rc.DELETE_RULE,
	kcu2.TABLE_CATALOG AS REF_TABLE_CATALOG, kcu2.TABLE_SCHEMA AS REF_TABLE_SCHEMA, kcu2.TABLE_NAME AS REF_TABLE_NAME, kcu2.COLUMN_NAME AS REF_COL_NAME
FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE AS kcu
INNER JOIN INFORMATION_SCHEMA.TABLE_CONSTRAINTS AS tc ON tc.CONSTRAINT_CATALOG = kcu.CONSTRAINT_CATALOG AND tc.CONSTRAINT_SCHEMA = kcu.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
LEFT JOIN INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS AS rc ON tc.CONSTRAINT_TYPE = 'FOREIGN KEY' AND rc.CONSTRAINT_CATALOG = tc.CONSTRAINT_CATALOG AND rc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND rc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
LEFT JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE AS kcu2 ON kcu2.CONSTRAINT_CATALOG = rc.UNIQUE_CONSTRAINT_CATALOG AND kcu2.CONSTRAINT_SCHEMA = rc.UNIQUE_CONSTRAINT_SCHEMA AND kcu2.CONSTRAINT_NAME = rc.UNIQUE_CONSTRAINT_NAME AND kcu2.ORDINAL_POSITION = kcu.ORDINAL_POSITION
ORDER BY tc.TABLE_SCHEMA, tc.TABLE_NAME, tc.CONSTRAINT_TYPE, kcu.CONSTRAINT_NAME, kcu.ORDINAL_POSITION`}
	reader, err := provider.GetConstraints(context.Background(), "", "", "")
	if err != nil {
		return err
	}

	for {
		constraint, err := reader.NextConstraint()
		if err != nil {
			return err
		}
		if constraint == nil {
			break
		}

		if table := tablesFinder.SequentialFind(catalog, constraint.SchemaName, constraint.TableName); table != nil {
			switch constraint.Type {
			case "PRIMARY KEY":
				if table.PrimaryKey == nil {
					table.PrimaryKey = &datatug.UniqueKey{Name: constraint.Name, Columns: []string{constraint.ColumnName}}
				} else {
					table.PrimaryKey.Columns = append(table.PrimaryKey.Columns, constraint.ColumnName)
				}
			case "UNIQUE":
				if len(table.UniqueKeys) > 0 && table.UniqueKeys[len(table.UniqueKeys)-1].Name == constraint.Name {
					i := len(table.UniqueKeys) - 1
					table.UniqueKeys[i].Columns = append(table.UniqueKeys[i].Columns, constraint.ColumnName)
				} else {
					table.UniqueKeys = append(table.UniqueKeys, &datatug.UniqueKey{Name: constraint.Name, Columns: []string{constraint.ColumnName}})
				}
			case "FOREIGN KEY":
				if len(table.ForeignKeys) > 0 && table.ForeignKeys[len(table.ForeignKeys)-1].Name == constraint.Name {
					i := len(table.ForeignKeys) - 1
					table.ForeignKeys[i].Columns = append(table.ForeignKeys[i].Columns, constraint.ColumnName)
				} else {
					//refTable := refTableFinder.FindTable(refTableCatalog, refTableSchema, refTableName)
					fk := datatug.ForeignKey{
						Name:     constraint.Name,
						Columns:  []string{constraint.ColumnName},
						RefTable: datatug.NewTableKey(constraint.RefTableName, constraint.RefTableSchema, constraint.RefTableCatalog, nil),
					}
					if constraint.MatchOption != "" {
						fk.MatchOption = constraint.MatchOption
					}
					if constraint.UpdateRule != "" {
						fk.UpdateRule = constraint.UpdateRule
					}
					if constraint.DeleteRule != "" {
						fk.DeleteRule = constraint.DeleteRule
					}
					table.ForeignKeys = append(table.ForeignKeys, &fk)

					{ // Update reference table
						refTable := schemer.FindTable(tablesFinder.Tables, constraint.RefTableCatalog, constraint.RefTableSchema, constraint.RefTableName)
						var refByFk *datatug.RefByForeignKey
						if refTable == nil {
							return fmt.Errorf("reference table not found: %v.%v.%v", constraint.RefTableCatalog, constraint.RefTableSchema, constraint.RefTableName)
						}
						var refByTable *datatug.ReferencedBy
						for _, refByTable = range refTable.ReferencedBy {
							if refByTable.Catalog() == catalog && refByTable.Schema() == constraint.SchemaName && refByTable.Name() == constraint.TableName {
								break
							}
						}
						if refByTable == nil || refByTable.Catalog() != catalog || refByTable.Schema() != constraint.SchemaName || refByTable.Name() != constraint.TableName {
							refByTable = &datatug.ReferencedBy{
								DBCollectionKey: table.DBCollectionKey,
								ForeignKeys:     make([]*datatug.RefByForeignKey, 0, 1),
							}
							refTable.ReferencedBy = append(refTable.ReferencedBy, refByTable)
						}
						for _, fk2 := range refByTable.ForeignKeys {
							if fk2.Name == fk.Name {
								refByFk = fk2
								goto fkAddedToRefByTable
							}
						}
						refByFk = &datatug.RefByForeignKey{
							Name:        fk.Name,
							MatchOption: fk.MatchOption,
							UpdateRule:  fk.UpdateRule,
							DeleteRule:  fk.DeleteRule,
						}
						refByTable.ForeignKeys = append(refByTable.ForeignKeys, refByFk)
					fkAddedToRefByTable:
						refByFk.Columns = append(refByFk.Columns, constraint.ColumnName)
					}
				}
			}
		} else {
			log.Printf("CollectionInfo not found: %v.%v.%v, table: %+v", catalog, constraint.SchemaName, constraint.TableName, table)
			continue
		}
	}

	return nil
}

func (s InformationSchema) getColumns(catalog string, tablesFinder schemer.SortedTables) (err error) {
	log.Println("Getting columns...")
	provider := ColumnsProvider{DB: s.db, SQL: `SELECT
    TABLE_SCHEMA,
    TABLE_NAME,
    COLUMN_NAME,
    ORDINAL_POSITION,
    COLUMN_DEFAULT,
    IS_NULLABLE,
    DATA_TYPE,
    CHARACTER_MAXIMUM_LENGTH,
    CHARACTER_OCTET_LENGTH,
	CHARACTER_SET_CATALOG,
	CHARACTER_SET_SCHEMA,
    CHARACTER_SET_NAME,
	COLLATION_CATALOG,
	COLLATION_SCHEMA,
    COLLATION_NAME
FROM INFORMATION_SCHEMA.COLUMNS ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`}
	columns, err := provider.GetColumns(context.Background(), "", schemer.ColumnsFilter{})
	if err != nil {
		return err
	}

	for _, column := range columns {
		if table := tablesFinder.SequentialFind(catalog, column.SchemaName, column.TableName); table != nil {
			table.Columns = append(table.Columns, &column.ColumnInfo)
		} else {
			log.Printf("CollectionInfo not found: %v.%v.%v", catalog, column.SchemaName, column.TableName)
			continue
		}
	}
	fmt.Println("Processed columns:", len(columns))
	return err
}

func (s InformationSchema) getIndexes(catalog string, tablesFinder schemer.SortedTables) (err error) {
	log.Println("Getting indexes...")
	provider := IndexesProvider{DB: s.db, SQL: `SELECT 
    SCHEMA_NAME(o.schema_id) AS schema_name,
	o.name AS object_name,
    CASE WHEN o.type = 'U' THEN 'CollectionInfo'
        WHEN o.type = 'V' THEN 'View'
		ELSE o.type
        END AS object_type,
	i.name,
	i.type,
	i.type_desc,
	is_unique,
	is_primary_key,
	is_unique_constraint
FROM sys.indexes AS i
INNER JOIN sys.objects o ON o.object_id = i.object_id
WHERE o.is_ms_shipped <> 1 --and index_id > 0
ORDER BY SCHEMA_NAME(o.schema_id) + '.' + o.name, i.name;`}
	reader, err := provider.GetIndexes(context.Background(), "", "", "")
	if err != nil {
		return err
	}

	for {
		index, err := reader.NextIndex()
		if err != nil {
			return err
		}
		if index == nil {
			break
		}

		if table := tablesFinder.SequentialFind(catalog, index.SchemaName, index.TableName); table != nil {
			table.Indexes = append(table.Indexes, index.Index)
		} else {
			log.Printf("CollectionInfo not found: %v.%v.%v", catalog, index.SchemaName, index.TableName)
			continue
		}
	}
	return nil
}
