//go:build e2e

// Drives one full proposal lifecycle against skillsd-registry's proposal
// tools: propose a change, inspect it, read the skill at that ref, list
// it back, cluster it, and (if submit_proposal is enabled on this
// deployment) submit it and confirm the resulting pull request URL comes
// back.
package verify

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type proposeChangeOutput struct {
	Proposal struct {
		Branch  string `json:"branch"`
		HeadSHA string `json:"head_sha"`
		Diff    string `json:"diff"`
		Commits []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
	} `json:"proposal"`
	Deduplicated bool `json:"deduplicated"`
}

type getProposalOutput struct {
	HeadSHA string `json:"head_sha"`
}

type listProposalsOutput struct {
	Proposals []struct {
		Branch string `json:"branch"`
	} `json:"proposals"`
}

type listClustersOutput struct {
	Clusters []any `json:"clusters"`
}

type submitProposalOutput struct {
	PullRequestURL string `json:"pull_request_url"`
}

// TestFullProposalLifecycle mirrors the old 20_proposal_flow.sh end to
// end: propose, re-fetch, read at ref, list, cluster, submit. Uses a
// timestamp-suffixed proposal_id, same as the original script, so the
// test is safely re-runnable against a live deployment without a
// "nothing changed" or "clean working tree" error from a previous run's
// identical commit.
func TestFullProposalLifecycle(t *testing.T) {
	session := connect(t, registryAddr())

	agentID := "verify-agent"
	proposalID := fmt.Sprintf("verify-%d", time.Now().Unix())
	branch := fmt.Sprintf("proposals/%s/%s/%s", agentID, skillName(), proposalID)

	content := fmt.Sprintf(
		"---\nname: %s\ndescription: A minimal placeholder skill seeded by "+
			"local/gitea-init.sh, edited by local/verify's e2e proposal test at %s "+
			"to exercise the proposal flow.\n---\n\n## When to use this skill\n\n"+
			"This is seed content for local dev only - edited by the verification test.\n",
		skillName(), time.Now().UTC().Format(time.RFC3339))

	var proposedHeadSHA string
	t.Run("propose_change", func(t *testing.T) {
		res := callTool(t, session, "propose_change", map[string]any{
			"skill_name":     skillName(),
			"agent_id":       agentID,
			"proposal_id":    proposalID,
			"commit_message": "verify: edit " + skillName(),
			"files": []map[string]any{
				{"file_path": "SKILL.md", "content": content},
			},
		})
		if res.IsError {
			t.Fatalf("propose_change failed: %s", contentText(res))
		}
		var out proposeChangeOutput
		decodeStructured(t, res, &out)

		if out.Proposal.Branch != branch {
			t.Errorf("proposal branch = %q, want %q", out.Proposal.Branch, branch)
		}
		if out.Deduplicated {
			t.Error("first proposal should not be deduplicated")
		}
		if out.Proposal.Diff == "" {
			t.Error("proposal has an empty diff")
		}
		if len(out.Proposal.Commits) == 0 {
			t.Error("proposal has no commits")
		}
		proposedHeadSHA = out.Proposal.HeadSHA
	})

	t.Run("get_proposal", func(t *testing.T) {
		res := callTool(t, session, "get_proposal", map[string]any{"branch": branch})
		if res.IsError {
			t.Fatalf("get_proposal failed: %s", contentText(res))
		}
		var out getProposalOutput
		decodeStructured(t, res, &out)
		if out.HeadSHA == "" {
			t.Error("get_proposal returned no head_sha")
		}
		if out.HeadSHA != proposedHeadSHA {
			t.Errorf("get_proposal head_sha = %q, want %q (from propose_change)", out.HeadSHA, proposedHeadSHA)
		}
	})

	t.Run("get_skill_at_ref", func(t *testing.T) {
		res := callTool(t, session, "get_skill_at_ref", map[string]any{
			"skill_name": skillName(),
			"ref":        branch,
		})
		if res.IsError {
			t.Fatalf("get_skill_at_ref failed: %s", contentText(res))
		}
		if !strings.Contains(contentText(res), "exercise the proposal flow") {
			t.Error("skill at the proposal ref does not carry the edited description")
		}
	})

	t.Run("list_proposals", func(t *testing.T) {
		res := callTool(t, session, "list_proposals", map[string]any{"skill_name": skillName()})
		if res.IsError {
			t.Fatalf("list_proposals failed: %s", contentText(res))
		}
		var out listProposalsOutput
		decodeStructured(t, res, &out)
		found := false
		for _, p := range out.Proposals {
			if p.Branch == branch {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("our proposal (%s) does not appear in list_proposals", branch)
		}
	})

	t.Run("list_proposal_clusters", func(t *testing.T) {
		res := callTool(t, session, "list_proposal_clusters", map[string]any{
			"skill_name":         skillName(),
			"include_singletons": true,
		})
		if res.IsError {
			t.Fatalf("list_proposal_clusters failed: %s", contentText(res))
		}
		var out listClustersOutput
		decodeStructured(t, res, &out)
		if len(out.Clusters) == 0 {
			t.Error("expected at least one cluster (include_singletons=true)")
		}
	})

	// submit_proposal - only if this deployment allows it. A disabled
	// registry is a legitimate deployment shape (propose-only), not a test
	// failure, so this is a skip, not a fail; anything else that goes
	// wrong here is a real failure.
	t.Run("submit_proposal", func(t *testing.T) {
		res := callTool(t, session, "submit_proposal", map[string]any{"branch": branch})
		if res.IsError {
			if strings.Contains(strings.ToLower(contentText(res)), "disabled") {
				t.Skip("submit_proposal is disabled on this deployment (registry.submitProposalEnabled=false or no GitHub auth configured)")
			}
			t.Fatalf("submit_proposal failed unexpectedly: %s", contentText(res))
		}
		var out submitProposalOutput
		decodeStructured(t, res, &out)
		if out.PullRequestURL == "" {
			t.Fatal("submit_proposal returned no pull_request_url")
		}

		// Best-effort reachability check - not fatal, since the PR host may
		// be behind auth or a different network namespace from this shell.
		resp, err := http.Get(out.PullRequestURL)
		if err != nil {
			t.Logf("pull request URL returned but not reachable from this shell "+
				"(expected if it's behind auth or a different network namespace): %s (%v)", out.PullRequestURL, err)
			return
		}
		defer resp.Body.Close()
		t.Logf("pull request URL is reachable (%s): status %d", out.PullRequestURL, resp.StatusCode)
	})
}
