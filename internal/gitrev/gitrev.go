// Package gitrev resolves the commit a working copy is checked out at.
//
// skillsd serves a directory an init container cloned for it, and every
// skill it returns is stamped with that clone's HEAD (SkillMetadata.commit).
// Without it, an outcome report naming a skill can't distinguish content
// that was always wrong from content a recent edit broke - so the whole
// evidence path downstream rests on this one string.
//
// The directory is mounted read-only in the serving container, so this
// package only ever reads refs; it never opens a worktree or takes a lock.
package gitrev

import (
	"fmt"

	"github.com/go-git/go-git/v5"
)

// Head returns the commit SHA that the git working copy at dir is checked
// out at. It reports an error if dir isn't a git repository - which is a
// legitimate way to run skillsd (pointing it at a plain directory of
// skills), so callers are expected to degrade to an empty commit rather
// than fail startup.
func Head(dir string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("gitrev: opening %s: %w", dir, err)
	}

	ref, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("gitrev: resolving HEAD in %s: %w", dir, err)
	}
	return ref.Hash().String(), nil
}
