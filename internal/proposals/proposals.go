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

// ProposeChange commits req's file changes onto the caller's proposal
// branch, creating it (forked from the current base branch HEAD) if it
// doesn't already exist, or appending a commit to it otherwise.
//
// The resulting skill is re-validated with skillparse after the commit
// lands: if the edit breaks SKILL.md's frontmatter, ProposeChange returns
// an error, but the commit itself is not rolled back - it's still visible
// via GetProposal, since a proposal branch is just the agent's own history
// and an invalid intermediate commit there is harmless. The agent is
// expected to fix it with a follow-up ProposeChange call.
func (s *Service) ProposeChange(ctx context.Context, req *skillsv1.ProposeChangeRequest) (*skillsv1.Proposal, error) {
	if req.GetSkillName() == "" {
		return nil, fmt.Errorf("proposals: skill_name is required")
	}
	if req.GetAgentId() == "" {
		return nil, fmt.Errorf("proposals: agent_id is required")
	}
	if req.GetProposalId() == "" {
		return nil, fmt.Errorf("proposals: proposal_id is required")
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
	message = appendSourceThreadTrailer(message, req.GetSourceThreadUri())

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

	return s.GetProposal(ctx, branch)
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
	for i, c := range log {
		authoredAt, _ := time.Parse(time.RFC3339, c.AuthoredAt)
		if i == 0 {
			updatedAt = authoredAt
			sourceThreadURI = extractSourceThreadTrailer(c.Message)
		}
		commits = append(commits, &skillsv1.CommitInfo{
			Sha:        c.SHA,
			Message:    c.Message,
			Author:     c.Author,
			AuthoredAt: timestamppb.New(authoredAt),
		})
	}

	return &skillsv1.Proposal{
		ProposalId:      proposalID,
		Branch:          branch,
		SkillName:       skillName,
		AgentId:         agentID,
		BaseSha:         base.String(),
		HeadSha:         head.String(),
		Diff:            diff,
		Commits:         commits,
		SourceThreadUri: sourceThreadURI,
		UpdatedAt:       timestamppb.New(updatedAt),
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

// Push pushes a proposal's branch to origin, ahead of a pull request being
// opened for it.
func (s *Service) Push(ctx context.Context, branch string) error {
	if _, _, _, ok := parseBranch(branch); !ok {
		return fmt.Errorf("proposals: %q is not a proposal branch (want proposals/<agent>/<skill>/<id>)", branch)
	}
	return s.repo.Push(ctx, branch)
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

	return skillparse.Load(ctx, backend, s.subPath, skillName, keys)
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

func appendSourceThreadTrailer(message, uri string) string {
	if uri == "" {
		return message
	}
	return fmt.Sprintf("%s\n\n%s: %s", message, sourceThreadTrailerKey, uri)
}

func extractSourceThreadTrailer(message string) string {
	prefix := sourceThreadTrailerKey + ": "
	for line := range strings.SplitSeq(message, "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return v
		}
	}
	return ""
}
