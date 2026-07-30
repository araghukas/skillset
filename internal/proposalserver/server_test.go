package proposalserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	return New(svc, client, branch, true, 0), gh, lastReq, &lastBody
}

func TestProposeChangeThenGetProposal(t *testing.T) {
	s, _, _, _ := newTestServer(t)
	ctx := context.Background()

	res, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
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
	p := res.Proposal

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

	res, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
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

func TestSubmitProposalDisabledReturnsFailedPrecondition(t *testing.T) {
	s, _, _, _ := newTestServer(t)
	s.submitProposalEnabled = false
	ctx := context.Background()

	res, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
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

	_, err = s.SubmitProposal(ctx, &skillsv1.SubmitProposalRequest{Branch: res.Proposal.Branch})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

// countingGitHub returns a stub GitHub server that counts pull request
// creations, so tests can assert a proposal is never submitted twice.
func countingGitHub(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url": "https://github.com/acme/skills/pull/7", "number": 7}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAutoSubmitFiresOnlyOnceTheThresholdIsReached(t *testing.T) {
	s, _, _, _ := newTestServer(t)
	ctx := context.Background()

	var prCalls int
	s.github = githubpr.New(countingGitHub(t, &prCalls).URL, "acme", "skills", "test-token")
	s.autoSubmitThreshold = 2

	fixed := validSkillMD("frontend-design", "the corrected description")
	mk := func(agent, id string) *skillsv1.ProposeChangeRequest {
		return &skillsv1.ProposeChangeRequest{
			SkillName:  "frontend-design",
			AgentId:    agent,
			ProposalId: id,
			Files:      []*skillsv1.FileChange{{FilePath: "SKILL.md", Content: fixed}},
		}
	}

	first, err := s.ProposeChange(ctx, mk("agent-1", "fix"))
	if err != nil {
		t.Fatal(err)
	}
	if first.GetAutoSubmitted() != nil {
		t.Fatal("a single agent's proposal must not auto-submit at a threshold of 2")
	}
	if prCalls != 0 {
		t.Fatalf("expected no pull request yet, got %d", prCalls)
	}

	// A second agent independently arrives at the same content: threshold met.
	second, err := s.ProposeChange(ctx, mk("agent-2", "also-fix"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.GetDeduplicated() {
		t.Fatal("expected the identical proposal to be deduplicated into an endorsement")
	}
	if second.GetAutoSubmitted() == nil {
		t.Fatal("expected reaching the corroboration threshold to open a pull request")
	}
	if got := second.GetAutoSubmitted().GetPullRequestNumber(); got != 7 {
		t.Fatalf("unexpected pull request number: %d", got)
	}
	if prCalls != 1 {
		t.Fatalf("expected exactly 1 pull request, got %d", prCalls)
	}

	// An explicit submit afterwards must return the existing pull request
	// rather than opening a duplicate.
	resp, err := s.SubmitProposal(ctx, &skillsv1.SubmitProposalRequest{Branch: second.GetProposal().GetBranch()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPullRequestNumber() != 7 {
		t.Fatalf("expected the existing pull request to be returned, got %+v", resp)
	}
	if prCalls != 1 {
		t.Fatalf("expected no second pull request to be opened, got %d calls", prCalls)
	}
}

func TestAutoSubmitDisabledByDefault(t *testing.T) {
	s, _, _, _ := newTestServer(t)
	ctx := context.Background()

	var prCalls int
	s.github = githubpr.New(countingGitHub(t, &prCalls).URL, "acme", "skills", "test-token")

	fixed := validSkillMD("frontend-design", "the corrected description")
	for _, agent := range []string{"agent-1", "agent-2", "agent-3"} {
		if _, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
			SkillName:  "frontend-design",
			AgentId:    agent,
			ProposalId: "fix",
			Files:      []*skillsv1.FileChange{{FilePath: "SKILL.md", Content: fixed}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if prCalls != 0 {
		t.Fatalf("auto-submission must stay off unless configured, got %d pull requests", prCalls)
	}
}

func TestPullRequestBodyCarriesCorroborationAndEvidence(t *testing.T) {
	s, _, _, lastBody := newTestServer(t)
	ctx := context.Background()

	fixed := validSkillMD("frontend-design", "the corrected description")
	if _, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
		SkillName:           "frontend-design",
		AgentId:             "agent-1",
		ProposalId:          "fix",
		Files:               []*skillsv1.FileChange{{FilePath: "SKILL.md", Content: fixed}},
		CommitMessage:       "fix the description",
		MotivatingReportIds: []string{"report-aaa"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.ProposeChange(ctx, &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-2",
		ProposalId: "also-fix",
		Files:      []*skillsv1.FileChange{{FilePath: "SKILL.md", Content: fixed}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.SubmitProposal(ctx, &skillsv1.SubmitProposalRequest{
		Branch: res.GetProposal().GetBranch(),
	}); err != nil {
		t.Fatal(err)
	}

	var sent map[string]string
	if err := json.Unmarshal(*lastBody, &sent); err != nil {
		t.Fatal(err)
	}
	body := sent["body"]
	// A reviewer should see why this is worth their attention without
	// leaving the pull request.
	if !strings.Contains(body, "Independently proposed by 2 agents") {
		t.Fatalf("expected the corroboration count in the PR body, got:\n%s", body)
	}
	if !strings.Contains(body, "agent-2") {
		t.Fatalf("expected the endorsing agent to be listed, got:\n%s", body)
	}
	if !strings.Contains(body, "report-aaa") {
		t.Fatalf("expected the motivating report ID in the PR body, got:\n%s", body)
	}
}
