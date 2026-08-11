//go:build e2e

// Package verify exercises a running local deployment (`make dev` / `tilt
// up`) over real HTTP, the same path an agent uses. It is not run as part
// of the normal test suite - `go test ./...` never builds this package,
// only `go test -tags e2e ./local/verify/...` (aliased as `make verify`)
// does.
//
// A hand-rolled JSON-RPC client over curl would have to speak the
// Streamable HTTP transport itself - SSE framing, the Accept and
// MCP-Protocol-Version headers - which is a worse client than grpcurl was
// for the protocol it replaced. The MCP Go SDK's own client hides all of
// that, and lets these tests assert on things bash never could: that a
// tool's annotations match its actual behavior, that an unknown skill
// arrives as a tool error rather than a protocol error, that schema
// validation rejects a missing required field before a handler runs.
//
// Deliberately not internal/*: this package exercises the deployed
// binaries over the network, not the Go API, so importing anything under
// internal/ here would defeat the point - a bug only reachable through
// the wire format wouldn't show up.
package verify

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// skillsdAddr and registryAddr mirror the old SKILLSD_ADDR / REGISTRY_ADDR
// env vars, defaulting to the port-forwards `make dev` sets up.
func skillsdAddr() string  { return getenv("SKILLSD_ADDR", "localhost:8080") }
func registryAddr() string { return getenv("REGISTRY_ADDR", "localhost:8081") }
func skillName() string    { return getenv("SKILL_NAME", "internal-comms") }

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// connectTimeout bounds how long a single connect attempt waits, so a
// misconfigured deployment fails fast with a clear timeout rather than
// hanging the whole suite.
const connectTimeout = 10 * time.Second

// connect opens an MCP session against the server at addr (a bare
// host:port, as SKILLSD_ADDR/REGISTRY_ADDR are), over the real HTTP
// Streamable transport - not mcp.NewInMemoryTransports, which would skip
// the HTTP layer, the chart, the Service, and the probe path entirely,
// i.e. everything this harness exists to test.
func connect(t *testing.T, addr string) *mcp.ClientSession {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "skillset-verify", Version: "e2e"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: "http://" + addr + "/mcp"}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connecting to %s: %v\nis the deployment up and port-forwarded? "+
			"(make dev / tilt up; SKILLSD_ADDR/REGISTRY_ADDR override the default localhost:8080/8081)", addr, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callTool calls name with args and fails the test on a protocol-level
// error (a malformed request, a transport failure) - callers that expect
// a tool-level error (IsError on the result) check that on the returned
// result themselves, since that is not a Go error at this layer by
// design: see mcp.ToolHandlerFor's doc comment on the distinction.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("calling %s: %v", name, err)
	}
	return res
}

func contentText(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}
