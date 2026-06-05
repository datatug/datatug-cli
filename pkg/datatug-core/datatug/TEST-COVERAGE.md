# Coverage metrics

| Metric | Value |
|---|---|
| Pre-run coverage | 93.9% |
| Post-run coverage | 99.6% |
| Uncovered statements remaining | 4 |

## Seams added

| File | Seam |
|---|---|
| `constants.go` | `var homedirDir = homedir.Dir` — already present; used by `TestDirPath_*` to override the homedir lookup |

## Documented gaps

### defensive/unreachable — DBCollectionKey.Validate() always returns nil

`DBCollectionKey.Validate()` (db_collection_key.go:68) unconditionally returns `nil`. All four remaining uncovered branches are error-return paths that are only reachable when `DBCollectionKey.Validate()` returns a non-nil error.

**Refactoring required:** Change `DBCollectionKey.Validate()` to validate its fields (e.g., require non-empty `Name`), or replace the direct call with an injectable seam. Without that production change these branches are structurally dead code.

#### Affected locations

| whyType | Function | File | Lines | Reason |
|---|---|---|---|---|
| defensive/unreachable | `CollectionInfo.Validate` | `db_collection.go` | 19–21 | `v.DBCollectionKey.Validate()` always returns nil, so the `return err` at line 20 is never reached |
| defensive/unreachable | `TableModel.Validate` | `db_model.go` | 215–217 | Same root cause — `DBCollectionKey.Validate()` always returns nil |
| defensive/unreachable | `TableKeys.Validate` | `db_objects.go` | 132–134 | Per-element `t.Validate()` delegates to `DBCollectionKey.Validate()` which always returns nil |
| defensive/unreachable | `Entity.Validate` | `entities.go` | 57–59 | `v.Tables.Validate()` calls `TableKeys.Validate()` → `DBCollectionKey.Validate()` — always nil |
