package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/registry"
	"github.com/araghukas/skillset/internal/storage"
)

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	writeSkill(t, root, "pdf-extractor", "---\nname: pdf-extractor\ndescription: extracts pdfs\nmetadata:\n  category: data\n---\nbody\n")
	writeSkill(t, root, "frontend-design", "---\nname: frontend-design\ndescription: designs frontends\nmetadata:\n  category: design\n---\nbody\n")
	writeSkill(t, root, "uncategorized", "---\nname: uncategorized\ndescription: no category set\n---\nbody\n")

	reg := registry.New(storage.NewFSBackend(root), "")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return New(reg)
}

func TestListSkillsFiltersByCategory(t *testing.T) {
	s := newTestServer(t)

	resp, err := s.ListSkills(context.Background(), &skillsv1.ListSkillsRequest{Category: "data"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 1 || resp.Skills[0].Name != "pdf-extractor" {
		t.Fatalf("expected only pdf-extractor, got %+v", resp.Skills)
	}
}

func TestListSkillsNoCategoryReturnsAll(t *testing.T) {
	s := newTestServer(t)

	resp, err := s.ListSkills(context.Background(), &skillsv1.ListSkillsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 3 {
		t.Fatalf("expected all 3 skills, got %d", len(resp.Skills))
	}
}

func TestListSkillsUnknownCategoryReturnsNone(t *testing.T) {
	s := newTestServer(t)

	resp, err := s.ListSkills(context.Background(), &skillsv1.ListSkillsRequest{Category: "nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 0 {
		t.Fatalf("expected no skills, got %+v", resp.Skills)
	}
}

func TestListSkillsCategoryExcludesUncategorizedSkills(t *testing.T) {
	s := newTestServer(t)

	resp, err := s.ListSkills(context.Background(), &skillsv1.ListSkillsRequest{Category: "design"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sk := range resp.Skills {
		if sk.Name == "uncategorized" {
			t.Fatalf("expected skill with no category set to be excluded from a category filter, got: %+v", resp.Skills)
		}
	}
	if len(resp.Skills) != 1 || resp.Skills[0].Name != "frontend-design" {
		t.Fatalf("expected only frontend-design, got %+v", resp.Skills)
	}
}

func TestListSkillsCategoryIsCaseSensitive(t *testing.T) {
	s := newTestServer(t)

	resp, err := s.ListSkills(context.Background(), &skillsv1.ListSkillsRequest{Category: "Data"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 0 {
		t.Fatalf("expected category matching to be case-sensitive and return nothing for %q, got %+v", "Data", resp.Skills)
	}
}

func TestGetSkillWithBinaryAssetDoesNotFail(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "with-binary-asset", "---\nname: with-binary-asset\ndescription: has an image\n---\nbody\n")
	assetDir := filepath.Join(root, "with-binary-asset", "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "logo.png"), []byte{0x89, 0x50, 0x4E, 0x47, 0xFF, 0xFE}, 0o644); err != nil {
		t.Fatal(err)
	}

	reg := registry.New(storage.NewFSBackend(root), "")
	if _, err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := New(reg)

	resp, err := s.GetSkill(context.Background(), &skillsv1.GetSkillRequest{
		SkillName:           "with-binary-asset",
		IncludeContextFiles: true,
	})
	if err != nil {
		t.Fatalf("expected GetSkill to succeed despite binary asset, got: %v", err)
	}
	if resp.Skill.Name != "with-binary-asset" {
		t.Fatalf("unexpected skill: %+v", resp.Skill)
	}
}

func TestGetClientGuideIsNotListedByListSkills(t *testing.T) {
	s := newTestServer(t)

	guide, err := s.GetClientGuide(context.Background(), &skillsv1.GetClientGuideRequest{})
	if err != nil {
		t.Fatalf("expected GetClientGuide to succeed, got: %v", err)
	}
	if guide.Skill.Name != "skillsd-client" {
		t.Fatalf("unexpected client guide skill: %+v", guide.Skill)
	}
	if len(guide.Skill.ContextFiles) == 0 {
		t.Fatal("expected client guide to include its SKILL.md as a context file")
	}

	list, err := s.ListSkills(context.Background(), &skillsv1.ListSkillsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sk := range list.Skills {
		if sk.Name == "skillsd-client" {
			t.Fatalf("expected the embedded client guide to be excluded from ListSkills, got: %+v", list.Skills)
		}
	}
}

func TestListSkillsCategoryCombinedWithIncludeContextFiles(t *testing.T) {
	s := newTestServer(t)

	resp, err := s.ListSkills(context.Background(), &skillsv1.ListSkillsRequest{
		Category:            "data",
		IncludeContextFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 1 || resp.Skills[0].Name != "pdf-extractor" {
		t.Fatalf("expected only pdf-extractor, got %+v", resp.Skills)
	}
	if len(resp.Skills[0].ContextFiles) == 0 {
		t.Fatal("expected context files to be populated when include_context_files is set")
	}
}
