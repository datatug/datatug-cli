# TEST-COVERAGE.md — pkg/dtgithub

## Coverage metrics

| | Value |
|---|---|
| Pre-run coverage | 1.7% (1 of ~59 statements) |
| Post-run coverage | 99.2% (58 of 59 statements) |
| Uncovered statements remaining | 1 |

## Seams added

Both seams are in `github_repo_projects_store.go`, replacing direct calls with
overridable package-level vars so tests can inject stubs without live I/O:

| File | Seam |
|---|---|
| `github_repo_projects_store.go` | `var createProjectFiles = dtprojcreator.CreateProjectFiles` — used in `CreateProject` |
| `github_repo_projects_store.go` | `var addProjectToSettings = dtconfig.AddProjectToSettings` — used in `addProjectToDataTugConfig` |

## Documented gaps

### defensive/unreachable — 1 statement

**Function:** `(*projectCreator).CreateProject` (line ~90 — `cloneRepo` error branch)

**Statement:**
```go
if err = c.cloneRepo(); err != nil {
    return fmt.Errorf("failed to clone GitHub repository '%s/%s': %w", ...)  // ← uncovered
}
```

**Reason:** `cloneRepo` unconditionally returns `nil` — the actual `git.PlainClone`
call is commented out, leaving no code path that can produce an error. The error
return branch is structurally unreachable without restoring or replacing the clone
logic.

**Refactoring required:** Introduce a seam for the clone operation (e.g.
`var cloneRepo = git.PlainClone` or a function var on `projectCreator`) so a test
can inject a failing clone. This requires un-commenting or replacing the clone stub
in `cloneRepo`, which is a production logic change beyond a simple seam addition.
