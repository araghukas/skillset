package proposalserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func validSkillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\nbody\n"
}

// newTestServer seeds an origin repo containing one skill, wires it up
// through gitrepo -> proposals -> proposalserver, and points the GitHub
// client at a stub server that always succeeds, recording the last request
// it received.
func newTestServer(t *testing.T) (*Server, *httptest.Server, *http.Request, *[]byte) {
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
	skillPath := filepath.Join("skills", "frontend-design", "SKILL.md")
	full := filepath.Join(seedDir, skillPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(validSkillMD("frontend-design", "designs frontends")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(skillPath); err != nil {
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

	repo, err := gitrepo.Open(context.Background(), t.TempDir(), originDir, branch, "")
	if err != nil {
		t.Fatal(err)
	}
	svc := proposals.New(repo, "skills", proposals.DefaultMaxFileContentBytes)

	var lastReq *http.Request
	var lastBody []byte
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastReq = r
		lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url": "https://github.com/acme/skills/pull/7", "number": 7}`))
	}))
	t.Cleanup(gh.Close)

	client := githubpr.New(gh.URL, "acme", "skills", "test-token")
	return New(svc, client, branch), gh, lastReq, &lastBody
}

func TestProposeChangeThenGetProposal(t *testing.T) {
	s, _, _, _ := newTestServer(t)
	ctx := context.Background()

	p, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "fix-typo",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "designs frontends, fixed")},
		},
		CommitMessage: "fix typo",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetProposal(ctx, &skillsv1.GetProposalRequest{Branch: p.Branch})
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != p.Branch {
		t.Fatalf("unexpected proposal: %+v", got)
	}
}

func TestProposeChangeInvalidFrontmatterReturnsInvalidArgument(t *testing.T) {
	s, _, _, _ := newTestServer(t)

	_, err := s.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "break-it",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: "not frontmatter"},
		},
		CommitMessage: "break it",
	})
	if err == nil {
		t.Fatal("expected error for invalid frontmatter")
	}
}

func TestGetProposalNotFound(t *testing.T) {
	s, _, _, _ := newTestServer(t)

	_, err := s.GetProposal(context.Background(), &skillsv1.GetProposalRequest{Branch: "proposals/nobody/nothing/nowhere"})
	if err == nil {
		t.Fatal("expected error for a nonexistent proposal")
	}
}

func TestGetSkillAtRefResolvesBaseBranch(t *testing.T) {
	s, _, _, _ := newTestServer(t)

	resp, err := s.GetSkillAtRef(context.Background(), &skillsv1.GetSkillAtRefRequest{
		SkillName: "frontend-design",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Skill.Description != "designs frontends" {
		t.Fatalf("unexpected description: %q", resp.Skill.Description)
	}
}

func TestSubmitProposalPushesAndOpensPullRequest(t *testing.T) {
	s, _, _, lastBody := newTestServer(t)
	ctx := context.Background()

	p, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
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

	resp, err := s.SubmitProposal(ctx, &skillsv1.SubmitProposalRequest{Branch: p.Branch})
	if err != nil {
		t.Fatal(err)
	}
	if resp.PullRequestUrl != "https://github.com/acme/skills/pull/7" || resp.PullRequestNumber != 7 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	var sent map[string]string
	if err := json.Unmarshal(*lastBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["head"] != p.Branch {
		t.Fatalf("expected PR head to be the proposal branch, got %+v", sent)
	}
}

func TestSubmitProposalUnknownBranchReturnsNotFound(t *testing.T) {
	s, _, _, _ := newTestServer(t)

	_, err := s.SubmitProposal(context.Background(), &skillsv1.SubmitProposalRequest{
		Branch: "proposals/nobody/nothing/nowhere",
	})
	if err == nil {
		t.Fatal("expected error for a nonexistent proposal")
	}
}
