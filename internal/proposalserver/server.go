// Package proposalserver implements skillsv1.ProposalServiceServer, adapting
// internal/proposals and internal/githubpr into gRPC request/response
// messages and status codes - the write-path counterpart to internal/server.
package proposalserver

import (
	"context"
	"log/slog"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/araghukas/skillset/internal/protomap"
	"github.com/araghukas/skillset/internal/submit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements skillsv1.ProposalServiceServer on top of a
// proposals.Service and a githubpr.Client.
type Server struct {
	skillsv1.UnimplementedProposalServiceServer

	proposals             *proposals.Service
	submitter             *submit.Submitter
	baseBranch            string
	submitProposalEnabled bool
	autoSubmitThreshold   int
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
func New(svc *proposals.Service, gh *githubpr.Client, baseBranch string, submitProposalEnabled bool, autoSubmitThreshold int) *Server {
	return &Server{
		proposals:             svc,
		submitter:             submit.New(svc, gh, baseBranch),
		baseBranch:            baseBranch,
		submitProposalEnabled: submitProposalEnabled,
		autoSubmitThreshold:   autoSubmitThreshold,
	}
}

func (s *Server) ProposeChange(ctx context.Context, req *skillsv1.ProposeChangeRequest) (*skillsv1.ProposeChangeResponse, error) {
	res, err := s.proposals.ProposeChange(ctx, protomap.ProposeInput(req))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp := &skillsv1.ProposeChangeResponse{
		Proposal:     protomap.Proposal(res.Proposal),
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
func (s *Server) maybeAutoSubmit(ctx context.Context, p *proposals.Proposal) *skillsv1.SubmitProposalResponse {
	if s.autoSubmitThreshold <= 0 || !s.submitProposalEnabled {
		return nil
	}
	if p.Corroboration < s.autoSubmitThreshold {
		return nil
	}

	submitted, err := s.submitter.Submit(ctx, p, "", "")
	if err != nil {
		slog.Error("auto-submit failed; proposal is still submittable by hand",
			"branch", p.Branch, "corroboration", p.Corroboration, "error", err)
		return nil
	}
	slog.Info("auto-submitted corroborated proposal",
		"branch", p.Branch, "corroboration", p.Corroboration,
		"threshold", s.autoSubmitThreshold, "pull_request", submitted.PullRequestURL)
	return protomap.Submission(submitted)
}

func (s *Server) ListProposalClusters(ctx context.Context, req *skillsv1.ListProposalClustersRequest) (*skillsv1.ListProposalClustersResponse, error) {
	clusters, err := s.proposals.ListClusters(ctx, req.GetSkillName(), req.GetIncludeSingletons())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &skillsv1.ListProposalClustersResponse{Clusters: protomap.Clusters(clusters)}, nil
}

func (s *Server) ListProposals(ctx context.Context, req *skillsv1.ListProposalsRequest) (*skillsv1.ListProposalsResponse, error) {
	list, err := s.proposals.ListProposals(ctx, req.GetSkillName(), req.GetAgentId())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &skillsv1.ListProposalsResponse{Proposals: protomap.Proposals(list)}, nil
}

func (s *Server) GetProposal(ctx context.Context, req *skillsv1.GetProposalRequest) (*skillsv1.Proposal, error) {
	p, err := s.proposals.GetProposal(ctx, req.GetBranch())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return protomap.Proposal(p), nil
}

func (s *Server) GetSkillAtRef(ctx context.Context, req *skillsv1.GetSkillAtRefRequest) (*skillsv1.GetSkillResponse, error) {
	md, err := s.proposals.GetSkillAtRef(ctx, req.GetSkillName(), req.GetRef(), req.GetIncludeContextFiles())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &skillsv1.GetSkillResponse{Skill: protomap.SkillMetadata(md)}, nil
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

	resp, err := s.submitter.Submit(ctx, p, req.GetPrTitle(), req.GetPrBody())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return protomap.Submission(resp), nil
}
