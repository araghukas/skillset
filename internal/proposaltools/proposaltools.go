// Package proposaltools registers skillsd-registry's write-path MCP tools:
// proposing edits to a skill, inspecting proposals and the clusters they
// fall into, reading a skill at any ref, and submitting a proposal as a
// pull request.
package proposaltools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/araghukas/skillset/internal/clientguide"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/araghukas/skillset/internal/submit"
	"github.com/araghukas/skillset/internal/toolresult"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps is what the proposal tools need to run.
type Deps struct {
	Proposals *proposals.Service
	Submitter *submit.Submitter

	// SubmitEnabled reports whether pull requests can be opened at all. When
	// false, submit_proposal is still registered but refuses - unlike the
	// evidence tools, which are omitted entirely when disabled. The
	// difference is that submission is a configuration gap an operator can
	// close, and an agent that has just built a proposal deserves to be told
	// that rather than left wondering where the tool went.
	SubmitEnabled bool

	// AutoSubmitThreshold is how many independent agents must arrive at
	// identical content before a pull request is opened without anyone
	// asking. Zero disables it.
	AutoSubmitThreshold int

	// DefaultMaxBytes is the context-file byte budget applied when a
	// caller's get_skill_at_ref call doesn't set max_bytes itself. Zero
	// uses toolresult.DefaultMaxBytes.
	DefaultMaxBytes int

	// DefaultMaxDiffBytes is the diff byte budget applied when a caller's
	// get_proposal call doesn't set max_diff_bytes itself. Zero uses
	// toolresult.DefaultMaxDiffBytes.
	DefaultMaxDiffBytes int

	// ClientGuideAppendix is forwarded to clientguide.AddTool - see its docs.
	// Callers should pass the same string given to clientguide.Instructions
	// so the connect-time instructions and get_client_guide agree.
	ClientGuideAppendix string
}

// Add registers the proposal tools on srv.
func Add(srv *mcp.Server, deps Deps) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "propose_change",
		Description: "Propose an edit to a skill. Send the full new content of each changed " +
			"file, not a patch - the server computes the diff. The change lands as a commit " +
			"on your own branch; nothing is merged and no pull request opens unless you call " +
			"submit_proposal. If another agent's open proposal already produces byte-identical " +
			"content, no new branch is created: you are recorded as independently corroborating " +
			"theirs, and it is returned with deduplicated set. Cite report IDs from " +
			"list_outcome_reports in motivating_report_ids so a reviewer can see the failures " +
			"this claims to fix.",
		Annotations: writeTool(true),
	}, proposeChange(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_proposals",
		Description: "List open proposals, optionally filtered by skill or by proposing agent. " +
			"Diffs are omitted here; call get_proposal for one proposal's diff.",
		Annotations: readOnly(),
	}, listProposals(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_proposal",
		Description: "Fetch one proposal by branch name, including its unified diff against the base branch, its commits, and the agents who independently corroborated it.",
		Annotations: readOnly(),
	}, getProposal(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_proposal_clusters",
		Description: "Group a skill's open proposals by whether they edit overlapping regions " +
			"of the same files - competing answers to what is probably the same defect. " +
			"Ordered most-corroborated first, so this is the queue to work from when deciding " +
			"what to review or fix next. Clusters are recomputed on every call and measure only " +
			"whether agents converged, never whether any proposal is good.",
		Annotations: readOnly(),
	}, listClusters(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_skill_at_ref",
		Description: "Read a skill's content as of any git ref - a proposal branch, a commit " +
			"SHA, or the base branch by default. Use it to see what a proposal would actually " +
			"produce, or to read the version a past outcome report refers to.",
		Annotations: readOnly(),
	}, getSkillAtRef(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "submit_proposal",
		Description: "Push a proposal's branch upstream and open a pull request for human " +
			"review. Safe to retry: if a pull request already exists for the branch, the " +
			"existing one is returned rather than a second being opened. This is the last " +
			"step an agent takes - merging is a human decision.",
		Annotations: writeTool(true, idempotent()),
	}, submitProposal(deps))

	clientguide.AddTool(srv, deps.ClientGuideAppendix)
}

func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
}

type annotationOpt func(*mcp.ToolAnnotations)

func idempotent() annotationOpt {
	return func(a *mcp.ToolAnnotations) { a.IdempotentHint = true }
}

// writeTool builds annotations for a mutating tool. DestructiveHint is set
// explicitly because the SDK defaults it to true when left nil, and nothing
// here is destructive: proposing appends a commit to the agent's own
// branch, and submitting opens a pull request. Neither overwrites anything.
func writeTool(openWorld bool, opts ...annotationOpt) *mcp.ToolAnnotations {
	a := &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		OpenWorldHint:   new(openWorld),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// FileEditInput is one file's full new content within a proposal.
type FileEditInput struct {
	FilePath string `json:"file_path" jsonschema:"path relative to the skill directory, e.g. \"SKILL.md\" or \"scripts/run.sh\""`
	Deleted  bool   `json:"deleted,omitempty" jsonschema:"remove this file; content is ignored when set"`
	Content  string `json:"content,omitempty" jsonschema:"the full new content of the file, not a patch"`
}

// ProposeChangeInput is a request to edit a skill.
type ProposeChangeInput struct {
	SkillName           string          `json:"skill_name" jsonschema:"skill to edit, as returned by list_skills"`
	AgentID             string          `json:"agent_id" jsonschema:"stable identifier for you, the calling agent; must not contain \"/\""`
	ProposalID          string          `json:"proposal_id" jsonschema:"short slug naming this line of work, unique per agent and skill, e.g. \"fix-stale-docker-flag\"; must not contain \"/\". Reuse it to add commits to the same proposal"`
	Files               []FileEditInput `json:"files" jsonschema:"the changed files, each with its full new content"`
	CommitMessage       string          `json:"commit_message,omitempty" jsonschema:"commit subject; defaulted from the skill name when omitted"`
	SourceThreadURI     string          `json:"source_thread_uri,omitempty" jsonschema:"pointer to the conversation that produced this change, if you have one"`
	MotivatingReportIDs []string        `json:"motivating_report_ids,omitempty" jsonschema:"report IDs from list_outcome_reports that justify this change; they reach the pull request so a reviewer sees the evidence"`
	AllowDuplicate      bool            `json:"allow_duplicate,omitempty" jsonschema:"force your own branch even if an identical proposal exists. Rarely wanted - corroborating the existing one is almost always better"`
}

// ProposeChangeOutput reports what happened to a proposed change.
type ProposeChangeOutput struct {
	Proposal      *proposals.Proposal   `json:"proposal" jsonschema:"your proposal, or - when deduplicated - the existing one you were recorded as corroborating"`
	Deduplicated  bool                  `json:"deduplicated,omitempty" jsonschema:"an open proposal already produced identical content, so no new branch was created and your contribution was recorded as corroboration of it"`
	AutoSubmitted *proposals.Submission `json:"auto_submitted,omitempty" jsonschema:"set if this call pushed the proposal to the corroboration threshold and a pull request was opened automatically"`
}

func proposeChange(deps Deps) mcp.ToolHandlerFor[ProposeChangeInput, ProposeChangeOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ProposeChangeInput) (*mcp.CallToolResult, ProposeChangeOutput, error) {
		files := make([]proposals.FileEdit, 0, len(in.Files))
		for _, f := range in.Files {
			files = append(files, proposals.FileEdit{
				FilePath: f.FilePath,
				Deleted:  f.Deleted,
				Content:  f.Content,
			})
		}

		res, err := deps.Proposals.ProposeChange(ctx, proposals.ProposeInput{
			SkillName:           in.SkillName,
			AgentID:             in.AgentID,
			ProposalID:          in.ProposalID,
			Files:               files,
			CommitMessage:       in.CommitMessage,
			SourceThreadURI:     in.SourceThreadURI,
			MotivatingReportIDs: in.MotivatingReportIDs,
			AllowDuplicate:      in.AllowDuplicate,
		})
		if err != nil {
			return nil, ProposeChangeOutput{}, err
		}

		return nil, ProposeChangeOutput{
			Proposal:      res.Proposal,
			Deduplicated:  res.Deduplicated,
			AutoSubmitted: maybeAutoSubmit(ctx, deps, res.Proposal),
		}, nil
	}
}

// maybeAutoSubmit opens a pull request if this proposal has now been
// corroborated by enough independent agents.
//
// Failures are logged and swallowed rather than returned: the agent's
// contribution is already committed and corroborated, and turning a forge
// outage into a failed propose_change would discard work that succeeded.
// The proposal remains submittable by hand.
func maybeAutoSubmit(ctx context.Context, deps Deps, p *proposals.Proposal) *proposals.Submission {
	if deps.AutoSubmitThreshold <= 0 || !deps.SubmitEnabled {
		return nil
	}
	if p.Corroboration < deps.AutoSubmitThreshold {
		return nil
	}

	submitted, err := deps.Submitter.Submit(ctx, p, "", "")
	if err != nil {
		slog.Error("auto-submit failed; proposal is still submittable by hand",
			"branch", p.Branch, "corroboration", p.Corroboration, "error", err)
		return nil
	}
	slog.Info("auto-submitted corroborated proposal",
		"branch", p.Branch, "corroboration", p.Corroboration,
		"threshold", deps.AutoSubmitThreshold, "pull_request", submitted.PullRequestURL)
	return submitted
}

// ListProposalsInput filters the proposal listing.
type ListProposalsInput struct {
	SkillName string `json:"skill_name,omitempty" jsonschema:"only proposals for this skill; omit for all skills"`
	AgentID   string `json:"agent_id,omitempty" jsonschema:"only proposals from this agent; omit for all agents"`
}

// ListProposalsOutput is the proposal listing.
type ListProposalsOutput struct {
	Proposals []*proposals.Proposal `json:"proposals" jsonschema:"matching proposals, without their diffs"`
	Total     int                   `json:"total"`
}

func listProposals(deps Deps) mcp.ToolHandlerFor[ListProposalsInput, ListProposalsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListProposalsInput) (*mcp.CallToolResult, ListProposalsOutput, error) {
		list, err := deps.Proposals.ListProposals(ctx, in.SkillName, in.AgentID)
		if err != nil {
			return nil, ListProposalsOutput{}, err
		}
		return nil, ListProposalsOutput{Proposals: list, Total: len(list)}, nil
	}
}

// GetProposalInput selects one proposal.
//
// OmitDiff is phrased negatively on purpose. An optional bool defaults to
// false when the caller leaves it out, so the field has to be named for
// what setting it does, not for the default behavior: get_proposal returns
// the diff unless you ask it not to.
type GetProposalInput struct {
	Branch       string `json:"branch" jsonschema:"fully-qualified branch name, as returned by list_proposals (proposals/<agent>/<skill>/<id>)"`
	OmitDiff     bool   `json:"omit_diff,omitempty" jsonschema:"skip the unified diff when you only need the proposal's metadata and corroboration; the diff is included by default"`
	MaxDiffBytes int    `json:"max_diff_bytes,omitempty" jsonschema:"cap on diff size; omit for 65536. A truncated diff is cut at a hunk boundary and flagged with diff_truncated"`
}

func getProposal(deps Deps) mcp.ToolHandlerFor[GetProposalInput, *proposals.Proposal] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetProposalInput) (*mcp.CallToolResult, *proposals.Proposal, error) {
		if in.Branch == "" {
			return nil, nil, fmt.Errorf("branch is required; call list_proposals to see the open branches")
		}
		p, err := deps.Proposals.GetProposal(ctx, in.Branch)
		if err != nil {
			return nil, nil, err
		}
		if in.OmitDiff {
			p.Diff = ""
		} else {
			maxDiffBytes := in.MaxDiffBytes
			if maxDiffBytes <= 0 {
				maxDiffBytes = deps.DefaultMaxDiffBytes
			}
			p.Diff, p.DiffTruncated = toolresult.TruncateDiff(p.Diff, maxDiffBytes)
		}
		return nil, p, nil
	}
}

// ListClustersInput filters the cluster listing.
type ListClustersInput struct {
	SkillName         string `json:"skill_name,omitempty" jsonschema:"only clusters for this skill; omit for all skills"`
	IncludeSingletons bool   `json:"include_singletons,omitempty" jsonschema:"include clusters of one proposal. Off by default - the point of clustering is to surface contention"`
}

// ListClustersOutput is the cluster listing.
type ListClustersOutput struct {
	Clusters []*proposals.Cluster `json:"clusters" jsonschema:"clusters ordered by distinct_agents descending - the review queue, most-corroborated first"`
	Total    int                  `json:"total"`
}

func listClusters(deps Deps) mcp.ToolHandlerFor[ListClustersInput, ListClustersOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListClustersInput) (*mcp.CallToolResult, ListClustersOutput, error) {
		clusters, err := deps.Proposals.ListClusters(ctx, in.SkillName, in.IncludeSingletons)
		if err != nil {
			return nil, ListClustersOutput{}, err
		}
		return nil, ListClustersOutput{Clusters: clusters, Total: len(clusters)}, nil
	}
}

// GetSkillAtRefInput selects a skill and a revision of it.
type GetSkillAtRefInput struct {
	SkillName           string   `json:"skill_name" jsonschema:"skill to read"`
	Ref                 string   `json:"ref,omitempty" jsonschema:"branch name or commit SHA; omit for the base branch HEAD"`
	IncludeContextFiles bool     `json:"include_context_files,omitempty" jsonschema:"return file content as well as metadata; omit for metadata only"`
	Paths               []string `json:"paths,omitempty" jsonschema:"return only these context files, by path relative to the skill directory; omit for all of them"`
	MaxBytes            int      `json:"max_bytes,omitempty" jsonschema:"cap on total context-file bytes returned; omit for 262144"`
}

func getSkillAtRef(deps Deps) mcp.ToolHandlerFor[GetSkillAtRefInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetSkillAtRefInput) (*mcp.CallToolResult, any, error) {
		if in.SkillName == "" {
			return nil, nil, fmt.Errorf("skill_name is required")
		}
		md, err := deps.Proposals.GetSkillAtRef(ctx, in.SkillName, in.Ref, in.IncludeContextFiles)
		if err != nil {
			return nil, nil, err
		}
		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = deps.DefaultMaxBytes
		}
		return &mcp.CallToolResult{
			Content: toolresult.Skill(md, in.IncludeContextFiles, in.Paths, maxBytes),
		}, nil, nil
	}
}

// SubmitProposalInput selects a proposal to submit.
type SubmitProposalInput struct {
	Branch  string `json:"branch" jsonschema:"fully-qualified branch name, as returned by propose_change or list_proposals"`
	PRTitle string `json:"pr_title,omitempty" jsonschema:"pull request title; defaulted from the proposal when omitted"`
	PRBody  string `json:"pr_body,omitempty" jsonschema:"pull request body; defaulted when omitted, including the corroborating agents and cited outcome reports"`
}

func submitProposal(deps Deps) mcp.ToolHandlerFor[SubmitProposalInput, *proposals.Submission] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SubmitProposalInput) (*mcp.CallToolResult, *proposals.Submission, error) {
		if !deps.SubmitEnabled {
			return nil, nil, fmt.Errorf("submitting proposals is disabled on this registry: it has no forge credentials configured. " +
				"The proposal is still committed on its branch and an operator can submit it")
		}
		if in.Branch == "" {
			return nil, nil, fmt.Errorf("branch is required; call list_proposals to see the open branches")
		}

		p, err := deps.Proposals.GetProposal(ctx, in.Branch)
		if err != nil {
			return nil, nil, err
		}
		submitted, err := deps.Submitter.Submit(ctx, p, in.PRTitle, in.PRBody)
		if err != nil {
			return nil, nil, err
		}
		return nil, submitted, nil
	}
}
