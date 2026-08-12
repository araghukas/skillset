// Package clientguide embeds skillsd's own client-facing usage guide: a
// SKILL.md, shaped and parsed exactly like any other skill, that explains
// the skillsd and skillsd-registry tools to a calling agent. It's embedded
// in the server binary rather than read from the skills repo the registry
// indexes, so it can't drift from the tools it documents and is always
// available regardless of how (or whether) the skills repo is configured.
package clientguide

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"mime"
	"path/filepath"
	"strings"

	"github.com/araghukas/skillset/internal/skill"
	"github.com/araghukas/skillset/internal/skillparse"
	"github.com/araghukas/skillset/internal/storage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResourceURI is where the guide is available as an MCP resource, for
// clients that prefer pulling a resource over calling a tool. Same content
// as the get_client_guide tool and the connect-time Instructions - all
// three read the same embedded files, so none of them can drift from the
// others.
const ResourceURI = "skillsd://client-guide"

//go:embed skillsd-client
var embedded embed.FS

// Guide is the parsed metadata and content of the embedded client guide.
var Guide = mustLoad()

func mustLoad() *skill.Metadata {
	ctx := context.Background()
	backend := &embedBackend{fsys: embedded}

	files, err := backend.List(ctx, "")
	if err != nil {
		panic(fmt.Sprintf("clientguide: listing embedded files: %v", err))
	}

	md, err := skillparse.Load(ctx, backend, "", "skillsd-client", files)
	if err != nil {
		panic(fmt.Sprintf("clientguide: loading embedded skill: %v", err))
	}
	return md
}

// Instructions returns the guide text to send as an MCP server's
// ServerOptions.Instructions.
//
// Connect-time instructions are paid by every session that has the server
// attached, whether or not the session uses it, so they carry only the
// universal sections: references/intro.md (the mental model) and
// references/typical-flow.md (the order to call things in). The per-tool
// reference files (references/skillsd.md, references/registry.md) are
// served by get_client_guide and the resource instead, where an agent
// that's actually working with the tools can fetch them. SKILL.md itself
// carries only frontmatter (required by skillparse) and a pointer to
// references/intro.md - it is deliberately not part of the assembled
// guide, since its raw content includes that frontmatter block.
//
// appendix, if non-empty, is appended verbatim after the guide sections -
// see AddTool for why this needs to be the same string passed there. It's
// deployment-specific content the static embedded files can't know on
// their own: skillsd appends a one-line count of the skills it serves,
// skillsd-registry appends a "Repository configuration" section naming the
// actual repos/branches skills are read from and proposals are opened
// against (see registryconfig.Config and cmd/skillsd-registry/main.go).
func Instructions(appendix string) string {
	text := joinContextFiles([]string{"references/intro.md", "references/typical-flow.md"})
	if appendix != "" {
		text += "\n\n" + appendix
	}
	return text
}

// joinContextFiles concatenates the named context files' trimmed content,
// in order, skipping any that don't exist.
func joinContextFiles(paths []string) string {
	var out []string
	for _, p := range paths {
		if cf, ok := Guide.ContextFile(p); ok {
			out = append(out, strings.TrimSpace(cf.Content))
		}
	}
	return strings.Join(out, "\n\n")
}

// AddTool registers get_client_guide, and the equivalent resource at
// ResourceURI, on srv. Both skillsd and skillsd-registry call this - the
// guide describes tools on both, so both need a way to fetch it that
// doesn't depend on the client having surfaced connect-time Instructions.
//
// appendix, if non-empty, is appended verbatim after the guide text on both
// the tool and the resource - the same string passed to the server's
// connect-time Instructions (see Instructions), so all three delivery paths
// still agree.
func AddTool(srv *mcp.Server, appendix string) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_client_guide",
		Description: "Read the full guide to using skillsd and skillsd-registry: every tool on " +
			"both servers, in more detail than the connect-time instructions carry.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: falsePtr()},
	}, getClientGuideTool(appendix))

	srv.AddResource(&mcp.Resource{
		URI:         ResourceURI,
		Name:        "skillsd client guide",
		Description: "Full usage guide for skillsd and skillsd-registry - the same content as the get_client_guide tool.",
		MIMEType:    "text/markdown",
	}, getClientGuideResource(appendix))
}

func falsePtr() *bool { v := false; return &v }

func getClientGuideTool(appendix string) mcp.ToolHandlerFor[struct{}, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		text, err := guideText(appendix)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	}
}

func getClientGuideResource(appendix string) func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		text, err := guideText(appendix)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: ResourceURI, MIMEType: "text/markdown", Text: text},
			},
		}, nil
	}
}

// guideFiles is the full guide, every component's file included - what
// get_client_guide and its resource serve, regardless of which server they
// run on, since an agent using one server benefits from knowing what the
// other does too.
var guideFiles = []string{
	"references/intro.md",
	"references/skillsd.md",
	"references/registry.md",
	"references/typical-flow.md",
}

func guideText(appendix string) (string, error) {
	if _, ok := Guide.ContextFile("references/intro.md"); !ok {
		return "", fmt.Errorf("the client guide is missing references/intro.md; this is a bug in the server build")
	}
	text := joinContextFiles(guideFiles)
	if appendix != "" {
		text += "\n\n" + appendix
	}
	return text, nil
}

// embedBackend is a minimal storage.Backend over an embed.FS, just enough
// to feed this package's single embedded skill through skillparse.Load.
type embedBackend struct {
	fsys embed.FS
}

func (b *embedBackend) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := fs.WalkDir(b.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		keys = append(keys, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func (b *embedBackend) Get(ctx context.Context, key string) (storage.FileObject, error) {
	content, err := b.fsys.ReadFile(key)
	if err != nil {
		return storage.FileObject{}, err
	}
	return storage.FileObject{
		Key:         key,
		Content:     content,
		ContentType: mime.TypeByExtension(filepath.Ext(key)),
	}, nil
}
