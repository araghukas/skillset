// Package clientguide embeds skillsd's own client-facing usage guide: a
// SKILL.md, shaped and parsed exactly like any other skill, that explains
// SkillService/ProposalService to a calling agent. It's embedded in the
// server binary rather than read from the skills repo the registry indexes,
// so it can't drift from the proto it documents and is always available
// regardless of how (or whether) the skills repo is configured.
package clientguide

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"mime"
	"path/filepath"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/skillparse"
	"github.com/araghukas/skillset/internal/storage"
)

//go:embed skillsd-client
var embedded embed.FS

// Guide is the parsed metadata and content of the embedded client guide.
var Guide = mustLoad()

func mustLoad() *skillsv1.SkillMetadata {
	ctx := context.Background()
	backend := &embedBackend{fsys: embedded}

	files, err := backend.List(ctx, "")
	if err != nil {
		panic(fmt.Sprintf("clientguide: listing embedded files: %v", err))
	}

	md, err := skillparse.Load(ctx, backend, "", "skillsd-client", files)
	if err != nil {
		panic(fmt.Sprintf("clientguide: loading embedded skill: %v", err))
	}
	return md
}

// embedBackend is a minimal storage.Backend over an embed.FS, just enough
// to feed this package's single embedded skill through skillparse.Load.
type embedBackend struct {
	fsys embed.FS
}

func (b *embedBackend) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := fs.WalkDir(b.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		keys = append(keys, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func (b *embedBackend) Get(ctx context.Context, key string) (storage.FileObject, error) {
	content, err := b.fsys.ReadFile(key)
	if err != nil {
		return storage.FileObject{}, err
	}
	return storage.FileObject{
		Key:         key,
		Content:     content,
		ContentType: mime.TypeByExtension(filepath.Ext(key)),
	}, nil
}
