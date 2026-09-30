# Test coverage overview

Measured on 2026-09-30 with `go test -race -count=1 -cover -coverprofile=… ./...`
on the Bubble Tea UI at `origin/main` plus the coverage tests for `apps/datatugapp/commands`.

| Run | Statements covered | Total statements | Coverage |
|-----|--------------------|------------------|----------|
| Whole module | 24,815 | 24,815 | 100.00% |

Every package below is at 100.0%; the gate is 100% statement coverage of `datatug`.
See [TEST-COVERAGE-PATTERNS.md](TEST-COVERAGE-PATTERNS.md) for how the tests are written
(seams instead of contorted tests, hermetic HOME, `navtest` for terminal screens).

## Per package

| Package | Coverage |
|---------|----------|
| `github.com/datatug/datatug-cli` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/commands` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtapiservice` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtsettings` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/aws/awsui` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/azure/azureui` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudui` | 100.0% |
| `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/dbviewer` | 100.0% |
| `github.com/datatug/datatug-cli/internal/hermetictest` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/accesspolicies` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/api` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/auth` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/auth/device` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/auth/gauth` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/auth/ghauth` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/chat` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/color` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dbcopy` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dbcopy/filter` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dtentity` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dtgithub` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dtio` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dtlog` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dtroot` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/dtstate` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/executionstore` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/httpsource` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/incidentstore` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/openvaultdb` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/personalqueries` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/projectschema` | n/a (no statements) |
| `github.com/datatug/datatug-cli/pkg/querywrite` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/schemers/firestoreschema` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/schemers/mssqlschema` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/schemers/sqliteschema` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/secureread` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/server` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/server/endpoints` | 100.0% |
| `github.com/datatug/datatug-cli/pkg/sqlexecute` | 100.0% |
