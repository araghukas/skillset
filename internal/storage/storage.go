package storage

import "context"

// Object is a single file read from the backing store.
type Object struct {
	Key         string
	Content     []byte
	ContentType string
}

// Backend abstracts the read-only source that skill definitions and their
// context files are loaded from. See, for example, FSBackend.
type Backend interface {
	// List returns the keys of all objects under prefix.
	List(ctx context.Context, prefix string) ([]string, error)

	// Get fetches a single object by key.
	Get(ctx context.Context, key string) (Object, error)
}
