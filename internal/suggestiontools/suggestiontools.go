// Package suggestiontools registers skillsd-registry's write-path MCP
// tools: recording suggested edits to a skill, inspecting suggestions and
// the clusters they fall into, and reading a skill at any ref.
//
// No tool here opens a pull request. That happens only when a suggestion
// reaches the corroboration threshold, which the registry evaluates itself -
// see maybeAutoSubmit.
package suggestiontools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/araghukas/skillset/internal/clientguide"
	"github.com/araghukas/skillset/internal/submit"
	"github.com/araghukas/skillset/internal/suggestions"
	"github.com/araghukas/skillset/internal/toolresult"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps is what the suggestion tools need to run.
type Deps struct {
	Suggestions *suggestions.Service
	Submitter   *submit.Submitter

	// SubmitConfigured reports whether a forge credential, owner, and repo
	// are all present. Without them a pull request cannot be opened, so
	// crossing the corroboration threshold has no effect.
	SubmitConfigured bool

	// AutoSubmitThreshold is how many independent agents must arrive at
	// identical content before a pull request is opened for it. Zero means
	// no pull request is ever opened and suggestions stay as local branches.
	AutoSubmitThreshold int

	// DefaultMaxBytes is the context-file byte budget applied when a
	// caller's get_skill_at_ref call doesn't set max_bytes itself. Zero
	// uses toolresult.DefaultMaxBytes.
	DefaultMaxBytes int

	// DefaultMaxDiffBytes is the diff byte budget applied when a caller's
	// get_suggestion call doesn't set max_diff_bytes itself. Zero uses
	// toolresult.DefaultMaxDiffBytes.
	DefaultMaxDiffBytes int

	// ClientGuideAppendix is forwarded to clientguide.AddTool - see its docs.
	// Callers should pass the same string given to clientguide.Instructions
	// so the connect-time instructions and get_client_guide agree.
	ClientGuideAppendix string
}

// Add registers the suggestion tools on srv.
func Add(srv *mcp.Server, deps Deps) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "record_suggestion",
		Description: "Record a suggested edit to a skill. Send the change as a unified diff in " +
			"patch; files, carrying whole file contents, is for new files that have nothing to " +
			"diff against. The change is recorded " +
			"as a commit inside the registry's own internal git store, purely for tracking and " +
			"corroboration; you have no git access to it and nothing is pushed anywhere until " +
			"the registry opens a pull request on its own. If another agent's open suggestion " +
			"already produces byte-identical content, no new branch is created: you are recorded " +
			"as independently corroborating theirs, and it is returned with deduplicated set. " +
			"A pull request opens only once enough agents have independently corroborated the " +
			"same content, which the registry decides on its own. Cite report IDs from " +
			"list_outcome_reports in motivating_report_ids so a reviewer can see the failures " +
			"this claims to fix.",
		Annotations: writeTool(true),
	}, recordSuggestion(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_suggestions",
		Description: "List open suggestions, optionally filtered by skill or by suggesting agent. " +
			"Diffs are omitted here; call get_suggestion for one suggestion's diff.",
		Annotations: readOnly(),
	}, listSuggestions(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_suggestion",
		Description: "Fetch one suggestion by branch name, including its unified diff against the base branch, its commits, and the agents who independently corroborated it.",
		Annotations: readOnly(),
	}, getSuggestion(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_suggestion_clusters",
		Description: "Group a skill's open suggestions by whether they edit overlapping regions " +
			"of the same files - competing answers to what is probably the same defect. " +
			"Ordered most-corroborated first, so this is the queue to work from when deciding " +
			"what to review or fix next. Clusters are recomputed on every call and measure only " +
			"whether agents converged, never whether any suggestion is good.",
		Annotations: readOnly(),
	}, listClusters(deps))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_skill_at_ref",
		Description: "Read a skill's content as of any git ref - a suggestion branch, a commit " +
			"SHA, or the base branch by default. Use it to see what a suggestion would actually " +
			"produce, or to read the version a past outcome report refers to.",
		Annotations: readOnly(),
	}, getSkillAtRef(deps))

	clientguide.AddTool(srv, deps.ClientGuideAppendix)
}

func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
}

// writeTool builds annotations for a mutating tool. DestructiveHint is set
// explicitly because the SDK defaults it to true when left nil, and nothing
// here is destructive: recording a suggestion appends a commit to the
// registry's own internal tracking branch for the agent, overwriting
// nothing.
func writeTool(openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: new(false),
		OpenWorldHint:   new(openWorld),
	}
}

// FileEditInput is one file's full new content within a suggestion.
type FileEditInput struct {
	FilePath string `json:"file_path" jsonschema:"path relative to the skill directory, e.g. \"SKILL.md\" or \"scripts/run.sh\""`
	Deleted  bool   `json:"deleted,omitempty" jsonschema:"remove this file; content is ignored when set"`
	Content  string `json:"content,omitempty" jsonschema:"the full new content of the file, not a patch"`
}

// RecordSuggestionInput is a request to edit a skill.
type RecordSuggestionInput struct {
	SkillName           string          `json:"skill_name" jsonschema:"skill to edit, as returned by list_skills"`
	AgentID             string          `json:"agent_id" jsonschema:"stable identifier for you, the calling agent; must not contain \"/\""`
	SuggestionID        string          `json:"suggestion_id" jsonschema:"short slug naming this line of work, unique per agent and skill, e.g. \"fix-stale-docker-flag\"; must not contain \"/\". Reuse it to add commits to the same suggestion"`
	Patch               string          `json:"patch,omitempty" jsonschema:"a unified diff of your change - how to send an edit to a file that already exists. Write the current file to a scratch path, edit a copy, and diff the two with \"git diff --no-index\" or \"diff -u\". Read the current content from get_skill_at_ref (not get_skill, whose SKILL.md carries a footer the registry does not store), and when revising your own suggestion, read it at your suggestion branch - that is what the patch applies to. Renames, mode changes and binary files are not supported"`
	Files               []FileEditInput `json:"files,omitempty" jsonschema:"whole files, each with its full content - for new files, which have nothing to diff against, and for creating a skill. Mutually exclusive with patch, so an edit to an existing file goes in patch instead"`
	CommitMessage       string          `json:"commit_message,omitempty" jsonschema:"commit subject; defaulted from the skill name when omitted"`
	SourceThreadURI     string          `json:"source_thread_uri,omitempty" jsonschema:"pointer to the conversation that produced this change, if you have one"`
	MotivatingReportIDs []string        `json:"motivating_report_ids,omitempty" jsonschema:"report IDs from list_outcome_reports that justify this change; they reach the pull request so a reviewer sees the evidence"`
	AllowDuplicate      bool            `json:"allow_duplicate,omitempty" jsonschema:"force your own branch even if an identical suggestion exists. Rarely wanted - corroborating the existing one is almost always better"`
}

// RecordSuggestionOutput reports what happened to a recorded suggestion.
type RecordSuggestionOutput struct {
	Suggestion    *suggestions.Suggestion `json:"suggestion" jsonschema:"your suggestion, or - when deduplicated - the existing one you were recorded as corroborating"`
	Deduplicated  bool                    `json:"deduplicated,omitempty" jsonschema:"an open suggestion already produced identical content, so no new branch was created and your contribution was recorded as corroboration of it"`
	AutoSubmitted *suggestions.Submission `json:"auto_submitted,omitempty" jsonschema:"set if this call pushed the suggestion to the corroboration threshold and a pull request was opened automatically"`
}

func recordSuggestion(deps Deps) mcp.ToolHandlerFor[RecordSuggestionInput, RecordSuggestionOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RecordSuggestionInput) (*mcp.CallToolResult, RecordSuggestionOutput, error) {
		files := make([]suggestions.FileEdit, 0, len(in.Files))
		for _, f := range in.Files {
			files = append(files, suggestions.FileEdit{
				FilePath: f.FilePath,
				Deleted:  f.Deleted,
				Content:  f.Content,
			})
		}

		res, err := deps.Suggestions.RecordSuggestion(ctx, suggestions.SuggestInput{
			SkillName:           in.SkillName,
			AgentID:             in.AgentID,
			SuggestionID:        in.SuggestionID,
			Files:               files,
			Patch:               in.Patch,
			CommitMessage:       in.CommitMessage,
			SourceThreadURI:     in.SourceThreadURI,
			MotivatingReportIDs: in.MotivatingReportIDs,
			AllowDuplicate:      in.AllowDuplicate,
		})
		if err != nil {
			return nil, RecordSuggestionOutput{}, err
		}

		return nil, RecordSuggestionOutput{
			Suggestion:    res.Suggestion,
			Deduplicated:  res.Deduplicated,
			AutoSubmitted: maybeAutoSubmit(ctx, deps, res.Suggestion),
		}, nil
	}
}

// maybeAutoSubmit opens a pull request if this suggestion has now been
// corroborated by enough independent agents. This is the only path to a pull
// request: no caller can ask for one.
//
// Failures are logged and swallowed rather than returned: the agent's
// contribution is already committed and corroborated, and turning a forge
// outage into a failed record_suggestion would discard work that succeeded.
// The suggestion stays on its branch and the next call that finds it at or
// above the threshold retries.
func maybeAutoSubmit(ctx context.Context, deps Deps, sg *suggestions.Suggestion) *suggestions.Submission {
	if deps.AutoSubmitThreshold <= 0 || !deps.SubmitConfigured {
		return nil
	}
	if sg.Corroboration < deps.AutoSubmitThreshold {
		return nil
	}

	submitted, err := deps.Submitter.Submit(ctx, sg)
	if err != nil {
		slog.Error("auto-submit failed; suggestion remains on its branch and will be retried",
			"branch", sg.Branch, "corroboration", sg.Corroboration, "error", err)
		return nil
	}
	slog.Info("auto-submitted corroborated suggestion",
		"branch", sg.Branch, "corroboration", sg.Corroboration,
		"threshold", deps.AutoSubmitThreshold, "pull_request", submitted.PullRequestURL)
	return submitted
}

// ListSuggestionsInput filters the suggestion listing.
type ListSuggestionsInput struct {
	SkillName string `json:"skill_name,omitempty" jsonschema:"only suggestions for this skill; omit for all skills"`
	AgentID   string `json:"agent_id,omitempty" jsonschema:"only suggestions from this agent; omit for all agents"`
}

// ListSuggestionsOutput is the suggestion listing.
type ListSuggestionsOutput struct {
	Suggestions []*suggestions.Suggestion `json:"suggestions" jsonschema:"matching suggestions, without their diffs"`
	Total       int                       `json:"total"`
}

func listSuggestions(deps Deps) mcp.ToolHandlerFor[ListSuggestionsInput, ListSuggestionsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListSuggestionsInput) (*mcp.CallToolResult, ListSuggestionsOutput, error) {
		list, err := deps.Suggestions.ListSuggestions(ctx, in.SkillName, in.AgentID)
		if err != nil {
			return nil, ListSuggestionsOutput{}, err
		}
		return nil, ListSuggestionsOutput{Suggestions: list, Total: len(list)}, nil
	}
}

// GetSuggestionInput selects one suggestion.
//
// OmitDiff is phrased negatively on purpose. An optional bool defaults to
// false when the caller leaves it out, so the field has to be named for
// what setting it does, not for the default behavior: get_suggestion
// returns the diff unless you ask it not to.
type GetSuggestionInput struct {
	Branch       string `json:"branch" jsonschema:"fully-qualified branch name, as returned by list_suggestions (suggestions/<agent>/<skill>/<id>)"`
	OmitDiff     bool   `json:"omit_diff,omitempty" jsonschema:"skip the unified diff when you only need the suggestion's metadata and corroboration; the diff is included by default"`
	MaxDiffBytes int    `json:"max_diff_bytes,omitempty" jsonschema:"cap on diff size; omit for 65536. A truncated diff is cut at a hunk boundary and flagged with diff_truncated"`
}

func getSuggestion(deps Deps) mcp.ToolHandlerFor[GetSuggestionInput, *suggestions.Suggestion] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetSuggestionInput) (*mcp.CallToolResult, *suggestions.Suggestion, error) {
		if in.Branch == "" {
			return nil, nil, fmt.Errorf("branch is required; call list_suggestions to see the open branches")
		}
		sg, err := deps.Suggestions.GetSuggestion(ctx, in.Branch)
		if err != nil {
			return nil, nil, err
		}
		if in.OmitDiff {
			sg.Diff = ""
		} else {
			maxDiffBytes := in.MaxDiffBytes
			if maxDiffBytes <= 0 {
				maxDiffBytes = deps.DefaultMaxDiffBytes
			}
			sg.Diff, sg.DiffTruncated = toolresult.TruncateDiff(sg.Diff, maxDiffBytes)
		}
		return nil, sg, nil
	}
}

// ListClustersInput filters the cluster listing.
type ListClustersInput struct {
	SkillName         string `json:"skill_name,omitempty" jsonschema:"only clusters for this skill; omit for all skills"`
	IncludeSingletons bool   `json:"include_singletons,omitempty" jsonschema:"include clusters of one suggestion. Off by default - the point of clustering is to surface contention"`
}

// ListClustersOutput is the cluster listing.
type ListClustersOutput struct {
	Clusters []*suggestions.Cluster `json:"clusters" jsonschema:"clusters ordered by distinct_agents descending - the review queue, most-corroborated first"`
	Total    int                    `json:"total"`
}

func listClusters(deps Deps) mcp.ToolHandlerFor[ListClustersInput, ListClustersOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListClustersInput) (*mcp.CallToolResult, ListClustersOutput, error) {
		clusters, err := deps.Suggestions.ListClusters(ctx, in.SkillName, in.IncludeSingletons)
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
		md, err := deps.Suggestions.GetSkillAtRef(ctx, in.SkillName, in.Ref, in.IncludeContextFiles)
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
