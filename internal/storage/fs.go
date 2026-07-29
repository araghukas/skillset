package storage

import (
	"context"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
)

// FSBackend is a Backend implementation that reads skill definitions from a
// local directory tree. It is intended to be pointed at a volume populated
// by a git-clone init container: the init container clones the skills repo
// into the volume before the main container starts, and FSBackend then
// reads it read-only for the lifetime of the process.
type FSBackend struct {
	root string
}

// NewFSBackend returns an FSBackend rooted at root.
func NewFSBackend(root string) *FSBackend {
	return &FSBackend{root: filepath.Clean(root)}
}

// List returns the keys (paths relative to root) of all files under prefix.
func (b *FSBackend) List(ctx context.Context, prefix string) ([]string, error) {
	start := filepath.Join(b.root, filepath.FromSlash(prefix))

	var keys []string
	err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(b.root, path)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: listing %s: %w", start, err)
	}

	return keys, nil
}

// Get reads a single file by key (a path relative to root).
func (b *FSBackend) Get(ctx context.Context, key string) (FileObject, error) {
	path := filepath.Join(b.root, filepath.FromSlash(key))

	content, err := os.ReadFile(path)
	if err != nil {
		return FileObject{}, fmt.Errorf("storage: reading %s: %w", path, err)
	}

	return FileObject{
		Key:         key,
		Content:     content,
		ContentType: mime.TypeByExtension(filepath.Ext(path)),
	}, nil
}
