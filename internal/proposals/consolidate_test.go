package proposals

import (
	"context"
	"strings"
	"testing"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
)

// propose is a terse helper for the many-agents scenarios below.
func propose(t *testing.T, svc *Service, agent, proposalID, content string) *ProposeResult {
	t.Helper()
	res, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    agent,
		ProposalId: proposalID,
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: content},
		},
		CommitMessage: "propose " + proposalID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestIdenticalProposalsCollapseIntoOneWithEndorsements(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	fixed := validSkillMD("frontend-design", "the corrected description")

	first := propose(t, svc, "agent-1", "fix", fixed)
	if first.Deduplicated {
		t.Fatal("the first proposal has nothing to deduplicate against")
	}
	if first.Proposal.GetCorroboration() != 1 {
		t.Fatalf("expected corroboration 1 for a lone proposal, got %d", first.Proposal.GetCorroboration())
	}

	second := propose(t, svc, "agent-2", "also-fix", fixed)
	if !second.Deduplicated {
		t.Fatal("expected the second agent's identical content to deduplicate onto the first proposal")
	}
	if got, want := second.Proposal.GetBranch(), first.Proposal.GetBranch(); got != want {
		t.Fatalf("expected to be returned the existing proposal %q, got %q", want, got)
	}
	if got := second.Proposal.GetCorroboration(); got != 2 {
		t.Fatalf("expected corroboration 2 after one endorsement, got %d", got)
	}

	// The endorsing agent must not have got a branch of its own - the whole
	// point is that N agents produce one pull request, not N.
	all, err := svc.ListProposals(context.Background(), "frontend-design", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 proposal branch after two identical proposals, got %d", len(all))
	}

	endorsements := second.Proposal.GetEndorsements()
	if len(endorsements) != 1 || endorsements[0].GetAgentId() != "agent-2" {
		t.Fatalf("expected a single endorsement by agent-2, got %+v", endorsements)
	}
	if endorsements[0].GetStale() {
		t.Fatal("an endorsement of the current head should not be stale")
	}
}

func TestDedupIgnoresWhitespaceOnlyDifferences(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	fixed := validSkillMD("frontend-design", "the corrected description")

	propose(t, svc, "agent-1", "fix", fixed)
	// Same content, trailing spaces and extra blank lines at EOF.
	messy := strings.ReplaceAll(fixed, "\nbody\n", "   \nbody   \n\n\n")

	second := propose(t, svc, "agent-2", "also-fix", messy)
	if !second.Deduplicated {
		t.Fatal("expected whitespace-only differences to normalize to the same content hash")
	}
}

func TestDifferentContentDoesNotDeduplicate(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	propose(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "one fix"))
	second := propose(t, svc, "agent-2", "fix", validSkillMD("frontend-design", "a different fix"))

	if second.Deduplicated {
		t.Fatal("proposals with different content must not collapse")
	}
	if second.Proposal.GetAgentId() != "agent-2" {
		t.Fatalf("expected agent-2 to get its own proposal, got %q", second.Proposal.GetAgentId())
	}
}

func TestAllowDuplicateForcesOwnBranch(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	fixed := validSkillMD("frontend-design", "the corrected description")

	propose(t, svc, "agent-1", "fix", fixed)

	res, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:      "frontend-design",
		AgentId:        "agent-2",
		ProposalId:     "fix",
		Files:          []*skillsv1.FileChange{{FilePath: "SKILL.md", Content: fixed}},
		AllowDuplicate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deduplicated {
		t.Fatal("allow_duplicate must bypass the dedup check")
	}
	if res.Proposal.GetAgentId() != "agent-2" {
		t.Fatalf("expected agent-2's own branch, got %q", res.Proposal.GetBranch())
	}
}

func TestEndorsementGoesStaleWhenProposalAdvances(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))
	ctx := context.Background()
	fixed := validSkillMD("frontend-design", "the corrected description")

	propose(t, svc, "agent-1", "fix", fixed)
	second := propose(t, svc, "agent-2", "also-fix", fixed)
	if second.Proposal.GetCorroboration() != 2 {
		t.Fatalf("precondition: expected corroboration 2, got %d", second.Proposal.GetCorroboration())
	}

	// agent-1 revises its proposal. agent-2 corroborated the previous
	// content and never saw this, so the agreement must not carry forward.
	propose(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "revised again"))

	p, err := svc.GetProposal(ctx, "proposals/agent-1/frontend-design/fix")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.GetCorroboration(); got != 1 {
		t.Fatalf("expected corroboration to fall back to 1 once the proposal moved on, got %d", got)
	}
	if len(p.GetEndorsements()) != 1 || !p.GetEndorsements()[0].GetStale() {
		t.Fatalf("expected the endorsement to be retained but marked stale, got %+v", p.GetEndorsements())
	}
}

func TestIteratingOnOwnBranchIsNeverDeduplicated(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	propose(t, svc, "agent-1", "fix", validSkillMD("frontend-design", "agent one's answer"))
	propose(t, svc, "agent-2", "fix", validSkillMD("frontend-design", "agent two's answer"))

	// agent-2 now revises into exactly what agent-1 already has. Its own
	// branch already exists, so it keeps it rather than being diverted.
	res := propose(t, svc, "agent-2", "fix", validSkillMD("frontend-design", "agent one's answer"))
	if res.Deduplicated {
		t.Fatal("an agent iterating on its own existing branch must not be redirected onto another agent's proposal")
	}
	if res.Proposal.GetAgentId() != "agent-2" {
		t.Fatalf("expected to stay on agent-2's branch, got %q", res.Proposal.GetBranch())
	}
}

func TestMotivatingReportIDsRoundTripThroughCommitTrailers(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	res, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "agent-1",
		ProposalId: "fix",
		Files: []*skillsv1.FileChange{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "fixed")},
		},
		CommitMessage:       "fix the thing",
		MotivatingReportIds: []string{"report-aaa", "report-bbb"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := res.Proposal.GetMotivatingReportIds()
	if len(got) != 2 || got[0] != "report-aaa" || got[1] != "report-bbb" {
		t.Fatalf("expected both report IDs to survive the round trip, got %v", got)
	}
}

func TestProposeChangeRejectsSlashesInIdentifiers(t *testing.T) {
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "original"))

	// A slash here would make the branch name and its endorsement refs
	// ambiguous to parse back.
	_, err := svc.ProposeChange(context.Background(), &skillsv1.ProposeChangeRequest{
		SkillName:  "frontend-design",
		AgentId:    "team/agent-1",
		ProposalId: "fix",
		Files: []*skillsv1.FileChange{
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
	propose(t, svc, "agent-1", "top-a", strings.Replace(base, "TOP LINE", "TOP LINE rewritten one way", 1))
	propose(t, svc, "agent-2", "top-b", strings.Replace(base, "TOP LINE", "TOP LINE rewritten another way", 1))
	// A third edits somewhere unrelated.
	propose(t, svc, "agent-3", "bottom", strings.Replace(base, "BOTTOM LINE", "BOTTOM LINE rewritten", 1))

	clusters, err := svc.ListClusters(ctx, "frontend-design", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected exactly 1 contested cluster (the distant edit should stand alone), got %d", len(clusters))
	}

	c := clusters[0]
	if len(c.GetProposals()) != 2 {
		t.Fatalf("expected the two competing top-line proposals to cluster, got %d", len(c.GetProposals()))
	}
	if c.GetDistinctAgents() != 2 {
		t.Fatalf("expected 2 distinct agents in the cluster, got %d", c.GetDistinctAgents())
	}
	if len(c.GetContestedPaths()) != 1 || !strings.HasSuffix(c.GetContestedPaths()[0], "SKILL.md") {
		t.Fatalf("expected SKILL.md to be reported as contested, got %v", c.GetContestedPaths())
	}

	for _, p := range c.GetProposals() {
		if p.GetAgentId() == "agent-3" {
			t.Fatal("the unrelated edit must not be pulled into the contested cluster")
		}
	}

	// With singletons included, the lone proposal shows up as its own cluster.
	withSingletons, err := svc.ListClusters(ctx, "frontend-design", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withSingletons) != 2 {
		t.Fatalf("expected 2 clusters when singletons are included, got %d", len(withSingletons))
	}
	if withSingletons[0].GetDistinctAgents() < withSingletons[1].GetDistinctAgents() {
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
