package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func testSignature() object.Signature {
	return object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}
}

// newOriginRepo creates a bare repo at a fresh temp dir, seeded with an
// initial commit containing files, and returns its path and default branch
// name (whatever go-git's PlainInit names it - not assumed to be "main").
func newOriginRepo(t *testing.T, files map[string]string) (dir, branch string) {
	t.Helper()

	seedDir := t.TempDir()
	seed, err := git.PlainInit(seedDir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		full := filepath.Join(seedDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(path); err != nil {
			t.Fatal(err)
		}
	}
	sig := testSignature()
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: &sig}); err != nil {
		t.Fatal(err)
	}

	head, err := seed.Head()
	if err != nil {
		t.Fatal(err)
	}
	branch = head.Name().Short()

	originDir := t.TempDir()
	if _, err := git.PlainClone(originDir, true, &git.CloneOptions{URL: seedDir}); err != nil {
		t.Fatal(err)
	}
	return originDir, branch
}

func TestOpenClonesIfMissing(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})

	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BaseHead(); err != nil {
		t.Fatalf("expected BaseHead to resolve after clone: %v", err)
	}
}

func TestOpenReopensExistingDir(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	dir := t.TempDir()

	if _, err := Open(context.Background(), dir, originDir, branch, ""); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(context.Background(), dir, originDir, branch, "")
	if err != nil {
		t.Fatalf("expected re-opening an existing clone to succeed, got: %v", err)
	}
	if _, err := repo.BaseHead(); err != nil {
		t.Fatal(err)
	}
}

func TestCommitOnBranchCreatesNewBranch(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}

	base, err := repo.BaseHead()
	if err != nil {
		t.Fatal(err)
	}

	hash, err := repo.CommitOnBranch("proposals/agent-1/foo/fix-typo", base,
		[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte("hello, fixed")}},
		"fix typo", testSignature())
	if err != nil {
		t.Fatal(err)
	}

	tree, err := repo.Tree(hash)
	if err != nil {
		t.Fatal(err)
	}
	f, err := tree.File("skills/foo/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	content, err := f.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello, fixed" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestCommitOnBranchAppendsToExistingBranch(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.BaseHead()
	if err != nil {
		t.Fatal(err)
	}

	branchName := "proposals/agent-1/foo/iterate"
	first, err := repo.CommitOnBranch(branchName, base,
		[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte("v1")}}, "first", testSignature())
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CommitOnBranch(branchName, base,
		[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte("v2")}}, "second", testSignature())
	if err != nil {
		t.Fatal(err)
	}

	commits, err := repo.Log(base, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits on branch, got %d: %+v", len(commits), commits)
	}
	if commits[0].SHA != second.String() || commits[1].SHA != first.String() {
		t.Fatalf("expected commits newest-first, got %+v", commits)
	}
}

func TestMergeBaseFindsOriginalForkPoint(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.BaseHead()
	if err != nil {
		t.Fatal(err)
	}

	proposalHead, err := repo.CommitOnBranch("proposals/agent-1/foo/fix", base,
		[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte("v1")}}, "propose", testSignature())
	if err != nil {
		t.Fatal(err)
	}

	// Advance the base branch further, simulating upstream progress after
	// the proposal already forked from it.
	newBaseHead, err := repo.CommitOnBranch(branch, base,
		[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte("unrelated update")}}, "advance base", testSignature())
	if err != nil {
		t.Fatal(err)
	}

	mb, err := repo.MergeBase(newBaseHead, proposalHead)
	if err != nil {
		t.Fatal(err)
	}
	if mb != base {
		t.Fatalf("expected merge base to be the original fork point %s, got %s", base, mb)
	}
}

func TestDiff(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.BaseHead()
	if err != nil {
		t.Fatal(err)
	}

	hash, err := repo.CommitOnBranch("proposals/agent-1/foo/fix-typo", base,
		[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte("hello, fixed")}}, "fix typo", testSignature())
	if err != nil {
		t.Fatal(err)
	}

	diff, err := repo.Diff(base, hash)
	if err != nil {
		t.Fatal(err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}
}

func TestPush(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.BaseHead()
	if err != nil {
		t.Fatal(err)
	}

	branchName := "proposals/agent-1/foo/fix-typo"
	if _, err := repo.CommitOnBranch(branchName, base,
		[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte("hello, fixed")}}, "fix typo", testSignature()); err != nil {
		t.Fatal(err)
	}

	if err := repo.Push(context.Background(), branchName); err != nil {
		t.Fatal(err)
	}

	origin, err := git.PlainOpen(originDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := origin.Reference(plumbing.NewBranchReferenceName(branchName), true); err != nil {
		t.Fatalf("expected pushed branch to exist on origin: %v", err)
	}
}

func TestBranchesWithPrefix(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.BaseHead()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"proposals/agent-1/foo/a", "proposals/agent-2/foo/b"} {
		if _, err := repo.CommitOnBranch(name, base,
			[]FileChange{{Path: "skills/foo/SKILL.md", Content: []byte(name)}}, "msg", testSignature()); err != nil {
			t.Fatal(err)
		}
	}

	names, err := repo.BranchesWithPrefix("proposals/")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 proposal branches, got %d: %v", len(names), names)
	}
}

func TestResolveRefBySHAAndByBranch(t *testing.T) {
	originDir, branch := newOriginRepo(t, map[string]string{"skills/foo/SKILL.md": "hello"})
	repo, err := Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := repo.BaseHead()
	if err != nil {
		t.Fatal(err)
	}

	byBranch, err := repo.ResolveRef(branch)
	if err != nil {
		t.Fatal(err)
	}
	if byBranch != base {
		t.Fatalf("expected resolving base branch name to match BaseHead, got %s want %s", byBranch, base)
	}

	bySHA, err := repo.ResolveRef(base.String())
	if err != nil {
		t.Fatal(err)
	}
	if bySHA != base {
		t.Fatalf("expected resolving SHA to round-trip, got %s want %s", bySHA, base)
	}

	byEmpty, err := repo.ResolveRef("")
	if err != nil {
		t.Fatal(err)
	}
	if byEmpty != base {
		t.Fatalf("expected empty ref to resolve to base HEAD, got %s want %s", byEmpty, base)
	}
}
