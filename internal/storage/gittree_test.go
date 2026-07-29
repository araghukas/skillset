package storage

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// newTestTree builds an in-memory git repo with the given files committed at
// its root, and returns the resulting commit's tree.
func newTestTree(t *testing.T, files map[string]string) *object.Tree {
	t.Helper()

	fs := memfs.New()
	repo, err := git.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	for path, content := range files {
		f, err := fs.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(path); err != nil {
			t.Fatal(err)
		}
	}

	hash, err := wt.Commit("test commit", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestGitTreeBackendListWithoutPrefix(t *testing.T) {
	tree := newTestTree(t, map[string]string{
		"frontend-design/SKILL.md":         "---\nname: frontend-design\n---\n",
		"frontend-design/references/x.txt": "notes",
		"other-skill/SKILL.md":             "---\nname: other-skill\n---\n",
	})

	backend := NewGitTreeBackend(tree)
	keys, err := backend.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d: %v", len(keys), keys)
	}
}

func TestGitTreeBackendListWithPrefix(t *testing.T) {
	tree := newTestTree(t, map[string]string{
		"skills/frontend-design/SKILL.md": "---\nname: frontend-design\n---\n",
		"skills/other-skill/SKILL.md":     "---\nname: other-skill\n---\n",
		"README.md":                       "not a skill",
	})

	backend := NewGitTreeBackend(tree)
	keys, err := backend.List(context.Background(), "skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys under prefix, got %d: %v", len(keys), keys)
	}
	for _, k := range keys {
		if k == "README.md" {
			t.Fatalf("expected README.md to be excluded by prefix filter, got: %v", keys)
		}
	}
}

func TestGitTreeBackendGet(t *testing.T) {
	tree := newTestTree(t, map[string]string{
		"frontend-design/SKILL.md": "---\nname: frontend-design\ndescription: test\n---\nbody\n",
	})

	backend := NewGitTreeBackend(tree)
	obj, err := backend.Get(context.Background(), "frontend-design/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(obj.Content) != "---\nname: frontend-design\ndescription: test\n---\nbody\n" {
		t.Fatalf("unexpected content: %q", obj.Content)
	}
}

func TestGitTreeBackendGetMissingKey(t *testing.T) {
	tree := newTestTree(t, map[string]string{
		"frontend-design/SKILL.md": "---\nname: frontend-design\n---\n",
	})

	backend := NewGitTreeBackend(tree)
	if _, err := backend.Get(context.Background(), "does-not-exist/SKILL.md"); err == nil {
		t.Fatal("expected error for missing key")
	}
}
