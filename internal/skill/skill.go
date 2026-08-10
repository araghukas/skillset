// Package skill holds the core skill types: the metadata footprint of one
// agentskills.io skill and its supporting context files.
//
// These are the types the whole service passes around - internal/skillparse
// builds them, internal/registry indexes them, internal/proposals reads them
// out of git, and the MCP tool layer serves them. They live in their own
// package so none of those has to import another just to name a skill.
package skill

import (
	"maps"
	"slices"
)

// Metadata is the complete metadata footprint for an individual skill, per
// the agentskills.io SKILL.md frontmatter spec
// (https://agentskills.io/specification).
type Metadata struct {
	// Name must match the skill's directory name.
	Name string `json:"name"`

	// Description says what the skill does and when to use it.
	Description string `json:"description"`

	License string `json:"license,omitempty"`

	// Compatibility describes environment requirements, e.g. "requires docker".
	Compatibility string `json:"compatibility,omitempty"`

	// Metadata is an arbitrary client-defined key-value map.
	Metadata map[string]string `json:"metadata,omitempty"`

	// AllowedTools is a space-separated list of pre-approved tools. Optional
	// and experimental, per the spec.
	AllowedTools string `json:"allowed_tools,omitempty"`

	// JSONSchema is a convenience projection of Metadata["json_schema"], for
	// skills that also expose a strict function-call interface.
	JSONSchema string `json:"json_schema,omitempty"`

	// ContextFiles is SKILL.md plus any scripts/, references/, assets/ files.
	ContextFiles []ContextFile `json:"context_files,omitempty"`

	// Commit is the git commit SHA this skill's content was read at - the
	// revision the serving process cloned, or the ref that was resolved.
	// Echo it back in report_outcome: an outcome attached to a skill name
	// alone can't distinguish a skill that was always broken from one a
	// recent edit broke. Empty only if the server could not determine the
	// revision it is serving.
	Commit string `json:"commit,omitempty"`
}

// ContextFile is one non-SKILL.md supporting file: a prompt template, a
// script, a reference doc.
type ContextFile struct {
	// FilePath is relative to the skill directory, e.g. "scripts/run.sh".
	FilePath string `json:"file_path"`

	// Content is the file's content. Always valid UTF-8: skillparse skips
	// files that aren't, since there is no way to carry them as JSON strings.
	Content string `json:"content"`

	MimeType string `json:"mime_type,omitempty"`
}

// Clone returns a deep copy of m.
//
// The registry's copy of a skill is shared across concurrent requests, so
// anything that mutates metadata - stripping context files, appending a
// footer - must clone first. Sharing any mutable field with the original
// corrupts the shared index for every later request on that replica, so
// every reference field below is copied, not aliased.
//
// TestMetadataCloneCopiesEveryField guards this against a field being added
// to Metadata without a corresponding copy here.
func (m *Metadata) Clone() *Metadata {
	if m == nil {
		return nil
	}
	out := *m
	out.Metadata = maps.Clone(m.Metadata)
	// ContextFile is all value fields, so cloning the slice deep-copies it.
	out.ContextFiles = slices.Clone(m.ContextFiles)
	return &out
}

// WithoutContextFiles returns a copy of m carrying no context files, for the
// callers that want metadata only. m itself is left untouched.
func (m *Metadata) WithoutContextFiles() *Metadata {
	if m == nil {
		return nil
	}
	out := *m
	out.Metadata = maps.Clone(m.Metadata)
	out.ContextFiles = nil
	return &out
}

// ContextFile returns the named context file, or false if the skill has no
// file at that path.
func (m *Metadata) ContextFile(filePath string) (ContextFile, bool) {
	for _, cf := range m.ContextFiles {
		if cf.FilePath == filePath {
			return cf, true
		}
	}
	return ContextFile{}, false
}
