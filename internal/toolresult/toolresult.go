// Package toolresult renders domain values into MCP tool results, and
// applies the size budgets that keep those results from overwhelming a
// calling agent's context window.
//
// The budgets matter more here than the equivalent gRPC message limits did.
// A gRPC response was consumed by a client process, which could stream or
// discard it; a tool result is injected into a model's context, where a
// single oversized reply can crowd out the task it was meant to serve. So
// the rule throughout is: never truncate mid-item, and always say what was
// left out and how to ask for it.
package toolresult

import (
	"fmt"
	"slices"
	"strings"

	"github.com/araghukas/skillset/internal/skill"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultMaxBytes bounds the total context-file content one call returns.
const DefaultMaxBytes = 256 << 10 // 256 KiB

// DefaultMaxDiffBytes bounds a unified diff carried in a proposal.
const DefaultMaxDiffBytes = 64 << 10 // 64 KiB

// Text is a shorthand for a single text content block.
func Text(format string, args ...any) *mcp.TextContent {
	return &mcp.TextContent{Text: fmt.Sprintf(format, args...)}
}

// Skill renders a skill as tool content: a metadata header, then one block
// per context file.
//
// The content is built by hand rather than left to the SDK's automatic
// marshalling of a typed result. A typed result would put the whole skill
// on the wire twice - once as structuredContent, once as the same JSON
// escaped into a text block - and the escaped copy is the worst possible
// rendering of a Markdown file for a model to read.
//
// paths, when non-empty, selects which context files to return. maxBytes
// caps their total size; files are included whole, in order, until the
// budget is spent, and a closing note names what was dropped.
func Skill(md *skill.Metadata, includeContextFiles bool, paths []string, maxBytes int) []mcp.Content {
	out := []mcp.Content{Text("%s", header(md))}
	if !includeContextFiles {
		return out
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	files := selectFiles(md.ContextFiles, paths)

	var used int
	var omitted []string
	for _, cf := range files {
		if used+len(cf.Content) > maxBytes && used > 0 {
			omitted = append(omitted, cf.FilePath)
			continue
		}
		used += len(cf.Content)
		out = append(out, Text("--- %s (%s, %d bytes) ---\n%s",
			cf.FilePath, mimeOrUnknown(cf.MimeType), len(cf.Content), cf.Content))
	}

	if len(omitted) > 0 {
		out = append(out, Text(
			"omitted %d of %d context files to stay within the %d byte budget: %s\n"+
				"re-call get_skill with paths set to the ones you need, or raise max_bytes.",
			len(omitted), len(files), maxBytes, strings.Join(omitted, ", ")))
	}
	return out
}

// header renders a skill's metadata as readable text.
func header(md *skill.Metadata) string {
	var b strings.Builder
	fmt.Fprintf(&b, "skill: %s\n", md.Name)
	fmt.Fprintf(&b, "description: %s\n", md.Description)
	if md.Commit != "" {
		fmt.Fprintf(&b, "commit: %s\n", md.Commit)
	}
	if md.License != "" {
		fmt.Fprintf(&b, "license: %s\n", md.License)
	}
	if md.Compatibility != "" {
		fmt.Fprintf(&b, "compatibility: %s\n", md.Compatibility)
	}
	if md.AllowedTools != "" {
		fmt.Fprintf(&b, "allowed-tools: %s\n", md.AllowedTools)
	}
	if len(md.Metadata) > 0 {
		fmt.Fprintf(&b, "metadata: %s\n", formatMap(md.Metadata))
	}
	fmt.Fprintf(&b, "context files: %d\n", len(md.ContextFiles))
	return b.String()
}

// selectFiles filters files to the requested paths, preserving the order
// the caller asked for. An empty paths list returns everything, in the
// order the skill carries.
func selectFiles(files []skill.ContextFile, paths []string) []skill.ContextFile {
	if len(paths) == 0 {
		return files
	}
	byPath := make(map[string]skill.ContextFile, len(files))
	for _, cf := range files {
		byPath[cf.FilePath] = cf
	}
	out := make([]skill.ContextFile, 0, len(paths))
	for _, p := range paths {
		if cf, ok := byPath[p]; ok {
			out = append(out, cf)
		}
	}
	return out
}

func mimeOrUnknown(mimeType string) string {
	if mimeType == "" {
		return "unknown type"
	}
	return mimeType
}

func formatMap(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	// Sorted so repeated calls render identically.
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

// TruncateDiff caps a unified diff at maxBytes, cutting at the last
// complete hunk header so the result is still a readable (if partial)
// diff rather than a fragment ending mid-line. It reports whether
// anything was removed.
func TruncateDiff(diff string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDiffBytes
	}
	if len(diff) <= maxBytes {
		return diff, false
	}

	cut := diff[:maxBytes]
	// Prefer the last hunk boundary; fall back to the last newline so we
	// never end mid-line.
	if i := strings.LastIndex(cut, "\n@@"); i > 0 {
		cut = cut[:i+1]
	} else if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i+1]
	}
	return cut, true
}
