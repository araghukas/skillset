package skilltools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/araghukas/skillset/internal/registry"
	"github.com/araghukas/skillset/internal/storage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeSkill(t *testing.T, root, name, description string, extra map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nbody of " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, body := range extra {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testRegistry(t *testing.T, names ...string) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		writeSkill(t, root, n, "does "+n, nil)
	}
	reg := registry.New(storage.NewFSBackend(root), "", "abc123")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return reg
}

// connect wires an in-memory client/server pair, which is the only way to
// assert on things the protocol layer owns: schema validation, tool
// annotations, and whether an error reached the model as a tool error or
// killed the request.
func connect(t *testing.T, reg *registry.Registry) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "skillsd", Version: "test"}, nil)
	Add(srv, reg, 0, reg.Catalog())

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Wait()
	})
	return cs
}

// TestToolSchemasMarkTheRightFieldsRequired is the test that justifies
// dropping protobuf.
//
// Required-ness is inferred from the absence of `omitempty` on a struct
// field. Generated protobuf structs carry `omitempty` on every field
// unconditionally, so they would produce schemas with nothing required at
// all - an agent would get no signal about which arguments it must supply.
// Hand-written inputs give us the distinction back, and this asserts it
// actually lands in the advertised schema.
func TestToolSchemasMarkTheRightFieldsRequired(t *testing.T) {
	cs := connect(t, testRegistry(t, "alpha"))

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	required := map[string][]string{}
	for _, tool := range tools.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshalling %s input schema: %v", tool.Name, err)
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshalling %s input schema: %v", tool.Name, err)
		}
		required[tool.Name] = schema.Required
	}

	if got := required["get_skill"]; len(got) != 1 || got[0] != "skill_name" {
		t.Errorf("get_skill required = %v, want [skill_name]", got)
	}
	// Every field on list_skills is an optional filter or pagination knob.
	if got := required["list_skills"]; len(got) != 0 {
		t.Errorf("list_skills required = %v, want none", got)
	}
}

func TestToolAnnotations(t *testing.T) {
	cs := connect(t, testRegistry(t, "alpha"))

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools.Tools))
	}

	for _, tool := range tools.Tools {
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tool.Name)
		}
		// OpenWorldHint defaults to true when left nil, which would claim
		// these tools reach the open internet. They read a local index.
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s does not explicitly set OpenWorldHint=false", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description; the description is the interface", tool.Name)
		}
	}
}

// A missing required argument should be rejected by schema validation
// before the handler runs.
func TestMissingRequiredArgumentIsRejected(t *testing.T) {
	cs := connect(t, testRegistry(t, "alpha"))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_skill",
		Arguments: map[string]any{},
	})
	if err == nil && !res.IsError {
		t.Fatal("calling get_skill with no arguments succeeded")
	}
}

// An unknown skill is a fact the model should see and act on, not a
// transport failure. It must arrive as a tool error with a usable message.
func TestUnknownSkillIsAToolError(t *testing.T) {
	cs := connect(t, testRegistry(t, "alpha"))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_skill",
		Arguments: map[string]any{"skill_name": "nope"},
	})
	if err != nil {
		t.Fatalf("unknown skill produced a protocol error rather than a tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("unknown skill did not set IsError")
	}
	text := contentText(t, res)
	if !strings.Contains(text, "nope") || !strings.Contains(text, "list_skills") {
		t.Errorf("error message should name the skill and point at a recovery: %q", text)
	}
}

func TestGetSkillReturnsMetadataOnlyByDefault(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "does alpha", map[string]string{"references/notes.txt": "some notes"})
	reg := registry.New(storage.NewFSBackend(root), "", "abc123")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, reg)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_skill",
		Arguments: map[string]any{"skill_name": "alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text := contentText(t, res); strings.Contains(text, "some notes") {
		t.Error("get_skill returned file content without include_context_files")
	}

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_skill",
		Arguments: map[string]any{"skill_name": "alpha", "include_context_files": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := contentText(t, res)
	if !strings.Contains(text, "some notes") {
		t.Error("get_skill with include_context_files did not return file content")
	}
	// Content is hand-built per file rather than one JSON blob, so a model
	// reads Markdown as Markdown.
	if res.StructuredContent != nil {
		t.Error("get_skill set StructuredContent; content-bearing tools return text blocks only")
	}
}

func TestGetSkillPathsFilter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "does alpha", map[string]string{
		"references/notes.txt": "some notes",
		"scripts/run.sh":       "#!/bin/sh\necho hi\n",
	})
	reg := registry.New(storage.NewFSBackend(root), "", "abc123")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, reg)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_skill",
		Arguments: map[string]any{
			"skill_name":            "alpha",
			"include_context_files": true,
			"paths":                 []string{"scripts/run.sh"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := contentText(t, res)
	if !strings.Contains(text, "echo hi") {
		t.Error("paths filter dropped the file that was asked for")
	}
	if strings.Contains(text, "some notes") {
		t.Error("paths filter returned a file that was not asked for")
	}
}

// The byte budget must drop whole files and say which, rather than cutting
// one off mid-content.
func TestGetSkillByteBudgetDropsWholeFiles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "does alpha", map[string]string{
		"references/big.txt": strings.Repeat("x", 4096),
	})
	reg := registry.New(storage.NewFSBackend(root), "", "abc123")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, reg)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_skill",
		Arguments: map[string]any{
			"skill_name":            "alpha",
			"include_context_files": true,
			"max_bytes":             100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := contentText(t, res)
	if !strings.Contains(text, "omitted") || !strings.Contains(text, "references/big.txt") {
		t.Errorf("over-budget reply should name the omitted file and how to get it: %q", text)
	}
	if strings.Contains(text, strings.Repeat("x", 200)) {
		t.Error("over-budget file was partially included; files should be dropped whole")
	}
}

func TestListSkillsPaginates(t *testing.T) {
	cs := connect(t, testRegistry(t, "alpha", "bravo", "charlie", "delta"))
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_skills",
		Arguments: map[string]any{"page_size": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var page ListSkillsOutput
	decodeStructured(t, res, &page)

	if len(page.Skills) != 2 {
		t.Fatalf("expected 2 skills on the first page, got %d", len(page.Skills))
	}
	if page.Total != 4 {
		t.Errorf("Total = %d, want 4", page.Total)
	}
	// Ordering must be stable for a cursor to mean anything.
	if page.Skills[0].Name != "alpha" || page.Skills[1].Name != "bravo" {
		t.Errorf("first page is not sorted by name: %v", page.Skills)
	}
	if page.NextCursor == "" {
		t.Fatal("expected a next_cursor with more results outstanding")
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_skills",
		Arguments: map[string]any{"page_size": 2, "cursor": page.NextCursor},
	})
	if err != nil {
		t.Fatal(err)
	}
	var second ListSkillsOutput
	decodeStructured(t, res, &second)

	if len(second.Skills) != 2 || second.Skills[0].Name != "charlie" {
		t.Errorf("second page = %v, want charlie and delta", second.Skills)
	}
	if second.NextCursor != "" {
		t.Errorf("last page should carry no cursor, got %q", second.NextCursor)
	}
}

// list_skills never returns file bodies. This is the behavior change that
// motivated the tool split: the old gRPC ListSkills could return every file
// of every skill in one reply, routinely over 4 MiB, which under MCP would
// land in a model's context window.
func TestListSkillsNeverReturnsFileContent(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "does alpha", map[string]string{"references/notes.txt": "SECRET-MARKER"})
	reg := registry.New(storage.NewFSBackend(root), "", "abc123")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, reg)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_skills"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contentText(t, res), "SECRET-MARKER") {
		t.Error("list_skills returned context-file content")
	}

	var page ListSkillsOutput
	decodeStructured(t, res, &page)
	if len(page.Skills) != 1 || page.Skills[0].ContextFiles != 2 {
		t.Errorf("expected a count of context files instead of their content, got %+v", page.Skills)
	}
}

func TestListSkillsRejectsGarbageCursor(t *testing.T) {
	cs := connect(t, testRegistry(t, "alpha"))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_skills",
		Arguments: map[string]any{"cursor": "!!!not-base64!!!"},
	})
	if err != nil {
		t.Fatalf("bad cursor produced a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("bad cursor was accepted")
	}
}

func TestGetClientGuideReturnsTheGuide(t *testing.T) {
	cs := connect(t, testRegistry(t, "alpha"))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_client_guide"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("get_client_guide failed: %s", contentText(t, res))
	}
	if text := contentText(t, res); len(text) < 500 {
		t.Errorf("client guide looks truncated or empty (%d bytes)", len(text))
	}
}

func contentText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, into any) {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatal("result carries no structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}
