package commands

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Resolve only the single-source path here. Federated queries retain their own
// validation and ClickHouse refusal in secureread before any source is opened.
func savedQuerySourceForClickHouse(ctx context.Context, store datatug.ProjectStore, dir, env string, q *datatug.QueryDef) (resolvedQuerySource, error) {
	if q.Type == datatug.QueryTypeTugQL {
		urls, err := federatedTugQLURLs(ctx, store, dir, env, q)
		if err != nil {
			return resolvedQuerySource{}, err
		}
		if len(urls) > 0 {
			return resolvedQuerySource{}, nil
		}
	}
	return resolveSQLOrTugQLSource(ctx, store, dir, env, q)
}

// Keep command dispatch testable without changing session construction or the
// executor's policy, source and provider boundaries.
var runClickHouseStructured = func(ctx context.Context, session secureread.Session, source string, query dal.Query, variables map[string]any) (secureread.Result, error) {
	return secureread.NewExecutor(session).RunStructured(ctx, source, query, variables)
}
var runNativeSQLCLI = (*secureread.Executor).RunNativeSQL
var runSavedTugQLCLI = (*secureread.Executor).RunTugQL

func runClickHouseQuery(cmd *cobra.Command, o queryOptions, backend dbcopy.BackendRef, query dal.Query, text string, variables map[string]any, policies []accesspolicies.Loaded, principal *access.Principal) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if o.from != "" {
		if !o.limitSet {
			o.limit = 100
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "limit: reading at most 100 rows (ClickHouse exploration default)")
		}
		// MergeTree has no generic durable key; preserve requested order only.
		query = fromQuery(o, nil)
	}
	session := secureread.Session{Policies: policies, Principal: principal, Unrestricted: o.noPolicies}
	result, err := runClickHouseStructured(ctx, session, backend.Raw, query, variables)
	if err != nil {
		return queryFailureFor(backend, err)
	}
	if !o.quiet {
		writeSavedQueryLimitations(cmd.ErrOrStderr(), result.Limitations)
	}
	if o.sink != nil {
		o.sink.meta.DB.Driver = "clickhouse"
		if ref, ok := fullDatatugRef(ctx, o.db); ok {
			o.sink.meta.DB = projectDB(ref, "clickhouse")
		}
		o.sink.meta.Query.Kind = "tugql"
		if o.from != "" {
			o.sink.meta.Query.Kind = "from"
			o.sink.meta.Query.From = o.from
		}
		o.sink.meta.Query.TugQL = text
		o.sink.meta.Query.Parameters = clickHouseTypedParameters(variables)
		o.sink.meta.Query.Policy = adHocPolicy(o)
		o.sink.meta.SetEditor(text, "tugql")
		return o.sink.writeAnonymous(cmd.OutOrStdout(), result)
	}
	return writeAnonymousResult(cmd.OutOrStdout(), o.format, result)
}

// JSON/YAML carry the existing typed recordset and ordinal native metadata.
// CSV/grid show labels in source order, including duplicates. No $key is added.
func writeAnonymousResult(w io.Writer, format string, result secureread.Result) error {
	if result.AnonymousRecordset == nil {
		return fmt.Errorf("anonymous recordset unavailable")
	}
	if len(result.OrdinalColumns) != len(result.AnonymousRecordset.Columns) {
		return fmt.Errorf("anonymous ordinal metadata unavailable")
	}
	if err := result.AnonymousRecordset.Validate(); err != nil {
		return fmt.Errorf("invalid anonymous recordset")
	}
	for _, col := range result.AnonymousRecordset.Columns {
		if !utf8.ValidString(col.Name) {
			return fmt.Errorf("invalid anonymous column encoding")
		}
	}
	for _, row := range result.AnonymousRecordset.Rows {
		for _, cell := range row {
			if !utf8.ValidString(cell.Str) {
				return fmt.Errorf("invalid anonymous cell encoding")
			}
		}
	}
	for _, col := range result.OrdinalColumns {
		if !utf8.ValidString(col.NativeType) || !utf8.ValidString(col.Encoding) {
			return fmt.Errorf("invalid anonymous metadata encoding")
		}
	}
	envelope := struct {
		Recordset *apicontract.Recordset     `json:"recordset" yaml:"recordset"`
		Columns   []secureread.OrdinalColumn `json:"ordinalColumns" yaml:"ordinalColumns"`
	}{result.AnonymousRecordset, result.OrdinalColumns}
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(envelope)
	case "yaml":
		return yaml.NewEncoder(w).Encode(envelope)
	case "jsonl":
		encoder := json.NewEncoder(w)
		if err := encoder.Encode(struct {
			Columns []apicontract.Column       `json:"columns"`
			Native  []secureread.OrdinalColumn `json:"ordinalColumns"`
		}{result.AnonymousRecordset.Columns, result.OrdinalColumns}); err != nil {
			return err
		}
		for _, row := range result.AnonymousRecordset.Rows {
			if err := encoder.Encode(row); err != nil {
				return err
			}
		}
		return nil
	case "csv", "grid":
		rows := make([][]string, 0, len(result.AnonymousRecordset.Rows)+1)
		header := make([]string, len(result.AnonymousRecordset.Columns))
		for i, col := range result.AnonymousRecordset.Columns {
			header[i] = col.Name
		}
		rows = append(rows, header)
		for _, row := range result.AnonymousRecordset.Rows {
			cells := make([]string, len(row))
			for i, value := range row {
				cells[i] = anonymousCellText(value)
			}
			rows = append(rows, cells)
		}
		if format == "csv" {
			out := csv.NewWriter(w)
			return out.WriteAll(rows)
		}
		out := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, row := range rows {
			quoted := make([]string, len(row))
			for i, value := range row {
				quoted[i] = strconv.Quote(value)
			}
			if _, err := fmt.Fprintln(out, strings.Join(quoted, "\t")); err != nil {
				return err
			}
		}
		return out.Flush()
	default:
		return fmt.Errorf("unsupported anonymous recordset format")
	}
}
func anonymousCellText(v apicontract.TypedValue) string {
	switch v.Type {
	case apicontract.ValueTypeNull:
		return "null"
	case apicontract.ValueTypeBoolean:
		return strconv.FormatBool(v.Bool)
	case apicontract.ValueTypeNumber:
		return strconv.FormatFloat(v.Num, 'g', -1, 64)
	default:
		return v.Str
	}
}
