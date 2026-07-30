package server

import (
	"context"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/clientguide"
	"github.com/araghukas/skillset/internal/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements skillsv1.SkillServiceServer on top of a Registry.
type Server struct {
	skillsv1.UnimplementedSkillServiceServer

	reg *registry.Registry
}

// New returns a Server backed by reg.
func New(reg *registry.Registry) *Server {
	return &Server{reg: reg}
}

func (s *Server) ListSkills(ctx context.Context, req *skillsv1.ListSkillsRequest) (*skillsv1.ListSkillsResponse, error) {
	skills := s.reg.List()
	out := make([]*skillsv1.SkillMetadata, 0, len(skills))
	for _, sk := range skills {
		if category := req.GetCategory(); category != "" && sk.Metadata.GetMetadata()["category"] != category {
			continue
		}
		out = append(out, metadataFor(sk, req.GetIncludeContextFiles()))
	}
	return &skillsv1.ListSkillsResponse{
		Skills:    out,
		IndexedAt: timestamppb.New(s.reg.IndexedAt()),
	}, nil
}

func (s *Server) GetSkill(ctx context.Context, req *skillsv1.GetSkillRequest) (*skillsv1.GetSkillResponse, error) {
	sk, ok := s.reg.Get(req.GetSkillName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "skill %q not found", req.GetSkillName())
	}
	return &skillsv1.GetSkillResponse{Skill: metadataFor(sk, req.GetIncludeContextFiles())}, nil
}

func (s *Server) GetClientGuide(ctx context.Context, req *skillsv1.GetClientGuideRequest) (*skillsv1.GetSkillResponse, error) {
	return &skillsv1.GetSkillResponse{Skill: clientguide.Guide}, nil
}

// metadataFor returns sk's metadata, stripped of context files unless the
// caller asked for them. The registry's copy is shared across concurrent
// requests, so a clone is made before mutating rather than trimming in place.
func metadataFor(sk *registry.Skill, includeContextFiles bool) *skillsv1.SkillMetadata {
	if includeContextFiles {
		return sk.Metadata
	}
	md := proto.Clone(sk.Metadata).(*skillsv1.SkillMetadata)
	md.ContextFiles = nil
	return md
}
