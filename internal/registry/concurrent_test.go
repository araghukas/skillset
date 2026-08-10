package registry

import (
	"context"
	"sync"
	"testing"

	"github.com/araghukas/skillset/internal/skillparse"
	"github.com/araghukas/skillset/internal/storage"
)

// TestConcurrentMetadataReadsDoNotCorruptTheIndex covers the invariant that
// makes skill.Metadata.Clone worth having.
//
// One Skill entry is shared by every request served by a replica. Callers
// that want metadata without context files must take a copy first; if the
// copy shares the original's map or its context-file backing array, one
// request trimming its own view silently empties the index for every later
// request on that replica. The failure is intermittent, load-dependent, and
// reads like a caching bug, so it is worth pinning explicitly.
//
// Run under -race to also catch the unsynchronized-write version of the
// same mistake.
func TestConcurrentMetadataReadsDoNotCorruptTheIndex(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "shared", "---\nname: shared\ndescription: read concurrently\nmetadata:\n  category: ops\n---\nbody\n")
	writeSkillFile(t, root, "shared", "references/notes.txt", []byte("supporting notes"))

	reg := New(storage.NewFSBackend(root), "", "abc123")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	sk, ok := reg.Get("shared")
	if !ok {
		t.Fatal("expected to find the shared skill")
	}
	wantFiles := len(sk.Metadata.ContextFiles)
	if wantFiles != 2 {
		t.Fatalf("expected 2 context files in the index, got %d", wantFiles)
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func() {
			defer wg.Done()
			entry, ok := reg.Get("shared")
			if !ok {
				t.Error("skill vanished from the index mid-run")
				return
			}
			// Half the callers take the stripped view, half take the full
			// one, mirroring include_context_files being set per request.
			if i%2 == 0 {
				stripped := entry.Metadata.WithoutContextFiles()
				if len(stripped.ContextFiles) != 0 {
					t.Errorf("WithoutContextFiles returned %d files", len(stripped.ContextFiles))
				}
				// A caller mutating its own copy must not reach the index.
				stripped.Metadata["category"] = "mutated"
				stripped.Commit = "mutated"
			} else {
				full := entry.Metadata.Clone()
				full.ContextFiles = nil
				full.Metadata["category"] = "mutated"
			}
		}()
	}
	wg.Wait()

	// The index must be exactly as it was loaded.
	after, ok := reg.Get("shared")
	if !ok {
		t.Fatal("skill vanished from the index")
	}
	if got := len(after.Metadata.ContextFiles); got != wantFiles {
		t.Errorf("index lost context files under concurrent reads: %d left, want %d", got, wantFiles)
	}
	if got := after.Metadata.Metadata["category"]; got != "ops" {
		t.Errorf("index metadata map was mutated by a caller: category = %q, want %q", got, "ops")
	}
	if got := after.Metadata.Commit; got != "abc123" {
		t.Errorf("index commit was mutated by a caller: %q", got)
	}
	if _, ok := after.Metadata.ContextFile(skillparse.SkillFileName); !ok {
		t.Error("index lost its SKILL.md context file")
	}
}
