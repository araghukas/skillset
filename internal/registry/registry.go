package registry

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/storage"
	"gopkg.in/yaml.v3"
)

// skillFileName is the well-known file, one per skill directory, that
// carries the skill's metadata as YAML frontmatter followed by its
// instructions as a markdown body, per the agentskills.io spec
// (https://agentskills.io/specification). Every other file alongside it
// (scripts/, references/, assets/, ...) is treated as a supporting context
// file.
const skillFileName = "SKILL.md"

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

// Skill is a single entry in the registry's skill index.
type Skill struct {
	Metadata *skillsv1.SkillMetadata
}

// index is the immutable snapshot swapped in on each load.
type index struct {
	byName    map[string]*Skill
	indexedAt time.Time
}

// Registry holds the current in-memory skill index, loaded once from a
// storage.Backend at startup. Reads never block: Load builds a new index and
// atomically swaps it in.
type Registry struct {
	backend storage.Backend
	prefix  string
	current atomic.Pointer[index]
}

// New creates a Registry backed by the given storage.Backend, rooted at
// prefix. Call Load before serving traffic to perform the initial load.
func New(backend storage.Backend, prefix string) *Registry {
	r := &Registry{backend: backend, prefix: prefix}
	r.current.Store(&index{byName: map[string]*Skill{}})
	return r
}

// Get returns the named skill, or false if it isn't in the current index.
func (r *Registry) Get(name string) (*Skill, bool) {
	s, ok := r.current.Load().byName[name]
	return s, ok
}

// List returns every skill in the current index.
func (r *Registry) List() []*Skill {
	idx := r.current.Load()
	out := make([]*Skill, 0, len(idx.byName))
	for _, s := range idx.byName {
		out = append(out, s)
	}
	return out
}

// IndexedAt returns the time the current index was built.
func (r *Registry) IndexedAt() time.Time {
	return r.current.Load().indexedAt
}

// Reads skill definitions from the storage backend and atomically
// swaps them in, replacing the previous index. It returns the number of
// skills indexed.
//
// Each skill is a directory directly under prefix containing a SKILL.md;
// every other file in that directory (including SKILL.md itself) is loaded
// as a supporting context file. Load is meant to be called once at startup,
// after an init container has populated the backing directory - there is no
// runtime re-indexing.
func (r *Registry) Load(ctx context.Context) (int, error) {
	keys, err := r.backend.List(ctx, r.prefix)
	if err != nil {
		return 0, fmt.Errorf("registry: listing skills: %w", err)
	}

	byDir := make(map[string][]string)
	for _, key := range keys {
		rel := strings.TrimPrefix(strings.TrimPrefix(key, r.prefix), "/")
		if rel == "" || !strings.Contains(rel, "/") {
			continue // not inside a skill directory
		}
		dir := rel[:strings.IndexByte(rel, '/')]
		byDir[dir] = append(byDir[dir], key)
	}

	byName := make(map[string]*Skill, len(byDir))
	for name, files := range byDir {
		sk, err := loadSkill(ctx, r.backend, r.prefix, name, files)
		if err != nil {
			return 0, fmt.Errorf("registry: loading skill %q: %w", name, err)
		}
		slog.Debug("registry: loaded skill", "name", name, "files", len(files))
		byName[name] = sk
	}

	slog.Info("registry: index built", "prefix", r.prefix, "skills", len(byName))
	r.current.Store(&index{byName: byName, indexedAt: time.Now()})
	return len(byName), nil
}

func loadSkill(ctx context.Context, backend storage.Backend, prefix, name string, files []string) (*Skill, error) {
	dirKey := path.Join(prefix, name)
	skillKey := path.Join(dirKey, skillFileName)

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
				slog.Debug("registry: frontmatter parse failed", "skill", name, "key", key, "error", err)
				return nil, fmt.Errorf("parsing %q: %w", key, err)
			}
			slog.Debug("registry: parsed frontmatter", "skill", name,
				"fm.name", parsed.Name, "fm.description", parsed.Description,
				"fm.license", parsed.License, "fm.compatibility", parsed.Compatibility,
				"fm.allowed_tools", parsed.AllowedTools, "fm.metadata_keys", mapKeys(parsed.Metadata))
			fm = parsed
		}

		if !utf8.Valid(obj.Content) {
			// SkillContextFile.content is a proto3 string field, which must be
			// valid UTF-8; binary assets (images, etc.) can't be represented
			// here, so skip them rather than fail the whole skill.
			slog.Debug("registry: skipping non-UTF-8 file", "skill", name, "key", key)
			continue
		}

		contextFiles = append(contextFiles, &skillsv1.SkillContextFile{
			FilePath: strings.TrimPrefix(key, dirKey+"/"),
			Content:  string(obj.Content),
			MimeType: obj.ContentType,
		})
	}

	if fm == nil {
		slog.Debug("registry: no frontmatter found", "skill", name, "skill_key", skillKey)
		return nil, fmt.Errorf("missing %s", skillFileName)
	}
	if fm.Name == "" {
		slog.Debug("registry: frontmatter validation failed", "skill", name, "reason", "empty name")
		return nil, fmt.Errorf("frontmatter missing required field %q", "name")
	}
	if fm.Name != name {
		slog.Debug("registry: frontmatter validation failed", "skill", name, "reason", "name mismatch", "fm.name", fm.Name)
		return nil, fmt.Errorf("frontmatter name %q does not match skill directory %q", fm.Name, name)
	}
	if fm.Description == "" {
		slog.Debug("registry: frontmatter validation failed", "skill", name, "reason", "empty description")
		return nil, fmt.Errorf("frontmatter missing required field %q", "description")
	}

	return &Skill{
		Metadata: &skillsv1.SkillMetadata{
			Name:          name,
			Description:   fm.Description,
			License:       fm.License,
			Compatibility: fm.Compatibility,
			Metadata:      fm.Metadata,
			AllowedTools:  fm.AllowedTools,
			JsonSchema:    fm.Metadata["json_schema"],
			ContextFiles:  contextFiles,
		},
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
