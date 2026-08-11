//go:build e2e

// Exercises skillsd-registry's evidence tools: report an outcome, then
// confirm it shows up both in the raw report listing and in the
// aggregated per-(skill, commit) signal.
package verify

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// resolveCommit reads skillsd's current commit for skillName, a
// prerequisite before reporting an outcome against it. get_skill's real
// Content is hand-built text, not structured JSON (see
// internal/toolresult.Skill), so the commit is parsed out of the
// "commit: <sha>" line rather than decoded as JSON.
func resolveCommit(t *testing.T) string {
	t.Helper()
	session := connect(t, skillsdAddr())
	res := callTool(t, session, "get_skill", map[string]any{"skill_name": skillName()})
	if res.IsError {
		t.Fatalf("resolving commit for %s: %s", skillName(), contentText(res))
	}
	for line := range strings.SplitSeq(contentText(res), "\n") {
		if commit, ok := strings.CutPrefix(line, "commit: "); ok && commit != "" {
			return commit
		}
	}
	return ""
}

type reportOutcomeOutput struct {
	Recorded bool `json:"recorded"`
}

type listOutcomeReportsOutput struct {
	Reports []struct {
		ReportID    string `json:"report_id"`
		SkillCommit string `json:"skill_commit"`
	} `json:"reports"`
}

type listSkillSignalsOutput struct {
	Signals []struct {
		SkillCommit      string `json:"skill_commit"`
		ReportedSessions int64  `json:"reported_sessions"`
	} `json:"signals"`
}

// TestEvidenceToolsPresence documents the MCP replacement for the old
// "Unimplemented means the service is off" convention: a disabled
// evidence store means the three evidence tools are simply absent from
// tools/list, which is asserted here rather than discovered by a failed
// call in every other test.
func TestEvidenceToolsPresence(t *testing.T) {
	session := connect(t, registryAddr())
	got := toolNames(t, session)
	have := got["report_outcome"] && got["list_skill_signals"] && got["list_outcome_reports"]
	t.Logf("evidence tools present: %v", have)
	if !have && (got["report_outcome"] || got["list_skill_signals"] || got["list_outcome_reports"]) {
		t.Error("evidence tools are partially registered - report_outcome, list_skill_signals, " +
			"and list_outcome_reports should all be present or all be absent together")
	}
}

// TestReportOutcomeRoundTrip reports an outcome, confirms the
// idempotent replay is a no-op, then confirms it shows up in
// both the raw listing and the aggregated signal. Skipped (not failed) if
// the evidence tools aren't registered on this deployment.
func TestReportOutcomeRoundTrip(t *testing.T) {
	registry := connect(t, registryAddr())
	if !toolNames(t, registry)["report_outcome"] {
		t.Skip("evidence tools are not registered on this deployment (registry.evidence.enabled=false)")
	}

	commit := resolveCommit(t)
	if commit == "" {
		t.Skipf("could not resolve a commit for %s; is skillsd deployed and seeded?", skillName())
	}

	reportID := fmt.Sprintf("verify-%d-%d", time.Now().Unix(), time.Now().Nanosecond())
	sessionID := fmt.Sprintf("verify-session-%d", time.Now().Nanosecond())

	args := map[string]any{
		"report_id":  reportID,
		"agent_id":   "verify-agent",
		"session_id": sessionID,
		"skills": []map[string]any{
			{
				"skill_name":   skillName(),
				"skill_commit": commit,
				"verdict":      "applied_with_correction",
				"note":         "e2e verify test exercising report_outcome",
			},
		},
	}

	t.Run("report_outcome", func(t *testing.T) {
		res := callTool(t, registry, "report_outcome", args)
		if res.IsError {
			t.Fatalf("report_outcome failed: %s", contentText(res))
		}
		var out reportOutcomeOutput
		decodeStructured(t, res, &out)
		if !out.Recorded {
			t.Error("first report should have Recorded=true")
		}
	})

	t.Run("report_outcome_idempotent_replay", func(t *testing.T) {
		res := callTool(t, registry, "report_outcome", args)
		if res.IsError {
			t.Fatalf("replaying report_outcome failed: %s", contentText(res))
		}
		var out reportOutcomeOutput
		decodeStructured(t, res, &out)
		if out.Recorded {
			t.Error("re-sending the same report_id should return Recorded=false")
		}
	})

	t.Run("list_outcome_reports", func(t *testing.T) {
		res := callTool(t, registry, "list_outcome_reports", map[string]any{"skill_name": skillName()})
		if res.IsError {
			t.Fatalf("list_outcome_reports failed: %s", contentText(res))
		}
		var out listOutcomeReportsOutput
		decodeStructured(t, res, &out)
		found := false
		for _, r := range out.Reports {
			if r.ReportID == reportID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("report %s does not appear in list_outcome_reports", reportID)
		}
	})

	t.Run("list_skill_signals", func(t *testing.T) {
		res := callTool(t, registry, "list_skill_signals", map[string]any{"skill_name": skillName()})
		if res.IsError {
			t.Fatalf("list_skill_signals failed: %s", contentText(res))
		}
		var out listSkillSignalsOutput
		decodeStructured(t, res, &out)
		var found bool
		for _, s := range out.Signals {
			if s.SkillCommit == commit {
				found = true
				if s.ReportedSessions < 1 {
					t.Errorf("signal for %s@%s has ReportedSessions=%d, want >= 1", skillName(), commit, s.ReportedSessions)
				}
			}
		}
		if !found {
			t.Errorf("no signal found for %s@%s", skillName(), commit)
		}
	})
}
