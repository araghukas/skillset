package proposals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newTestService seeds an origin repo containing one skill under
// skills/<name>, clones it into a fresh gitrepo.Repo, and returns a
// Service backed by it, along with the base branch's name.
func newTestService(t *testing.T, skillName, skillMD string) (*Service, string) {
	t.Helper()

	seedDir := t.TempDir()
	seed, err := git.PlainInit(seedDir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join("skills", skillName, "SKILL.md")
	full := filepath.Join(seedDir, skillPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(skillPath); err != nil {
		t.Fatal(err)
	}

	// Also seed a supporting context file, so tests can exercise deleting
	// an existing file (rather than SKILL.md itself).
	refPath := filepath.Join("skills", skillName, "references", "old.txt")
	refFull := filepath.Join(seedDir, refPath)
	if err := os.MkdirAll(filepath.Dir(refFull), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refFull, []byte("old reference content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(refPath); err != nil {
		t.Fatal(err)
	}

	sig := &object.Signature{Name: "seed", Email: "seed@example.com"}
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}
	head, err := seed.Head()
	if err != nil {
		t.Fatal(err)
	}
	branch := head.Name().Short()

	originDir := t.TempDir()
	if _, err := git.PlainClone(originDir, true, &git.CloneOptions{URL: seedDir}); err != nil {
		t.Fatal(err)
	}

	repo, err := gitrepo.Open(context.Background(), t.TempDir(), originDir, branch, nil)
	if err != nil {
		t.Fatal(err)
	}

	return New(repo, "skills", DefaultMaxFileContentBytes), branch
}

func validSkillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\nbody\n"
}

func TestProposeChangeCreatesProposal(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))

	res, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "fix-typo",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "designs frontends, fixed")},
		},
		CommitMessage:   "fix typo",
		SourceThreadUri: "s3://threads/abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Proposal
	if p.Branch != "proposals/agent-1/frontend-design/fix-typo" {
		t.Fatalf("unexpected branch: %q", p.Branch)
	}
	if len(p.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(p.Commits))
	}
	if p.Diff == "" {
		t.Fatal("expected non-empty diff")
	}
	if p.SourceThreadUri != "s3://threads/abc" {
		t.Fatalf("expected source_thread_uri to round-trip, got %q", p.SourceThreadUri)
	}
}

func TestProposeChangeRejectsInvalidFrontmatter(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))

	_, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "break-it",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: "not valid frontmatter"},
		},
		CommitMessage: "break it",
	})
	if err == nil {
		t.Fatal("expected error for invalid frontmatter")
	}
}

func TestProposeChangeRejectsNonUTF8Content(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))

	_, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "bad-encoding",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: string([]byte{0x89, 0x50, 0x4E, 0x47, 0xFF, 0xFE})},
		},
		CommitMessage: "bad encoding",
	})
	if err == nil {
		t.Fatal("expected error for non-UTF-8 content")
	}
}

func TestProposeChangeRejectsOversizedContent(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))

	_, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "too-big",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: strings.Repeat("a", DefaultMaxFileContentBytes+1)},
		},
		CommitMessage: "too big",
	})
	if err == nil {
		t.Fatal("expected error for oversized content")
	}
}

func TestProposeChangeAllowsOversizedContentWhenDeleted(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))

	// Content is ignored (and thus not validated) when deleted is set.
	_, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "delete-something",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "kept")},
			{FilePath: "references/old.txt", Deleted: true, Content: strings.Repeat("a", DefaultMaxFileContentBytes+1)},
		},
		CommitMessage: "delete something",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProposeChangeAppendsSecondCommitOnRepeatCall(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))
	ctx := context.Background()

	req := func(desc string) *skillsv1.ProposeChangeRequest {
		return &skillsv1.ProposeChangeRequest{
			SkillName:  "frontend-design",
			AgentId:    "agent-1",
			ProposalId: "iterate",
			Files: []*skillsv1.FileChange{
				{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", desc)},
			},
			CommitMessage: desc,
		}
	}

	if _, err := svc.ProposeChange(ctx, req("v1")); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ProposeChange(ctx, req("v2"))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Proposal
	if len(p.Commits) != 2 {
		t.Fatalf("expected 2 commits after two ProposeChange calls on the same proposal, got %d", len(p.Commits))
	}
}

func TestGetProposalRejectsNonProposalBranch(t *testing.T) {
	svc, branch := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))

	if _, err := svc.GetProposal(context.Background(), branch); err == nil {
		t.Fatal("expected error for a branch outside the proposals/ namespace")
	}
}

func TestListProposalsFiltersBySkillAndAgent(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))
	ctx := context.Background()

	mustPropose := func(agentID, proposalID string) {
		t.Helper()
		_, err := svc.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
			SkillName:  "frontend-design",
			AgentId:    agentID,
			ProposalId: proposalID,
			Files: []*skillsv1.FileChange{
				{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", agentID+"-"+proposalID)},
			},
			CommitMessage: "change",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	mustPropose("agent-1", "a")
	mustPropose("agent-2", "b")

	all, err := svc.ListProposals(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 proposals, got %d", len(all))
	}

	byAgent, err := svc.ListProposals(ctx, "", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 || byAgent[0].AgentId != "agent-1" {
		t.Fatalf("expected only agent-1's proposal, got %+v", byAgent)
	}
}

func TestGetSkillAtRefResolvesBaseAndProposalBranch(t *testing.T) {
	svc, branch := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	ctx := context.Background()

	atBase, err := svc.GetSkillAtRef(ctx, "frontend-design", branch, false)
	if err != nil {
		t.Fatal(err)
	}
	if atBase.Description != "original" {
		t.Fatalf("expected base description %q, got %q", "original", atBase.Description)
	}

	res, err := svc.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "update-desc",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "updated")},
		},
		CommitMessage: "update description",
	})
	if err != nil {
		t.Fatal(err)
	}

	atProposal, err := svc.GetSkillAtRef(ctx, "frontend-design", res.Proposal.Branch, false)
	if err != nil {
		t.Fatal(err)
	}
	if atProposal.Description != "updated" {
		t.Fatalf("expected proposal description %q, got %q", "updated", atProposal.Description)
	}

	// The base branch itself must be unaffected by the proposal.
	atBaseAfter, err := svc.GetSkillAtRef(ctx, "frontend-design", branch, false)
	if err != nil {
		t.Fatal(err)
	}
	if atBaseAfter.Description != "original" {
		t.Fatalf("expected base branch to remain %q, got %q", "original", atBaseAfter.Description)
	}
}

// TestGetSkillAtRefNeverCarriesRegistryOnboardingFooter guards the isolation
// the registry's onboarding footer relies on: skillparse.Load is shared
// between internal/registry (which appends the footer before serving) and
// this package (which reads content straight out of git for diffing and
// dedup). If the footer ever leaked in here, every proposal's diff would
// show it as a spurious removal, and dedup hashing would drift from what
// was actually proposed.
func TestGetSkillAtRefNeverCarriesRegistryOnboardingFooter(t *testing.T) {
	svc, branch := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	md, err := svc.GetSkillAtRef(context.Background(), "frontend-design", branch, true)
	if err != nil {
		t.Fatal(err)
	}

	for _, cf := range md.ContextFiles {
		if cf.FilePath != "SKILL.md" {
			continue
		}
		if strings.Contains(cf.Content, "ProposeChange") {
			t.Fatalf("expected git-sourced SKILL.md content to be free of the served onboarding footer, got: %q", cf.Content)
		}
		if cf.Content != validSkillMD("frontend-design", "original") {
			t.Fatalf("expected git-sourced SKILL.md content to match what was committed exactly, got: %q", cf.Content)
		}
	}
}

func TestGetSkillAtRefEmptyRefResolvesToBaseHead(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	md, err := svc.GetSkillAtRef(context.Background(), "frontend-design", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if md.Description != "original" {
		t.Fatalf("unexpected description: %q", md.Description)
	}
}

func TestPushRejectsNonProposalBranch(t *testing.T) {
	svc, branch := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	if err := svc.Push(context.Background(), branch); err == nil {
		t.Fatal("expected error pushing a non-proposal branch")
	}
}
