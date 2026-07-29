// Package skillparse reads a single skill's SKILL.md frontmatter and
// supporting context files off a storage.Backend and builds the
// skills.v1.SkillMetadata proto for it. It is shared by internal/registry
// (a static, once-loaded index) and the git-backed proposal reader (which
// resolves the same shape at an arbitrary commit), so the two never drift in
// how they parse or validate a skill.
package skillparse

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"unicode/utf8"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/storage"
	"gopkg.in/yaml.v3"
)

// SkillFileName is the well-known file, one per skill directory, that
// carries the skill's metadata as YAML frontmatter followed by its
// instructions as a markdown body, per the agentskills.io spec
// (https://agentskills.io/specification). Every other file alongside it
// (scripts/, references/, assets/, ...) is treated as a supporting context
// file.
const SkillFileName = "SKILL.md"

var frontmatterDelim = []byte("---")

// frontmatter is the on-disk shape of the YAML block at the top of
// SKILL.md, per the agentskills.io frontmatter fields.
type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

// Load reads a single skill named name, rooted at prefix within backend,
// from the given set of file keys (all keys inside the skill's directory,
// including its SKILL.md). It returns the fully populated SkillMetadata, or
// an error if SKILL.md is missing or its frontmatter fails validation.
func Load(ctx context.Context, backend storage.Backend, prefix, name string, files []string) (*skillsv1.SkillMetadata, error) {
	dirKey := path.Join(prefix, name)
	skillKey := path.Join(dirKey, SkillFileName)

	var fm *frontmatter
	var contextFiles []*skillsv1.SkillContextFile

	for _, key := range files {
		obj, err := backend.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("reading %q: %w", key, err)
		}

		if key == skillKey {
			parsed, err := parseFrontmatter(obj.Content)
			if err != nil {
				slog.Debug("skillparse: frontmatter parse failed", "skill", name, "key", key, "error", err)
				return nil, fmt.Errorf("parsing %q: %w", key, err)
			}
			slog.Debug("skillparse: parsed frontmatter", "skill", name,
				"fm.name", parsed.Name, "fm.description", parsed.Description,
				"fm.license", parsed.License, "fm.compatibility", parsed.Compatibility,
				"fm.allowed_tools", parsed.AllowedTools, "fm.metadata_keys", mapKeys(parsed.Metadata))
			fm = parsed
		}

		if !utf8.Valid(obj.Content) {
			// SkillContextFile.content is a proto3 string field, which must be
			// valid UTF-8; binary assets (images, etc.) can't be represented
			// here, so skip them rather than fail the whole skill.
			slog.Debug("skillparse: skipping non-UTF-8 file", "skill", name, "key", key)
			continue
		}

		contextFiles = append(contextFiles, &skillsv1.SkillContextFile{
			FilePath: strings.TrimPrefix(key, dirKey+"/"),
			Content:  string(obj.Content),
			MimeType: obj.ContentType,
		})
	}

	if fm == nil {
		slog.Debug("skillparse: no frontmatter found", "skill", name, "skill_key", skillKey)
		return nil, fmt.Errorf("missing %s", SkillFileName)
	}
	if fm.Name == "" {
		slog.Debug("skillparse: frontmatter validation failed", "skill", name, "reason", "empty name")
		return nil, fmt.Errorf("frontmatter missing required field %q", "name")
	}
	if fm.Name != name {
		slog.Debug("skillparse: frontmatter validation failed", "skill", name, "reason", "name mismatch", "fm.name", fm.Name)
		return nil, fmt.Errorf("frontmatter name %q does not match skill directory %q", fm.Name, name)
	}
	if fm.Description == "" {
		slog.Debug("skillparse: frontmatter validation failed", "skill", name, "reason", "empty description")
		return nil, fmt.Errorf("frontmatter missing required field %q", "description")
	}

	return &skillsv1.SkillMetadata{
		Name:          name,
		Description:   fm.Description,
		License:       fm.License,
		Compatibility: fm.Compatibility,
		Metadata:      fm.Metadata,
		AllowedTools:  fm.AllowedTools,
		JsonSchema:    fm.Metadata["json_schema"],
		ContextFiles:  contextFiles,
	}, nil
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// parseFrontmatter splits a SKILL.md file into its leading "---"-delimited
// YAML block and parses it. The markdown body that follows is not parsed
// here; it's carried verbatim as part of the file's SkillContextFile entry.
func parseFrontmatter(content []byte) (*frontmatter, error) {
	trimmed := bytes.TrimLeft(content, " \t\r\n")
	trimmed = bytes.TrimPrefix(trimmed, []byte("\xef\xbb\xbf"))
	trimmed = bytes.TrimLeft(trimmed, " \t\r\n")
	if !bytes.HasPrefix(trimmed, frontmatterDelim) {
		return nil, fmt.Errorf("missing frontmatter (must start with '---')")
	}

	rest := trimmed[len(frontmatterDelim):]
	fmBytes, _, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return nil, fmt.Errorf("unterminated frontmatter block")
	}

	var fm frontmatter
	if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
		return nil, fmt.Errorf("invalid frontmatter YAML: %w", err)
	}

	return &fm, nil
}
