// Package gitrepo wraps a single git working copy used by skillsd-registry:
// open-or-clone it, keep its base branch fresh, commit agent-proposed file
// changes onto a branch, diff/log a branch against base, and push a branch
// upstream. It knows nothing about proposals, agents, or skills - that
// naming and orchestration lives in internal/proposals.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// FileChange is one file's full new content (or a deletion) to apply in a
// single commit.
type FileChange struct {
	Path    string
	Deleted bool
	Content []byte
}

// CommitInfo is a single commit's metadata, independent of any proto shape.
type CommitInfo struct {
	SHA        string
	Message    string
	Author     string
	AuthoredAt string
}

// Repo is a single git working copy, safe for concurrent use: all mutating
// operations (checkout, commit, fetch, push) serialize on an internal mutex.
type Repo struct {
	mu         sync.Mutex
	repo       *git.Repository
	auth       *githttp.BasicAuth
	baseBranch string
}

// Open opens the git working copy at dir, cloning it from cloneURL first if
// dir doesn't yet contain a repository. Authentication currently uses token
// over HTTPS.
func Open(ctx context.Context, dir, cloneURL, baseBranch, token string) (*Repo, error) {

	// TODO: explore alternate and/or more secure options for Auth
	auth := &githttp.BasicAuth{Username: "x-access-token", Password: token}

	repo, err := git.PlainOpen(dir)
	switch {
	case errors.Is(err, git.ErrRepositoryNotExists):
		repo, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
			URL:           cloneURL,
			Auth:          auth,
			ReferenceName: plumbing.NewBranchReferenceName(baseBranch),
			SingleBranch:  true,
		})
		if err != nil {
			return nil, fmt.Errorf("gitrepo: cloning %s into %s: %w", cloneURL, dir, err)
		}
	case err != nil:
		return nil, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}

	return &Repo{repo: repo, auth: auth, baseBranch: baseBranch}, nil
}

// BaseBranch returns the name of the branch proposals and PRs target.
func (r *Repo) BaseBranch() string {
	return r.baseBranch
}

// BaseHead returns the current tip of the local base branch.
func (r *Repo) BaseHead() (plumbing.Hash, error) {
	return r.ResolveRef(r.baseBranch)
}

// RefreshBase fetches the base branch from origin and fast-forwards the
// local base branch ref to match. Called on a background timer and
// opportunistically before forking a new proposal branch.
func (r *Repo) RefreshBase(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	refSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", r.baseBranch, r.baseBranch))
	err := r.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       r.auth,
		RefSpecs:   []config.RefSpec{refSpec},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("gitrepo: fetching %s: %w", r.baseBranch, err)
	}

	remoteRef, err := r.repo.Reference(plumbing.NewRemoteReferenceName("origin", r.baseBranch), true)
	if err != nil {
		return fmt.Errorf("gitrepo: resolving fetched %s: %w", r.baseBranch, err)
	}

	localRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(r.baseBranch), remoteRef.Hash())
	if err := r.repo.Storer.SetReference(localRef); err != nil {
		return fmt.Errorf("gitrepo: updating local %s: %w", r.baseBranch, err)
	}
	return nil
}

// ResolveRef resolves a branch name, tag, or commit SHA to a commit hash. An
// empty ref resolves to the base branch's current tip.
func (r *Repo) ResolveRef(ref string) (plumbing.Hash, error) {
	if ref == "" {
		ref = r.baseBranch
	}
	h, err := r.repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: resolving ref %q: %w", ref, err)
	}
	return *h, nil
}

// MergeBase returns the best common ancestor of a and b - the commit a
// proposal branch actually forked from, which may lag behind the base
// branch's current tip if it has advanced since. Callers use this rather
// than BaseHead() when diffing/logging an existing proposal branch, since
// using the current tip there would walk clean past the fork point.
func (r *Repo) MergeBase(a, b plumbing.Hash) (plumbing.Hash, error) {
	commitA, err := r.repo.CommitObject(a)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: resolving commit %s: %w", a, err)
	}
	commitB, err := r.repo.CommitObject(b)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: resolving commit %s: %w", b, err)
	}
	bases, err := commitA.MergeBase(commitB)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: computing merge base of %s and %s: %w", a, b, err)
	}
	if len(bases) == 0 {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: no common ancestor between %s and %s", a, b)
	}
	return bases[0].Hash, nil
}

// Tree returns the file tree at hash.
func (r *Repo) Tree(hash plumbing.Hash) (*object.Tree, error) {
	commit, err := r.repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: resolving commit %s: %w", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: resolving tree for commit %s: %w", hash, err)
	}
	return tree, nil
}

// CommitOnBranch applies files as a single commit on branch, creating the
// branch from fromBase first if it doesn't already exist locally, or
// appending to its current tip otherwise. It returns the new commit's hash.
func (r *Repo) CommitOnBranch(branch string, fromBase plumbing.Hash, files []FileChange, message string, author object.Signature) (plumbing.Hash, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	branchRef := plumbing.NewBranchReferenceName(branch)

	wt, err := r.repo.Worktree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: getting worktree: %w", err)
	}

	if _, err := r.repo.Reference(branchRef, true); err != nil {
		if err := wt.Checkout(&git.CheckoutOptions{Hash: fromBase, Branch: branchRef, Create: true}); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("gitrepo: creating branch %q: %w", branch, err)
		}
	} else if err := wt.Checkout(&git.CheckoutOptions{Branch: branchRef}); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: checking out branch %q: %w", branch, err)
	}

	for _, fc := range files {
		if fc.Deleted {
			if _, err := wt.Remove(fc.Path); err != nil {
				return plumbing.ZeroHash, fmt.Errorf("gitrepo: removing %s: %w", fc.Path, err)
			}
			continue
		}

		if dir := path.Dir(fc.Path); dir != "." {
			if err := wt.Filesystem.MkdirAll(dir, 0o755); err != nil {
				return plumbing.ZeroHash, fmt.Errorf("gitrepo: creating parent dirs for %s: %w", fc.Path, err)
			}
		}
		f, err := wt.Filesystem.Create(fc.Path)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("gitrepo: creating %s: %w", fc.Path, err)
		}
		_, writeErr := f.Write(fc.Content)
		closeErr := f.Close()
		if writeErr != nil {
			return plumbing.ZeroHash, fmt.Errorf("gitrepo: writing %s: %w", fc.Path, writeErr)
		}
		if closeErr != nil {
			return plumbing.ZeroHash, fmt.Errorf("gitrepo: closing %s: %w", fc.Path, closeErr)
		}
		if _, err := wt.Add(fc.Path); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("gitrepo: staging %s: %w", fc.Path, err)
		}
	}

	hash, err := wt.Commit(message, &git.CommitOptions{Author: &author})
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitrepo: committing to %q: %w", branch, err)
	}
	return hash, nil
}

// BranchesWithPrefix returns the short names of all local branches whose
// name starts with prefix.
func (r *Repo) BranchesWithPrefix(prefix string) ([]string, error) {
	refs, err := r.repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: listing branches: %w", err)
	}
	defer refs.Close()

	var names []string
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: iterating branches: %w", err)
	}
	return names, nil
}

// Diff returns the unified diff between the from and to commits.
func (r *Repo) Diff(from, to plumbing.Hash) (string, error) {
	fromCommit, err := r.repo.CommitObject(from)
	if err != nil {
		return "", fmt.Errorf("gitrepo: resolving commit %s: %w", from, err)
	}
	toCommit, err := r.repo.CommitObject(to)
	if err != nil {
		return "", fmt.Errorf("gitrepo: resolving commit %s: %w", to, err)
	}
	patch, err := fromCommit.Patch(toCommit)
	if err != nil {
		return "", fmt.Errorf("gitrepo: diffing %s..%s: %w", from, to, err)
	}
	return patch.String(), nil
}

// Log returns commits reachable from to, back to (but excluding) from,
// oldest-parent-first traversal assumed linear - true here since proposal
// branches are only ever appended to, never merged into.
func (r *Repo) Log(from, to plumbing.Hash) ([]CommitInfo, error) {
	cur, err := r.repo.CommitObject(to)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: resolving commit %s: %w", to, err)
	}

	var commits []CommitInfo
	for cur.Hash != from {
		commits = append(commits, CommitInfo{
			SHA:        cur.Hash.String(),
			Message:    cur.Message,
			Author:     cur.Author.Name,
			AuthoredAt: cur.Author.When.Format("2006-01-02T15:04:05Z07:00"),
		})
		if cur.NumParents() == 0 {
			break
		}
		cur, err = cur.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("gitrepo: walking history from %s: %w", to, err)
		}
	}
	return commits, nil
}

// Push pushes branch to origin.
func (r *Repo) Push(ctx context.Context, branch string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch))
	err := r.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       r.auth,
		RefSpecs:   []config.RefSpec{refSpec},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("gitrepo: pushing %q: %w", branch, err)
	}
	return nil
}
