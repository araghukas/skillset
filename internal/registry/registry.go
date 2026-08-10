package registry

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/araghukas/skillset/internal/skill"
	"github.com/araghukas/skillset/internal/skillparse"
	"github.com/araghukas/skillset/internal/storage"
)

// Skill is a single entry in the registry's skill index.
//
// Metadata is shared across every concurrent request that reads this entry.
// Callers that need to mutate it - stripping context files, say - must
// Clone it first; see skill.Metadata.Clone.
type Skill struct {
	Metadata *skill.Metadata
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
	commit  string
	current atomic.Pointer[index]
}

// New creates a Registry backed by the given storage.Backend, rooted at
// prefix. commit is the revision the backend's content was read at; it is
// stamped onto every skill's metadata so outcome reports can be attributed
// to a specific version of the content. Pass an empty string if the
// revision genuinely isn't known - the metadata field is then empty, and
// callers downstream treat the reports as unversioned.
//
// Call Load before serving traffic to perform the initial load.
func New(backend storage.Backend, prefix, commit string) *Registry {
	r := &Registry{backend: backend, prefix: prefix, commit: commit}
	r.current.Store(&index{byName: map[string]*Skill{}})
	return r
}

// Get returns the named skill, or false if it isn't in the current index.
func (r *Registry) Get(name string) (*Skill, bool) {
	s, ok := r.current.Load().byName[name]
	return s, ok
}

// List returns every skill in the current index, sorted by name.
//
// The order is part of the contract, not an accident of iteration: the
// list_skills tool paginates with an opaque cursor, and a cursor is only
// meaningful over a stable sequence.
func (r *Registry) List() []*Skill {
	idx := r.current.Load()
	out := make([]*Skill, 0, len(idx.byName))
	for _, s := range idx.byName {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *Skill) int {
		return strings.Compare(a.Metadata.Name, b.Metadata.Name)
	})
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
		md, err := skillparse.Load(ctx, r.backend, r.prefix, name, files)
		if err != nil {
			return 0, fmt.Errorf("registry: loading skill %q: %w", name, err)
		}
		md.Commit = r.commit
		appendOnboardingFooter(md)
		slog.Debug("registry: loaded skill", "name", name, "files", len(files))
		byName[name] = &Skill{Metadata: md}
	}

	slog.Info("registry: index built", "prefix", r.prefix, "skills", len(byName), "commit", r.commit)
	r.current.Store(&index{byName: byName, indexedAt: time.Now()})
	return len(byName), nil
}
