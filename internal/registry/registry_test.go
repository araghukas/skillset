package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/araghukas/skillset/internal/skillparse"
	"github.com/araghukas/skillset/internal/storage"
)

// writeSkill creates a skill directory containing a SKILL.md with the given
// content, under root/name.
func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skillparse.SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSkillFile writes an additional file (e.g. a supporting asset) at
// root/name/relPath, creating any intermediate directories as needed.
func writeSkillFile(t *testing.T, root, name, relPath string, content []byte) {
	t.Helper()
	full := filepath.Join(root, name, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadWithPrefix is a regression test for a bug where skill keys were
// mismatched against the backend's raw keys whenever the registry was rooted
// at a non-empty prefix (e.g. SKILLS_SUBPATH=skills. Every skill silently
// failed to load with "missing SKILL.md" because the file's full key still
// carried the prefix while the expected key did not.
func TestLoadWithPrefix(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	writeSkill(t, skillsDir, "frontend-design", "---\nname: frontend-design\ndescription: test desc\n---\nbody\n")

	reg := New(storage.NewFSBackend(root), "skills", "")
	n, err := reg.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 skill, got %d", n)
	}

	sk, ok := reg.Get("frontend-design")
	if !ok {
		t.Fatal("expected to find frontend-design skill")
	}
	if sk.Metadata.Description != "test desc" {
		t.Fatalf("unexpected description: %q", sk.Metadata.Description)
	}
	if len(sk.Metadata.ContextFiles) != 1 || sk.Metadata.ContextFiles[0].FilePath != skillparse.SkillFileName {
		t.Fatalf("unexpected context files: %+v", sk.Metadata.ContextFiles)
	}
}

// TestCatalog covers the listing appended to skillsd's connect-time
// instructions: every loaded skill's name and description should appear,
// sorted by name, and an empty index should produce an empty string rather
// than a heading with nothing under it.
func TestCatalog(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "zeta-skill", "---\nname: zeta-skill\ndescription: does zeta things\n---\nbody\n")
	writeSkill(t, root, "alpha-skill", "---\nname: alpha-skill\ndescription: does alpha things\n---\nbody\n")

	reg := New(storage.NewFSBackend(root), "", "")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := reg.Catalog()
	wantAlpha := strings.Index(got, "alpha-skill")
	wantZeta := strings.Index(got, "zeta-skill")
	if wantAlpha == -1 || wantZeta == -1 {
		t.Fatalf("catalog missing an expected skill name: %q", got)
	}
	if wantAlpha > wantZeta {
		t.Errorf("catalog not sorted by name: %q", got)
	}
	if !strings.Contains(got, "does alpha things") || !strings.Contains(got, "does zeta things") {
		t.Errorf("catalog missing a description: %q", got)
	}

	if empty := New(storage.NewFSBackend(t.TempDir()), "", "").Catalog(); empty != "" {
		t.Errorf("expected empty catalog for an empty index, got %q", empty)
	}
}

// TestLoadWithoutPrefix covers the default, unprefixed configuration (empty
// SKILLS_SUBPATH), where the registry root directly contains skill
// directories.
func TestLoadWithoutPrefix(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "frontend-design", "---\nname: frontend-design\ndescription: test desc\n---\nbody\n")

	reg := New(storage.NewFSBackend(root), "", "")
	n, err := reg.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 skill, got %d", n)
	}
	if _, ok := reg.Get("frontend-design"); !ok {
		t.Fatal("expected to find frontend-design skill")
	}
}

// TestLoadSkipsNonUTF8ContextFiles is a regression test for a crash where a
// binary asset (e.g. an image) alongside SKILL.md broke serialization, since
// ContextFile.Content is carried as a JSON string and must be valid UTF-8.
// Non-UTF-8 files should be skipped, not fail the whole skill.
func TestLoadSkipsNonUTF8ContextFiles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "with-binary-asset", "---\nname: with-binary-asset\ndescription: has an image\n---\nbody\n")
	writeSkillFile(t, root, "with-binary-asset", "references/notes.txt", []byte("plain text notes"))
	// 0xFF 0xFE is not valid UTF-8 in any position.
	writeSkillFile(t, root, "with-binary-asset", "assets/logo.png", []byte{0x89, 0x50, 0x4E, 0x47, 0xFF, 0xFE, 0x00, 0x01})

	reg := New(storage.NewFSBackend(root), "", "")
	n, err := reg.Load(context.Background())
	if err != nil {
		t.Fatalf("expected load to succeed despite binary asset, got: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 skill, got %d", n)
	}

	sk, ok := reg.Get("with-binary-asset")
	if !ok {
		t.Fatal("expected to find with-binary-asset skill")
	}

	var paths []string
	for _, cf := range sk.Metadata.ContextFiles {
		paths = append(paths, cf.FilePath)
	}
	for _, want := range []string{skillparse.SkillFileName, "references/notes.txt"} {
		found := false
		for _, p := range paths {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected context file %q to be present, got: %v", want, paths)
		}
	}
	for _, p := range paths {
		if p == "assets/logo.png" {
			t.Fatalf("expected binary asset to be skipped, but found it in context files: %v", paths)
		}
	}
}

// TestLoadAppendsOnboardingFooterToSkillMd is a regression test for the
// self-onboarding footer: every served skill's SKILL.md content should
// carry a short note pointing agents at the proposal workflow, even when
// the installer's own system prompt says nothing about skillsd.
func TestLoadAppendsOnboardingFooterToSkillMd(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "frontend-design", "---\nname: frontend-design\ndescription: test desc\n---\nbody\n")
	writeSkillFile(t, root, "frontend-design", "references/notes.txt", []byte("supporting notes"))

	reg := New(storage.NewFSBackend(root), "", "")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	sk, ok := reg.Get("frontend-design")
	if !ok {
		t.Fatal("expected to find frontend-design skill")
	}

	skillMD, ok := sk.Metadata.ContextFile(skillparse.SkillFileName)
	if !ok {
		t.Fatal("expected a SKILL.md context file")
	}
	if !strings.Contains(skillMD.Content, "propose_change") {
		t.Fatalf("expected SKILL.md content to carry the onboarding footer, got: %q", skillMD.Content)
	}
	if !strings.HasPrefix(skillMD.Content, "---\nname: frontend-design") {
		t.Fatalf("expected original SKILL.md content to be preserved before the footer, got: %q", skillMD.Content)
	}

	notes, ok := sk.Metadata.ContextFile("references/notes.txt")
	if !ok {
		t.Fatal("expected a references/notes.txt context file")
	}
	if strings.Contains(notes.Content, "propose_change") {
		t.Fatalf("expected the onboarding footer to be appended only to SKILL.md, not to supporting files, got: %q", notes.Content)
	}
}

// TestLoadFailsWhenSkillMdItselfIsNotUTF8 documents current behavior: if
// SKILL.md's frontmatter parses (frontmatter parsing works on raw bytes), but
// the file overall contains invalid UTF-8, the file is skipped as a context
// file like any other, while the skill itself still loads.
func TestLoadFailsWhenSkillMdItselfIsNotUTF8(t *testing.T) {
	root := t.TempDir()
	// Valid frontmatter followed by an invalid UTF-8 byte sequence in the body.
	content := append([]byte("---\nname: odd-body\ndescription: has invalid utf8 in body\n---\nbody "), 0xFF, 0xFE)
	writeSkillFile(t, root, "odd-body", skillparse.SkillFileName, content)

	reg := New(storage.NewFSBackend(root), "", "")
	n, err := reg.Load(context.Background())
	if err != nil {
		t.Fatalf("expected load to succeed, got: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 skill, got %d", n)
	}

	sk, ok := reg.Get("odd-body")
	if !ok {
		t.Fatal("expected to find odd-body skill")
	}
	if sk.Metadata.Description != "has invalid utf8 in body" {
		t.Fatalf("unexpected description: %q", sk.Metadata.Description)
	}
	if len(sk.Metadata.ContextFiles) != 0 {
		t.Fatalf("expected SKILL.md to be skipped as a context file due to invalid UTF-8, got: %+v", sk.Metadata.ContextFiles)
	}
}
