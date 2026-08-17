// Package toollog provides an MCP receiving middleware that logs every tool
// call at info level. It is shared by skillsd and skillsd-registry so that
// every tool - list_skills, get_skill, record_suggestion, and any tool
// either server adds in the future - is logged uniformly from one place,
// rather than each tool handler logging its own invocation.
package toollog

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodCallTool is the JSON-RPC method name for a tool invocation. It's
// unexported in the SDK, so it's restated here.
const methodCallTool = "tools/call"

// Middleware logs every tools/call request: the tool name, how long it took,
// how it ended, and - for tools listed in extractors - fields specific to
// that call, such as which skill a get_skill or record_suggestion named.
// Other JSON-RPC methods (initialize, tools/list, ...) pass through
// unlogged, since tool invocation is the surface this exists to audit.
func Middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != methodCallTool {
			return next(ctx, method, req)
		}

		name := "unknown"
		var rawArgs json.RawMessage
		if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
			name = p.Name
			rawArgs = p.Arguments
		}
		fields := extractors[name]

		start := time.Now()
		result, err := next(ctx, method, req)
		durationMS := time.Since(start).Milliseconds()

		args := []any{"tool", name, "duration_ms", durationMS}
		if fields.request != nil {
			args = append(args, fields.request(rawArgs)...)
		}

		if err != nil {
			slog.Error("tool call failed", append(args, "error", err)...)
			return result, err
		}
		res, ok := result.(*mcp.CallToolResult)
		if ok && res.IsError {
			slog.Info("tool call returned an error result", args...)
			return result, err
		}
		if ok && fields.response != nil {
			if raw, ok := res.StructuredContent.(json.RawMessage); ok {
				args = append(args, fields.response(raw)...)
			}
		}
		slog.Info("tool call", args...)
		return result, err
	}
}
