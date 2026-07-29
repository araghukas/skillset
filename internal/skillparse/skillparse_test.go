package skillparse

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/araghukas/skillset/internal/storage"
)

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadParsesFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "frontend-design", "---\nname: frontend-design\ndescription: test desc\n---\nbody\n")

	backend := storage.NewFSBackend(root)
	md, err := Load(context.Background(), backend, "", "frontend-design", []string{"frontend-design/" + SkillFileName})
	if err != nil {
		t.Fatal(err)
	}
	if md.Description != "test desc" {
		t.Fatalf("unexpected description: %q", md.Description)
	}
}

func TestLoadRejectsNameMismatch(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "frontend-design", "---\nname: other-name\ndescription: test desc\n---\nbody\n")

	backend := storage.NewFSBackend(root)
	_, err := Load(context.Background(), backend, "", "frontend-design", []string{"frontend-design/" + SkillFileName})
	if err == nil {
		t.Fatal("expected error for frontmatter name mismatch")
	}
}

func TestLoadRejectsMissingSkillFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty-skill"), 0o755); err != nil {
		t.Fatal(err)
	}

	backend := storage.NewFSBackend(root)
	_, err := Load(context.Background(), backend, "", "empty-skill", nil)
	if err == nil {
		t.Fatal("expected error for missing SKILL.md")
	}
}
