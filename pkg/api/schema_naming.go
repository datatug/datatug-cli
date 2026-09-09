package api

import "strings"

// defaultSchemaForDriver returns the schema name a driver resolves an
// unqualified table/collection name to (SQLite's single implicit database
// is always named "main"; PostgreSQL's default search_path entry is
// "public"), or "" for a driver with no such convention (e.g. ingitdb, which
// has no schema concept at all). Read from the resolved catalog's own
// Driver field (see resolveSourceURL) rather than assumed unconditionally,
// so a caller never strips a qualifier for a source where it would not
// actually be the implicit default.
func defaultSchemaForDriver(driver string) string {
	switch strings.ToLower(driver) {
	case "sqlite3", "sqlite":
		return "main"
	case "postgres", "postgresql", "pgx":
		return "public"
	default:
		return ""
	}
}

// PolicyCollectionName derives the collection name a structured query's
// FROM clause should carry for BOTH execution and access-policy matching
// (dal-go/dalgo's access.SecureReadSession derives its Resource path
// directly from this same name — there is no separate "policy-only" name in
// DALgo's own model), given the physical, possibly schema-qualified name a
// caller supplied (e.g. exec/select's ?from=) and the resolved source's
// driver.
//
// S101's root cause: a policy `path` names the collection the way the
// semantic layer does (bare, e.g. "/Customer" — semantic/columns uses
// collection=Customer with no schema), but a real browser's table-browse
// request names the schema-qualified physical form the SQL driver itself
// reports (e.g. "main.Customer" for SQLite) — an EXACT policy path match
// against "main.Customer" then never matches a "/Customer" rule, denying
// every request for that table regardless of row content.
//
// Rule (this stream's decision — assumption by the lead session, not a
// founder ruling; api-contract.md is silent on this): when name is
// qualified by the driver's own DEFAULT schema (case-insensitively), that
// qualifier is dropped — safe for execution too, since the default schema
// is exactly what an unqualified name already resolves to for that driver,
// so "main.Customer" and "Customer" name the identical SQLite table. A
// NON-default schema (or a driver with no default-schema convention at
// all, or an already-bare name) is returned unchanged, so
// "archive.Customer" keeps its own distinct policy identity from
// "/Customer" or "/main.Customer" — two same-named tables in different
// schemas must never share a policy by accident. Never widens access: this
// only ever narrows a qualified name to its bare form when they are
// provably the same table; an unmatched path still denies exactly as
// before.
func PolicyCollectionName(name, driver string) string {
	schema := defaultSchemaForDriver(driver)
	if schema == "" {
		return name
	}
	prefix := schema + "."
	if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
		return name[len(prefix):]
	}
	return name
}
