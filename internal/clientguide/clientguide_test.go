package clientguide

import (
	"strings"
	"testing"
)

// TestGuideLoadsWithoutPanicking is a basic sanity check on the package
// var - if the embedded SKILL.md's frontmatter fails validation, Guide's
// initialization panics at package init, which is a much worse failure
// mode than a normal test failure. This at least confirms the happy path
// works before anything downstream (Instructions, the tool, the resource)
// is exercised.
func TestGuideLoadsWithoutPanicking(t *testing.T) {
	if Guide == nil {
		t.Fatal("Guide is nil")
	}
	if Guide.Name != "skillsd-client" {
		t.Errorf("Guide.Name = %q, want skillsd-client", Guide.Name)
	}
	if Guide.Description == "" {
		t.Error("Guide.Description is empty")
	}
}

// TestGuideHasEveryReferenceFile guards against a renamed or deleted
// reference file silently shipping a truncated guide - Instructions and
// guideFiles name these paths by string, so nothing else would catch a typo
// or a missing file at compile time.
func TestGuideHasEveryReferenceFile(t *testing.T) {
	want := []string{"SKILL.md", "references/intro.md", "references/skillsd.md", "references/registry.md", "references/typical-flow.md"}
	for _, p := range want {
		cf, ok := Guide.ContextFile(p)
		if !ok {
			t.Errorf("embedded guide is missing %s", p)
			continue
		}
		if len(strings.TrimSpace(cf.Content)) == 0 {
			t.Errorf("%s is empty", p)
		}
	}
}

// TestInstructionsCarryUniversalContent guards against a universal file
// going missing or empty silently shipping truncated Instructions, and
// confirms the sections that must reach every session - the mental model
// and the typical flow - are actually there.
func TestInstructionsCarryUniversalContent(t *testing.T) {
	got := Instructions("")
	if len(got) < 500 {
		t.Fatalf("Instructions looks empty or truncated (%d bytes): %q", len(got), got)
	}
	for _, phrase := range []string{
		"skillsd-registry", // from references/intro.md
		"## Typical flow",  // references/typical-flow.md
		"report_outcome",   // referenced in the "using a skill" flow
		"get_client_guide", // where the per-tool reference lives
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("instructions missing universal content %q", phrase)
		}
	}
}

// TestInstructionsOmitPerToolReference is the volume control: the per-tool
// reference files are fetched on demand through get_client_guide, and
// connect-time Instructions must not quietly grow them back.
func TestInstructionsOmitPerToolReference(t *testing.T) {
	got := Instructions("")
	for _, phrase := range []string{
		"## Discovering and reading skills",  // references/skillsd.md
		"## Reporting how a skill performed", // references/registry.md
	} {
		if strings.Contains(got, phrase) {
			t.Errorf("instructions include per-tool reference content %q, which belongs only in get_client_guide", phrase)
		}
	}
}

// TestGuideTextIncludesEveryReferenceFile confirms the full guide behind
// get_client_guide and the resource covers both servers' tools, not just
// the universal sections.
func TestGuideTextIncludesEveryReferenceFile(t *testing.T) {
	got, err := guideText("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"list_skills", "get_skill", "propose_change", "report_outcome", "get_proposal"} {
		if !strings.Contains(got, tool) {
			t.Errorf("full guide does not mention %q", tool)
		}
	}
}

// TestGuideAdvertisesNoSubmitTool guards the guide against describing a tool
// agents cannot call. Opening a pull request is the registry's decision, and
// the text an agent reads has to say so - a guide that offers a submit call
// sends agents looking for one that was never served.
func TestGuideAdvertisesNoSubmitTool(t *testing.T) {
	full, err := guideText("")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{Instructions(""), full} {
		if strings.Contains(text, "submit_proposal") {
			t.Error("guide advertises a submit_proposal tool, which no server registers")
		}
	}
}

// TestInstructionsAppendsAppendix confirms a non-empty appendix string is
// appended after the guide sections - the mechanism skillsd (a skill
// count) and skillsd-registry (a repo configuration section) both rely on.
func TestInstructionsAppendsAppendix(t *testing.T) {
	base := Instructions("")
	withCount := Instructions("This server currently serves 3 skills; call list_skills to see them.")

	if !strings.HasPrefix(withCount, base) {
		t.Fatalf("Instructions with an appendix should extend the base output, not replace it")
	}
	if !strings.Contains(withCount, "3 skills") {
		t.Errorf("appendix content missing from Instructions output: %q", withCount)
	}

	section := "## Repository configuration\n\n" +
		"- Skills are read from, and proposals are forked from, https://github.com/acme/skills.git on branch \"main\".\n"
	if got := Instructions(section); !strings.Contains(got, "https://github.com/acme/skills.git") {
		t.Errorf("expected repo URL in instructions, got: %q", got)
	}
}

// TestJoinContextFilesSkipsMissing confirms a nonexistent path is silently
// omitted rather than erroring or inserting a placeholder.
func TestJoinContextFilesSkipsMissing(t *testing.T) {
	got := joinContextFiles([]string{"references/intro.md", "does/not/exist.md"})
	want, _ := Guide.ContextFile("references/intro.md")
	if got != strings.TrimSpace(want.Content) {
		t.Errorf("joinContextFiles with a missing path should equal the existing file alone")
	}
}
