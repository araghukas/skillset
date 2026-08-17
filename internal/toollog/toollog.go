// Package toollog provides an MCP receiving middleware that logs every tool
// call at info level. It is shared by skillsd and skillsd-registry so that
// every tool - list_skills, get_skill, record_suggestion, and any tool
// either server adds in the future - is logged uniformly from one place,
// rather than each tool handler logging its own invocation.
package toollog

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodCallTool is the JSON-RPC method name for a tool invocation. It's
// unexported in the SDK, so it's restated here.
const methodCallTool = "tools/call"

// Middleware logs every tools/call request: the tool name, how long it took,
// and how it ended. Other JSON-RPC methods (initialize, tools/list, ...)
// pass through unlogged, since tool invocation is the surface this exists to
// audit.
func Middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != methodCallTool {
			return next(ctx, method, req)
		}

		name := "unknown"
		if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
			name = p.Name
		}

		start := time.Now()
		result, err := next(ctx, method, req)
		durationMS := time.Since(start).Milliseconds()

		if err != nil {
			slog.Error("tool call failed", "tool", name, "duration_ms", durationMS, "error", err)
			return result, err
		}
		if res, ok := result.(*mcp.CallToolResult); ok && res.IsError {
			slog.Info("tool call returned an error result", "tool", name, "duration_ms", durationMS)
			return result, err
		}
		slog.Info("tool call", "tool", name, "duration_ms", durationMS)
		return result, err
	}
}
