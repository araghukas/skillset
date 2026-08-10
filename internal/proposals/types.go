package proposals

import "time"

// FileEdit is one file's full new content within a proposal.
//
// Proposals carry complete file contents rather than patches: the server
// computes the diff itself, so callers never need to worry about patch
// context lines or applying a patch against a base they may not have in
// sync. It converts to a gitrepo.FileChange on the way to a commit; the two
// are deliberately separate types because this one is agent-supplied and
// relative to the skill directory, while gitrepo's is repo-relative.
type FileEdit struct {
	// FilePath is relative to the skill directory, e.g. "scripts/run.sh".
	FilePath string `json:"file_path"`

	// Deleted removes the file; Content is ignored when it is set.
	Deleted bool `json:"deleted,omitempty"`

	// Content is the full new file content, not a patch.
	Content string `json:"content,omitempty"`
}

// Commit is one commit on a proposal branch.
type Commit struct {
	SHA        string    `json:"sha"`
	Message    string    `json:"message"`
	Author     string    `json:"author"`
	AuthoredAt time.Time `json:"authored_at,omitzero"`
}

// Endorsement records that an agent independently arrived at exactly the
// content a proposal already contains.
//
// There is deliberately no way to endorse a proposal you have merely read
// and agreed with: an endorsement is only meaningful as evidence if it was
// produced without knowledge of the proposal it lands on, so the only way
// to create one is for propose_change to find your content already there.
//
// Endorsements live in git as refs under refs/endorsements/, not in a
// database, so they survive exactly as long as the repository does.
type Endorsement struct {
	AgentID string `json:"agent_id"`

	// EndorsedSHA is the proposal head at the moment of endorsement.
	EndorsedSHA string `json:"endorsed_sha"`

	// Stale is true if the proposal has advanced since. Stale endorsements
	// do not count toward the auto-submit threshold.
	Stale bool `json:"stale,omitempty"`

	EndorsedAt time.Time `json:"endorsed_at,omitzero"`
}

// Proposal is a single agent's line of edits to one skill, tracked as a
// branch in the underlying git repository.
type Proposal struct {
	// ProposalID is a caller-chosen slug, unique per (AgentID, SkillName).
	ProposalID string `json:"proposal_id"`

	// Branch is the full branch name: proposals/<agent>/<skill>/<id>.
	Branch string `json:"branch"`

	SkillName string `json:"skill_name"`
	AgentID   string `json:"agent_id"`

	// BaseSHA is the base branch commit this proposal forked from.
	BaseSHA string `json:"base_sha"`

	// HeadSHA is the current tip of the proposal branch.
	HeadSHA string `json:"head_sha"`

	// Diff is the unified diff of head against base. Empty on list paths,
	// which omit it deliberately - see Service.ListProposals.
	Diff string `json:"diff,omitempty"`

	// DiffTruncated reports that Diff was cut short to fit a size budget.
	// Fetch the proposal on its own, with a larger budget, for the rest.
	DiffTruncated bool `json:"diff_truncated,omitempty"`

	Commits []Commit `json:"commits,omitempty"`

	// SourceThreadURI points at the agent's uploaded conversation JSON, if
	// one was provided.
	SourceThreadURI string `json:"source_thread_uri,omitempty"`

	UpdatedAt time.Time `json:"updated_at,omitzero"`

	// ContentHash is a normalized digest of every file in the skill's
	// directory at HeadSHA. Two proposals with the same ContentHash produce
	// the same skill, whatever route they took to get there; it is the key
	// propose_change deduplicates on.
	ContentHash string `json:"content_hash"`

	// Endorsements are other agents that independently produced this exact
	// content. The proposing agent is not counted among them.
	Endorsements []Endorsement `json:"endorsements,omitempty"`

	// Corroboration is 1 (the proposing agent) plus the number of non-stale
	// endorsements: how many agents independently arrived at this content.
	Corroboration int `json:"corroboration"`

	// MotivatingReportIDs are outcome report IDs the proposing agent cited
	// as the reason for this change, carried in commit trailers.
	MotivatingReportIDs []string `json:"motivating_report_ids,omitempty"`
}

// Cluster is a set of open proposals for one skill whose diffs touch
// overlapping line ranges of the same files.
//
// Overlap is the signal: two agents rewriting the same passage are almost
// certainly responding to the same defect, even when their fixes differ and
// their content hashes don't match. Non-overlapping proposals are left
// alone - they're orthogonal edits that happen to share a skill.
//
// Clusters are recomputed from branch state on every call. Nothing about
// them is stored, and no judgment is applied: the server measures whether
// independent agents converged on the same region, never whether any of
// their proposals is good.
type Cluster struct {
	Proposals []*Proposal `json:"proposals"`

	// ContestedPaths are the files more than one proposal in this cluster
	// modifies, sorted.
	ContestedPaths []string `json:"contested_paths,omitempty"`

	// DistinctAgents counts unique agent IDs across every proposal in the
	// cluster and their non-stale endorsements.
	DistinctAgents int `json:"distinct_agents"`
}

// Submission is the pull request opened for a proposal.
type Submission struct {
	PullRequestURL    string `json:"pull_request_url"`
	PullRequestNumber int64  `json:"pull_request_number,omitempty"`
}

// ProposeInput is a request to commit file changes onto a proposal branch.
type ProposeInput struct {
	SkillName  string
	AgentID    string
	ProposalID string
	Files      []FileEdit

	// CommitMessage is defaulted from the skill name when empty.
	CommitMessage string

	// SourceThreadURI points at the agent's uploaded conversation JSON.
	SourceThreadURI string

	// MotivatingReportIDs are outcome report IDs that motivated this change.
	// Supplying them is what makes a proposal arrive carrying its own
	// evidence, so a reviewer can see the failures it claims to fix rather
	// than taking the change on faith. Not validated against the evidence
	// store - the registry runs fine with evidence collection disabled.
	MotivatingReportIDs []string

	// AllowDuplicate skips the content-hash dedup check and forces a branch
	// of the caller's own even if an identical proposal already exists.
	// Rarely wanted; endorsing the existing proposal is almost always
	// better.
	AllowDuplicate bool
}

// ProposeResult is the outcome of a ProposeChange call: either the caller's
// own proposal, or - when their content already existed - the proposal they
// were recorded as endorsing instead.
type ProposeResult struct {
	Proposal     *Proposal
	Deduplicated bool
}
