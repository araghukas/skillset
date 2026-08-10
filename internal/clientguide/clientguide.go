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
// three read the embedded SKILL.md, so none of them can drift from the
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
// ServerOptions.Instructions for the given component: "skillsd" or
// "registry".
//
// The embedded SKILL.md marks its sections with HTML comments
// (<!-- shared:start/end -->, <!-- skillsd:start/end -->,
// <!-- registry:start/end -->) so a server only advertises the tools it
// actually has: skillsd's Instructions carries the "shared" sections plus
// "skillsd", never the proposal-workflow content that lives under
// "registry", and vice versa. A section name may appear more than once in
// the document (skillsd-client/SKILL.md uses two separate "shared" blocks);
// every occurrence is included, concatenated in document order.
func Instructions(component string) string {
	cf, ok := Guide.ContextFile("SKILL.md")
	if !ok {
		return ""
	}

	var out []string
	for _, name := range []string{"shared", component} {
		out = append(out, extractSections(cf.Content, name)...)
	}
	return strings.Join(out, "\n\n")
}

// extractSections returns the trimmed content of every
// <!-- name:start -->...<!-- name:end --> region in content, in document
// order.
func extractSections(content, name string) []string {
	start := "<!-- " + name + ":start -->"
	end := "<!-- " + name + ":end -->"

	var out []string
	for {
		i := strings.Index(content, start)
		if i < 0 {
			break
		}
		rest := content[i+len(start):]
		j := strings.Index(rest, end)
		if j < 0 {
			// An unterminated section is a bug in the source document, not
			// a runtime condition to recover from silently: better to drop
			// it loudly (via the accompanying test) than serve a truncated
			// guide.
			break
		}
		out = append(out, strings.TrimSpace(rest[:j]))
		content = rest[j+len(end):]
	}
	return out
}

// AddTool registers get_client_guide, and the equivalent resource at
// ResourceURI, on srv. Both skillsd and skillsd-registry call this - the
// guide describes tools on both, so both need a way to fetch it that
// doesn't depend on the client having surfaced connect-time Instructions.
func AddTool(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_client_guide",
		Description: "Read the full guide to using skillsd and skillsd-registry: which tools " +
			"exist on each, how the propose/endorse/submit workflow fits together, and what the " +
			"outcome verdicts mean. The same text is delivered as this server's instructions at " +
			"connect time; call this if you need it again or did not receive it.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: falsePtr()},
	}, getClientGuideTool)

	srv.AddResource(&mcp.Resource{
		URI:         ResourceURI,
		Name:        "skillsd client guide",
		Description: "Full usage guide for skillsd and skillsd-registry - the same content as the get_client_guide tool.",
		MIMEType:    "text/markdown",
	}, getClientGuideResource)
}

func falsePtr() *bool { v := false; return &v }

func getClientGuideTool(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	cf, ok := Guide.ContextFile("SKILL.md")
	if !ok {
		return nil, nil, fmt.Errorf("the client guide is missing its SKILL.md; this is a bug in the server build")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: cf.Content}},
	}, nil, nil
}

func getClientGuideResource(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	cf, ok := Guide.ContextFile("SKILL.md")
	if !ok {
		return nil, fmt.Errorf("the client guide is missing its SKILL.md; this is a bug in the server build")
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{URI: ResourceURI, MIMEType: "text/markdown", Text: cf.Content},
		},
	}, nil
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
