// Package proposalserver implements skillsv1.ProposalServiceServer, adapting
// internal/proposals and internal/githubpr into gRPC request/response
// messages and status codes - the write-path counterpart to internal/server.
package proposalserver

import (
	"context"
	"fmt"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/proposals"
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
}

// New returns a Server backed by svc, opening pull requests via gh against
// baseBranch. If submitProposalEnabled is false, SubmitProposal refuses all
// requests instead of pushing branches or opening pull requests - the rest
// of the service (ProposeChange, GetProposal, GetSkillAtRef, ...) is
// unaffected.
func New(svc *proposals.Service, gh *githubpr.Client, baseBranch string, submitProposalEnabled bool) *Server {
	return &Server{proposals: svc, github: gh, baseBranch: baseBranch, submitProposalEnabled: submitProposalEnabled}
}

func (s *Server) ProposeChange(ctx context.Context, req *skillsv1.ProposeChangeRequest) (*skillsv1.Proposal, error) {
	p, err := s.proposals.ProposeChange(ctx, req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return p, nil
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

	if err := s.proposals.Push(ctx, req.GetBranch()); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("pushing branch: %v", err))
	}

	title := req.GetPrTitle()
	if title == "" {
		title = defaultTitle(p)
	}
	body := req.GetPrBody()
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
		return nil, status.Error(codes.Internal, fmt.Sprintf("opening pull request: %v", err))
	}

	return &skillsv1.SubmitProposalResponse{
		PullRequestUrl:    pr.URL,
		PullRequestNumber: pr.Number,
	}, nil
}

func defaultTitle(p *skillsv1.Proposal) string {
	return fmt.Sprintf("skillsd: propose changes to %s (%s)", p.GetSkillName(), p.GetAgentId())
}

func defaultBody(p *skillsv1.Proposal) string {
	body := fmt.Sprintf("Proposed by agent `%s` via skillsd-registry.\n\nCommits:\n", p.GetAgentId())
	for _, c := range p.GetCommits() {
		sha := c.GetSha()
		if len(sha) > 7 {
			sha = sha[:7]
		}
		body += fmt.Sprintf("- %s: %s\n", sha, c.GetMessage())
	}
	if p.GetSourceThreadUri() != "" {
		body += fmt.Sprintf("\nSource conversation: %s\n", p.GetSourceThreadUri())
	}
	return body
}
