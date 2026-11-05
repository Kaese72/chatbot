// Package mcpserver exposes the chatbot's tool surface (internal/llm's
// ToolDefinitions, executed by internal/llm.Dispatcher) as a Model Context
// Protocol server over Streamable HTTP, so an external MCP client - another
// agent, an IDE, a desktop assistant - can control the system directly,
// without going through a chatbot conversation (and so without the chatbot's
// own Anthropic API key being spent).
//
// Authentication is the same usertoken middleware as the rest of the
// chatbot's API, except that API tokens are accepted here (see main.go). Tool
// calls are made against device-store with the caller's own bearer token,
// forwarded unchanged - not with an impersonation token. That way device-store
// sees exactly the credential the caller presented and enforces exactly its
// permissions: an API token scoped below its owner's permissions stays
// scoped, which minting a fresh impersonation token for the owner would not.
package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Kaese72/chatbot/internal/devicestore"
	"github.com/Kaese72/chatbot/internal/llm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Path is where the MCP endpoint is mounted. Deliberately outside
// /chatbot-service/v0/, so main.go can accept API tokens here while still
// refusing them on every conversation and api-key route.
const Path = "/chatbot-service/mcp"

type callerTokenContextKey struct{}

// callerToken is the tokenProvider of the device-store client built by
// NewHandler: the bearer token the MCP caller authenticated with, as placed
// on the context by the tool handler. Fails rather than calling device-store
// unauthenticated if there is none.
func callerToken(ctx context.Context) (string, error) {
	token, ok := ctx.Value(callerTokenContextKey{}).(string)
	if !ok || token == "" {
		return "", errors.New("no caller token on context; refusing to proceed")
	}
	return token, nil
}

// bearerFromHeader extracts the bearer token from an HTTP request header. The
// request has already been authenticated by usertoken's middleware by the time
// a tool handler runs, so this only re-reads what was verified there.
func bearerFromHeader(header http.Header) string {
	authHeader := header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
}

// NewHandler returns the HTTP handler serving the MCP endpoint. It must be
// mounted behind usertoken's middleware: it does no verification of its own,
// it only forwards the already-verified bearer token to device-store.
//
// The server is stateless (no Mcp-Session-Id): every request is
// self-contained, so it works across the chatbot's replicas without sticky
// sessions. Responses are plain JSON rather than SSE streams, since no tool
// streams progress.
func NewHandler(deviceStoreURL string, version string) http.Handler {
	dispatcher := llm.NewDispatcher(devicestore.NewClient(deviceStoreURL, callerToken))

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "humi",
		Title:   "Humi",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: "Controls the devices and device groups of a Humi home automation system. Use list_devices / list_groups (or fuzzy_find_group when a group is referred to by name) to discover IDs and the exact capability names and argument shapes, then trigger_device_capability / trigger_group_capability to act.",
	})

	for _, def := range llm.ToolDefinitions() {
		tool := def.OfTool
		if tool == nil {
			continue
		}
		name := tool.Name
		server.AddTool(&mcp.Tool{
			Name:        name,
			Description: tool.Description.Value,
			InputSchema: inputSchema(tool.InputSchema.Properties, tool.InputSchema.Required),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if req.Extra != nil {
				ctx = context.WithValue(ctx, callerTokenContextKey{}, bearerFromHeader(req.Extra.Header))
			}
			success, text := dispatcher.Dispatch(ctx, name, req.Params.Arguments)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: text}},
				IsError: !success,
			}, nil
		})
	}

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

// inputSchema turns an Anthropic tool's input schema into the JSON Schema
// object MCP expects, so internal/llm.ToolDefinitions stays the single
// definition of the tool surface for both the chatbot's own LLM and MCP
// clients.
func inputSchema(properties any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
