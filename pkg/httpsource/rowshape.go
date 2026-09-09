package httpsource

import "sort"

// inferRowsPath returns the dalgo2http.Collection.RowsPath this query needs
// to reach its row object, inferred from a sample response body (typically
// the project's recorded fixture — see readFixtureSample) against the
// query's declared recordset columns.
//
// This is the "sensible defaults... documented in pkg/httpsource" this
// package's own doc promises: the QueryDef JSON schema does not (yet) carry
// an explicit rowsPath field, so it must be inferred from data this
// translation already has to read anyway. The policy: check the sample's
// root object first (root keys overlapping any declared column means no
// wrapping is needed); if nothing overlaps at the root, check each of the
// root's directly-nested object-valued fields (one level deep — this
// package does not chase wrapping any deeper) for the same overlap, and
// return the first match's key, in a deterministic (sorted) key order so
// this function's result never depends on Go's randomized map iteration.
// Falls back to "" (root) when nothing matches, or when no sample is
// available: "" is also the CORRECT answer for an endpoint that needs no
// wrapping at all, so this fallback is not merely "give up".
func inferRowsPath(sample map[string]any, columns []string) string {
	if len(sample) == 0 || len(columns) == 0 {
		return ""
	}
	if overlaps(sample, columns) {
		return ""
	}
	keys := make([]string, 0, len(sample))
	for k := range sample {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if obj, ok := sample[k].(map[string]any); ok && overlaps(obj, columns) {
			return k
		}
	}
	return ""
}

func overlaps(obj map[string]any, columns []string) bool {
	for _, c := range columns {
		if _, ok := obj[c]; ok {
			return true
		}
	}
	return false
}

// inferKeyField returns the dalgo2http.Collection.KeyField this query
// should use, given row — the sample's row-shaped value AFTER RowsPath
// resolution (see BuildCollection) — the query's own lookup parameter id,
// and its declared recordset columns.
//
// Policy: use paramID when the response echoes the lookup value back as a
// same-named row field (true of e.g. a "country-facts" query keyed by
// "name" whose response includes `"name": "Canada"`) — this is also what
// makes dal.DB.Get work for the resulting collection, since KeyField then
// coincides with a declared Param (see dalgo2http's own
// Capabilities.SupportsGet). Otherwise fall back to the first declared
// recordset column actually present in row: a stable, always-present field.
// dal.DB.Get is then correctly NOT supported for the collection — the
// endpoint genuinely has no by-key lookup for what happens to be its
// primary parameter (e.g. an exchange-rate query keyed by a fixed "base"
// currency, not by the "to" parameter that varies per request). With no
// sample to check at all, paramID is returned as the best available answer:
// assuming the endpoint echoes its own lookup parameter back is the more
// common REST shape, and a wrong guess here only affects whether Get is
// offered — ExecuteQueryToRecordsReader's correctness does not depend on it.
func inferKeyField(row map[string]any, paramID string, columns []string) string {
	if row == nil {
		return paramID
	}
	if _, ok := row[paramID]; ok {
		return paramID
	}
	for _, c := range columns {
		if _, ok := row[c]; ok {
			return c
		}
	}
	return paramID
}
