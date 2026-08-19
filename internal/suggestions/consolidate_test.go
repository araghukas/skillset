package suggestions

import (
	"context"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// suggest is a terse helper for the many-agents scenarios below.
func suggest(t *testing.T, svc *Service, agent, suggestionID, content string) *SuggestResult {
	t.Helper()
	res, err := svc.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:    "frontend-design",
		AgentID:      agent,
		SuggestionID: suggestionID,
		Files: []FileEdit{
			{FilePath: "SKILL.md", Content: content},
		},
		CommitMessage: "suggest " + suggestionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// endorse endorses branch at the head the endorser just read, failing the
// test on error.
func endorse(t *testing.T, svc *Service, branch, endorser, headSHA string) *Suggestion {
	t.Helper()
	sg, err := svc.EndorseSuggestion(context.Background(), branch, endorser, plumbing.NewHash(headSHA))
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

func TestEndorsementRaisesCorroboration(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	first := suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "the corrected description"))
	if first.Suggestion.Corroboration != 1 {
		t.Fatalf("expected corroboration 1 for a lone suggestion, got %d", first.Suggestion.Corroboration)
	}

	sg := endorse(t, svc, first.Suggestion.Branch, "agent-2", first.Suggestion.HeadSHA)
	if got := sg.Corroboration; got != 2 {
		t.Fatalf("expected corroboration 2 after one endorsement, got %d", got)
	}
	if len(sg.Endorsements) != 1 || sg.Endorsements[0].AgentID != "agent-2" {
		t.Fatalf("expected a single endorsement by agent-2, got %+v", sg.Endorsements)
	}
	if sg.Endorsements[0].Stale {
		t.Fatal("an endorsement of the current head should not be stale")
	}

	// Endorsing creates no branch of the endorser's own - the whole point is
	// that N agents produce one pull request, not N.
	all, err := svc.ListSuggestions(context.Background(), "frontend-design", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 suggestion branch after an endorsement, got %d", len(all))
	}
}

func TestSelfEndorsementIsRejected(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	first := suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "fixed"))
	_, err := svc.EndorseSuggestion(context.Background(), first.Suggestion.Branch, "agent-1", plumbing.NewHash(first.Suggestion.HeadSHA))
	if err == nil {
		t.Fatal("expected the suggesting agent's own endorsement to be rejected")
	}
}

func TestEndorsementOfSupersededHeadIsRejected(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	first := suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "fixed"))
	staleHead := first.Suggestion.HeadSHA

	// The suggestion advances after agent-2 read it but before it endorses.
	suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "revised"))

	_, err := svc.EndorseSuggestion(context.Background(), first.Suggestion.Branch, "agent-2", plumbing.NewHash(staleHead))
	if err == nil {
		t.Fatal("expected an endorsement of a superseded head to be rejected")
	}

	sg, err := svc.GetSuggestion(context.Background(), first.Suggestion.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if len(sg.Endorsements) != 0 {
		t.Fatalf("a refused endorsement must not be recorded, got %+v", sg.Endorsements)
	}
}

func TestRepeatEndorsementDoesNotInflateCorroboration(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	first := suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "fixed"))
	endorse(t, svc, first.Suggestion.Branch, "agent-2", first.Suggestion.HeadSHA)
	sg := endorse(t, svc, first.Suggestion.Branch, "agent-2", first.Suggestion.HeadSHA)

	if got := sg.Corroboration; got != 2 {
		t.Fatalf("expected a repeat endorsement by the same agent to count once, got corroboration %d", got)
	}
	if len(sg.Endorsements) != 1 {
		t.Fatalf("expected one endorsement record for agent-2, got %d", len(sg.Endorsements))
	}
}

func TestEndorsementGoesStaleWhenSuggestionAdvances(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	ctx := context.Background()

	first := suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "the corrected description"))
	sg := endorse(t, svc, first.Suggestion.Branch, "agent-2", first.Suggestion.HeadSHA)
	if sg.Corroboration != 2 {
		t.Fatalf("precondition: expected corroboration 2, got %d", sg.Corroboration)
	}

	// agent-1 revises its suggestion. agent-2 endorsed the previous content
	// and never saw this, so the agreement must not carry forward.
	suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "revised again"))

	sg, err := svc.GetSuggestion(ctx, "suggestions/agent-1/frontend-design/fix")
	if err != nil {
		t.Fatal(err)
	}
	if got := sg.Corroboration; got != 1 {
		t.Fatalf("expected corroboration to fall back to 1 once the suggestion moved on, got %d", got)
	}
	if len(sg.Endorsements) != 1 || !sg.Endorsements[0].Stale {
		t.Fatalf("expected the endorsement to be retained but marked stale, got %+v", sg.Endorsements)
	}
}

func TestMotivatingReportIDsRoundTripThroughCommitTrailers(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	res, err := svc.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:    "frontend-design",
		AgentID:      "agent-1",
		SuggestionID: "fix",
		Files: []FileEdit{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "fixed")},
		},
		CommitMessage:       "fix the thing",
		MotivatingReportIDs: []string{"report-aaa", "report-bbb"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := res.Suggestion.MotivatingReportIDs
	if len(got) != 2 || got[0] != "report-aaa" || got[1] != "report-bbb" {
		t.Fatalf("expected both report IDs to survive the round trip, got %v", got)
	}
}

func TestRecordSuggestionRejectsSlashesInIdentifiers(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	// A slash here would make the branch name and its endorsement refs
	// ambiguous to parse back.
	_, err := svc.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:    "frontend-design",
		AgentID:      "team/agent-1",
		SuggestionID: "fix",
		Files: []FileEdit{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "fixed")},
		},
	})
	if err == nil {
		t.Fatal("expected an agent_id containing a slash to be rejected")
	}
}

func TestClusteringGroupsOverlappingEditsAndSeparatesDistantOnes(t *testing.T) {
	// A skill body with enough distance between the two edit sites that
	// they land outside each other's diff context.
	base := "---\nname: frontend-design\ndescription: original\n---\n" +
		"TOP LINE\n" + strings.Repeat("filler\n", 40) + "BOTTOM LINE\n"

	svc, _ := newTestService(t, "frontend-design", base)
	ctx := context.Background()

	// Two agents rewrite the top line differently: same region, competing
	// answers - the case clustering exists to surface.
	suggest(t, svc, "agent-1", "top-a", strings.Replace(base, "TOP LINE", "TOP LINE rewritten one way", 1))
	suggest(t, svc, "agent-2", "top-b", strings.Replace(base, "TOP LINE", "TOP LINE rewritten another way", 1))
	// A third edits somewhere unrelated.
	suggest(t, svc, "agent-3", "bottom", strings.Replace(base, "BOTTOM LINE", "BOTTOM LINE rewritten", 1))

	clusters, err := svc.ListClusters(ctx, "frontend-design", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected exactly 1 contested cluster (the distant edit should stand alone), got %d", len(clusters))
	}

	c := clusters[0]
	if len(c.Suggestions) != 2 {
		t.Fatalf("expected the two competing top-line suggestions to cluster, got %d", len(c.Suggestions))
	}
	if c.DistinctAgents != 2 {
		t.Fatalf("expected 2 distinct agents in the cluster, got %d", c.DistinctAgents)
	}
	if len(c.ContestedPaths) != 1 || !strings.HasSuffix(c.ContestedPaths[0], "SKILL.md") {
		t.Fatalf("expected SKILL.md to be reported as contested, got %v", c.ContestedPaths)
	}

	for _, sg := range c.Suggestions {
		if sg.AgentID == "agent-3" {
			t.Fatal("the unrelated edit must not be pulled into the contested cluster")
		}
	}

	// With singletons included, the lone suggestion shows up as its own cluster.
	withSingletons, err := svc.ListClusters(ctx, "frontend-design", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withSingletons) != 2 {
		t.Fatalf("expected 2 clusters when singletons are included, got %d", len(withSingletons))
	}
	if withSingletons[0].DistinctAgents < withSingletons[1].DistinctAgents {
		t.Fatal("expected clusters to be sorted most-corroborated first")
	}
}
