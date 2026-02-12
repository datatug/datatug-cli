# GitHub Agents Configuration

This directory contains configuration for GitHub Copilot agents with Model Context Protocol (MCP) support.

## gopls MCP Server

This repository is configured to use the [gopls MCP server](https://go.dev/gopls/features/mcp) for enhanced Go language support in GitHub Copilot coding agents.

### What is gopls MCP?

The gopls MCP (Model Context Protocol) server exposes gopls language server features to AI agents, providing:

- **Smart code navigation**: Go-to-definition, find references, workspace symbol search
- **Code analysis**: Diagnostics, type information, package APIs
- **Testing support**: Run tests, analyze coverage
- **Security**: Vulnerability checking with `govulncheck`
- **Refactoring**: Code formatting, renaming, and modifications

### Configuration Files

- **go-expert.yml**: Custom agent configuration with gopls MCP server
- **gopls-mcp-instructions.md**: Full instructions for using gopls MCP tools effectively

### Using the go-expert Agent

The `go-expert` agent is configured with gopls MCP and provides structured workflows for:

#### Read Workflow (Understanding Code)
1. Use `go_workspace` to understand workspace structure
2. Use `go_search` to find symbols
3. Use `go_file_context` to understand file dependencies
4. Use `go_package_api` to understand package APIs

#### Edit Workflow (Making Changes)
1. Read and understand the code first
2. Find all references before modifying symbols
3. Make the required edits
4. Check for errors with `go_diagnostics`
5. Fix any errors
6. Check for vulnerabilities if dependencies changed
7. Run targeted tests

### Requirements

To use the gopls MCP server, you need:

- Go 1.25 or later
- gopls installed: `go install golang.org/x/tools/gopls@latest`

### Security

The gopls MCP server:
- Has the same access capabilities as gopls in your IDE
- Can read files and execute `go` commands
- Cannot directly write to source files without agent instruction
- Makes narrowly scoped network requests (module downloads, vulnerability database)

Always run `go_vulncheck` when:
- Starting a new session (after confirming Go workspace)
- Adding or updating dependencies

## References

- [gopls MCP Features](https://go.dev/gopls/features/mcp)
- [GitHub Copilot MCP Documentation](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/extend-coding-agent-with-mcp)
- [Model Context Protocol](https://modelcontextprotocol.io/)
