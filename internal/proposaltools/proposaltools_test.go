package proposaltools

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
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/araghukas/skillset/internal/submit"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func validSkillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\nbody\n"
}

// testRig seeds an origin repo containing one skill and wires it up
// through gitrepo -> proposals -> the MCP tool handlers, with the GitHub
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
	svc := proposals.New(repo, "skills", proposals.DefaultMaxFileContentBytes)

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
			Proposals:           svc,
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

// TestFullProposalWorkflow exercises the entire path an agent takes over
// the real MCP protocol: propose a change, list it, and read it back with
// its diff. This is the end-to-end confirmation that the tool layer, the
// domain layer, and the underlying git repository actually cooperate - the
// individual pieces are covered elsewhere, but only this test proves the
// whole chain works through the protocol these tools are served over.
func TestFullProposalWorkflow(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "propose_change",
		Arguments: map[string]any{
			"skill_name":  "frontend-design",
			"agent_id":    "agent-1",
			"proposal_id": "fix-typo",
			"files": []map[string]any{
				{"file_path": "SKILL.md", "content": validSkillMD("frontend-design", "designs frontends, correctly")},
			},
			"motivating_report_ids": []string{"report-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var proposed ProposeChangeOutput
	decodeStructured(t, res, &proposed)
	if proposed.Deduplicated {
		t.Fatal("first proposal should not be deduplicated")
	}
	branch := proposed.Proposal.Branch
	if branch == "" {
		t.Fatal("proposal has no branch")
	}
	if proposed.Proposal.Corroboration != 1 {
		t.Errorf("Corroboration = %d, want 1", proposed.Proposal.Corroboration)
	}

	// list_proposals should find it, without a diff.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_proposals",
		Arguments: map[string]any{"skill_name": "frontend-design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var list ListProposalsOutput
	decodeStructured(t, res, &list)
	if len(list.Proposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(list.Proposals))
	}
	if list.Proposals[0].Diff != "" {
		t.Error("list_proposals should not include diffs")
	}

	// get_proposal should return the same proposal, with its diff this time.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_proposal",
		Arguments: map[string]any{"branch": branch},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got proposals.Proposal
	decodeStructured(t, res, &got)
	if got.Diff == "" {
		t.Error("get_proposal should include a diff by default")
	}
	if !strings.Contains(got.Diff, "correctly") {
		t.Errorf("diff does not mention the change: %q", got.Diff)
	}
	if len(got.MotivatingReportIDs) != 1 || got.MotivatingReportIDs[0] != "report-1" {
		t.Errorf("MotivatingReportIDs = %v, want [report-1]", got.MotivatingReportIDs)
	}

	// A single agent below the threshold reaches no forge at all: the
	// proposal exists only as a local branch.
	if *rig.ghCalls != 0 {
		t.Errorf("no pull request should have been opened; forge calls = %d", *rig.ghCalls)
	}
}

// TestNoToolOpensPullRequests pins the guarantee the tool surface makes:
// corroboration is the only thing that reaches the forge, so nothing an
// agent can call is allowed to push a branch or open a pull request.
func TestNoToolOpensPullRequests(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "propose_change", "list_proposals", "get_proposal",
			"list_proposal_clusters", "get_skill_at_ref", "get_client_guide":
		default:
			t.Errorf("unexpected tool registered: %q", tool.Name)
		}
	}
}

// TestProposeChangeDeduplicatesIdenticalContent covers the mechanism the
// whole registry exists for: two agents independently arriving at the same
// fix collapse into one proposal with two corroborators, not two competing
// pull requests.
func TestProposeChangeDeduplicatesIdenticalContent(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	fixed := validSkillMD("frontend-design", "the corrected description")

	first, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "propose_change",
		Arguments: map[string]any{
			"skill_name":  "frontend-design",
			"agent_id":    "agent-1",
			"proposal_id": "fix",
			"files":       []map[string]any{{"file_path": "SKILL.md", "content": fixed}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var firstOut ProposeChangeOutput
	decodeStructured(t, first, &firstOut)

	second, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "propose_change",
		Arguments: map[string]any{
			"skill_name":  "frontend-design",
			"agent_id":    "agent-2",
			"proposal_id": "same-fix",
			"files":       []map[string]any{{"file_path": "SKILL.md", "content": fixed}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var secondOut ProposeChangeOutput
	decodeStructured(t, second, &secondOut)

	if !secondOut.Deduplicated {
		t.Fatal("identical content from a second agent should be deduplicated")
	}
	if secondOut.Proposal.Branch != firstOut.Proposal.Branch {
		t.Errorf("deduplication landed on a different branch: %q vs %q",
			secondOut.Proposal.Branch, firstOut.Proposal.Branch)
	}
	if secondOut.Proposal.Corroboration != 2 {
		t.Errorf("Corroboration = %d, want 2", secondOut.Proposal.Corroboration)
	}
}

// TestProposeChangeAutoSubmitsAtThreshold covers the only path to a pull
// request: enough independent corroboration opens one, with nobody asking.
//
// The third agent matters as much as the second. Every call that lands on
// an already-submitted proposal finds it at or above the threshold, so the
// submit path is re-entered repeatedly and must return the existing pull
// request rather than opening another.
func TestProposeChangeAutoSubmitsAtThreshold(t *testing.T) {
	rig := newTestRig(t, true, 2)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	fixed := validSkillMD("frontend-design", "the corrected description")
	for i, agent := range []string{"agent-1", "agent-2", "agent-3"} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "propose_change",
			Arguments: map[string]any{
				"skill_name":  "frontend-design",
				"agent_id":    agent,
				"proposal_id": "fix",
				"files":       []map[string]any{{"file_path": "SKILL.md", "content": fixed}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out ProposeChangeOutput
		decodeStructured(t, res, &out)

		if i == 0 {
			if out.AutoSubmitted != nil {
				t.Fatal("should not auto-submit before the threshold is reached")
			}
			continue
		}
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
// threshold but nothing to submit with. Proposals still commit and still
// corroborate; they just never leave the volume.
func TestAutoSubmitWithoutCredentialsStaysLocal(t *testing.T) {
	rig := newTestRig(t, false, 2)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	fixed := validSkillMD("frontend-design", "the corrected description")
	for _, agent := range []string{"agent-1", "agent-2"} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "propose_change",
			Arguments: map[string]any{
				"skill_name":  "frontend-design",
				"agent_id":    agent,
				"proposal_id": "fix",
				"files":       []map[string]any{{"file_path": "SKILL.md", "content": fixed}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out ProposeChangeOutput
		decodeStructured(t, res, &out)
		if out.AutoSubmitted != nil {
			t.Fatalf("%s: reported a pull request with no credentials configured", agent)
		}
	}
	if *rig.ghCalls != 0 {
		t.Errorf("no pull request should have been opened; forge calls = %d", *rig.ghCalls)
	}
}

func TestGetProposalDiffTruncation(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	big := validSkillMD("frontend-design", strings.Repeat("x", 4096))
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "propose_change",
		Arguments: map[string]any{
			"skill_name":  "frontend-design",
			"agent_id":    "agent-1",
			"proposal_id": "big-change",
			"files":       []map[string]any{{"file_path": "SKILL.md", "content": big}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out ProposeChangeOutput
	decodeStructured(t, res, &out)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_proposal",
		Arguments: map[string]any{"branch": out.Proposal.Branch, "max_diff_bytes": 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got proposals.Proposal
	decodeStructured(t, res, &got)
	if !got.DiffTruncated {
		t.Fatal("expected DiffTruncated to be set for an over-budget diff")
	}
	if len(got.Diff) > 200 {
		t.Errorf("diff was not actually truncated: %d bytes", len(got.Diff))
	}
}

func TestGetProposalOmitDiff(t *testing.T) {
	rig := newTestRig(t, true, 0)
	cs := connect(t, rig.deps)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "propose_change",
		Arguments: map[string]any{
			"skill_name":  "frontend-design",
			"agent_id":    "agent-1",
			"proposal_id": "fix",
			"files":       []map[string]any{{"file_path": "SKILL.md", "content": validSkillMD("frontend-design", "fixed")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out ProposeChangeOutput
	decodeStructured(t, res, &out)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_proposal",
		Arguments: map[string]any{"branch": out.Proposal.Branch, "omit_diff": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got proposals.Proposal
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

	tool, ok := byName["propose_change"]
	if !ok {
		t.Fatal("propose_change not registered")
	}
	if tool.Annotations.ReadOnlyHint {
		t.Error("propose_change writes a commit and must not be marked read-only")
	}
	if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
		t.Error("propose_change should explicitly set DestructiveHint=false (the SDK defaults it to true)")
	}

	for _, name := range []string{"list_proposals", "get_proposal", "list_proposal_clusters", "get_skill_at_ref"} {
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
