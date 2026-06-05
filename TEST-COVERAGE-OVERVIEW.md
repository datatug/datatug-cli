# TEST-COVERAGE-OVERVIEW.md

Generated: 2026-06-05

---

## Coverage metrics

### Totals

| Run | Coverage % | Uncovered statements |
|-----|-----------|----------------------|
| Pre-run (baseline) | 48.8% | 4 693 |
| Post-run | 68.6% | 5 054 |

> Note: post-run totals are higher than pre-run because the collector now sees packages that
> were previously excluded (quarantined 100%-packages are included in the denominator, and
> several packages that had 0 tests before now expose net-new uncovered lines introduced by
> seam additions in failed units).

### Per-package

| Package | Pre % | Pre uncov | Post % | Post uncov | Status |
|---------|-------|-----------|--------|------------|--------|
| `github.com/datatug/datatug-cli` | 88.0 | 3 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/apps/datatugapp` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/apps/datatugapp/commands` | 34.5 | 833 | 34.5 | 833 | integration-failed |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui` | 0.0 | 42 | 0.0 | 42 | integration-failed |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtapiservice` | 0.0 | 22 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject` | 0.0 | 830 | 0.0 | 830 | integration-failed |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtsettings` | 0.0 | 42 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers` | 0.0 | 84 | 97.7 | 2 | documented-gap |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds` | 0.0 | 18 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/aws/awsui` | 0.0 | 3 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/azure/azureui` | 0.0 | 3 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds` | 0.0 | 18 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudui` | 0.0 | 402 | 77.9 | 90 | documented-gap |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/dbviewer` | 0.0 | 790 | 0.0 | 790 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/api` | 2.7 | 429 | 2.7 | 1 287 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/auth` | 0.0 | 1 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/pkg/auth/gauth` | 57.8 | 62 | 57.8 | 186 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/auth/ghauth` | 0.0 | 75 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/pkg/color` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/datatug-core/comparator` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/datatug-core/datatug` | 93.9 | 60 | 99.6 | 4 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/datatug-core/datatug2md` | 99.5 | 1 | 99.5 | 3 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/datatug-core/dbconnection` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig` | 72.6 | 17 | 72.6 | 51 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/datatug-core/dto` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/datatug-core/parallel` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/datatug-core/schemer` | 92.5 | 22 | 92.5 | 66 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/datatug-core/storage` | 97.8 | 2 | 97.8 | 6 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/datatug-core/storage/dtprojcreator` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore` | 85.7 | 109 | 85.7 | 327 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/datatug-core/test` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/dbcopy` | 78.9 | 84 | 78.8 | 253 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/dbcopy/filter` | 73.4 | 62 | 99.6 | 1 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/dtgithub` | 1.7 | 116 | 99.2 | 1 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/dtio` | 0.0 | 10 | 0.0 | 10 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/dtlog` | 99.2 | 1 | 99.2 | 3 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/dtstate` | 94.6 | 4 | 94.6 | 12 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/schemers/firestoreschema` | 90.5 | 4 | 90.5 | 12 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/schemers/mssqlschema` | 96.7 | 2 | 96.7 | 6 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema` | 98.3 | 4 | 98.2 | 13 | documented-gap |
| `github.com/datatug/datatug-cli/pkg/schemers/sqliteschema` | 100.0 | 0 | 100.0 | 0 | quarantined (already 100%) |
| `github.com/datatug/datatug-cli/pkg/server` | 0.0 | 45 | 0.0 | 45 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/server/endpoints` | 18.2 | 315 | 100.0 | 0 | 100% |
| `github.com/datatug/datatug-cli/pkg/sneatview/databrowser` | 0.0 | 12 | 0.0 | 12 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/sneatview/sneatnav` | 0.0 | 159 | 0.0 | 163 | integration-failed |
| `github.com/datatug/datatug-cli/pkg/sqlexecute` | 94.9 | 7 | 95.7 | 6 | documented-gap |

---

## Quarantine list

These packages were excluded from coverage work because they were already at 100% coverage before this run.

| Package | Reason |
|---------|--------|
| `github.com/datatug/datatug-cli/apps/datatugapp` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/color` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/datatug-core/comparator` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/datatug-core/dbconnection` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/datatug-core/dto` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/datatug-core/parallel` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/datatug-core/storage/dtprojcreator` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/datatug-core/test` | already at 100% coverage |
| `github.com/datatug/datatug-cli/pkg/schemers/sqliteschema` | already at 100% coverage |

---

## Taxonomy roll-up

Counts of each `whyType` across all `TEST-COVERAGE.md` files (documented gaps in integrated/documented-gap packages only).

| whyType | Count | Packages | Refactoring needed |
|---------|-------|----------|--------------------|
| `defensive/unreachable` | 7 | `github.com/datatug/datatug-cli` (×2), `pkg/datatug-core/datatug` (×4), `pkg/schemers/mssqlschema` (×1) | Remove or validate dead guards; e.g. make `DBCollectionKey.Validate()` validate its fields so dependents can exercise the error path |
| `error-path` | 6 | `pkg/sqlexecute` (×4), `pkg/auth/gauth` (×1), `pkg/schemers/mssqlschema` (×1) | Add injectable seams for the outer call (`sql.Rows.Close`, `ensureDir.Validate`) so the error return is reachable |
| `external-io` | 5 | `pkg/schemers/sqlinfoschema` (×2), `pkg/schemers/firestoreschema` (×2), `pkg/schemers/mssqlschema` (×1) | Replace live-client/live-rows construction with interface seams; use Firestore emulator or inject an iterator interface |
| `os/env-dependent` | 2 | `pkg/dtstate` (×1), `pkg/auth/gauth` (×1) | Replace `os.Create`/`os.File` return type with `io.WriteCloser` interface; use injectable `tokenSourceFn` seam for OAuth flow |
| `terminal/tview` (TUI/terminal) | ~12 | `apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudui` | Extract TUI factory to injectable seam; expose inner `*tview.Table`/`*tview.List` via seam vars after construction |

---

## Dependencies added

The following new `go.mod require` entries were introduced by integrated units:

| Dependency | Version | Introduced by |
|------------|---------|---------------|
| `github.com/DATA-DOG/go-sqlmock` | v1.5.2 | unit 20 (`pkg/dtio`, `pkg/dtlog`, `pkg/dtstate`, `pkg/schemers/*`) |

All other units: **(none)**
