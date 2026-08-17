// Package gitrepo wraps a single git working copy used by skillsd-registry:
// open-or-clone it, keep its base branch fresh, commit agent-suggested file
// changes onto a branch, diff/log a branch against base, and push a branch
// upstream. It knows nothing about suggestions, agents, or skills - that
// naming and orchestration lives in internal/suggestions.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
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

// TokenSource yields the credential used as the HTTPS password for clone,
// fetch, and push. It's satisfied by internal/githubauth's static-token and
// GitHub App sources; declared here so this package stays independent of how
// the credential is obtained.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Repo is a single git working copy, safe for concurrent use: all mutating
// operations (checkout, commit, fetch, push) serialize on an internal mutex.
type Repo struct {
	mu         sync.Mutex
	repo       *git.Repository
	tokens     TokenSource
	baseBranch string
}

// Open opens the git working copy at dir, cloning it from cloneURL first if
// dir doesn't yet contain a repository.
//
// tokens supplies the HTTPS credential; a nil TokenSource means
// unauthenticated access, which only works for a public repository. It is
// consulted per network operation rather than once here: a GitHub App
// installation token expires within the hour, and this process is expected
// to outlive many of them.
func Open(ctx context.Context, dir, cloneURL, baseBranch string, tokens TokenSource) (*Repo, error) {
	r := &Repo{tokens: tokens, baseBranch: baseBranch}

	repo, err := git.PlainOpen(dir)
	switch {
	case errors.Is(err, git.ErrRepositoryNotExists):
		auth, authErr := r.auth(ctx)
		if authErr != nil {
			return nil, authErr
		}
		repo, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
			URL:           cloneURL,
			Auth:          auth,
			ReferenceName: plumbing.NewBranchReferenceName(baseBranch),
			SingleBranch:  true,
		})
		if err != nil {
			slog.Error("cloning repo failed", "url", cloneURL, "dir", dir, "branch", baseBranch, "error", err)
			return nil, fmt.Errorf("gitrepo: cloning %s into %s: %w", cloneURL, dir, err)
		}
		slog.Info("cloned repo", "url", cloneURL, "dir", dir, "branch", baseBranch)
	case err != nil:
		return nil, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}

	r.repo = repo
	return r, nil
}

// auth builds the HTTPS credential for one network operation. It returns a
// nil AuthMethod when no TokenSource is configured, which go-git reads as an
// unauthenticated request.
//
// The "x-access-token" username is what GitHub expects alongside an
// installation token or a PAT; Gitea accepts any non-empty username too.
func (r *Repo) auth(ctx context.Context) (transport.AuthMethod, error) {
	if r.tokens == nil {
		return nil, nil
	}
	token, err := r.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: obtaining token: %w", err)
	}
	if token == "" {
		return nil, nil
	}
	return &githttp.BasicAuth{Username: "x-access-token", Password: token}, nil
}

// BaseBranch returns the name of the branch suggestions and PRs target.
func (r *Repo) BaseBranch() string {
	return r.baseBranch
}

// BaseHead returns the current tip of the local base branch.
func (r *Repo) BaseHead() (plumbing.Hash, error) {
	return r.ResolveRef(r.baseBranch)
}

// RefreshBase fetches the base branch from origin and fast-forwards the
// local base branch ref to match. Called on a background timer and
// opportunistically before forking a new suggestion branch.
func (r *Repo) RefreshBase(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	auth, err := r.auth(ctx)
	if err != nil {
		return err
	}

	refSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", r.baseBranch, r.baseBranch))
	err = r.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		RefSpecs:   []config.RefSpec{refSpec},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("gitrepo: fetching %s: %w", r.baseBranch, err)
	}
	slog.Info("fetched base branch", "branch", r.baseBranch, "up_to_date", errors.Is(err, git.NoErrAlreadyUpToDate))

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
// suggestion branch actually forked from, which may lag behind the base
// branch's current tip if it has advanced since. Callers use this rather
// than BaseHead() when diffing/logging an existing suggestion branch, since
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
	slog.Info("committed to branch", "branch", branch, "commit", hash.String(), "files", len(files))
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
// oldest-parent-first traversal assumed linear - true here since suggestion
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

// Push pushes branch to origin, along with any extra fully-qualified refs
// named in alsoRefs. The extra refs let annotations that live outside
// refs/heads - endorsements, submission markers - reach the remote in the
// same round trip as the branch they describe, so they survive the loss of
// this component's volume.
func (r *Repo) Push(ctx context.Context, branch string, alsoRefs ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	refSpecs := []config.RefSpec{
		config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch)),
	}
	for _, ref := range alsoRefs {
		refSpecs = append(refSpecs, config.RefSpec(fmt.Sprintf("+%s:%s", ref, ref)))
	}

	auth, err := r.auth(ctx)
	if err != nil {
		return err
	}

	err = r.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       auth,
		RefSpecs:   refSpecs,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		slog.Error("pushing branch failed", "branch", branch, "also_refs", alsoRefs, "error", err)
		return fmt.Errorf("gitrepo: pushing %q: %w", branch, err)
	}
	slog.Info("pushed branch", "branch", branch, "also_refs", alsoRefs, "up_to_date", errors.Is(err, git.NoErrAlreadyUpToDate))
	return nil
}

// Annotation is a dated, addressed note attached to a commit and stored as
// a git tag object under an arbitrary ref. Endorsements and submission
// markers are both annotations: they need an author, a timestamp, and a
// target commit, none of which a bare ref can carry, and using a real tag
// object gets all three without inventing a metadata store.
type Annotation struct {
	Ref     string // Fully-qualified ref the annotation is stored at
	Target  string // Commit SHA the annotation points at
	Author  string
	Message string
	At      time.Time
}

// Annotate writes a tag object targeting commit and points ref at it,
// replacing whatever ref held before. ref must be fully qualified (it lives
// outside refs/heads and refs/tags by design, so it is never mistaken for a
// branch or a release tag).
func (r *Repo) Annotate(ref, author, message string, target plumbing.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	tag := &object.Tag{
		Name:       path.Base(ref),
		Tagger:     object.Signature{Name: author, Email: author + "@agents.local", When: time.Now()},
		Message:    message,
		Target:     target,
		TargetType: plumbing.CommitObject,
	}

	enc := r.repo.Storer.NewEncodedObject()
	if err := tag.Encode(enc); err != nil {
		return fmt.Errorf("gitrepo: encoding annotation %q: %w", ref, err)
	}
	hash, err := r.repo.Storer.SetEncodedObject(enc)
	if err != nil {
		return fmt.Errorf("gitrepo: storing annotation %q: %w", ref, err)
	}

	if err := r.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), hash)); err != nil {
		return fmt.Errorf("gitrepo: setting ref %q: %w", ref, err)
	}
	return nil
}

// Annotations returns every annotation stored under the given fully-
// qualified ref prefix, in ref-name order.
func (r *Repo) Annotations(prefix string) ([]Annotation, error) {
	iter, err := r.repo.Storer.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: listing refs: %w", err)
	}
	defer iter.Close()

	var out []Annotation
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		tag, err := r.repo.TagObject(ref.Hash())
		if err != nil {
			// A ref under this prefix that isn't a tag object isn't ours;
			// skip it rather than failing the whole listing.
			return nil
		}
		out = append(out, Annotation{
			Ref:     name,
			Target:  tag.Target.String(),
			Author:  tag.Tagger.Name,
			Message: tag.Message,
			At:      tag.Tagger.When,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: iterating refs: %w", err)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// LineRange is a half-open [Start, End) interval of 1-indexed line numbers
// on the *base* side of a diff. A pure insertion has Start == End: it
// occupies no base lines but still marks a position two suggestions can
// collide at.
type LineRange struct {
	Start int
	End   int
}

// ChangedRanges returns, per file path, the base-side line ranges that
// differ between the from and to commits.
//
// This is what clustering compares. go-git has no merge-tree, so rather
// than attempting a real three-way merge to detect conflicts, overlap of
// these ranges stands in for it: two suggestions rewriting the same lines are
// answering the same question, whether or not git would call it a textual
// conflict. It is the cheaper signal and arguably the more meaningful one,
// since edits that merge cleanly can still be competing answers.
func (r *Repo) ChangedRanges(from, to plumbing.Hash) (map[string][]LineRange, error) {
	fromCommit, err := r.repo.CommitObject(from)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: resolving commit %s: %w", from, err)
	}
	toCommit, err := r.repo.CommitObject(to)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: resolving commit %s: %w", to, err)
	}
	patch, err := fromCommit.Patch(toCommit)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: diffing %s..%s: %w", from, to, err)
	}

	out := make(map[string][]LineRange)
	for _, fp := range patch.FilePatches() {
		fromFile, toFile := fp.Files()

		// A file added by this diff has no base side to measure, and one
		// deleted has no surviving side; either way the whole path is
		// contested, which an empty range at line 0 represents.
		key := ""
		switch {
		case fromFile != nil:
			key = fromFile.Path()
		case toFile != nil:
			key = toFile.Path()
		default:
			continue
		}
		if fromFile == nil || toFile == nil {
			out[key] = append(out[key], LineRange{Start: 0, End: 0})
			continue
		}

		line := 1
		for _, chunk := range fp.Chunks() {
			n := countLines(chunk.Content())
			switch chunk.Type() {
			case diff.Equal:
				line += n
			case diff.Delete:
				out[key] = append(out[key], LineRange{Start: line, End: line + n})
				line += n
			case diff.Add:
				// Insertions consume no base lines, so the position is
				// recorded as a zero-width range and `line` doesn't move.
				out[key] = append(out[key], LineRange{Start: line, End: line})
			}
		}
	}
	return out, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
