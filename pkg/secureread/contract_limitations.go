package secureread

import "github.com/datatug/datatug-core/pkg/apicontract"

// ToContractLimitations folds this package's own internally-split
// Limitation entries (a standalone "rowsFiltered" marker, a standalone
// "hiddenColumns" marker, one "policy"/"nativeSql" entry per applied
// policy — see result.go) into api-contract.md's single-shape
// Limitation{policy, rowsFiltered, hiddenColumns} list — the SAME shape
// every policy-enforced read endpoint reports (S101: exec/select gets this
// treatment too, not just exec/run_query, which is where this folding logic
// originally lived before moving here so both callers share one
// implementation).
//
// Phase 1's demo scenarios apply exactly one named policy per request
// (customers-support, security-matrix, etc.), so folding every applied
// policy name into ONE combined entry alongside the overall rowsFiltered/
// hiddenColumns flags is accurate for them. A project with more than one
// DISTINCT policy applying different row/column restrictions to the SAME
// request would see them combined into that one entry too (its
// rowsFiltered/hiddenColumns already reflect the union). Attributing
// rowsFiltered/hiddenColumns to the SPECIFIC policy that caused each would
// need this package's own internal Limitation shape to carry that
// association, which it does not yet — flagged as a Task 13 ("converge
// protected execution") follow-up, not fixed here.
func ToContractLimitations(in []Limitation) []apicontract.Limitation {
	var policyNames []string
	rowsFiltered := false
	var hiddenColumns []string
	for _, l := range in {
		switch l.Kind {
		case LimitationPolicy, LimitationNativeSQL:
			if l.Policy != "" && !containsStr(policyNames, l.Policy) {
				policyNames = append(policyNames, l.Policy)
			}
		case LimitationRowsFiltered:
			rowsFiltered = true
		case LimitationHiddenColumns:
			hiddenColumns = append(hiddenColumns, l.Columns...)
		}
	}
	if len(policyNames) == 0 && !rowsFiltered && len(hiddenColumns) == 0 {
		return []apicontract.Limitation{}
	}
	policy := "policy"
	if len(policyNames) > 0 {
		policy = joinStrings(policyNames, ",")
	}
	if hiddenColumns == nil {
		hiddenColumns = []string{}
	}
	return []apicontract.Limitation{{Policy: policy, RowsFiltered: rowsFiltered, HiddenColumns: hiddenColumns}}
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
