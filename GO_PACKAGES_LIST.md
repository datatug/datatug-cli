# Complete List of Go Packages in datatug-cli

This document provides a comprehensive list of all Go packages in the datatug-cli repository, discovered using the gopls MCP server.

## Total: 49 Go Packages

### Main Package
1. `github.com/datatug/datatug-cli` - Main entry point

### Application Packages (apps/)
2. `github.com/datatug/datatug-cli/apps`
3. `github.com/datatug/datatug-cli/apps/datatugapp` - Main application
4. `github.com/datatug/datatug-cli/apps/datatugapp/commands` - CLI commands
5. `github.com/datatug/datatug-cli/apps/datatugapp/console` - Console interface
6. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui` - UI components
7. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtapiservice` - API service UI
8. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject` - Project UI
9. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtsettings` - Settings UI
10. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers` - Viewers
11. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds` - Cloud viewers
12. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/aws/awsui` - AWS UI
13. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/azure/azureui` - Azure UI
14. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds` - GCloud commands
15. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudui` - GCloud UI
16. `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/dbviewer` - Database viewer
17. `github.com/datatug/datatug-cli/apps/global` - Global app state

### Core Packages (pkg/)
18. `github.com/datatug/datatug-cli/pkg/api` - API layer (15+ functions)
19. `github.com/datatug/datatug-cli/pkg/auth` - Authentication
20. `github.com/datatug/datatug-cli/pkg/auth/gauth` - Google authentication
21. `github.com/datatug/datatug-cli/pkg/auth/ghauth` - GitHub authentication
22. `github.com/datatug/datatug-cli/pkg/color` - Color styling
23. `github.com/datatug/datatug-cli/pkg/dtgithub` - GitHub integration
24. `github.com/datatug/datatug-cli/pkg/dtio` - I/O utilities
25. `github.com/datatug/datatug-cli/pkg/dtlog` - Logging with PostHog
26. `github.com/datatug/datatug-cli/pkg/dtstate` - State management
27. `github.com/datatug/datatug-cli/pkg/sqlexecute` - SQL execution

### DataTug Core Packages (pkg/datatug-core/)
28. `github.com/datatug/datatug-cli/pkg/datatug-core/comparator` - Database comparison
29. `github.com/datatug/datatug-cli/pkg/datatug-core/datatug` - Core data structures (extensive API)
30. `github.com/datatug/datatug-cli/pkg/datatug-core/datatug2md` - Markdown conversion
31. `github.com/datatug/datatug-cli/pkg/datatug-core/dbconnection` - Database connections
32. `github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig` - Configuration
33. `github.com/datatug/datatug-cli/pkg/datatug-core/dto` - Data transfer objects
34. `github.com/datatug/datatug-cli/pkg/datatug-core/parallel` - Parallel processing
35. `github.com/datatug/datatug-cli/pkg/datatug-core/schemer` - Schema operations
36. `github.com/datatug/datatug-cli/pkg/datatug-core/storage` - Storage layer
37. `github.com/datatug/datatug-cli/pkg/datatug-core/storage/dtprojcreator` - Project creator
38. `github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore` - File storage
39. `github.com/datatug/datatug-cli/pkg/datatug-core/test` - Testing utilities

### Schema Provider Packages (pkg/schemers/)
40. `github.com/datatug/datatug-cli/pkg/schemers` - Schema provider interface
41. `github.com/datatug/datatug-cli/pkg/schemers/firestoreschema` - Google Firestore
42. `github.com/datatug/datatug-cli/pkg/schemers/mssqlschema` - MS SQL Server
43. `github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema` - SQL information schema
44. `github.com/datatug/datatug-cli/pkg/schemers/sqliteschema` - SQLite

### Server Packages (pkg/server/)
45. `github.com/datatug/datatug-cli/pkg/server` - HTTP server
46. `github.com/datatug/datatug-cli/pkg/server/endpoints` - API endpoints

### View Packages (pkg/sneatview/)
47. `github.com/datatug/datatug-cli/pkg/sneatview/databrowser` - Data browser
48. `github.com/datatug/datatug-cli/pkg/sneatview/sneatnav` - Navigation

### Test Package
49. `github.com/datatug/datatug-cli/cmd/test_gopls_mcp` - gopls MCP validation

## Package Statistics

- **Total packages**: 49 (including test package)
- **Application packages**: 16
- **Core library packages**: 10
- **DataTug-core packages**: 12
- **Schema providers**: 5
- **Server packages**: 2
- **Authentication packages**: 3
- **Utility packages**: 5
- **View packages**: 2

## Package Dependencies

The packages are organized in layers:

```
apps/
├── datatugapp (main application)
    ├── commands (uses api, server)
    ├── console (uses api)
    └── datatugui (uses api, datatug-core)
        └── dtviewers (specialized viewers)

pkg/
├── api (uses datatug-core, server)
├── server (uses api, datatug-core)
├── datatug-core/
│   ├── datatug (core types)
│   ├── storage (uses datatug)
│   ├── schemer (uses datatug)
│   └── dbconnection (connection handling)
├── schemers/ (implements schemer interface)
│   ├── mssqlschema
│   ├── sqliteschema
│   └── firestoreschema
└── utilities (auth, color, dtlog, etc.)
```

## Key Exported APIs

### pkg/api
- `GetAgentInfo()` - Agent information
- `CreateProject()`, `GetProjects()` - Project management
- `ExecuteSelect()`, `ExecuteCommands()` - SQL execution
- `UpdateDbSchema()` - Schema updates
- Board, Query, Entity management functions

### pkg/server
- `NewHttpServer()` - Create HTTP server
- `ServeHTTP()` - Start server
- `Shutdown()` - Graceful shutdown

### pkg/datatug-core/datatug
- Extensive data structures (50+ types)
- Core types: Project, Database, Table, Column, etc.
- Validation interfaces

### pkg/datatug-core/storage
- Storage interface abstraction
- File-based storage implementation
- Project loading/saving

### pkg/schemers
- `Provider` interface
- Schema collection from various databases
- Support for MSSQL, SQLite, Firestore

## Discovered via gopls MCP Server

All packages were discovered and analyzed using the following gopls tools:
- `go_workspace` - Workspace structure
- `go list ./...` command - Complete package list
- `go_search` - Symbol search across codebase
- `go_package_api` - Package API extraction
- `go_file_context` - Dependency analysis
- `go_symbol_references` - Cross-reference tracking

This comprehensive package structure demonstrates the full capability of the gopls MCP server to analyze and navigate large Go codebases.
