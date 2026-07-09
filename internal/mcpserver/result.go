package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// jsonResult marshals v (indented, per this unit's "応答形式: JSON(構造化)"
// requirement) into a *mcp.CallToolResult whose single TextContent item
// carries the JSON — the shape every read/write tool handler in this package
// returns on success. Content is populated explicitly (rather than relying
// on ToolHandlerFor's StructuredContent auto-population) so every tool's
// response is self-describing JSON text an agent can read directly, while
// StructuredContent is also set for MCP clients that prefer the typed path.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("mcpserver: marshal result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}

// toolError builds a *mcp.CallToolResult reporting a tool-level failure
// (IsError=true, per mcp.CallToolResult's own doc comment: "any errors that
// originate from the tool should be reported inside the Content field... not
// as an MCP protocol-level error response" — this is what lets an agent see
// the failure and self-correct, e.g. by retrying get_bead with a corrected
// ID). op names the failing operation for the message; err is wrapped with
// %v (not %w — this is a terminal, string-only rendering, not a Go error to
// be further unwrapped).
func toolError(op string, err error) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: %v", op, err)}},
	}, nil
}
