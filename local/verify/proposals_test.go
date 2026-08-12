//go:build e2e

// Drives one full proposal lifecycle against skillsd-registry's proposal
// tools: propose a change, inspect it, read the skill at that ref, list it
// back, and cluster it. A second test drives enough independent agents at
// identical content to cross the deployment's corroboration threshold and
// confirms the pull request the registry opens on its own.
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
		Branch        string `json:"branch"`
		HeadSHA       string `json:"head_sha"`
		Diff          string `json:"diff"`
		Corroboration int    `json:"corroboration"`
		Commits       []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
	} `json:"proposal"`
	Deduplicated  bool `json:"deduplicated"`
	AutoSubmitted *struct {
		PullRequestURL string `json:"pull_request_url"`
	} `json:"auto_submitted"`
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

// TestFullProposalLifecycle exercises the full proposal flow end to
// end: propose, re-fetch, read at ref, list, cluster. Uses a
// timestamp-suffixed proposal_id so the test is safely re-runnable
// against a live deployment without a "nothing changed" or "clean
// working tree" error from a previous run's identical commit.
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

}

// maxCorroboratingAgents bounds TestAutoSubmitAtThreshold. The threshold is
// a server-side setting no tool exposes, so the test discovers it by adding
// agents until a pull request appears. The bound keeps a deployment with a
// deliberately high threshold - or none configured at all - from turning
// into an unbounded loop.
const maxCorroboratingAgents = 5

// TestAutoSubmitAtThreshold drives identical content from a series of
// distinct agents until the registry opens a pull request on its own. This
// is the only path to a pull request, so it is the only place this harness
// can observe one.
//
// A deployment that never crosses the threshold within the bound is a
// legitimate shape - no credential configured, or a threshold set higher
// than this test is willing to drive - so that is a skip, not a failure.
func TestAutoSubmitAtThreshold(t *testing.T) {
	session := connect(t, registryAddr())

	stamp := time.Now().Unix()
	content := fmt.Sprintf(
		"---\nname: %s\ndescription: A minimal placeholder skill seeded by "+
			"local/gitea-init.sh, edited by local/verify's auto-submit test at %s "+
			"to exercise the corroboration threshold.\n---\n\n## When to use this skill\n\n"+
			"This is seed content for local dev only - edited by the verification test.\n",
		skillName(), time.Now().UTC().Format(time.RFC3339))

	var prURL string
	for i := 1; i <= maxCorroboratingAgents && prURL == ""; i++ {
		agentID := fmt.Sprintf("verify-corroborator-%d", i)
		res := callTool(t, session, "propose_change", map[string]any{
			"skill_name":     skillName(),
			"agent_id":       agentID,
			"proposal_id":    fmt.Sprintf("auto-submit-%d", stamp),
			"commit_message": "verify: edit " + skillName(),
			"files": []map[string]any{
				{"file_path": "SKILL.md", "content": content},
			},
		})
		if res.IsError {
			t.Fatalf("propose_change as %s failed: %s", agentID, contentText(res))
		}
		var out proposeChangeOutput
		decodeStructured(t, res, &out)

		// Every agent after the first proposes content that already exists,
		// so it must land as corroboration rather than a branch of its own.
		if i > 1 && !out.Deduplicated {
			t.Fatalf("%s got its own branch for content that already existed", agentID)
		}
		if out.AutoSubmitted != nil {
			if out.AutoSubmitted.PullRequestURL == "" {
				t.Fatalf("%s: auto_submitted is set but carries no pull_request_url", agentID)
			}
			prURL = out.AutoSubmitted.PullRequestURL
			t.Logf("pull request opened at corroboration %d: %s", out.Proposal.Corroboration, prURL)
		}
	}

	if prURL == "" {
		t.Skipf("no pull request opened within %d corroborating agents: this deployment has "+
			"autoSubmitEndorsements set higher, set to 0, or no GitHub credential configured",
			maxCorroboratingAgents)
	}

	// Best-effort reachability check - not fatal, since the PR host may be
	// behind auth or a different network namespace from this shell.
	resp, err := http.Get(prURL)
	if err != nil {
		t.Logf("pull request URL returned but not reachable from this shell "+
			"(expected if it's behind auth or a different network namespace): %s (%v)", prURL, err)
		return
	}
	defer resp.Body.Close()
	t.Logf("pull request URL is reachable (%s): status %d", prURL, resp.StatusCode)
}
