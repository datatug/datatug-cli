# MCP Server Configuration Guide

This guide explains how to configure the gopls MCP server for GitHub Copilot coding agent in the repository settings.

## Prerequisites

- Repository administrator access
- gopls v0.21.0 or later installed (the agent will need gopls available in its environment)

## Configuration Steps

### 1. Navigate to Repository Settings

1. Go to the main page of your repository on GitHub
2. Click **Settings** (requires admin access)
3. In the sidebar under "Code & automation", click **Copilot** → **Coding agent**

### 2. Add MCP Configuration

In the **MCP configuration** section, add the following JSON configuration:

```json
{
  "mcpServers": {
    "gopls": {
      "type": "stdio",
      "command": "gopls",
      "args": ["mcp"],
      "tools": ["*"]
    }
  }
}
```

### 3. Configuration Options

#### Basic Configuration (Recommended)

The configuration above enables all gopls MCP tools. This is recommended for full functionality.

#### Restricted Tool Configuration

If you want to limit which gopls tools are available, specify them explicitly:

```json
{
  "mcpServers": {
    "gopls": {
      "type": "stdio",
      "command": "gopls",
      "args": ["mcp"],
      "tools": [
        "go_workspace",
        "go_search",
        "go_file_context",
        "go_package_api",
        "go_symbol_references",
        "go_diagnostics",
        "go_vulncheck"
      ]
    }
  }
}
```

#### Configuration with Environment Variables

If you need to pass environment variables to gopls:

```json
{
  "mcpServers": {
    "gopls": {
      "type": "stdio",
      "command": "gopls",
      "args": ["mcp"],
      "tools": ["*"],
      "env": {
        "GOPATH": "${COPILOT_MCP_GOPATH}",
        "GOPROXY": "${COPILOT_MCP_GOPROXY}"
      }
    }
  }
}
```

Then add the corresponding variables to your Copilot environment with the `COPILOT_MCP_` prefix.

### 4. Available gopls MCP Tools

The gopls MCP server provides the following tools:

| Tool | Description | Use Case |
|------|-------------|----------|
| `go_workspace` | Analyze workspace structure | Understand if it's a module, workspace, or GOPATH project |
| `go_search` | Fuzzy symbol search | Find types, functions, or variables by name |
| `go_file_context` | File dependency analysis | Understand intra-package dependencies |
| `go_package_api` | Package API inspection | View public APIs of packages |
| `go_symbol_references` | Find symbol references | Locate all uses of a symbol before refactoring |
| `go_diagnostics` | Build and analysis errors | Check for compilation and lint errors |
| `go_vulncheck` | Vulnerability scanning | Check for known security vulnerabilities |
| `go_test` | Run tests | Execute Go tests for specific packages |
| `go_rename` | Rename symbols | Safely rename symbols across the codebase |
| `go_format` | Format code | Apply gofmt formatting |

### 5. Using the Custom Agent

Once the MCP server is configured:

1. The `go-expert` custom agent (defined in `go-expert.md`) will have access to all gopls tools
2. Start a Copilot chat and reference the agent: `@go-expert`
3. The agent will automatically use gopls tools to understand and edit Go code

### 6. Verification

To verify the configuration is working:

1. Start a Copilot coding agent session
2. Ask a question about the Go code in your repository
3. The agent should use gopls tools like `go_workspace`, `go_search`, etc.
4. Check the agent's response for tool usage indicators

## Troubleshooting

### gopls not found

If you get "command not found" errors:

- Ensure gopls is installed: `go install golang.org/x/tools/gopls@latest`
- Check that gopls is in the PATH for the execution environment
- Verify gopls version: `gopls version` (should be v0.21.0 or later)

### Tools not available

If the agent can't use gopls tools:

- Verify the JSON configuration syntax is correct
- Check that the `tools` array includes the desired tools
- Ensure the `go-expert.md` agent has `gopls/*` in its tools list

### Performance issues

If gopls is slow or times out:

- Consider limiting tools to only what's needed
- Check available memory and CPU resources
- Review gopls logs for errors

## References

- [gopls MCP Features](https://go.dev/gopls/features/mcp)
- [GitHub Copilot MCP Documentation](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/extend-coding-agent-with-mcp)
- [Custom Agents Configuration](https://docs.github.com/en/copilot/reference/custom-agents-configuration)
