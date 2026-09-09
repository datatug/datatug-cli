package endpoints

import (
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// toContractRecordset shapes a secureread.Result's Columns/Rows into the
// appendix's Recordset (api-contract.md "Shared JSON types": "rows have
// exactly one value per returned column"). Column types are inferred from
// the first non-null observed value in that column (see fromGoValue's own
// doc for why: no richer per-column type registry exists yet in this
// codebase to consult instead) and default to "string" for an
// all-null/empty column.
func toContractRecordset(result secureread.Result) (apicontract.Recordset, error) {
	columnTypes := make([]string, len(result.Columns))
	for i, name := range result.Columns {
		for _, row := range result.Rows {
			if v, ok := row.Data[name]; ok && v != nil {
				tv, err := fromGoValue(v, "")
				if err == nil {
					columnTypes[i] = string(tv.Type)
					break
				}
			}
		}
		if columnTypes[i] == "" {
			columnTypes[i] = string(apicontract.ValueTypeString)
		}
	}
	columns := make([]apicontract.Column, len(result.Columns))
	for i, name := range result.Columns {
		columns[i] = apicontract.Column{Name: name, Type: columnTypes[i]}
	}
	rows := make([][]apicontract.TypedValue, len(result.Rows))
	for r, row := range result.Rows {
		values := make([]apicontract.TypedValue, len(result.Columns))
		for c, name := range result.Columns {
			tv, err := fromGoValue(row.Data[name], columnTypes[c])
			if err != nil {
				return apicontract.Recordset{}, err
			}
			values[c] = tv
		}
		rows[r] = values
	}
	return apicontract.Recordset{Columns: columns, Rows: rows}, nil
}

// toContractLimitations folds a secureread.Result's own Limitations into
// the appendix's single-shape Limitation{policy, rowsFiltered,
// hiddenColumns} list — now secureread.ToContractLimitations (S101: moved
// there so pkg/api's exec/select gets the identical folding logic without
// pkg/api needing to import this package).
func toContractLimitations(in []secureread.Limitation) []apicontract.Limitation {
	return secureread.ToContractLimitations(in)
}

// nowRFC3339UTC is Provenance.ObservedAt's exact wire format (api-contract.md
// TypedValue's own "datetime" rule: "RFC3339 normalized to UTC").
func nowRFC3339UTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
