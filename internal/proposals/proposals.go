// Package proposals implements the branch-naming convention and
// orchestration behind ProposalService: turning an agent's proposed file
// changes into a commit on a namespaced branch, and reading proposals and
// skills back out of the underlying gitrepo.Repo.
package proposals

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/skillparse"
	"github.com/araghukas/skillset/internal/storage"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// sourceThreadTrailerKey is the commit-message trailer a proposal's
// source_thread_uri is carried in. There's no separate metadata store for
// proposals - everything about them lives in git, so this rides along in
// the commit message rather than needing a database.
const sourceThreadTrailerKey = "Source-Thread"

// motivatedByTrailerKey carries one EvidenceService report ID per
// occurrence. Riding in the commit message means the evidence a proposal
// cites reaches the pull request even when the evidence store itself is
// disabled, unreachable, or has since aged the reports out.
const motivatedByTrailerKey = "Motivated-By"

// DefaultMaxFileContentBytes is the default cap on a single FileChange's
// content. Skill files (SKILL.md, small scripts/references) are expected
// to be well under this.
const DefaultMaxFileContentBytes = 1 << 20 // 1 MiB

// Service resolves agent proposals and ref-scoped skill reads against a
// single gitrepo.Repo.
type Service struct {
	repo         *gitrepo.Repo
	subPath      string
	maxFileBytes int
}

// New returns a Service backed by repo. subPath is the optional
// subdirectory within the repo skill directories live under, matching
// internal/registry's SKILLS_SUBPATH. maxFileBytes caps a single
// FileChange's content; use DefaultMaxFileContentBytes if the caller has no
// specific requirement.
func New(repo *gitrepo.Repo, subPath string, maxFileBytes int) *Service {
	return &Service{repo: repo, subPath: subPath, maxFileBytes: maxFileBytes}
}

// ProposeResult is the outcome of a ProposeChange call: either the caller's
// own proposal, or - when their content already existed - the proposal they
// were recorded as endorsing instead.
type ProposeResult struct {
	Proposal     *skillsv1.Proposal
	Deduplicated bool
}

// ProposeChange commits req's file changes onto the caller's proposal
// branch, creating it (forked from the current base branch HEAD) if it
// doesn't already exist, or appending a commit to it otherwise.
//
// Before creating a new branch, it checks whether another agent's open
// proposal for this skill already produces identical content. If one does,
// no branch is created: the caller is recorded as an endorser of that
// proposal and it is returned with Deduplicated set. This is where N agents
// noticing one defect collapse into a single pull request carrying N
// signatures, instead of N pull requests saying the same thing.
//
// The check is skipped once the caller's own branch exists, so an agent
// iterating on its own proposal is never diverted onto someone else's
// mid-flight, and skipped entirely if req.AllowDuplicate is set.
//
// The resulting skill is re-validated with skillparse after the commit
// lands: if the edit breaks SKILL.md's frontmatter, ProposeChange returns
// an error, but the commit itself is not rolled back - it's still visible
// via GetProposal, since a proposal branch is just the agent's own history
// and an invalid intermediate commit there is harmless. The agent is
// expected to fix it with a follow-up ProposeChange call.
func (s *Service) ProposeChange(ctx context.Context, req *skillsv1.ProposeChangeRequest) (*ProposeResult, error) {
	if req.GetSkillName() == "" {
		return nil, fmt.Errorf("proposals: skill_name is required")
	}
	if req.GetAgentId() == "" {
		return nil, fmt.Errorf("proposals: agent_id is required")
	}
	if req.GetProposalId() == "" {
		return nil, fmt.Errorf("proposals: proposal_id is required")
	}
	// The branch name and every annotation ref hanging off it are built by
	// joining these three with "/", and parsed back by splitting on it.
	for label, v := range map[string]string{
		"agent_id":    req.GetAgentId(),
		"skill_name":  req.GetSkillName(),
		"proposal_id": req.GetProposalId(),
	} {
		if strings.Contains(v, "/") {
			return nil, fmt.Errorf("proposals: %s %q must not contain %q", label, v, "/")
		}
	}
	if len(req.GetFiles()) == 0 {
		return nil, fmt.Errorf("proposals: at least one file change is required")
	}
	for _, fc := range req.GetFiles() {
		if fc.GetDeleted() {
			continue
		}
		if !utf8.ValidString(fc.GetContent()) {
			return nil, fmt.Errorf("proposals: file %q is not valid UTF-8", fc.GetFilePath())
		}
		if len(fc.GetContent()) > s.maxFileBytes {
			return nil, fmt.Errorf("proposals: file %q is %d bytes, exceeding the %d byte limit", fc.GetFilePath(), len(fc.GetContent()), s.maxFileBytes)
		}
	}

	branch := branchName(req.GetAgentId(), req.GetSkillName(), req.GetProposalId())

	base, err := s.repo.BaseHead()
	if err != nil {
		return nil, fmt.Errorf("proposals: resolving base branch: %w", err)
	}

	_, branchErr := s.repo.ResolveRef(branch)
	isNewBranch := branchErr != nil

	if isNewBranch && !req.GetAllowDuplicate() {
		dup, err := s.duplicateOf(ctx, req, base)
		if err != nil {
			return nil, err
		}
		if dup != nil {
			if err := s.Endorse(dup.GetBranch(), req.GetAgentId(), plumbing.NewHash(dup.GetHeadSha())); err != nil {
				return nil, fmt.Errorf("proposals: recording endorsement: %w", err)
			}
			// Re-read so the returned proposal carries the endorsement
			// just written, and the corroboration count that follows from it.
			refreshed, err := s.GetProposal(ctx, dup.GetBranch())
			if err != nil {
				return nil, err
			}
			return &ProposeResult{Proposal: refreshed, Deduplicated: true}, nil
		}
	}

	files := make([]gitrepo.FileChange, 0, len(req.GetFiles()))
	for _, fc := range req.GetFiles() {
		files = append(files, gitrepo.FileChange{
			Path:    path.Join(s.subPath, req.GetSkillName(), fc.GetFilePath()),
			Deleted: fc.GetDeleted(),
			Content: []byte(fc.GetContent()),
		})
	}

	message := req.GetCommitMessage()
	if message == "" {
		message = fmt.Sprintf("propose: %s", req.GetSkillName())
	}
	message = appendTrailers(message, req.GetSourceThreadUri(), req.GetMotivatingReportIds())

	author := object.Signature{
		Name:  req.GetAgentId(),
		Email: req.GetAgentId() + "@agents.local",
		When:  time.Now(),
	}

	head, err := s.repo.CommitOnBranch(branch, base, files, message, author)
	if err != nil {
		return nil, fmt.Errorf("proposals: committing change: %w", err)
	}

	if _, err := s.skillAt(ctx, req.GetSkillName(), head); err != nil {
		return nil, fmt.Errorf("proposals: resulting skill is invalid: %w", err)
	}

	p, err := s.GetProposal(ctx, branch)
	if err != nil {
		return nil, err
	}
	return &ProposeResult{Proposal: p}, nil
}

// duplicateOf computes the content hash req's changes would produce and
// returns another agent's open proposal that already matches it, if any.
//
// The hash is computed from the prospective file set rather than from a
// commit, so the common case - discovering the duplicate - costs nothing and
// leaves no abandoned branch behind.
func (s *Service) duplicateOf(ctx context.Context, req *skillsv1.ProposeChangeRequest, base plumbing.Hash) (*skillsv1.Proposal, error) {
	current, err := s.skillFilesAt(ctx, req.GetSkillName(), base)
	if err != nil {
		// A skill that doesn't exist at base yet can't have a duplicate
		// proposal to collapse into; let the commit path handle it.
		return nil, nil
	}

	prospective := applyChanges(current, s.subPath, req.GetSkillName(), req.GetFiles())
	return s.findDuplicate(ctx, req.GetSkillName(), req.GetAgentId(), hashFiles(prospective))
}

// ListProposals returns every proposal, optionally filtered by skill and/or
// agent.
func (s *Service) ListProposals(ctx context.Context, skillFilter, agentFilter string) ([]*skillsv1.Proposal, error) {
	names, err := s.repo.BranchesWithPrefix("proposals/")
	if err != nil {
		return nil, fmt.Errorf("proposals: listing branches: %w", err)
	}

	var out []*skillsv1.Proposal
	for _, name := range names {
		agentID, skillName, _, ok := parseBranch(name)
		if !ok {
			continue
		}
		if skillFilter != "" && skillFilter != skillName {
			continue
		}
		if agentFilter != "" && agentFilter != agentID {
			continue
		}

		p, err := s.GetProposal(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// GetProposal fetches a single proposal by its fully-qualified branch name.
func (s *Service) GetProposal(ctx context.Context, branch string) (*skillsv1.Proposal, error) {
	agentID, skillName, proposalID, ok := parseBranch(branch)
	if !ok {
		return nil, fmt.Errorf("proposals: %q is not a proposal branch (want proposals/<agent>/<skill>/<id>)", branch)
	}

	head, err := s.repo.ResolveRef(branch)
	if err != nil {
		return nil, fmt.Errorf("proposals: resolving branch %q: %w", branch, err)
	}
	baseHead, err := s.repo.BaseHead()
	if err != nil {
		return nil, fmt.Errorf("proposals: resolving base branch: %w", err)
	}
	base, err := s.repo.MergeBase(baseHead, head)
	if err != nil {
		return nil, fmt.Errorf("proposals: finding fork point of %q: %w", branch, err)
	}

	diff, err := s.repo.Diff(base, head)
	if err != nil {
		return nil, fmt.Errorf("proposals: diffing %q: %w", branch, err)
	}
	log, err := s.repo.Log(base, head)
	if err != nil {
		return nil, fmt.Errorf("proposals: reading history of %q: %w", branch, err)
	}

	commits := make([]*skillsv1.CommitInfo, 0, len(log))
	var updatedAt time.Time
	var sourceThreadURI string
	var reportIDs []string
	seenReport := make(map[string]struct{})
	for i, c := range log {
		authoredAt, _ := time.Parse(time.RFC3339, c.AuthoredAt)
		if i == 0 {
			updatedAt = authoredAt
			sourceThreadURI = extractTrailer(c.Message, sourceThreadTrailerKey)
		}
		// Report IDs accumulate across the whole branch: each commit cites
		// what motivated that revision, and the proposal as a whole rests on
		// all of them.
		for _, id := range extractTrailers(c.Message, motivatedByTrailerKey) {
			if _, ok := seenReport[id]; ok {
				continue
			}
			seenReport[id] = struct{}{}
			reportIDs = append(reportIDs, id)
		}
		commits = append(commits, &skillsv1.CommitInfo{
			Sha:        c.SHA,
			Message:    c.Message,
			Author:     c.Author,
			AuthoredAt: timestamppb.New(authoredAt),
		})
	}

	contentHash, err := s.contentHashAt(ctx, skillName, head)
	if err != nil {
		return nil, fmt.Errorf("proposals: hashing %q: %w", branch, err)
	}
	endorsements, corroboration, err := s.endorsementsFor(branch, head)
	if err != nil {
		return nil, fmt.Errorf("proposals: reading endorsements of %q: %w", branch, err)
	}

	return &skillsv1.Proposal{
		ProposalId:          proposalID,
		Branch:              branch,
		SkillName:           skillName,
		AgentId:             agentID,
		BaseSha:             base.String(),
		HeadSha:             head.String(),
		Diff:                diff,
		Commits:             commits,
		SourceThreadUri:     sourceThreadURI,
		UpdatedAt:           timestamppb.New(updatedAt),
		ContentHash:         contentHash,
		Endorsements:        endorsements,
		Corroboration:       corroboration,
		MotivatingReportIds: reportIDs,
	}, nil
}

// GetSkillAtRef resolves ref (a branch name, a commit SHA, or "" for the
// base branch HEAD) and returns skillName's metadata as of that commit.
func (s *Service) GetSkillAtRef(ctx context.Context, skillName, ref string, includeContextFiles bool) (*skillsv1.SkillMetadata, error) {
	hash, err := s.repo.ResolveRef(ref)
	if err != nil {
		return nil, fmt.Errorf("proposals: resolving ref %q: %w", ref, err)
	}

	md, err := s.skillAt(ctx, skillName, hash)
	if err != nil {
		return nil, err
	}
	if !includeContextFiles {
		md = proto.Clone(md).(*skillsv1.SkillMetadata)
		md.ContextFiles = nil
	}
	return md, nil
}

// SkillExistsAt reports whether skillName parsed cleanly as of ref, which
// is how EvidenceService validates that a reported outcome names a real
// version of a real skill before storing it. Reports are primary data that
// nothing can reconstruct, so it is worth one tree lookup to keep the ones
// that are meaningless out.
func (s *Service) SkillExistsAt(ctx context.Context, skillName, ref string) error {
	hash, err := s.repo.ResolveRef(ref)
	if err != nil {
		return fmt.Errorf("proposals: resolving ref %q: %w", ref, err)
	}
	if _, err := s.skillAt(ctx, skillName, hash); err != nil {
		return err
	}
	return nil
}

// Push pushes a proposal's branch to origin, ahead of a pull request being
// opened for it, along with the endorsement refs attached to it - the
// corroboration behind a proposal is part of the proposal, and leaving it
// on a local volume would mean losing the reason the pull request is worth
// reviewing.
func (s *Service) Push(ctx context.Context, branch string) error {
	if _, _, _, ok := parseBranch(branch); !ok {
		return fmt.Errorf("proposals: %q is not a proposal branch (want proposals/<agent>/<skill>/<id>)", branch)
	}
	refs, err := s.PushRefs(branch)
	if err != nil {
		return err
	}
	return s.repo.Push(ctx, branch, refs...)
}

func (s *Service) skillAt(ctx context.Context, skillName string, hash plumbing.Hash) (*skillsv1.SkillMetadata, error) {
	tree, err := s.repo.Tree(hash)
	if err != nil {
		return nil, err
	}

	backend := storage.NewGitTreeBackend(tree)
	dirPrefix := path.Join(s.subPath, skillName)
	keys, err := backend.List(ctx, dirPrefix)
	if err != nil {
		return nil, fmt.Errorf("proposals: listing %s: %w", dirPrefix, err)
	}

	md, err := skillparse.Load(ctx, backend, s.subPath, skillName, keys)
	if err != nil {
		return nil, err
	}
	md.Commit = hash.String()
	return md, nil
}

// branchName builds the namespaced branch a proposal lives on.
func branchName(agentID, skillName, proposalID string) string {
	return fmt.Sprintf("proposals/%s/%s/%s", agentID, skillName, proposalID)
}

// parseBranch reverses branchName, reporting ok=false if name doesn't
// follow the proposals/<agent>/<skill>/<id> convention.
func parseBranch(name string) (agentID, skillName, proposalID string, ok bool) {
	const prefix = "proposals/"
	if !strings.HasPrefix(name, prefix) {
		return "", "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(name, prefix), "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// appendTrailers attaches a proposal's out-of-band metadata to its commit
// message, one trailer line per value.
func appendTrailers(message, sourceThreadURI string, reportIDs []string) string {
	var trailers []string
	if sourceThreadURI != "" {
		trailers = append(trailers, fmt.Sprintf("%s: %s", sourceThreadTrailerKey, sourceThreadURI))
	}
	for _, id := range reportIDs {
		if id == "" {
			continue
		}
		trailers = append(trailers, fmt.Sprintf("%s: %s", motivatedByTrailerKey, id))
	}
	if len(trailers) == 0 {
		return message
	}
	return message + "\n\n" + strings.Join(trailers, "\n")
}

// extractTrailer returns the first value for key, or "".
func extractTrailer(message, key string) string {
	if values := extractTrailers(message, key); len(values) > 0 {
		return values[0]
	}
	return ""
}

// extractTrailers returns every value for key, in order.
func extractTrailers(message, key string) []string {
	prefix := key + ": "
	var out []string
	for line := range strings.SplitSeq(message, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}
