package mcpserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const appsExtension = "io.modelcontextprotocol/ui"
const appsMIME = "text/html;profile=mcp-app"
const appsResourceURI = "ui://datatug/query-explorer"

//go:embed app/index.html
var appsHTML string

type appsModeKey struct{}
type appsRegistryKey struct{}
type appsSession struct {
	enabled  bool
	registry *appsRegistry
}

// appsProtocol stores only the immutable initialization mode. Result capabilities
// and their independently checked authority are added by the registry layer.
type appsProtocol struct {
	mu    sync.Mutex
	modes map[*mcp.ServerSession]*appsSession
}

func newAppsProtocol() *appsProtocol {
	return &appsProtocol{modes: make(map[*mcp.ServerSession]*appsSession)}
}

func supportsApps(params *mcp.InitializeParams) bool {
	if params == nil || params.Capabilities == nil {
		return false
	}
	// Normalize SDK extension settings without retaining client-owned maps.
	body, err := json.Marshal(params.Capabilities.Extensions[appsExtension])
	if err != nil {
		return false
	}
	var capability struct {
		MIMETypes []string `json:"mimeTypes"`
	}
	if json.Unmarshal(body, &capability) != nil {
		return false
	}
	for _, mime := range capability.MIMETypes {
		if mime == appsMIME {
			return true
		}
	}
	return false
}

func (a *appsProtocol) mode(ss *mcp.ServerSession) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if mode, ok := a.modes[ss]; ok {
		return mode.enabled
	}
	// For the SDK's sessionless protocol, InitializeParams is populated from the
	// first request before middleware runs. Later request metadata is never read.
	mode := supportsApps(ss.InitializeParams())
	session := &appsSession{enabled: mode, registry: newAppsRegistry()}
	a.modes[ss] = session
	go func() {
		_ = ss.Wait()
		a.mu.Lock()
		session.registry.close()
		delete(a.modes, ss)
		a.mu.Unlock()
	}()
	return mode
}

func (a *appsProtocol) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		ss, ok := req.GetSession().(*mcp.ServerSession)
		if !ok {
			return next(ctx, method, req)
		}
		// An explicit initialize is synchronous in the SDK. Snapshot immediately
		// after it succeeds, before its response permits any client tools request.
		if method == "initialize" || method == "server/discover" {
			result, err := next(ctx, method, req)
			if err == nil {
				a.mode(ss)
			}
			return result, err
		}
		// Pre-initialization pings/notifications must not freeze an absent capability.
		if ss.InitializeParams() == nil {
			return next(ctx, method, req)
		}
		mode := a.mode(ss)
		if method == "resources/read" && !mode {
			if params, ok := req.GetParams().(*mcp.ReadResourceParams); ok && params.URI == appsResourceURI {
				return nil, errors.New("MCP Apps resource unavailable on this connection")
			}
		}
		a.mu.Lock()
		registry := a.modes[ss].registry
		a.mu.Unlock()
		ctx = context.WithValue(context.WithValue(ctx, appsModeKey{}, mode), appsRegistryKey{}, registry)
		privateCall := false
		if method == "tools/call" {
			if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
				privateCall = mode && (params.Name == "datatug_run_query" || isAppsTool(params.Name))
				if isAppsTool(params.Name) && !mode {
					return appsError(), nil
				}
				if privateCall && len(params.Arguments) > maxRequest {
					return appsError(), nil
				}
			}
		}
		result, err := next(ctx, method, req)
		if privateCall {
			if err != nil {
				return appsError(), nil
			}
			if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult.IsError {
				return appsError(), nil
			}
		}
		if err != nil {
			return result, err
		}
		if method == "tools/list" {
			if listed, ok := result.(*mcp.ListToolsResult); ok {
				projected := *listed
				projected.Tools = nil
				for _, tool := range listed.Tools {
					if !mode && isAppsTool(tool.Name) {
						continue
					}
					copy := *tool
					if mode && copy.Name == "datatug_run_query" {
						copy.Description = "Run an authorized saved SQL query and show its private results in the DataTug App. Only a summary enters model context."
						copy.Meta = mcp.Meta{"ui": map[string]any{"resourceUri": appsResourceURI, "visibility": []string{"model", "app"}}}
					}
					projected.Tools = append(projected.Tools, &copy)
				}
				return &projected, nil
			}
		}
		if method == "resources/list" && !mode {
			if listed, ok := result.(*mcp.ListResourcesResult); ok {
				projected := *listed
				projected.Resources = nil
				for _, resource := range listed.Resources {
					if resource.URI != appsResourceURI {
						projected.Resources = append(projected.Resources, resource)
					}
				}
				return &projected, nil
			}
		}
		return result, nil
	}
}

func (a *appsProtocol) addResource(s *mcp.Server) {
	s.AddResource(&mcp.Resource{URI: appsResourceURI, Name: "DataTug query explorer", MIMEType: appsMIME},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: appsResourceURI, MIMEType: appsMIME, Text: appsHTML,
				Meta: mcp.Meta{"ui": map[string]any{"csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}, "frameDomains": []string{}}}}}}}, nil
		})
}

func addRunQuery(s *mcp.Server, client *Client, description string) {
	mcp.AddTool(s, &mcp.Tool{Name: "datatug_run_query", Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input runInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			private, _ := ctx.Value(appsModeKey{}).(bool)
			if private {
				registry, _ := ctx.Value(appsRegistryKey{}).(*appsRegistry)
				if registry == nil {
					return appsError(), nil, nil
				}
				result, err := client.appsInitial(ctx, registry, input)
				return result, nil, err
			}
			out, err := client.runQuery(ctx, input)
			if err != nil {
				return nil, nil, err
			}
			body, err := json.Marshal(out)
			if err != nil || len(body) > maxBody {
				return nil, nil, errors.New("MCP output exceeds 2 MiB; request a smaller result")
			}
			return nil, out, nil
		})
}

func isAppsTool(name string) bool {
	return name == "datatug_apps_result" || name == "datatug_apps_rerun"
}
func appsError() *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: errAppsUnavailable.Error()}}}
}
func (a *appsProtocol) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, session := range a.modes {
		session.registry.close()
	}
}
func addAppsTools(s *mcp.Server, client *Client) {
	tool := func(name, description string) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Meta: mcp.Meta{"ui": map[string]any{"resourceUri": appsResourceURI, "visibility": []string{"app"}}}}
	}
	mcp.AddTool(s, tool("datatug_apps_result", "Read an existing authorized private result."), func(ctx context.Context, _ *mcp.CallToolRequest, input appsResultInput) (*mcp.CallToolResult, any, error) {
		registry, _ := ctx.Value(appsRegistryKey{}).(*appsRegistry)
		if registry == nil {
			return appsError(), nil, nil
		}
		result, err := client.appsContinue(ctx, registry, input.Handle, nil, false)
		return result, nil, err
	})
	mcp.AddTool(s, tool("datatug_apps_rerun", "Rerun the same authorized saved query with declared parameter values."), func(ctx context.Context, _ *mcp.CallToolRequest, input appsRerunInput) (*mcp.CallToolResult, any, error) {
		registry, _ := ctx.Value(appsRegistryKey{}).(*appsRegistry)
		if registry == nil {
			return appsError(), nil, nil
		}
		result, err := client.appsContinue(ctx, registry, input.Handle, input.Parameters, true)
		return result, nil, err
	})
}
