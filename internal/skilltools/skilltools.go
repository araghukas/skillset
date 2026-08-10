// Package skilltools registers skillsd's read-path MCP tools: discovering
// skills, fetching one, and fetching the client guide.
package skilltools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/araghukas/skillset/internal/clientguide"
	"github.com/araghukas/skillset/internal/registry"
	"github.com/araghukas/skillset/internal/skill"
	"github.com/araghukas/skillset/internal/toolresult"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// Add registers the read-path tools on srv. defaultMaxBytes is the
// context-file byte budget applied when a caller's get_skill call doesn't
// set max_bytes itself; pass 0 to use toolresult.DefaultMaxBytes.
func Add(srv *mcp.Server, reg *registry.Registry, defaultMaxBytes int) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_skills",
		Description: "List the skills this server currently serves. Returns metadata only - " +
			"name, description, compatibility, and the commit each was read at - not file " +
			"content. Start here to find a skill for a task, then call get_skill to read it. " +
			"Results are paginated and ordered by name.",
		Annotations: readOnly(),
	}, listSkills(reg))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_skill",
		Description: "Fetch one skill by name, optionally including its context files " +
			"(SKILL.md plus any scripts, references, and assets). Use paths to request " +
			"specific files when you already know which you need. Note this server does not " +
			"execute anything a skill ships with; running its scripts is up to you.",
		Annotations: readOnly(),
	}, getSkill(reg, defaultMaxBytes))

	clientguide.AddTool(srv)
}

func readOnly() *mcp.ToolAnnotations {
	// ReadOnlyHint makes DestructiveHint moot, but OpenWorldHint defaults
	// to true and must be turned off explicitly: these tools read a local
	// index, not the open internet.
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

func ptr[T any](v T) *T { return &v }

// ListSkillsInput selects and pages through the skill index.
type ListSkillsInput struct {
	Category string `json:"category,omitempty" jsonschema:"return only skills whose metadata[\"category\"] equals this exactly; omit to list every skill"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"next_cursor from a previous call; omit for the first page"`
	PageSize int    `json:"page_size,omitempty" jsonschema:"skills per page, 1-200; omit for 50"`
}

// SkillSummary is one skill as it appears in a listing: enough to decide
// whether to fetch it, and nothing more.
type SkillSummary struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Compatibility string            `json:"compatibility,omitempty" jsonschema:"environment this skill needs, e.g. \"requires docker\""`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Commit        string            `json:"commit,omitempty" jsonschema:"git commit this skill's content was read at; echo it back in report_outcome"`
	ContextFiles  int               `json:"context_files" jsonschema:"how many files get_skill would return for this skill"`
}

// ListSkillsOutput is one page of the index.
type ListSkillsOutput struct {
	Skills     []SkillSummary `json:"skills"`
	IndexedAt  time.Time      `json:"indexed_at,omitzero" jsonschema:"when this server built its index"`
	Total      int            `json:"total" jsonschema:"skills matching the filter across all pages"`
	NextCursor string         `json:"next_cursor,omitempty" jsonschema:"pass as cursor to fetch the next page; absent on the last page"`
}

func listSkills(reg *registry.Registry) mcp.ToolHandlerFor[ListSkillsInput, ListSkillsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListSkillsInput) (*mcp.CallToolResult, ListSkillsOutput, error) {
		all := reg.List()

		matching := make([]*registry.Skill, 0, len(all))
		for _, sk := range all {
			if in.Category != "" && sk.Metadata.Metadata["category"] != in.Category {
				continue
			}
			matching = append(matching, sk)
		}

		start, err := decodeCursor(in.Cursor)
		if err != nil {
			return nil, ListSkillsOutput{}, err
		}
		if start > len(matching) {
			start = len(matching)
		}

		size := in.PageSize
		switch {
		case size <= 0:
			size = defaultPageSize
		case size > maxPageSize:
			size = maxPageSize
		}

		end := min(start+size, len(matching))

		out := ListSkillsOutput{
			Skills:    make([]SkillSummary, 0, end-start),
			IndexedAt: reg.IndexedAt(),
			Total:     len(matching),
		}
		for _, sk := range matching[start:end] {
			out.Skills = append(out.Skills, summarize(sk.Metadata))
		}
		if end < len(matching) {
			out.NextCursor = encodeCursor(end)
		}
		return nil, out, nil
	}
}

func summarize(md *skill.Metadata) SkillSummary {
	return SkillSummary{
		Name:          md.Name,
		Description:   md.Description,
		Compatibility: md.Compatibility,
		Metadata:      md.Metadata,
		Commit:        md.Commit,
		ContextFiles:  len(md.ContextFiles),
	}
}

// GetSkillInput selects one skill and how much of it to return.
type GetSkillInput struct {
	SkillName           string   `json:"skill_name" jsonschema:"skill to fetch, as returned by list_skills"`
	IncludeContextFiles bool     `json:"include_context_files,omitempty" jsonschema:"return file content as well as metadata; omit for metadata only"`
	Paths               []string `json:"paths,omitempty" jsonschema:"return only these context files, by path relative to the skill directory (e.g. \"SKILL.md\"); omit for all of them"`
	MaxBytes            int      `json:"max_bytes,omitempty" jsonschema:"cap on total context-file bytes returned; omit for 262144. Files are returned whole, and any dropped are named in the reply"`
}

func getSkill(reg *registry.Registry, defaultMaxBytes int) mcp.ToolHandlerFor[GetSkillInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetSkillInput) (*mcp.CallToolResult, any, error) {
		if in.SkillName == "" {
			return nil, nil, fmt.Errorf("skill_name is required")
		}
		sk, ok := reg.Get(in.SkillName)
		if !ok {
			return nil, nil, fmt.Errorf("no skill named %q; call list_skills to see what is available", in.SkillName)
		}
		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = defaultMaxBytes
		}
		return &mcp.CallToolResult{
			Content: toolresult.Skill(sk.Metadata, in.IncludeContextFiles, in.Paths, maxBytes),
		}, nil, nil
	}
}

// Cursors are an encoded offset. The index is immutable for the lifetime of
// the process - it is loaded once at startup and never re-indexed - so an
// offset cannot drift out from under a caller, and there is nothing to gain
// from a more elaborate encoding. Base64 keeps callers from reading meaning
// into it and paging by arithmetic.
func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return 0, fmt.Errorf("invalid cursor %q: pass back a next_cursor from a previous list_skills call, or omit it to start from the beginning", cursor)
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor %q: pass back a next_cursor from a previous list_skills call, or omit it to start from the beginning", cursor)
	}
	return offset, nil
}
