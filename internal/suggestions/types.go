package suggestions

import "time"

// FileEdit is one file's full new content within a suggestion.
//
// Suggestions carry complete file contents rather than patches: the server
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

// Commit is one commit on a suggestion branch.
type Commit struct {
	SHA        string    `json:"sha"`
	Message    string    `json:"message"`
	Author     string    `json:"author"`
	AuthoredAt time.Time `json:"authored_at,omitzero"`
}

// Endorsement records that an agent independently arrived at exactly the
// content a suggestion already contains.
//
// There is deliberately no way to endorse a suggestion you have merely read
// and agreed with: an endorsement is only meaningful as evidence if it was
// produced without knowledge of the suggestion it lands on, so the only way
// to create one is for record_suggestion to find your content already there.
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

	// ContentHash is a normalized digest of every file in the skill's
	// directory at HeadSHA. Two suggestions with the same ContentHash produce
	// the same skill, whatever route they took to get there; it is the key
	// record_suggestion deduplicates on.
	ContentHash string `json:"content_hash"`

	// Endorsements are other agents that independently produced this exact
	// content. The suggesting agent is not counted among them.
	Endorsements []Endorsement `json:"endorsements,omitempty"`

	// Corroboration is 1 (the suggesting agent) plus the number of non-stale
	// endorsements: how many agents independently arrived at this content.
	Corroboration int `json:"corroboration"`

	// MotivatingReportIDs are outcome report IDs the suggesting agent cited
	// as the reason for this change, carried in commit trailers.
	MotivatingReportIDs []string `json:"motivating_report_ids,omitempty"`
}

// Cluster is a set of open suggestions for one skill whose diffs touch
// overlapping line ranges of the same files.
//
// Overlap is the signal: two agents rewriting the same passage are almost
// certainly responding to the same defect, even when their fixes differ and
// their content hashes don't match. Non-overlapping suggestions are left
// alone - they're orthogonal edits that happen to share a skill.
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
	Files        []FileEdit

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

	// AllowDuplicate skips the content-hash dedup check and forces a branch
	// of the caller's own even if an identical suggestion already exists.
	// Rarely wanted; endorsing the existing suggestion is almost always
	// better.
	AllowDuplicate bool
}

// SuggestResult is the outcome of a RecordSuggestion call: either the
// caller's own suggestion, or - when their content already existed - the
// suggestion they were recorded as endorsing instead.
type SuggestResult struct {
	Suggestion   *Suggestion
	Deduplicated bool
}
