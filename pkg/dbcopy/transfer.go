package dbcopy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
)

// CopyTable is one fully described source collection in a provider-neutral
// transfer. Ref retains the source namespace; Definition carries portable
// schema and constraint metadata.
type CopyTable struct {
	Ref        dal.CollectionRef
	Definition *dbschema.CollectionDef
}

// CopySink plans a target-specific representation without changing the target.
// It is the seam that lets `datatug db copy` transfer between DALgo sources and
// provider-owned bulk writers while keeping traversal and value streaming here.
type CopySink interface {
	Preflight(context.Context, []CopyTable) (CopySinkPlan, error)
}

// CopySinkPlan is target-specific but operates on source collections and row
// values, so no source-provider input preparation is required.
type CopySinkPlan interface {
	EncodeRow(CopyTable, dbschema.SourceRow) ([]byte, error)
	TargetName(CopyTable) string
	Prepare(context.Context) error
	LoadTable(context.Context, CopyTable, io.Reader) (targetName string, rows int64, err error)
}

type copyPlanWarnings interface{ Warnings() []string }

var removeCopyStagingDir = os.RemoveAll

// ErrStagingCleanup means temporary source rows may remain on local disk.
var ErrStagingCleanup = errors.New("transfer staging cleanup failed; temporary source files may remain")

// CopyToSink performs a provider-neutral transfer. It fully introspects and
// stages every supported source row before calling Prepare, so an unsupported
// schema or value cannot leave a partially created destination.
func CopyToSink(ctx context.Context, source dal.DB, sink CopySink, opts CopyOpts) (summary SourceSummary, retErr error) {
	summary = SourceSummary{TargetBackend: "provider-bulk-writer", RowsByTable: map[string]int64{}, RowSkips: map[string]string{}}
	if source == nil || sink == nil {
		return summary, errors.New("copy source and target are required")
	}
	refs, err := dbschema.ListCollections(ctx, source, nil)
	if err != nil {
		return summary, fmt.Errorf("list source collections: %w", err)
	}
	if len(refs) == 0 {
		return summary, ErrSourceHasNoTables
	}
	refs, err = filter.ApplyTableFilter(refs, opts.Filters)
	if err != nil {
		return summary, err
	}
	summary.Tables = len(refs)
	if len(refs) == 0 {
		return summary, nil
	}

	tables := make([]CopyTable, 0, len(refs))
	for _, ref := range refs {
		def, err := dbschema.DescribeCollection(ctx, source, &ref)
		if err != nil {
			return summary, fmt.Errorf("describe source collection %q: %w", ref.Path(), err)
		}
		if def == nil || def.Name != ref.Name() {
			return summary, fmt.Errorf("source collection %q returned inconsistent schema", ref.Path())
		}
		tables = append(tables, CopyTable{Ref: ref, Definition: def})
	}
	plan, err := sink.Preflight(ctx, tables)
	if err != nil {
		return summary, fmt.Errorf("preflight target: %w", err)
	}
	if plan == nil {
		return summary, errors.New("target returned an empty transfer plan")
	}
	if warningPlan, ok := plan.(copyPlanWarnings); ok {
		summary.Warnings = append(summary.Warnings, warningPlan.Warnings()...)
	}

	stageDir, err := os.MkdirTemp("", "datatug-db-copy-")
	if err != nil {
		return summary, fmt.Errorf("create transfer staging directory: %w", err)
	}
	defer func() {
		if removeCopyStagingDir(stageDir) != nil {
			// The underlying OS error can contain the machine-specific temp path.
			if retErr != nil {
				retErr = errors.Join(retErr, ErrStagingCleanup)
			} else {
				retErr = ErrStagingCleanup
			}
		}
	}()
	staged := make(map[string]string, len(tables))
	for _, table := range tables {
		path := filepath.Join(stageDir, fmt.Sprintf("table-%03d.ndjson", len(staged)))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return summary, fmt.Errorf("stage source collection %q: %w", table.Ref.Path(), err)
		}
		writer := bufio.NewWriterSize(file, 128*1024)
		var rows int64
		var copyErr error
		if !opts.SchemaOnly {
			rows, copyErr = stageCopyRows(ctx, source, table, plan, opts, writer)
		}
		flushErr := writer.Flush()
		closeErr := file.Close()
		if copyErr != nil {
			return summary, copyErr
		}
		if flushErr != nil {
			return summary, fmt.Errorf("flush staged collection %q: %w", table.Ref.Path(), flushErr)
		}
		if closeErr != nil {
			return summary, fmt.Errorf("close staged collection %q: %w", table.Ref.Path(), closeErr)
		}
		staged[table.Ref.Path()] = path
		summary.RowsByTable[table.Ref.Path()] = rows
	}

	if err := plan.Prepare(ctx); err != nil {
		return summary, fmt.Errorf("prepare target: %w", err)
	}
	if opts.SchemaOnly {
		for _, table := range tables {
			summary.Created++
			summary.CreatedNames = append(summary.CreatedNames, plan.TargetName(table))
		}
		return summary, nil
	}
	for _, table := range tables {
		path := staged[table.Ref.Path()]
		file, err := os.Open(path)
		if err != nil {
			return summary, fmt.Errorf("open staged collection %q: %w", table.Ref.Path(), err)
		}
		targetName, rows, loadErr := plan.LoadTable(ctx, table, file)
		closeErr := file.Close()
		if loadErr != nil {
			return summary, fmt.Errorf("load target collection %q: %w", table.Ref.Path(), loadErr)
		}
		if closeErr != nil {
			return summary, fmt.Errorf("close staged collection %q: %w", table.Ref.Path(), closeErr)
		}
		if rows != summary.RowsByTable[table.Ref.Path()] {
			return summary, fmt.Errorf("target row count for %q differs from staged source (%d != %d)", table.Ref.Path(), rows, summary.RowsByTable[table.Ref.Path()])
		}
		summary.Created++
		summary.CreatedNames = append(summary.CreatedNames, targetName)
		summary.RowsCopied += rows
	}
	return summary, nil
}

func stageCopyRows(ctx context.Context, source dal.DB, table CopyTable, plan CopySinkPlan, opts CopyOpts, writer io.Writer) (rows int64, err error) {
	encode := func(row dbschema.SourceRow) error {
		if err := normalizeSQLiteBooleanValues(table, row); err != nil {
			return fmt.Errorf("normalize source row from %q: %w", table.Ref.Path(), err)
		}
		line, err := plan.EncodeRow(table, row)
		if err != nil {
			return fmt.Errorf("encode source row from %q: %w", table.Ref.Path(), err)
		}
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return errors.New("target row encoder must return one newline-terminated JSON record")
		}
		if _, err := writer.Write(line); err != nil {
			return err
		}
		rows++
		return nil
	}

	if opts.Filters == nil || !opts.Filters.HasRowFilters() {
		if sourceRows, ok := dal.As[dbschema.SourceRowsReader](source); ok {
			cursor, err := sourceRows.OpenSourceRows(ctx, &table.Ref)
			if err != nil {
				return rows, fmt.Errorf("open physical rows for %q: %w", table.Ref.Path(), err)
			}
			defer func() {
				if closeErr := cursor.Close(); closeErr != nil && err == nil {
					err = fmt.Errorf("close physical rows for %q: %w", table.Ref.Path(), closeErr)
				}
			}()
			for {
				row, err := cursor.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return rows, fmt.Errorf("read physical row from %q: %w", table.Ref.Path(), err)
				}
				if err := encode(row); err != nil {
					return rows, err
				}
			}
			return rows, nil
		}
	}

	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(table.Ref))
	if opts.Filters != nil {
		if group, ok := opts.Filters.Where[table.Definition.Name]; ok && group != nil {
			conds, err := filter.CompileWhereForTable(table.Definition.Name, group, table.Definition)
			if err != nil {
				return rows, fmt.Errorf("compile where for %q: %w", table.Ref.Path(), err)
			}
			if adapterName(source) == sqliteAdapterName {
				conds = sqliteTextTimeConstants(conds)
			}
			if len(conds) > 0 {
				builder = builder.Where(conds...)
			}
		}
		if limit, ok := opts.Filters.LimitsByTable[table.Definition.Name]; ok && limit > 0 {
			builder = builder.Limit(limit)
		}
	}
	reader, err := source.ExecuteQueryToRecordsReader(ctx, builder.SelectIntoRecordset())
	if err != nil {
		return rows, fmt.Errorf("query source collection %q: %w", table.Ref.Path(), err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close recordset reader for %q: %w", table.Ref.Path(), closeErr)
		}
	}()
	for {
		rec, err := reader.Next()
		if errors.Is(err, io.EOF) || errors.Is(err, dal.ErrNoMoreRecords) {
			break
		}
		if err != nil {
			return rows, fmt.Errorf("read source collection %q: %w", table.Ref.Path(), err)
		}
		values, ok := rec.Data().(map[string]any)
		if !ok {
			return rows, fmt.Errorf("source collection %q returned record data %T, expected map[string]any", table.Ref.Path(), rec.Data())
		}
		if err := encode(dbschema.SourceRow{Values: values}); err != nil {
			return rows, err
		}
	}
	return rows, nil
}

func normalizeSQLiteBooleanValues(table CopyTable, row dbschema.SourceRow) error {
	definition := table.Definition
	if definition == nil || definition.SourceDefinition == nil || definition.SourceDefinition.Dialect != "sqlite" {
		return nil
	}
	for _, field := range definition.Fields {
		name := string(field.Name)
		declared := sourceDeclaredType(definition.SourceDefinition, name)
		if declared != "bool" && declared != "boolean" {
			continue
		}
		if field.Type != dbschema.Bool {
			return fmt.Errorf("SQLite column %q declares %s but its portable type is %s", name, declared, field.Type)
		}
		value, exists := row.Values[name]
		if !exists {
			return fmt.Errorf("SQLite boolean column %q is missing from the source row", name)
		}
		if value == nil {
			continue
		}
		boolean, err := sqliteBooleanValue(value)
		if err != nil {
			return fmt.Errorf("SQLite boolean column %q: %w", name, err)
		}
		row.Values[name] = boolean
	}
	return nil
}

func sqliteBooleanValue(value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case int64:
		if v == 0 || v == 1 {
			return v == 1, nil
		}
	}
	return false, fmt.Errorf("value has unsupported representation %T; expected BOOLEAN, INTEGER 0/1, or NULL", value)
}
