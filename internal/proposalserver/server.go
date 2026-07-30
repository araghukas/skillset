// Package proposalserver implements skillsv1.ProposalServiceServer, adapting
// internal/proposals and internal/githubpr into gRPC request/response
// messages and status codes - the write-path counterpart to internal/server.
package proposalserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/go-git/go-git/v5/plumbing"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements skillsv1.ProposalServiceServer on top of a
// proposals.Service and a githubpr.Client.
type Server struct {
	skillsv1.UnimplementedProposalServiceServer

	proposals             *proposals.Service
	github                *githubpr.Client
	baseBranch            string
	submitProposalEnabled bool
	autoSubmitThreshold   int32
}

// New returns a Server backed by svc, opening pull requests via gh against
// baseBranch. If submitProposalEnabled is false, SubmitProposal refuses all
// requests instead of pushing branches or opening pull requests - the rest
// of the service (ProposeChange, GetProposal, GetSkillAtRef, ...) is
// unaffected.
//
// autoSubmitThreshold is the number of independent agents that must arrive
// at identical content before a pull request is opened without anyone
// asking. Zero disables it, which is the default: it is the one behavior
// here that acts on its own, and it should be switched on deliberately,
// with the trust model behind agent_id understood first.
func New(svc *proposals.Service, gh *githubpr.Client, baseBranch string, submitProposalEnabled bool, autoSubmitThreshold int32) *Server {
	return &Server{
		proposals:             svc,
		github:                gh,
		baseBranch:            baseBranch,
		submitProposalEnabled: submitProposalEnabled,
		autoSubmitThreshold:   autoSubmitThreshold,
	}
}

func (s *Server) ProposeChange(ctx context.Context, req *skillsv1.ProposeChangeRequest) (*skillsv1.ProposeChangeResponse, error) {
	res, err := s.proposals.ProposeChange(ctx, req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp := &skillsv1.ProposeChangeResponse{
		Proposal:     res.Proposal,
		Deduplicated: res.Deduplicated,
	}
	resp.AutoSubmitted = s.maybeAutoSubmit(ctx, res.Proposal)
	return resp, nil
}

// maybeAutoSubmit opens a pull request if this proposal has now been
// corroborated by enough independent agents.
//
// Failures here are logged and swallowed rather than returned: the agent's
// contribution is already committed and endorsed, and turning a GitHub
// outage into a failed ProposeChange would discard work that succeeded. The
// proposal remains submittable by hand.
func (s *Server) maybeAutoSubmit(ctx context.Context, p *skillsv1.Proposal) *skillsv1.SubmitProposalResponse {
	if s.autoSubmitThreshold <= 0 || !s.submitProposalEnabled {
		return nil
	}
	if p.GetCorroboration() < s.autoSubmitThreshold {
		return nil
	}

	submitted, err := s.submit(ctx, p, "", "")
	if err != nil {
		slog.Error("auto-submit failed; proposal is still submittable by hand",
			"branch", p.GetBranch(), "corroboration", p.GetCorroboration(), "error", err)
		return nil
	}
	slog.Info("auto-submitted corroborated proposal",
		"branch", p.GetBranch(), "corroboration", p.GetCorroboration(),
		"threshold", s.autoSubmitThreshold, "pull_request", submitted.GetPullRequestUrl())
	return submitted
}

func (s *Server) ListProposalClusters(ctx context.Context, req *skillsv1.ListProposalClustersRequest) (*skillsv1.ListProposalClustersResponse, error) {
	clusters, err := s.proposals.ListClusters(ctx, req.GetSkillName(), req.GetIncludeSingletons())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &skillsv1.ListProposalClustersResponse{Clusters: clusters}, nil
}

func (s *Server) ListProposals(ctx context.Context, req *skillsv1.ListProposalsRequest) (*skillsv1.ListProposalsResponse, error) {
	list, err := s.proposals.ListProposals(ctx, req.GetSkillName(), req.GetAgentId())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &skillsv1.ListProposalsResponse{Proposals: list}, nil
}

func (s *Server) GetProposal(ctx context.Context, req *skillsv1.GetProposalRequest) (*skillsv1.Proposal, error) {
	p, err := s.proposals.GetProposal(ctx, req.GetBranch())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return p, nil
}

func (s *Server) GetSkillAtRef(ctx context.Context, req *skillsv1.GetSkillAtRefRequest) (*skillsv1.GetSkillResponse, error) {
	md, err := s.proposals.GetSkillAtRef(ctx, req.GetSkillName(), req.GetRef(), req.GetIncludeContextFiles())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &skillsv1.GetSkillResponse{Skill: md}, nil
}

// SubmitProposal pushes the proposal's branch upstream and opens a GitHub
// pull request against the base branch for human review. There is no local
// merge step and no proposal status to update - the pull request itself,
// on GitHub, is the review mechanism from here on.
func (s *Server) SubmitProposal(ctx context.Context, req *skillsv1.SubmitProposalRequest) (*skillsv1.SubmitProposalResponse, error) {
	if !s.submitProposalEnabled {
		return nil, status.Error(codes.FailedPrecondition, "submitting proposals is disabled on this registry")
	}

	p, err := s.proposals.GetProposal(ctx, req.GetBranch())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	resp, err := s.submit(ctx, p, req.GetPrTitle(), req.GetPrBody())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return resp, nil
}

// submit pushes a proposal and opens a pull request for it, or returns the
// pull request it already has.
//
// The already-submitted check is what makes this callable from both the
// explicit RPC and the auto-submit path without either one opening a second
// pull request for the same branch. Like every other fact about a proposal,
// the record of submission is a ref in the repository, not a row somewhere.
func (s *Server) submit(ctx context.Context, p *skillsv1.Proposal, title, body string) (*skillsv1.SubmitProposalResponse, error) {
	if existing, ok, err := s.proposals.Submission(p.GetBranch()); err != nil {
		return nil, fmt.Errorf("checking for an existing pull request: %w", err)
	} else if ok {
		return existing, nil
	}

	if err := s.proposals.Push(ctx, p.GetBranch()); err != nil {
		return nil, fmt.Errorf("pushing branch: %w", err)
	}

	if title == "" {
		title = defaultTitle(p)
	}
	if body == "" {
		body = defaultBody(p)
	}

	pr, err := s.github.CreatePullRequest(ctx, githubpr.PullRequestInput{
		Title: title,
		Body:  body,
		Head:  p.GetBranch(),
		Base:  s.baseBranch,
	})
	if err != nil {
		return nil, fmt.Errorf("opening pull request: %w", err)
	}

	if err := s.proposals.MarkSubmitted(p.GetBranch(), plumbing.NewHash(p.GetHeadSha()), pr.URL, pr.Number); err != nil {
		// The pull request exists; failing the call now would tell the
		// caller nothing happened when something did. Log instead - the
		// cost of a lost marker is a duplicate-PR attempt later, which
		// GitHub itself rejects.
		slog.Error("could not record submission marker", "branch", p.GetBranch(), "error", err)
	}

	return &skillsv1.SubmitProposalResponse{
		PullRequestUrl:    pr.URL,
		PullRequestNumber: pr.Number,
	}, nil
}

func defaultTitle(p *skillsv1.Proposal) string {
	return fmt.Sprintf("skillsd: propose changes to %s (%s)", p.GetSkillName(), p.GetAgentId())
}

// defaultBody writes not just what changed, but how many independent
// agents arrived at it and which recorded failures it claims to fix.
func defaultBody(p *skillsv1.Proposal) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Proposed by agent `%s` via skillsd-registry.\n\n", p.GetAgentId())

	if n := p.GetCorroboration(); n > 1 {
		fmt.Fprintf(&b, "**Independently proposed by %d agents.** Each arrived at identical "+
			"content without seeing the others' work:\n\n- `%s` (opened this proposal)\n", n, p.GetAgentId())
		for _, e := range p.GetEndorsements() {
			if !e.GetStale() {
				fmt.Fprintf(&b, "- `%s`\n", e.GetAgentId())
			}
		}
		b.WriteString("\n")
	}

	if ids := p.GetMotivatingReportIds(); len(ids) > 0 {
		fmt.Fprintf(&b, "Motivated by %d recorded outcome report(s): %s\n\n",
			len(ids), "`"+strings.Join(ids, "`, `")+"`")
	}

	b.WriteString("Commits:\n")
	for _, c := range p.GetCommits() {
		sha := c.GetSha()
		if len(sha) > 7 {
			sha = sha[:7]
		}
		fmt.Fprintf(&b, "- %s: %s\n", sha, firstLine(c.GetMessage()))
	}

	if p.GetSourceThreadUri() != "" {
		fmt.Fprintf(&b, "\nSource conversation: %s\n", p.GetSourceThreadUri())
	}
	return b.String()
}

// firstLine keeps commit trailers (Source-Thread, Motivated-By) out of the
// bulleted commit list, where they'd repeat what the sections above say.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
