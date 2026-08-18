package suggestions

import (
	"context"
	"strings"
	"testing"
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

func TestIdenticalSuggestionsCollapseIntoOneWithEndorsements(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	fixed := validSkillMD("frontend-design", "the corrected description")

	first := suggest(t, svc, "agent-1", "fix", fixed)
	if first.Deduplicated {
		t.Fatal("the first suggestion has nothing to deduplicate against")
	}
	if first.Suggestion.Corroboration != 1 {
		t.Fatalf("expected corroboration 1 for a lone suggestion, got %d", first.Suggestion.Corroboration)
	}

	second := suggest(t, svc, "agent-2", "also-fix", fixed)
	if !second.Deduplicated {
		t.Fatal("expected the second agent's identical content to deduplicate onto the first suggestion")
	}
	if got, want := second.Suggestion.Branch, first.Suggestion.Branch; got != want {
		t.Fatalf("expected to be returned the existing suggestion %q, got %q", want, got)
	}
	if got := second.Suggestion.Corroboration; got != 2 {
		t.Fatalf("expected corroboration 2 after one endorsement, got %d", got)
	}

	// The endorsing agent must not have got a branch of its own - the whole
	// point is that N agents produce one pull request, not N.
	all, err := svc.ListSuggestions(context.Background(), "frontend-design", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 suggestion branch after two identical suggestions, got %d", len(all))
	}

	endorsements := second.Suggestion.Endorsements
	if len(endorsements) != 1 || endorsements[0].AgentID != "agent-2" {
		t.Fatalf("expected a single endorsement by agent-2, got %+v", endorsements)
	}
	if endorsements[0].Stale {
		t.Fatal("an endorsement of the current head should not be stale")
	}
}

func TestDedupIgnoresWhitespaceOnlyDifferences(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	fixed := validSkillMD("frontend-design", "the corrected description")

	suggest(t, svc, "agent-1", "fix", fixed)
	// Same content, trailing spaces and extra blank lines at EOF.
	messy := strings.ReplaceAll(fixed, "\nbody\n", "   \nbody   \n\n\n")

	second := suggest(t, svc, "agent-2", "also-fix", messy)
	if !second.Deduplicated {
		t.Fatal("expected whitespace-only differences to normalize to the same content hash")
	}
}

func TestDifferentContentDoesNotDeduplicate(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "one fix"))
	second := suggest(t, svc, "agent-2", "fix", validSkillMD("frontend-design", "a different fix"))

	if second.Deduplicated {
		t.Fatal("suggestions with different content must not collapse")
	}
	if second.Suggestion.AgentID != "agent-2" {
		t.Fatalf("expected agent-2 to get its own suggestion, got %q", second.Suggestion.AgentID)
	}
}

func TestAllowDuplicateForcesOwnBranch(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	fixed := validSkillMD("frontend-design", "the corrected description")

	suggest(t, svc, "agent-1", "fix", fixed)

	res, err := svc.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:      "frontend-design",
		AgentID:        "agent-2",
		SuggestionID:   "fix",
		Files:          []FileEdit{{FilePath: "SKILL.md", Content: fixed}},
		AllowDuplicate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deduplicated {
		t.Fatal("allow_duplicate must bypass the dedup check")
	}
	if res.Suggestion.AgentID != "agent-2" {
		t.Fatalf("expected agent-2's own branch, got %q", res.Suggestion.Branch)
	}
}

func TestEndorsementGoesStaleWhenSuggestionAdvances(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	ctx := context.Background()
	fixed := validSkillMD("frontend-design", "the corrected description")

	suggest(t, svc, "agent-1", "fix", fixed)
	second := suggest(t, svc, "agent-2", "also-fix", fixed)
	if second.Suggestion.Corroboration != 2 {
		t.Fatalf("precondition: expected corroboration 2, got %d", second.Suggestion.Corroboration)
	}

	// agent-1 revises its suggestion. agent-2 corroborated the previous
	// content and never saw this, so the agreement must not carry forward.
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

func TestIteratingOnOwnBranchIsNeverDeduplicated(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	suggest(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "agent one's answer"))
	suggest(t, svc, "agent-2", "fix", validSkillMD("frontend-design", "agent two's answer"))

	// agent-2 now revises into exactly what agent-1 already has. Its own
	// branch already exists, so it keeps it rather than being diverted.
	res := suggest(t, svc, "agent-2", "fix", validSkillMD("frontend-design", "agent one's answer"))
	if res.Deduplicated {
		t.Fatal("an agent iterating on its own existing branch must not be redirected onto another agent's suggestion")
	}
	if res.Suggestion.AgentID != "agent-2" {
		t.Fatalf("expected to stay on agent-2's branch, got %q", res.Suggestion.Branch)
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

func TestNormalizeContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"crlf becomes lf", "a\r\nb\r\n", "a\nb\n"},
		{"trailing spaces stripped", "a   \nb\t\n", "a\nb\n"},
		{"trailing blank lines dropped", "a\nb\n\n\n\n", "a\nb\n"},
		{"missing final newline added", "a\nb", "a\nb\n"},
		{"empty stays empty", "\n\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(normalizeContent([]byte(tc.in))); got != tc.want {
				t.Fatalf("normalizeContent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
