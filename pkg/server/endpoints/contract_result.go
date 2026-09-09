package endpoints

import (
	"time"

	"github.com/datatug/datatug-cli/pkg/apicontract_local"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// toContractRecordset shapes a secureread.Result's Columns/Rows into the
// appendix's Recordset (api-contract.md "Shared JSON types": "rows have
// exactly one value per returned column"). Column types are inferred from
// the first non-null observed value in that column (see
// apicontract_local.FromGoValue's own doc for why: no richer per-column
// type registry exists yet in this codebase to consult instead) and default
// to "string" for an all-null/empty column.
func toContractRecordset(result secureread.Result) (apicontract_local.Recordset, error) {
	columnTypes := make([]string, len(result.Columns))
	for i, name := range result.Columns {
		for _, row := range result.Rows {
			if v, ok := row.Data[name]; ok && v != nil {
				tv, err := apicontract_local.FromGoValue(v, "")
				if err == nil {
					columnTypes[i] = string(tv.Type)
					break
				}
			}
		}
		if columnTypes[i] == "" {
			columnTypes[i] = string(apicontract_local.TypeString)
		}
	}
	columns := make([]apicontract_local.Column, len(result.Columns))
	for i, name := range result.Columns {
		columns[i] = apicontract_local.Column{Name: name, Type: columnTypes[i]}
	}
	rows := make([][]apicontract_local.TypedValue, len(result.Rows))
	for r, row := range result.Rows {
		values := make([]apicontract_local.TypedValue, len(result.Columns))
		for c, name := range result.Columns {
			tv, err := apicontract_local.FromGoValue(row.Data[name], columnTypes[c])
			if err != nil {
				return apicontract_local.Recordset{}, err
			}
			values[c] = tv
		}
		rows[r] = values
	}
	return apicontract_local.Recordset{Columns: columns, Rows: rows}, nil
}

// toContractLimitations folds secureread's internally-split Limitation
// entries (a standalone "rowsFiltered" marker, a standalone
// "hiddenColumns" marker, one "policy"/"nativeSql" entry per applied
// policy — see pkg/secureread/result.go) into the appendix's single-shape
// Limitation{policy, rowsFiltered, hiddenColumns} list.
//
// Phase 1's demo scenarios apply exactly one named policy per request
// (customers-support, security-matrix, etc.), so folding every applied
// policy name into ONE combined entry alongside the overall rowsFiltered/
// hiddenColumns flags is accurate for them. A project with more than one
// DISTINCT policy applying different row/column restrictions to the SAME
// request would see them combined into that one entry too (its
// rowsFiltered/hiddenColumns already reflect the union). Attributing
// rowsFiltered/hiddenColumns to the SPECIFIC policy that caused each would
// need secureread's own internal Limitation shape to carry that
// association, which it does not yet — flagged as a Task 13 ("converge
// protected execution") follow-up, not fixed here.
func toContractLimitations(in []secureread.Limitation) []apicontract_local.Limitation {
	var policyNames []string
	rowsFiltered := false
	var hiddenColumns []string
	for _, l := range in {
		switch l.Kind {
		case secureread.LimitationPolicy, secureread.LimitationNativeSQL:
			if l.Policy != "" && !containsStr(policyNames, l.Policy) {
				policyNames = append(policyNames, l.Policy)
			}
		case secureread.LimitationRowsFiltered:
			rowsFiltered = true
		case secureread.LimitationHiddenColumns:
			hiddenColumns = append(hiddenColumns, l.Columns...)
		}
	}
	if len(policyNames) == 0 && !rowsFiltered && len(hiddenColumns) == 0 {
		return []apicontract_local.Limitation{}
	}
	policy := "policy"
	if len(policyNames) > 0 {
		policy = joinStrings(policyNames, ",")
	}
	if hiddenColumns == nil {
		hiddenColumns = []string{}
	}
	return []apicontract_local.Limitation{{Policy: policy, RowsFiltered: rowsFiltered, HiddenColumns: hiddenColumns}}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func joinStrings(list []string, sep string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

// nowRFC3339UTC is Provenance.ObservedAt's exact wire format (api-contract.md
// TypedValue's own "datetime" rule: "RFC3339 normalized to UTC").
func nowRFC3339UTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
