package suggestiontools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/araghukas/skillset/internal/githubauth"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/submit"
	"github.com/araghukas/skillset/internal/suggestions"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func validSkillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\nbody\n"
}

// testRig seeds an origin repo containing one skill and wires it up
// through gitrepo -> suggestions -> the MCP tool handlers, with the GitHub
// client pointed at a stub server that always succeeds.
type testRig struct {
	deps    Deps
	ghCalls *int
}

func newTestRig(t *testing.T, submitConfigured bool, autoSubmitThreshold int) testRig {
	t.Helper()

	seedDir := t.TempDir()
	seed, err := git.PlainInit(seedDir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join("skills", "frontend-design", "SKILL.md")
	full := filepath.Join(seedDir, skillPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(validSkillMD("frontend-design", "designs frontends")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(skillPath); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "seed", Email: "seed@example.com"}
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}
	head, err := seed.Head()
	if err != nil {
		t.Fatal(err)
	}
	branch := head.Name().Short()

	originDir := t.TempDir()
	if _, err := git.PlainClone(originDir, true, &git.CloneOptions{URL: seedDir}); err != nil {
		t.Fatal(err)
	}

	repo, err := gitrepo.Open(context.Background(), t.TempDir(), originDir, branch, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := suggestions.New(repo, "skills", suggestions.DefaultMaxFileContentBytes)

	calls := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		_ = body
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url": "https://github.com/acme/skills/pull/7", "number": 7}`))
	}))
	t.Cleanup(gh.Close)

	client := githubpr.New(gh.URL, "acme", "skills", githubauth.Static("test-token"))

	return testRig{
		deps: Deps{
			Suggestions:         svc,
			Submitter:           submit.New(svc, client, branch),
			SubmitConfigured:    submitConfigured,
			AutoSubmitThreshold: autoSubmitThreshold,
		},
		ghCalls: &calls,
	}
}

func connect(t *testing.T, deps Deps) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "skillsd-registry", Version: "test"}, nil)
	Add(srv, deps)

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Wait()
	})
	return cs
}

func contentText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, into any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool call failed: %s", contentText(t, res))
	}
	if res.StructuredContent == nil {
		t.Fatal("result carries no structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

// TestFullSuggestionWorkflow exercises the entire path an agent takes over
// the real MCP protocol: record a suggestion, list it, and read it back with
// its diff. This is the end-to-end confirmation that the tool layer, the
// domain layer, and the underlying git repository actually cooperate - the
// individual pieces are covered elsewhere, but only this test proves the
// whole chain works through the protocol these tools are served over.
func TestFullSuggestionWorkflow(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "record_suggestion",
		Arguments: map[string]any{
			"skill_name":    "frontend-design",
			"agent_id":      "agent-1",
			"suggestion_id": "fix-typo",
			"files": []map[string]any{
				{"file_path": "SKILL.md", "content": validSkillMD("frontend-design", "designs frontends, correctly")},
			},
			"motivating_report_ids": []string{"report-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var recorded RecordSuggestionOutput
	decodeStructured(t, res, &recorded)
	branch := recorded.Suggestion.Branch
	if branch == "" {
		t.Fatal("suggestion has no branch")
	}
	if recorded.Suggestion.Corroboration != 1 {
		t.Errorf("Corroboration = %d, want 1", recorded.Suggestion.Corroboration)
	}

	// list_suggestions should find it, without a diff.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_suggestions",
		Arguments: map[string]any{"skill_name": "frontend-design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var list ListSuggestionsOutput
	decodeStructured(t, res, &list)
	if len(list.Suggestions) != 1 {
		t.Fatalf("expected 1 suggestion, got %d", len(list.Suggestions))
	}
	if list.Suggestions[0].Diff != "" {
		t.Error("list_suggestions should not include diffs")
	}

	// get_suggestion should return the same suggestion, with its diff this time.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_suggestion",
		Arguments: map[string]any{"branch": branch},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got suggestions.Suggestion
	decodeStructured(t, res, &got)
	if got.Diff == "" {
		t.Error("get_suggestion should include a diff by default")
	}
	if !strings.Contains(got.Diff, "correctly") {
		t.Errorf("diff does not mention the change: %q", got.Diff)
	}
	if len(got.MotivatingReportIDs) != 1 || got.MotivatingReportIDs[0] != "report-1" {
		t.Errorf("MotivatingReportIDs = %v, want [report-1]", got.MotivatingReportIDs)
	}

	// A single agent below the threshold reaches no forge at all: the
	// suggestion exists only as a local branch.
	if *rig.ghCalls != 0 {
		t.Errorf("no pull request should have been opened; forge calls = %d", *rig.ghCalls)
	}
}

// TestNoToolOpensPullRequests pins the guarantee the tool surface makes:
// the endorsement threshold is the only thing that reaches the forge, so
// nothing an agent can call is allowed to ask for a pull request directly.
func TestNoToolOpensPullRequests(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "record_suggestion", "endorse_suggestion", "list_suggestions",
			"get_suggestion", "list_suggestion_clusters", "get_skill_at_ref",
			"get_client_guide":
		default:
			t.Errorf("unexpected tool registered: %q", tool.Name)
		}
	}
}

// record is a helper for the endorsement scenarios below: one agent's
// suggestion of content, returning the tool's structured output.
func record(t *testing.T, cs *mcp.ClientSession, agent, suggestionID, content string) RecordSuggestionOutput {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "record_suggestion",
		Arguments: map[string]any{
			"skill_name":    "frontend-design",
			"agent_id":      agent,
			"suggestion_id": suggestionID,
			"files":         []map[string]any{{"file_path": "SKILL.md", "content": content}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out RecordSuggestionOutput
	decodeStructured(t, res, &out)
	return out
}

// endorse calls endorse_suggestion and returns the raw result, leaving
// error-result assertions to the caller.
func endorse(t *testing.T, cs *mcp.ClientSession, branch, agent, headSHA string) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "endorse_suggestion",
		Arguments: map[string]any{
			"branch":   branch,
			"agent_id": agent,
			"head_sha": headSHA,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestEndorseSuggestionRaisesCorroboration covers the mechanism the registry
// now runs on: a second agent that read the diff and approved it as-is
// raises the suggestion's corroboration instead of filing a near-duplicate.
func TestEndorseSuggestionRaisesCorroboration(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)

	first := record(t, cs, "agent-1", "fix", validSkillMD("frontend-design", "the corrected description"))

	var out EndorseSuggestionOutput
	decodeStructured(t, endorse(t, cs, first.Suggestion.Branch, "agent-2", first.Suggestion.HeadSHA), &out)

	if out.Suggestion.Branch != first.Suggestion.Branch {
		t.Errorf("endorsement landed on a different branch: %q vs %q",
			out.Suggestion.Branch, first.Suggestion.Branch)
	}
	if out.Suggestion.Corroboration != 2 {
		t.Errorf("Corroboration = %d, want 2", out.Suggestion.Corroboration)
	}
}

// TestEndorseSuggestionGuards pins the two refusals that keep an endorsement
// honest: you cannot endorse your own suggestion, and you cannot endorse a
// head you did not read.
func TestEndorseSuggestionGuards(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)

	first := record(t, cs, "agent-1", "fix", validSkillMD("frontend-design", "fixed"))

	if res := endorse(t, cs, first.Suggestion.Branch, "agent-1", first.Suggestion.HeadSHA); !res.IsError {
		t.Error("self-endorsement was accepted")
	}

	// The suggestion advances; the old head is no longer endorsable.
	staleHead := first.Suggestion.HeadSHA
	record(t, cs, "agent-1", "fix", validSkillMD("frontend-design", "revised"))
	if res := endorse(t, cs, first.Suggestion.Branch, "agent-2", staleHead); !res.IsError {
		t.Error("an endorsement of a superseded head was accepted")
	}
}

// TestEndorsementAutoSubmitsAtThreshold covers the only path to a pull
// request: enough agents standing behind one suggestion opens one, with
// nobody asking.
//
// The third agent matters as much as the second. Every endorsement that
// lands on an already-submitted suggestion finds it at or above the
// threshold, so the submit path is re-entered repeatedly and must return
// the existing pull request rather than opening another.
func TestEndorsementAutoSubmitsAtThreshold(t *testing.T) {
	rig := newTestRig(t, true, 2)
	cs := connect(t, rig.deps)

	first := record(t, cs, "agent-1", "fix", validSkillMD("frontend-design", "the corrected description"))
	if first.AutoSubmitted != nil {
		t.Fatal("should not auto-submit before the threshold is reached")
	}

	for _, agent := range []string{"agent-2", "agent-3"} {
		var out EndorseSuggestionOutput
		decodeStructured(t, endorse(t, cs, first.Suggestion.Branch, agent, first.Suggestion.HeadSHA), &out)
		if out.AutoSubmitted == nil {
			t.Fatalf("%s: should auto-submit once corroboration reaches the threshold", agent)
		}
		if out.AutoSubmitted.PullRequestURL == "" {
			t.Errorf("%s: auto-submitted response has no pull request URL", agent)
		}
	}
	if *rig.ghCalls != 1 {
		t.Errorf("expected exactly 1 pull request opened, got %d calls", *rig.ghCalls)
	}
}

// TestAutoSubmitWithoutCredentialsStaysLocal covers a registry that has a
// threshold but nothing to submit with. Suggestions still commit and still
// gather endorsements; they just never leave the volume.
func TestAutoSubmitWithoutCredentialsStaysLocal(t *testing.T) {
	rig := newTestRig(t, false, 2)
	cs := connect(t, rig.deps)

	first := record(t, cs, "agent-1", "fix", validSkillMD("frontend-design", "the corrected description"))

	var out EndorseSuggestionOutput
	decodeStructured(t, endorse(t, cs, first.Suggestion.Branch, "agent-2", first.Suggestion.HeadSHA), &out)
	if out.AutoSubmitted != nil {
		t.Fatal("reported a pull request with no credentials configured")
	}
	if *rig.ghCalls != 0 {
		t.Errorf("no pull request should have been opened; forge calls = %d", *rig.ghCalls)
	}
}

func TestGetSuggestionDiffTruncation(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	big := validSkillMD("frontend-design", strings.Repeat("x", 4096))
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "record_suggestion",
		Arguments: map[string]any{
			"skill_name":    "frontend-design",
			"agent_id":      "agent-1",
			"suggestion_id": "big-change",
			"files":         []map[string]any{{"file_path": "SKILL.md", "content": big}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out RecordSuggestionOutput
	decodeStructured(t, res, &out)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_suggestion",
		Arguments: map[string]any{"branch": out.Suggestion.Branch, "max_diff_bytes": 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got suggestions.Suggestion
	decodeStructured(t, res, &got)
	if !got.DiffTruncated {
		t.Fatal("expected DiffTruncated to be set for an over-budget diff")
	}
	if len(got.Diff) > 200 {
		t.Errorf("diff was not actually truncated: %d bytes", len(got.Diff))
	}
}

func TestGetSuggestionOmitDiff(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "record_suggestion",
		Arguments: map[string]any{
			"skill_name":    "frontend-design",
			"agent_id":      "agent-1",
			"suggestion_id": "fix",
			"files":         []map[string]any{{"file_path": "SKILL.md", "content": validSkillMD("frontend-design", "fixed")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out RecordSuggestionOutput
	decodeStructured(t, res, &out)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_suggestion",
		Arguments: map[string]any{"branch": out.Suggestion.Branch, "omit_diff": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got suggestions.Suggestion
	decodeStructured(t, res, &got)
	if got.Diff != "" {
		t.Errorf("omit_diff should leave the diff empty, got %d bytes", len(got.Diff))
	}
}

func TestGetSkillAtRefBaseBranch(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_skill_at_ref",
		Arguments: map[string]any{"skill_name": "frontend-design", "include_context_files": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("get_skill_at_ref failed: %s", contentText(t, res))
	}
	if !strings.Contains(contentText(t, res), "designs frontends") {
		t.Error("get_skill_at_ref did not return the skill's content")
	}
}

func TestGetSkillAtRefUnknownSkillIsToolError(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_skill_at_ref",
		Arguments: map[string]any{"skill_name": "does-not-exist"},
	})
	if err != nil {
		t.Fatalf("unknown skill produced a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("unknown skill was accepted")
	}
}

func TestToolAnnotationsMatchTheirGuarantees(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}

	for _, name := range []string{"record_suggestion", "endorse_suggestion"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if tool.Annotations.ReadOnlyHint {
			t.Errorf("%s writes to the repository and must not be marked read-only", name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Errorf("%s should explicitly set DestructiveHint=false (the SDK defaults it to true)", name)
		}
	}

	for _, name := range []string{"list_suggestions", "get_suggestion", "list_suggestion_clusters", "get_skill_at_ref"} {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("%s not registered", name)
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s should be read-only", name)
		}
	}
}

// TestRecordSuggestionAcceptsPatch confirms a unified diff survives the
// protocol boundary and lands as an ordinary suggestion.
func TestRecordSuggestionAcceptsPatch(t *testing.T) {
	rig := newTestRig(t, false, 0)
	cs := connect(t, rig.deps)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "record_suggestion",
		Arguments: map[string]any{
			"skill_name":    "frontend-design",
			"agent_id":      "agent-1",
			"suggestion_id": "fix-description",
			"patch": `--- a/SKILL.md
+++ b/SKILL.md
@@ -1,5 +1,5 @@
 ---
 name: frontend-design
-description: designs frontends
+description: designs frontends, correctly
 ---
 body
`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var recorded RecordSuggestionOutput
	decodeStructured(t, res, &recorded)
	if recorded.Suggestion.Branch != "suggestions/agent-1/frontend-design/fix-description" {
		t.Fatalf("unexpected branch %q", recorded.Suggestion.Branch)
	}
	if !strings.Contains(recorded.Suggestion.Diff, "+description: designs frontends, correctly") {
		t.Errorf("diff does not carry the patched line:\n%s", recorded.Suggestion.Diff)
	}
}

// TestRecordSuggestionPatchErrorReachesAgent: a rejected patch is only useful
// if the reason survives to the caller, so assert what the agent actually
// reads back.
func TestRecordSuggestionPatchErrorReachesAgent(t *testing.T) {
	rig := newTestRig(t, false, 0)
	cs := connect(t, rig.deps)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "record_suggestion",
		Arguments: map[string]any{
			"skill_name":    "frontend-design",
			"agent_id":      "agent-1",
			"suggestion_id": "stale",
			"patch": `--- a/SKILL.md
+++ b/SKILL.md
@@ -1,5 +1,5 @@
 ---
 name: frontend-design
-description: something the skill never said
+description: designs frontends, correctly
 ---
 body
`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected an error result for a patch that does not apply")
	}

	text := contentText(t, res)
	for _, want := range []string{"SKILL.md", "hunk #1", "but found", "get_skill_at_ref"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text is missing %q:\n%s", want, text)
		}
	}
}
