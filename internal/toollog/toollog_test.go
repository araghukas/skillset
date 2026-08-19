package toollog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func call(t *testing.T, name string, args, structuredOut string, handlerErr error) string {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if handlerErr != nil {
			return nil, handlerErr
		}
		return &mcp.CallToolResult{StructuredContent: json.RawMessage(structuredOut)}, nil
	}

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(args)}}
	if _, err := Middleware(next)(context.Background(), methodCallTool, req); err != handlerErr {
		t.Fatalf("unexpected error: %v", err)
	}
	return buf.String()
}

func TestMiddleware_RecordSuggestionFields(t *testing.T) {
	out := call(t, "record_suggestion",
		`{"skill_name":"deploy-helm","agent_id":"agent-1","commit_message":"fix stale flag"}`,
		`{}`, nil)

	for _, want := range []string{`tool=record_suggestion`, `skill=deploy-helm`, `commit_message="fix stale flag"`, `auto_submitted=false`} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %q; got %q", want, out)
		}
	}
}

func TestMiddleware_EndorseSuggestionFields(t *testing.T) {
	out := call(t, "endorse_suggestion",
		`{"branch":"suggestions/agent-1/deploy-helm/fix","agent_id":"agent-2"}`,
		`{"auto_submitted":{"pull_request_url":"https://example.com/pr/7"}}`, nil)

	for _, want := range []string{`tool=endorse_suggestion`, `branch=suggestions/agent-1/deploy-helm/fix`, `agent=agent-2`, `auto_submitted=true`} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %q; got %q", want, out)
		}
	}
}

func TestMiddleware_GetSkillFields(t *testing.T) {
	out := call(t, "get_skill", `{"skill_name":"deploy-helm"}`, `{}`, nil)
	if !strings.Contains(out, "skill=deploy-helm") {
		t.Errorf("log line missing skill field; got %q", out)
	}
}

func TestMiddleware_UnknownToolHasNoExtraFields(t *testing.T) {
	out := call(t, "list_skills", `{"category":"deploy"}`, `{"total":3}`, nil)
	if strings.Contains(out, "category=") {
		t.Errorf("expected no request fields for unregistered tool; got %q", out)
	}
}
