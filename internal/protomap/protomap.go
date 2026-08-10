// Package protomap converts between the service's domain types and the
// generated protobuf messages.
//
// It is transitional. The domain types are the destination; the protobuf
// messages are on their way out along with gRPC, and this package exists
// only so the gRPC surface keeps working while the layers underneath it are
// converted. It is deleted in the same change that deletes proto/ and gen/.
//
// Nothing here should acquire logic. If a conversion needs a decision made,
// that decision belongs in the domain package, not in the mapper.
package protomap

import (
	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/evidence"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/araghukas/skillset/internal/skill"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SkillMetadata converts a skill.Metadata to its protobuf message.
func SkillMetadata(md *skill.Metadata) *skillsv1.SkillMetadata {
	if md == nil {
		return nil
	}
	out := &skillsv1.SkillMetadata{
		Name:          md.Name,
		Description:   md.Description,
		License:       md.License,
		Compatibility: md.Compatibility,
		Metadata:      md.Metadata,
		AllowedTools:  md.AllowedTools,
		JsonSchema:    md.JSONSchema,
		Commit:        md.Commit,
	}
	for _, cf := range md.ContextFiles {
		out.ContextFiles = append(out.ContextFiles, &skillsv1.SkillContextFile{
			FilePath: cf.FilePath,
			Content:  cf.Content,
			MimeType: cf.MimeType,
		})
	}
	return out
}

// FileEdits converts protobuf file changes to their domain form.
func FileEdits(in []*skillsv1.FileChange) []proposals.FileEdit {
	if in == nil {
		return nil
	}
	out := make([]proposals.FileEdit, 0, len(in))
	for _, fc := range in {
		out = append(out, proposals.FileEdit{
			FilePath: fc.GetFilePath(),
			Deleted:  fc.GetDeleted(),
			Content:  fc.GetContent(),
		})
	}
	return out
}

// ProposeInput converts a ProposeChange request to its domain form.
func ProposeInput(req *skillsv1.ProposeChangeRequest) proposals.ProposeInput {
	return proposals.ProposeInput{
		SkillName:           req.GetSkillName(),
		AgentID:             req.GetAgentId(),
		ProposalID:          req.GetProposalId(),
		Files:               FileEdits(req.GetFiles()),
		CommitMessage:       req.GetCommitMessage(),
		SourceThreadURI:     req.GetSourceThreadUri(),
		MotivatingReportIDs: req.GetMotivatingReportIds(),
		AllowDuplicate:      req.GetAllowDuplicate(),
	}
}

// Proposal converts a domain proposal to its protobuf message.
func Proposal(p *proposals.Proposal) *skillsv1.Proposal {
	if p == nil {
		return nil
	}
	out := &skillsv1.Proposal{
		ProposalId:          p.ProposalID,
		Branch:              p.Branch,
		SkillName:           p.SkillName,
		AgentId:             p.AgentID,
		BaseSha:             p.BaseSHA,
		HeadSha:             p.HeadSHA,
		Diff:                p.Diff,
		SourceThreadUri:     p.SourceThreadURI,
		UpdatedAt:           timestamppb.New(p.UpdatedAt),
		ContentHash:         p.ContentHash,
		Corroboration:       int32(p.Corroboration),
		MotivatingReportIds: p.MotivatingReportIDs,
	}
	for _, c := range p.Commits {
		out.Commits = append(out.Commits, &skillsv1.CommitInfo{
			Sha:        c.SHA,
			Message:    c.Message,
			Author:     c.Author,
			AuthoredAt: timestamppb.New(c.AuthoredAt),
		})
	}
	for _, e := range p.Endorsements {
		out.Endorsements = append(out.Endorsements, &skillsv1.Endorsement{
			AgentId:     e.AgentID,
			EndorsedSha: e.EndorsedSHA,
			Stale:       e.Stale,
			EndorsedAt:  timestamppb.New(e.EndorsedAt),
		})
	}
	return out
}

// Proposals converts a slice of domain proposals.
func Proposals(in []*proposals.Proposal) []*skillsv1.Proposal {
	if in == nil {
		return nil
	}
	out := make([]*skillsv1.Proposal, 0, len(in))
	for _, p := range in {
		out = append(out, Proposal(p))
	}
	return out
}

// Clusters converts domain clusters to their protobuf messages.
func Clusters(in []*proposals.Cluster) []*skillsv1.ProposalCluster {
	if in == nil {
		return nil
	}
	out := make([]*skillsv1.ProposalCluster, 0, len(in))
	for _, c := range in {
		out = append(out, &skillsv1.ProposalCluster{
			Proposals:      Proposals(c.Proposals),
			ContestedPaths: c.ContestedPaths,
			DistinctAgents: int32(c.DistinctAgents),
		})
	}
	return out
}

// Submission converts a domain submission to its protobuf message.
func Submission(s *proposals.Submission) *skillsv1.SubmitProposalResponse {
	if s == nil {
		return nil
	}
	return &skillsv1.SubmitProposalResponse{
		PullRequestUrl:    s.PullRequestURL,
		PullRequestNumber: s.PullRequestNumber,
	}
}

// Verdict converts a protobuf verdict to its evidence-store form. The two
// share a numbering, which TestVerdictIntegersAreFrozen pins in place.
func Verdict(v skillsv1.Verdict) evidence.Verdict {
	return evidence.Verdict(v)
}

// ProtoVerdict is the inverse of Verdict.
func ProtoVerdict(v evidence.Verdict) skillsv1.Verdict {
	return skillsv1.Verdict(v)
}
