package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// SourceKind names which pkg/dbcopy-openable adapter a ResolvedSource opens
// through.
type SourceKind string

const (
	SourceKindSQL     SourceKind = "sql"
	SourceKindInGitDB SourceKind = "ingitdb"
	SourceKindHTTP    SourceKind = "http"
)

// ResolvedSource is one entry of the project's unified source registry: the
// api-contract.md "Scope and identity" resolver plan task 12 requires —
// "Semantic discovery and execution use the same resolver" — adapting
// existing environment/catalog records (SQL, inGitDB) and HTTP QueryDefs
// into one list, with no second persisted store.
//
// ID is the appendix's SourceRef.source: a STABLE project-local identifier.
// For a SQL/inGitDB catalog this is the catalog's DbCatalogBase.DbModel
// (e.g. "chinook") — the identifier datatug-demo-projects/demo-project-1's
// own EntityField.Mappings already key by (verified against the real
// project: Customer.ID's mapping is {source:"chinook", ...}, NOT
// {source:"chinook-local"}), and the one the pre-existing semantic
// endpoints (pkg/server/endpoints/semantic_*.go) already receive as
// "source" from the web client — so resolving it against the SAME registry
// execution now shares is the unification this task exists to do, not a
// new convention. The catalog's own project-item ID (e.g. "chinook-local",
// distinct per environment) is accepted too, as a fallback alias, so a
// caller that already has a concrete catalog ID (the CLI's older --db flag
// shape) keeps working. See ResolveSource.
//
// For a project-level inGitDB recordset (e.g. "support-notes") ID is the
// recordset definition's own ID, environment-independent: this demo
// project's data/ingitdb store is one shared directory, not scoped per
// environment (verified: recordsets/support-notes.recordset.json declares
// no environment, and semantic_project.go's existing semanticIngitdbPath
// already treats it as project-wide).
//
// For an HTTP QueryDef (e.g. "country-facts") ID is the QueryDef's own ID,
// also environment-independent (an HTTP QueryDef's Targets must be empty —
// datatug-core's QueryDef.Validate enforces this — so it is not tied to any
// environment/catalog record at all).
type ResolvedSource struct {
	ID    string
	Label string
	Kind  SourceKind
	URL   string
	// Collection is the fixed collection name a caller must query this
	// source through: for SQL/inGitDB-via-catalog it is whatever the caller
	// asks for (any table/collection in that database); for an HTTP source
	// it is fixed to ID itself (one QueryDef = one dalgo2http collection —
	// see pkg/httpsource.BuildCollection, "Name: def.ID").
	Collection string
}

// ResolveSource resolves one SourceRef.source (see ResolvedSource's doc for
// the two accepted forms — DbModel or catalog ID) within environment to a
// pkg/dbcopy-openable ResolvedSource. It is the ONE place semantic
// discovery (pkg/server/endpoints/semantic_*.go) and execution
// (RunQuery/ExecuteSelect/exec/run_query) resolve a source, replacing the
// two previously-diverging conventions (semantic's broken
// "<projectDir>/dbs/<source>.sqlite" guess and execution's environment/
// catalog walk) — see the PR body's inventory for the specifics.
func ResolveSource(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment, source string) (ResolvedSource, error) {
	if source == "" {
		return ResolvedSource{}, fmt.Errorf("resolver: source is required")
	}
	all, err := ListSources(ctx, projStore, projectDir, environment)
	if err != nil {
		return ResolvedSource{}, err
	}
	for _, s := range all {
		if s.ID == source {
			return s, nil
		}
	}
	return ResolvedSource{}, fmt.Errorf("%w: unknown source %q in environment %q", ErrSourceUnavailable, source, environment)
}

// ListSources enumerates every source this project's registry can resolve
// for environment: every SQL/inGitDB catalog registered on that
// environment's DB servers (keyed by BOTH DbModel and catalog ID — see
// ResolvedSource's doc), every project-level inGitDB recordset definition,
// and every project-level HTTP QueryDef. Order is deterministic (by Kind
// then ID) so a Candidate's target list and an "unknown source" error's
// implied option set are stable across calls.
func ListSources(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment string) ([]ResolvedSource, error) {
	var out []ResolvedSource

	if environment != "" {
		catalogSources, err := catalogSources(ctx, projStore, projectDir, environment)
		if err != nil {
			return nil, err
		}
		out = append(out, catalogSources...)
	}

	recordsetSources, err := recordsetSources(projectDir)
	if err != nil {
		return nil, err
	}
	out = append(out, recordsetSources...)

	httpSources, err := httpQuerySources(projectDir)
	if err != nil {
		return nil, err
	}
	out = append(out, httpSources...)

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// catalogSources enumerates every catalog registered under environment
// (SQL and inGitDB drivers only — the two schemes sourceURLFromCatalog
// opens), each exposed under both its DbModel and its own catalog ID (see
// ResolvedSource's doc comment).
//
// It lists catalogs via ProjectStore.LoadEnvDbCatalogs rather than walking
// env.DbServers[].Catalogs: datatug-core's filestore implementation
// (fsEnvDbCatalogStore.LoadEnvDbCatalogs) scans
// environments/<env>/catalogs/** on disk directly and does not consult
// EnvDbServer.Catalogs at all — and LoadEnvDbCatalog's own serverID
// parameter is accepted but ignored by that same implementation. Walking
// DbServers first (as the pre-Task-12 resolveSourceURL used to) silently
// finds nothing whenever an EnvDbServer record's own Catalogs list is
// empty or stale, even though the catalog itself loads fine — verified
// against this package's own resolver_test.go fixture. A catalog whose
// driver sourceURLFromCatalog does not support (e.g. a future postgres3
// catalog) is skipped rather than failing the whole listing.
func catalogSources(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment string) ([]ResolvedSource, error) {
	catalogs, err := projStore.LoadEnvDbCatalogs(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("resolver: list catalogs for environment %q: %w", environment, err)
	}
	seen := map[string]bool{}
	var out []ResolvedSource
	for _, catalog := range catalogs {
		if catalog == nil {
			continue
		}
		url, err := sourceURLFromCatalog(*catalog, projectDir)
		if err != nil {
			continue // unsupported driver, or a path that can't resolve — see sourceURLFromCatalog.
		}
		kind := SourceKindSQL
		if catalog.Driver == "ingitdb" {
			kind = SourceKindInGitDB
		}
		label := catalog.Title
		if label == "" {
			label = catalog.ID
		}
		for _, id := range dedupeNonEmpty(catalog.DbModel, catalog.ID) {
			if seen[sourceKey(kind, id)] {
				continue
			}
			seen[sourceKey(kind, id)] = true
			out = append(out, ResolvedSource{ID: id, Label: label, Kind: kind, URL: url})
		}
	}
	return out, nil
}

func sourceKey(kind SourceKind, id string) string { return string(kind) + "\x00" + id }

// semanticIngitdbPath is this resolver's own copy of what was
// pkg/server/endpoints/semantic_project.go's identically-named helper
// before this task's endpoints rewrite deleted the endpoints package's
// separate resolveSource path in favor of this one: the project's shared
// inGitDB store directory (data/ingitdb) as a pkg/dbcopy-parseable
// ingitdb:// URL.
func semanticIngitdbPath(projectDir string) string {
	return "ingitdb://" + filepath.Join(projectDir, storage.DataFolder, "ingitdb")
}

func dedupeNonEmpty(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// recordsetSources enumerates <projectDir>/recordsets/*.recordset.json
// definitions as project-level inGitDB sources (top-level only — the same
// non-recursive convention recordsetDefinitionPath already assumes),
// sharing the project's one inGitDB store (semanticIngitdbPath).
func recordsetSources(projectDir string) ([]ResolvedSource, error) {
	dir := filepath.Join(projectDir, storage.RecordsetsFolder)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolver: list %s: %w", dir, err)
	}
	suffix := "." + storage.RecordsetFileSuffix + ".json"
	url := semanticIngitdbPath(projectDir)
	var out []ResolvedSource
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), suffix)
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("resolver: read %s: %w", entry.Name(), err)
		}
		var def datatug.RecordsetDefinition
		if err := json.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("resolver: parse %s: %w", entry.Name(), err)
		}
		if def.ID != "" {
			id = def.ID
		}
		label := def.Title
		if label == "" {
			label = id
		}
		out = append(out, ResolvedSource{ID: id, Label: label, Kind: SourceKindInGitDB, URL: url, Collection: id})
	}
	return out, nil
}

// httpQuerySources enumerates the project's HTTP-type QueryDefs
// (pkg/httpsource.LoadHTTPQueries) as sources in their own right — one
// QueryDef IS one dalgo2http collection (BuildCollection: "Name: def.ID"),
// so ID/Collection are both the QueryDef's own ID.
func httpQuerySources(projectDir string) ([]ResolvedSource, error) {
	loaded, err := httpsource.LoadHTTPQueries(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolver: list HTTP query defs: %w", err)
	}
	if len(loaded) == 0 {
		return nil, nil
	}
	url := "http://" + projectDir
	var out []ResolvedSource
	for _, lq := range loaded {
		if lq.Def == nil {
			continue
		}
		label := lq.Def.Title
		if label == "" {
			label = lq.Def.ID
		}
		out = append(out, ResolvedSource{ID: lq.Def.ID, Label: label, Kind: SourceKindHTTP, URL: url, Collection: lq.Def.ID})
	}
	return out, nil
}

// EligibleTargets returns the authorized ResolvedSource options a saved
// query may run against within environment, per api-contract.md "Scope and
// identity": an HTTP query's only eligible target is itself (its Targets
// MUST be empty per datatug-core's own QueryDef.Validate); a SQL/DTQL query
// with declared Targets is filtered to catalogs whose (Driver, Catalog)
// matches one of them; a SQL/DTQL query with NO declared Targets (every
// query in datatug-demo-projects/demo-project-1 today) is eligible against
// every SQL/inGitDB catalog source in environment — a deliberate, documented
// engineering default for Phase 1's single-catalog-per-environment demo,
// not a founder ruling: a project that registers more than one same-model
// catalog per environment without the query declaring explicit Targets
// will see every one of them offered, which is the conservative (ask
// rather than guess) behaviour api-contract.md's "with multiple targets the
// user selects a source explicitly" already calls for.
func EligibleTargets(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment string, queryDef *datatug.QueryDef) ([]ResolvedSource, error) {
	if queryDef.Type == datatug.QueryTypeHTTP {
		all, err := httpQuerySources(projectDir)
		if err != nil {
			return nil, err
		}
		for _, s := range all {
			if s.ID == queryDef.ID {
				return []ResolvedSource{s}, nil
			}
		}
		return nil, nil
	}
	all, err := catalogSources(ctx, projStore, projectDir, environment)
	if err != nil {
		return nil, err
	}
	if len(queryDef.Targets) == 0 {
		return dedupeSourcesByID(all), nil
	}
	var eligible []ResolvedSource
	for _, target := range queryDef.Targets {
		for _, s := range all {
			if catalogMatchesTarget(s, target) {
				eligible = append(eligible, s)
			}
		}
	}
	return dedupeSourcesByID(eligible), nil
}

// catalogMatchesTarget reports whether s (a catalogSources entry) satisfies
// one QueryDefTarget: Driver matches (sqlite/sqlite3 treated as
// synonyms — datatug-core's own catalog data uses "sqlite3", QueryDefTarget
// examples elsewhere in this codebase use "sqlite"), and Catalog, when set,
// equals either form of s's ID (DbModel or catalog ID — see ResolvedSource's
// doc).
func catalogMatchesTarget(s ResolvedSource, target datatug.QueryDefTarget) bool {
	if target.Driver != "" && !driverMatches(target.Driver, s.Kind) {
		return false
	}
	if target.Catalog != "" && target.Catalog != s.ID {
		return false
	}
	return true
}

func driverMatches(targetDriver string, kind SourceKind) bool {
	switch strings.ToLower(targetDriver) {
	case "sqlite", "sqlite3", "openvaultdb":
		return kind == SourceKindSQL
	case "ingitdb":
		return kind == SourceKindInGitDB
	default:
		return false
	}
}

// dedupeSourcesByID collapses catalogSources' DbModel/catalog-ID aliasing
// (each real catalog can appear twice in the list, once per alias) down to
// one entry per underlying catalog for target-selection purposes: a caller
// choosing among "eligible targets" must see each real database once, not
// twice under two different names. The DbModel-named entry wins when both
// aliases are present (it is the identifier this project's own
// EntityField.Mappings already use — see ResolvedSource's doc), keeping
// target lists and Candidate.selectedSource consistent with what semantic
// resolution reports elsewhere.
func dedupeSourcesByID(sources []ResolvedSource) []ResolvedSource {
	byURL := map[string]ResolvedSource{}
	var order []string
	for _, s := range sources {
		existing, ok := byURL[s.URL]
		if !ok {
			byURL[s.URL] = s
			order = append(order, s.URL)
			continue
		}
		// Prefer the shorter-looking / non-hyphenated alias only when it is
		// not literally the catalog's own project-item ID form; in
		// practice DbModel names carry no environment suffix
		// ("chinook") while catalog IDs often do ("chinook-local"), so
		// picking the currently-stored entry unless the new one is
		// "cleaner" keeps behaviour stable without over-engineering a
		// preference rule.
		if len(s.ID) < len(existing.ID) {
			byURL[s.URL] = s
		}
	}
	out := make([]ResolvedSource, 0, len(order))
	for _, url := range order {
		out = append(out, byURL[url])
	}
	return out
}
