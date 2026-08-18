// Package suggestions implements the branch-naming convention and
// orchestration behind the suggestion tools: turning an agent's suggested
// file changes into a commit on a namespaced branch, and reading
// suggestions and skills back out of the underlying gitrepo.Repo.
package suggestions

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/skill"
	"github.com/araghukas/skillset/internal/skillparse"
	"github.com/araghukas/skillset/internal/storage"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// sourceThreadTrailerKey is the commit-message trailer a suggestion's
// source_thread_uri is carried in. There's no separate metadata store for
// suggestions - everything about them lives in git, so this rides along in
// the commit message rather than needing a database.
const sourceThreadTrailerKey = "Source-Thread"

// motivatedByTrailerKey carries one EvidenceService report ID per
// occurrence. Riding in the commit message means the evidence a suggestion
// cites reaches the pull request even when the evidence store itself is
// disabled, unreachable, or has since aged the reports out.
const motivatedByTrailerKey = "Motivated-By"

// DefaultMaxFileContentBytes is the default cap on a single FileEdit's
// content. Skill files (SKILL.md, small scripts/references) are expected
// to be well under this.
const DefaultMaxFileContentBytes = 1 << 20 // 1 MiB

// Service resolves agent suggestions and ref-scoped skill reads against a
// single gitrepo.Repo.
type Service struct {
	repo         *gitrepo.Repo
	subPath      string
	maxFileBytes int
}

// New returns a Service backed by repo. subPath is the optional
// subdirectory within the repo skill directories live under, matching
// internal/registry's SKILLS_SUBPATH. maxFileBytes caps a single
// FileEdit's content; use DefaultMaxFileContentBytes if the caller has no
// specific requirement.
func New(repo *gitrepo.Repo, subPath string, maxFileBytes int) *Service {
	return &Service{repo: repo, subPath: subPath, maxFileBytes: maxFileBytes}
}

// RecordSuggestion commits req's file changes onto the caller's suggestion
// branch, creating it (forked from the current base branch HEAD) if it
// doesn't already exist, or appending a commit to it otherwise.
//
// The change arrives as a unified diff, which is expanded into full file
// contents before anything else happens - so a caller who only knows what
// changed never has to restate a file they didn't write, and everything below
// this point sees one kind of request. Whole file contents are sent directly
// only for a file that doesn't exist yet.
//
// Before creating a new branch, it checks whether another agent's open
// suggestion for this skill already produces identical content. If one
// does, no branch is created: the caller is recorded as an endorser of that
// suggestion and it is returned with Deduplicated set. This is where N
// agents noticing one defect collapse into a single pull request carrying N
// signatures, instead of N pull requests saying the same thing.
//
// The check is skipped once the caller's own branch exists, so an agent
// iterating on its own suggestion is never diverted onto someone else's
// mid-flight, and skipped entirely if req.AllowDuplicate is set.
//
// The resulting skill is re-validated with skillparse after the commit
// lands: if the edit breaks SKILL.md's frontmatter, RecordSuggestion
// returns an error, but the commit itself is not rolled back - it's still
// visible via GetSuggestion, since a suggestion branch is just the
// registry's own internal bookkeeping for the agent's history and an
// invalid intermediate commit there is harmless. The agent is expected to
// fix it with a follow-up RecordSuggestion call.
func (s *Service) RecordSuggestion(ctx context.Context, req SuggestInput) (*SuggestResult, error) {
	if req.SkillName == "" {
		return nil, fmt.Errorf("suggestions: skill_name is required")
	}
	if req.AgentID == "" {
		return nil, fmt.Errorf("suggestions: agent_id is required")
	}
	if req.SuggestionID == "" {
		return nil, fmt.Errorf("suggestions: suggestion_id is required")
	}
	// The branch name and every annotation ref hanging off it are built by
	// joining these three with "/", and parsed back by splitting on it.
	for label, v := range map[string]string{
		"agent_id":      req.AgentID,
		"skill_name":    req.SkillName,
		"suggestion_id": req.SuggestionID,
	} {
		if strings.Contains(v, "/") {
			return nil, fmt.Errorf("suggestions: %s %q must not contain %q", label, v, "/")
		}
	}
	switch {
	case len(req.Files) == 0 && req.Patch == "":
		return nil, fmt.Errorf("suggestions: a change is required: send patch with a unified diff, or files with whole file contents when the file is new")
	case len(req.Files) > 0 && req.Patch != "":
		return nil, fmt.Errorf("suggestions: send either files or patch, not both")
	case len(req.Patch) > s.maxFileBytes:
		return nil, fmt.Errorf("suggestions: patch is %d bytes, exceeding the %d byte limit", len(req.Patch), s.maxFileBytes)
	}

	branch := branchName(req.AgentID, req.SkillName, req.SuggestionID)

	base, err := s.repo.BaseHead()
	if err != nil {
		return nil, fmt.Errorf("suggestions: resolving base branch: %w", err)
	}

	tip, branchErr := s.repo.ResolveRef(branch)
	isNewBranch := branchErr != nil

	// A patch is expanded into full file contents here, above everything that
	// acts on a suggestion, so nothing below has to know which form the caller
	// sent. It applies to the caller's own branch tip when they're iterating -
	// the content their last call left - and to the base branch otherwise,
	// which is also the tree the duplicate check reads.
	if req.Patch != "" {
		at := tip
		if isNewBranch {
			at = base
		}
		expanded, err := s.expandPatch(ctx, req, branch, at, !isNewBranch)
		if err != nil {
			return nil, err
		}
		req.Files, req.Patch = expanded, ""
	}

	// Copied because the paths below are rewritten to their cleaned form, and
	// the caller's slice is theirs.
	req.Files = slices.Clone(req.Files)

	for i, fc := range req.Files {
		clean, err := cleanSkillRelPath(fc.FilePath)
		if err != nil {
			return nil, err
		}
		req.Files[i].FilePath = clean

		if fc.Deleted {
			continue
		}
		if !utf8.ValidString(fc.Content) {
			return nil, fmt.Errorf("suggestions: file %q is not valid UTF-8", fc.FilePath)
		}
		if len(fc.Content) > s.maxFileBytes {
			return nil, fmt.Errorf("suggestions: file %q is %d bytes, exceeding the %d byte limit", fc.FilePath, len(fc.Content), s.maxFileBytes)
		}
	}

	if isNewBranch && !req.AllowDuplicate {
		dup, err := s.duplicateOf(ctx, req, base)
		if err != nil {
			return nil, err
		}
		if dup != nil {
			if err := s.Endorse(dup.Branch, req.AgentID, plumbing.NewHash(dup.HeadSHA)); err != nil {
				return nil, fmt.Errorf("suggestions: recording endorsement: %w", err)
			}
			// Re-read so the returned suggestion carries the endorsement
			// just written, and the corroboration count that follows from it.
			refreshed, err := s.GetSuggestion(ctx, dup.Branch)
			if err != nil {
				return nil, err
			}
			return &SuggestResult{Suggestion: refreshed, Deduplicated: true}, nil
		}
	}

	files := make([]gitrepo.FileChange, 0, len(req.Files))
	for _, fc := range req.Files {
		files = append(files, gitrepo.FileChange{
			Path:    path.Join(s.subPath, req.SkillName, fc.FilePath),
			Deleted: fc.Deleted,
			Content: []byte(fc.Content),
		})
	}

	message := req.CommitMessage
	if message == "" {
		message = fmt.Sprintf("suggest: %s", req.SkillName)
	}
	message = appendTrailers(message, req.SourceThreadURI, req.MotivatingReportIDs)

	author := object.Signature{
		Name:  req.AgentID,
		Email: req.AgentID + "@agents.local",
		When:  time.Now(),
	}

	head, err := s.repo.CommitOnBranch(branch, base, files, message, author)
	if err != nil {
		return nil, fmt.Errorf("suggestions: committing change: %w", err)
	}

	if _, err := s.skillAt(ctx, req.SkillName, head); err != nil {
		return nil, fmt.Errorf("suggestions: resulting skill is invalid: %w", err)
	}

	sg, err := s.GetSuggestion(ctx, branch)
	if err != nil {
		return nil, err
	}
	return &SuggestResult{Suggestion: sg}, nil
}

// duplicateOf computes the content hash req's changes would produce and
// returns another agent's open suggestion that already matches it, if any.
//
// The hash is computed from the prospective file set rather than from a
// commit, so the common case - discovering the duplicate - costs nothing and
// leaves no abandoned branch behind.
func (s *Service) duplicateOf(ctx context.Context, req SuggestInput, base plumbing.Hash) (*Suggestion, error) {
	current, err := s.skillFilesAt(ctx, req.SkillName, base)
	if err != nil {
		// A skill that doesn't exist at base yet can't have a duplicate
		// suggestion to collapse into; let the commit path handle it.
		return nil, nil
	}

	prospective := applyChanges(current, s.subPath, req.SkillName, req.Files)
	return s.findDuplicate(ctx, req.SkillName, req.AgentID, hashFiles(prospective))
}

// ListSuggestions returns every suggestion, optionally filtered by skill
// and/or agent.
//
// The returned suggestions carry no diff. Computing one means a
// tree-to-tree comparison per branch, and the results land in a calling
// agent's context window - a listing of twenty suggestions should not cost
// twenty diffs when the caller is choosing which one to look at. Fetch a
// specific suggestion with GetSuggestion to see its diff.
func (s *Service) ListSuggestions(ctx context.Context, skillFilter, agentFilter string) ([]*Suggestion, error) {
	names, err := s.repo.BranchesWithPrefix("suggestions/")
	if err != nil {
		return nil, fmt.Errorf("suggestions: listing branches: %w", err)
	}

	var out []*Suggestion
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

		sg, err := s.getSuggestion(ctx, name, false)
		if err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, nil
}

// GetSuggestion fetches a single suggestion, with its diff, by its
// fully-qualified branch name.
func (s *Service) GetSuggestion(ctx context.Context, branch string) (*Suggestion, error) {
	return s.getSuggestion(ctx, branch, true)
}

// getSuggestion reads one suggestion branch. withDiff controls whether the
// unified diff is computed; see ListSuggestions for why it is optional.
func (s *Service) getSuggestion(ctx context.Context, branch string, withDiff bool) (*Suggestion, error) {
	agentID, skillName, suggestionID, ok := parseBranch(branch)
	if !ok {
		return nil, fmt.Errorf("suggestions: %q is not a suggestion branch (want suggestions/<agent>/<skill>/<id>)", branch)
	}

	head, err := s.repo.ResolveRef(branch)
	if err != nil {
		return nil, fmt.Errorf("suggestions: resolving branch %q: %w", branch, err)
	}
	baseHead, err := s.repo.BaseHead()
	if err != nil {
		return nil, fmt.Errorf("suggestions: resolving base branch: %w", err)
	}
	base, err := s.repo.MergeBase(baseHead, head)
	if err != nil {
		return nil, fmt.Errorf("suggestions: finding fork point of %q: %w", branch, err)
	}

	var diff string
	if withDiff {
		diff, err = s.repo.Diff(base, head)
		if err != nil {
			return nil, fmt.Errorf("suggestions: diffing %q: %w", branch, err)
		}
	}
	log, err := s.repo.Log(base, head)
	if err != nil {
		return nil, fmt.Errorf("suggestions: reading history of %q: %w", branch, err)
	}

	commits := make([]Commit, 0, len(log))
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
		// what motivated that revision, and the suggestion as a whole rests
		// on all of them.
		for _, id := range extractTrailers(c.Message, motivatedByTrailerKey) {
			if _, ok := seenReport[id]; ok {
				continue
			}
			seenReport[id] = struct{}{}
			reportIDs = append(reportIDs, id)
		}
		commits = append(commits, Commit{
			SHA:        c.SHA,
			Message:    c.Message,
			Author:     c.Author,
			AuthoredAt: authoredAt,
		})
	}

	contentHash, err := s.contentHashAt(ctx, skillName, head)
	if err != nil {
		return nil, fmt.Errorf("suggestions: hashing %q: %w", branch, err)
	}
	endorsements, corroboration, err := s.endorsementsFor(branch, head)
	if err != nil {
		return nil, fmt.Errorf("suggestions: reading endorsements of %q: %w", branch, err)
	}

	return &Suggestion{
		SuggestionID:        suggestionID,
		Branch:              branch,
		SkillName:           skillName,
		AgentID:             agentID,
		BaseSHA:             base.String(),
		HeadSHA:             head.String(),
		Diff:                diff,
		Commits:             commits,
		SourceThreadURI:     sourceThreadURI,
		UpdatedAt:           updatedAt,
		ContentHash:         contentHash,
		Endorsements:        endorsements,
		Corroboration:       corroboration,
		MotivatingReportIDs: reportIDs,
	}, nil
}

// GetSkillAtRef resolves ref (a branch name, a commit SHA, or "" for the
// base branch HEAD) and returns skillName's metadata as of that commit.
func (s *Service) GetSkillAtRef(ctx context.Context, skillName, ref string, includeContextFiles bool) (*skill.Metadata, error) {
	hash, err := s.repo.ResolveRef(ref)
	if err != nil {
		return nil, fmt.Errorf("suggestions: resolving ref %q: %w", ref, err)
	}

	md, err := s.skillAt(ctx, skillName, hash)
	if err != nil {
		return nil, err
	}
	if !includeContextFiles {
		md = md.WithoutContextFiles()
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
		return fmt.Errorf("suggestions: resolving ref %q: %w", ref, err)
	}
	if _, err := s.skillAt(ctx, skillName, hash); err != nil {
		return err
	}
	return nil
}

// Push pushes a suggestion's branch to origin, ahead of a pull request being
// opened for it, along with the endorsement refs attached to it - the
// corroboration behind a suggestion is part of the suggestion, and leaving
// it on a local volume would mean losing the reason the pull request is
// worth reviewing.
func (s *Service) Push(ctx context.Context, branch string) error {
	if _, _, _, ok := parseBranch(branch); !ok {
		return fmt.Errorf("suggestions: %q is not a suggestion branch (want suggestions/<agent>/<skill>/<id>)", branch)
	}
	refs, err := s.PushRefs(branch)
	if err != nil {
		return err
	}
	return s.repo.Push(ctx, branch, refs...)
}

// cleanSkillRelPath returns p in the canonical form a file within a skill
// directory takes, and rejects anything that would resolve outside it.
//
// Every edit's path is joined onto the skill's directory before it is
// committed, so without this a caller could name another skill's files, or the
// repository's own configuration, and have the result staged on their branch.
func cleanSkillRelPath(p string) (string, error) {
	reject := func(why string) error {
		return fmt.Errorf("suggestions: file path %q %s; paths are relative to the skill directory, e.g. \"SKILL.md\" or \"scripts/run.sh\"", p, why)
	}

	switch {
	case p == "":
		return "", reject("is empty")
	case strings.ContainsRune(p, 0):
		return "", reject("contains a null byte")
	case path.IsAbs(p):
		return "", reject("is absolute")
	}

	clean := path.Clean(p)
	for _, part := range strings.Split(clean, "/") {
		switch part {
		case "..":
			return "", reject("escapes the skill directory")
		case ".git":
			return "", reject("is inside a git directory")
		}
	}
	if clean == "." {
		return "", reject("names no file")
	}
	return clean, nil
}

func (s *Service) skillAt(ctx context.Context, skillName string, hash plumbing.Hash) (*skill.Metadata, error) {
	tree, err := s.repo.Tree(hash)
	if err != nil {
		return nil, err
	}

	backend := storage.NewGitTreeBackend(tree)
	dirPrefix := path.Join(s.subPath, skillName)
	keys, err := backend.List(ctx, dirPrefix)
	if err != nil {
		return nil, fmt.Errorf("suggestions: listing %s: %w", dirPrefix, err)
	}

	md, err := skillparse.Load(ctx, backend, s.subPath, skillName, keys)
	if err != nil {
		return nil, err
	}
	md.Commit = hash.String()
	return md, nil
}

// branchName builds the namespaced branch a suggestion lives on.
func branchName(agentID, skillName, suggestionID string) string {
	return fmt.Sprintf("suggestions/%s/%s/%s", agentID, skillName, suggestionID)
}

// parseBranch reverses branchName, reporting ok=false if name doesn't
// follow the suggestions/<agent>/<skill>/<id> convention.
func parseBranch(name string) (agentID, skillName, suggestionID string, ok bool) {
	const prefix = "suggestions/"
	if !strings.HasPrefix(name, prefix) {
		return "", "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(name, prefix), "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// appendTrailers attaches a suggestion's out-of-band metadata to its commit
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
