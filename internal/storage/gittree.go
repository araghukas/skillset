package storage

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// GitTreeBackend is a Backend implementation that reads files out of a
// single git commit's tree rather than a live filesystem. It lets
// skillparse.Load resolve a skill "as of" an arbitrary ref (a branch, a
// suggestion, a raw commit) through the exact same parsing/validation path
// FSBackend feeds the static registry with.
type GitTreeBackend struct {
	tree *object.Tree
}

// NewGitTreeBackend returns a GitTreeBackend reading from tree.
func NewGitTreeBackend(tree *object.Tree) *GitTreeBackend {
	return &GitTreeBackend{tree: tree}
}

// List returns the keys (paths relative to the tree root) of all files
// under prefix.
func (b *GitTreeBackend) List(ctx context.Context, prefix string) ([]string, error) {
	iter := b.tree.Files()
	defer iter.Close()

	var keys []string
	err := iter.ForEach(func(f *object.File) error {
		if withinPrefix(f.Name, prefix) {
			keys = append(keys, f.Name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: listing tree: %w", err)
	}

	return keys, nil
}

// Get reads a single file by key (a path relative to the tree root).
func (b *GitTreeBackend) Get(ctx context.Context, key string) (FileObject, error) {
	file, err := b.tree.File(key)
	if err != nil {
		return FileObject{}, fmt.Errorf("storage: reading %s: %w", key, err)
	}

	content, err := file.Contents()
	if err != nil {
		return FileObject{}, fmt.Errorf("storage: reading contents of %s: %w", key, err)
	}

	return FileObject{
		Key:         key,
		Content:     []byte(content),
		ContentType: mime.TypeByExtension(filepath.Ext(key)),
	}, nil
}

func withinPrefix(name, prefix string) bool {
	if prefix == "" {
		return true
	}
	return name == prefix || strings.HasPrefix(name, prefix+"/")
}
