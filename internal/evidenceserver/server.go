// Package evidenceserver implements skillsv1.EvidenceServiceServer,
// adapting internal/evidence into gRPC messages and status codes - the
// evidence-path counterpart to internal/proposalserver.
package evidenceserver

import (
	"context"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/evidence"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SkillResolver checks that a reported (skill, commit) pair actually
// existed. It's satisfied by proposals.Service, which already has the
// repository open.
type SkillResolver interface {
	SkillExistsAt(ctx context.Context, skillName, commit string) error
}

// Server implements skillsv1.EvidenceServiceServer on top of a
// evidence.Store.
type Server struct {
	skillsv1.UnimplementedEvidenceServiceServer

	store    *evidence.Store
	resolver SkillResolver
	verify   bool
}

// New returns a Server backed by store.
//
// If verify is true, ReportOutcome rejects reports naming a skill/commit
// pair the registry's repository doesn't contain. This is the main defense
// against a misconfigured agent quietly filling the store with rows that
// look like signal - and it costs one tree lookup per reported skill.
func New(store *evidence.Store, resolver SkillResolver, verify bool) *Server {
	return &Server{store: store, resolver: resolver, verify: verify}
}

func (s *Server) ReportOutcome(ctx context.Context, req *skillsv1.ReportOutcomeRequest) (*skillsv1.ReportOutcomeResponse, error) {
	report := evidence.Report{
		ReportID:  req.GetReportId(),
		AgentID:   req.GetAgentId(),
		SessionID: req.GetSessionId(),
	}
	for _, o := range req.GetSkills() {
		report.Skills = append(report.Skills, evidence.SkillOutcome{
			SkillName:   o.GetSkillName(),
			SkillCommit: o.GetSkillCommit(),
			Verdict:     evidence.Verdict(o.GetVerdict()),
			Note:        o.GetNote(),
		})
	}

	if s.verify {
		for _, o := range report.Skills {
			if err := s.resolver.SkillExistsAt(ctx, o.SkillName, o.SkillCommit); err != nil {
				// FailedPrecondition, not InvalidArgument: the commonest
				// cause is a commit newer than this registry's last fetch of
				// the base branch, which resolves on its own within a fetch
				// interval. The report ID makes retrying free, so the right
				// advice is to retry rather than to drop the observation.
				return nil, status.Errorf(codes.FailedPrecondition,
					"cannot attribute outcome to %s@%s: %v; if that commit is newer than this "+
						"registry's last fetch, retry with the same report_id shortly",
					o.SkillName, o.SkillCommit, err)
			}
		}
	}

	recorded, err := s.store.RecordReport(ctx, report)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &skillsv1.ReportOutcomeResponse{Recorded: recorded}, nil
}

func (s *Server) ListSkillSignals(ctx context.Context, req *skillsv1.ListSkillSignalsRequest) (*skillsv1.ListSkillSignalsResponse, error) {
	signals, err := s.store.ListSignals(ctx, req.GetSkillName(), int(req.GetMinReportedSessions()))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	out := make([]*skillsv1.SkillSignal, 0, len(signals))
	for _, sig := range signals {
		counts := make([]*skillsv1.VerdictCount, 0, len(sig.VerdictCounts))
		// Iterate the enum in order rather than the map, so a signal
		// serializes identically on every call.
		for v := skillsv1.Verdict_VERDICT_APPLIED; v <= skillsv1.Verdict_VERDICT_NOT_APPLICABLE; v++ {
			if n, ok := sig.VerdictCounts[evidence.Verdict(v)]; ok {
				counts = append(counts, &skillsv1.VerdictCount{Verdict: v, Count: n})
			}
		}
		out = append(out, &skillsv1.SkillSignal{
			SkillName:         sig.SkillName,
			SkillCommit:       sig.SkillCommit,
			ReportedSessions:  sig.ReportedSessions,
			VerdictCounts:     counts,
			DefectRate:        sig.DefectRate,
			NotApplicableRate: sig.NotApplicableRate,
			FirstReportedAt:   timestamppb.New(sig.FirstReportedAt),
			LastReportedAt:    timestamppb.New(sig.LastReportedAt),
		})
	}
	return &skillsv1.ListSkillSignalsResponse{Signals: out}, nil
}

func (s *Server) ListOutcomeReports(ctx context.Context, req *skillsv1.ListOutcomeReportsRequest) (*skillsv1.ListOutcomeReportsResponse, error) {
	if req.GetSkillName() == "" {
		return nil, status.Error(codes.InvalidArgument, "skill_name is required")
	}

	reports, err := s.store.ListReports(ctx, evidence.ReportFilter{
		SkillName:         req.GetSkillName(),
		SkillCommit:       req.GetSkillCommit(),
		Verdict:           evidence.Verdict(req.GetVerdict()),
		FilterVerdict:     req.GetVerdict() != skillsv1.Verdict_VERDICT_UNSPECIFIED,
		ExcludeEmptyNotes: req.GetExcludeEmptyNotes(),
		Limit:             int(req.GetLimit()),
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	out := make([]*skillsv1.OutcomeReport, 0, len(reports))
	for _, r := range reports {
		out = append(out, &skillsv1.OutcomeReport{
			ReportId:    r.ReportID,
			AgentId:     r.AgentID,
			SessionId:   r.SessionID,
			SkillName:   r.SkillName,
			SkillCommit: r.SkillCommit,
			Verdict:     skillsv1.Verdict(r.Verdict),
			Note:        r.Note,
			ReportedAt:  timestamppb.New(r.ReportedAt),
		})
	}
	return &skillsv1.ListOutcomeReportsResponse{Reports: out}, nil
}
