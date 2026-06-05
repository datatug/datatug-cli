# TEST-COVERAGE.md — gcloudcmds

## Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 0.0% |
| Post-run coverage | 100.0% |
| Uncovered statements remaining | 0 |

## Seams added

| File | Seam |
|------|------|
| `projects_command.go` | `var getGCloudProjects = gauth.GetGCloudProjects` — lets tests inject a fake project list (or error) without real Google Cloud auth. |
| `projects_command.go` | `var openGCloudProjectsScreen = gcloudui.OpenGCloudProjectsScreen` — lets tests intercept the TUI screen-open call triggered by `--format ""`. |

## Documented gaps

None — all branches covered.
