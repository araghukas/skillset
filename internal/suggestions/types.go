package suggestions

import "time"

// FileEdit is one file's full new content within a suggestion.
//
// Complete file contents are the form a suggestion is recorded in: a patch
// supplied instead is expanded into these before anything else runs, so
// the commit and clustering both see one kind of request. It
// converts to a gitrepo.FileChange on the way to a commit; the two are
// deliberately separate types because this one is agent-supplied and relative
// to the skill directory, while gitrepo's is repo-relative.
type FileEdit struct {
	// FilePath is relative to the skill directory, e.g. "scripts/run.sh".
	FilePath string `json:"file_path"`

	// Deleted removes the file; Content is ignored when it is set.
	Deleted bool `json:"deleted,omitempty"`

	// Content is the full new file content, not a patch.
	Content string `json:"content,omitempty"`
}

// Commit is one commit on a suggestion branch.
type Commit struct {
	SHA        string    `json:"sha"`
	Message    string    `json:"message"`
	Author     string    `json:"author"`
	AuthoredAt time.Time `json:"authored_at,omitzero"`
}

// Endorsement records that an agent read a suggestion's diff and would
// approve it exactly as it stands.
//
// An endorsement is a judgment, not a computation: the endorsing agent
// decided the suggestion already says what it would have said. What stays
// mechanical is the counting - one endorsement per agent per suggestion,
// pinned to the commit the agent actually reviewed.
//
// Endorsements live in git as refs under refs/endorsements/, not in a
// database, so they survive exactly as long as the repository does.
type Endorsement struct {
	AgentID string `json:"agent_id"`

	// EndorsedSHA is the suggestion head at the moment of endorsement.
	EndorsedSHA string `json:"endorsed_sha"`

	// Stale is true if the suggestion has advanced since. Stale endorsements
	// do not count toward the auto-submit threshold.
	Stale bool `json:"stale,omitempty"`

	EndorsedAt time.Time `json:"endorsed_at,omitzero"`
}

// Suggestion is a single agent's line of edits to one skill, tracked as a
// branch in the underlying git repository.
type Suggestion struct {
	// SuggestionID is a caller-chosen slug, unique per (AgentID, SkillName).
	SuggestionID string `json:"suggestion_id"`

	// Branch is the full branch name: suggestions/<agent>/<skill>/<id>.
	Branch string `json:"branch"`

	SkillName string `json:"skill_name" jsonschema:"the skill this suggestion is for"`
	AgentID   string `json:"agent_id"`

	// BaseSHA is the base branch commit this suggestion forked from.
	BaseSHA string `json:"base_sha"`

	// HeadSHA is the current tip of the suggestion branch.
	HeadSHA string `json:"head_sha"`

	// Diff is the unified diff of head against base. Empty on list paths,
	// which omit it deliberately - see Service.ListSuggestions.
	Diff string `json:"diff,omitempty"`

	// DiffTruncated reports that Diff was cut short to fit a size budget.
	// Fetch the suggestion on its own, with a larger budget, for the rest.
	DiffTruncated bool `json:"diff_truncated,omitempty"`

	Commits []Commit `json:"commits,omitempty"`

	// SourceThreadURI points at the agent's uploaded conversation JSON, if
	// one was provided.
	SourceThreadURI string `json:"source_thread_uri,omitempty"`

	UpdatedAt time.Time `json:"updated_at,omitzero"`

	// Endorsements are other agents that reviewed this suggestion and
	// approved it as-is. The suggesting agent is not counted among them.
	Endorsements []Endorsement `json:"endorsements,omitempty"`

	// Corroboration is 1 (the suggesting agent) plus the number of non-stale
	// endorsements: how many agents stand behind this exact content.
	Corroboration int `json:"corroboration"`

	// MotivatingReportIDs are outcome report IDs the suggesting agent cited
	// as the reason for this change, carried in commit trailers.
	MotivatingReportIDs []string `json:"motivating_report_ids,omitempty"`
}

// Cluster is a set of open suggestions for one skill whose diffs touch
// overlapping line ranges of the same files.
//
// Overlap is the signal: two agents rewriting the same passage are almost
// certainly responding to the same defect, even when their wording differs.
// Non-overlapping suggestions are left alone - they're orthogonal edits
// that happen to share a skill.
//
// Clusters are recomputed from branch state on every call. Nothing about
// them is stored, and no judgment is applied: the server measures whether
// independent agents converged on the same region, never whether any of
// their suggestions is good.
type Cluster struct {
	Suggestions []*Suggestion `json:"suggestions"`

	// ContestedPaths are the files more than one suggestion in this cluster
	// modifies, sorted.
	ContestedPaths []string `json:"contested_paths,omitempty"`

	// DistinctAgents counts unique agent IDs across every suggestion in the
	// cluster and their non-stale endorsements.
	DistinctAgents int `json:"distinct_agents"`
}

// Submission is the pull request opened for a suggestion.
type Submission struct {
	PullRequestURL    string `json:"pull_request_url"`
	PullRequestNumber int64  `json:"pull_request_number,omitempty"`
}

// SuggestInput is a request to commit file changes onto a suggestion branch.
type SuggestInput struct {
	SkillName    string
	AgentID      string
	SuggestionID string

	// Patch is a unified diff of the change, expanded into Files against the
	// suggestion branch's tip - or the base branch, for a suggestion that
	// doesn't exist yet. Exactly one of Patch and Files must be set.
	Patch string

	// Files carries whole files, each with its full content: what a new file
	// or a new skill is sent as, having nothing to diff against.
	Files []FileEdit

	// CommitMessage is defaulted from the skill name when empty.
	CommitMessage string

	// SourceThreadURI points at the agent's uploaded conversation JSON.
	SourceThreadURI string

	// MotivatingReportIDs are outcome report IDs that motivated this change.
	// Supplying them is what makes a suggestion arrive carrying its own
	// evidence, so a reviewer can see the failures it claims to fix rather
	// than taking the change on faith. Not validated against the evidence
	// store - the registry runs fine with evidence collection disabled.
	MotivatingReportIDs []string
}

// SuggestResult is the outcome of a RecordSuggestion call.
type SuggestResult struct {
	Suggestion *Suggestion
}
