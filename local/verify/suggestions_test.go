//go:build e2e

// Drives one full suggestion lifecycle against skillsd-registry's suggestion
// tools: record a suggestion, inspect it, read the skill at that ref, list it
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

type recordSuggestionOutput struct {
	Suggestion struct {
		Branch        string `json:"branch"`
		HeadSHA       string `json:"head_sha"`
		Diff          string `json:"diff"`
		Corroboration int    `json:"corroboration"`
		Commits       []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
	} `json:"suggestion"`
	Deduplicated  bool `json:"deduplicated"`
	AutoSubmitted *struct {
		PullRequestURL string `json:"pull_request_url"`
	} `json:"auto_submitted"`
}

type getSuggestionOutput struct {
	HeadSHA string `json:"head_sha"`
}

type listSuggestionsOutput struct {
	Suggestions []struct {
		Branch string `json:"branch"`
	} `json:"suggestions"`
}

type listClustersOutput struct {
	Clusters []any `json:"clusters"`
}

// TestFullSuggestionLifecycle exercises the full suggestion flow end to
// end: record, re-fetch, read at ref, list, cluster. Uses a
// timestamp-suffixed suggestion_id so the test is safely re-runnable
// against a live deployment without a "nothing changed" or "clean
// working tree" error from a previous run's identical commit.
func TestFullSuggestionLifecycle(t *testing.T) {
	session := connect(t, registryAddr())

	agentID := "verify-agent"
	suggestionID := fmt.Sprintf("verify-%d", time.Now().Unix())
	branch := fmt.Sprintf("suggestions/%s/%s/%s", agentID, skillName(), suggestionID)

	content := fmt.Sprintf(
		"---\nname: %s\ndescription: A minimal placeholder skill seeded by "+
			"local/gitea-init.sh, edited by local/verify's e2e suggestion test at %s "+
			"to exercise the suggestion flow.\n---\n\n## When to use this skill\n\n"+
			"This is seed content for local dev only - edited by the verification test.\n",
		skillName(), time.Now().UTC().Format(time.RFC3339))

	var suggestedHeadSHA string
	t.Run("record_suggestion", func(t *testing.T) {
		res := callTool(t, session, "record_suggestion", map[string]any{
			"skill_name":     skillName(),
			"agent_id":       agentID,
			"suggestion_id":  suggestionID,
			"commit_message": "verify: edit " + skillName(),
			"files": []map[string]any{
				{"file_path": "SKILL.md", "content": content},
			},
		})
		if res.IsError {
			t.Fatalf("record_suggestion failed: %s", contentText(res))
		}
		var out recordSuggestionOutput
		decodeStructured(t, res, &out)

		if out.Suggestion.Branch != branch {
			t.Errorf("suggestion branch = %q, want %q", out.Suggestion.Branch, branch)
		}
		if out.Deduplicated {
			t.Error("first suggestion should not be deduplicated")
		}
		if out.Suggestion.Diff == "" {
			t.Error("suggestion has an empty diff")
		}
		if len(out.Suggestion.Commits) == 0 {
			t.Error("suggestion has no commits")
		}
		suggestedHeadSHA = out.Suggestion.HeadSHA
	})

	t.Run("get_suggestion", func(t *testing.T) {
		res := callTool(t, session, "get_suggestion", map[string]any{"branch": branch})
		if res.IsError {
			t.Fatalf("get_suggestion failed: %s", contentText(res))
		}
		var out getSuggestionOutput
		decodeStructured(t, res, &out)
		if out.HeadSHA == "" {
			t.Error("get_suggestion returned no head_sha")
		}
		if out.HeadSHA != suggestedHeadSHA {
			t.Errorf("get_suggestion head_sha = %q, want %q (from record_suggestion)", out.HeadSHA, suggestedHeadSHA)
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
		if !strings.Contains(contentText(res), "exercise the suggestion flow") {
			t.Error("skill at the suggestion ref does not carry the edited description")
		}
	})

	t.Run("list_suggestions", func(t *testing.T) {
		res := callTool(t, session, "list_suggestions", map[string]any{"skill_name": skillName()})
		if res.IsError {
			t.Fatalf("list_suggestions failed: %s", contentText(res))
		}
		var out listSuggestionsOutput
		decodeStructured(t, res, &out)
		found := false
		for _, sg := range out.Suggestions {
			if sg.Branch == branch {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("our suggestion (%s) does not appear in list_suggestions", branch)
		}
	})

	t.Run("list_suggestion_clusters", func(t *testing.T) {
		res := callTool(t, session, "list_suggestion_clusters", map[string]any{
			"skill_name":         skillName(),
			"include_singletons": true,
		})
		if res.IsError {
			t.Fatalf("list_suggestion_clusters failed: %s", contentText(res))
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
		res := callTool(t, session, "record_suggestion", map[string]any{
			"skill_name":     skillName(),
			"agent_id":       agentID,
			"suggestion_id":  fmt.Sprintf("auto-submit-%d", stamp),
			"commit_message": "verify: edit " + skillName(),
			"files": []map[string]any{
				{"file_path": "SKILL.md", "content": content},
			},
		})
		if res.IsError {
			t.Fatalf("record_suggestion as %s failed: %s", agentID, contentText(res))
		}
		var out recordSuggestionOutput
		decodeStructured(t, res, &out)

		// Every agent after the first suggests content that already exists,
		// so it must land as corroboration rather than a branch of its own.
		if i > 1 && !out.Deduplicated {
			t.Fatalf("%s got its own branch for content that already existed", agentID)
		}
		if out.AutoSubmitted != nil {
			if out.AutoSubmitted.PullRequestURL == "" {
				t.Fatalf("%s: auto_submitted is set but carries no pull_request_url", agentID)
			}
			prURL = out.AutoSubmitted.PullRequestURL
			t.Logf("pull request opened at corroboration %d: %s", out.Suggestion.Corroboration, prURL)
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
