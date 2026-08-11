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
// reference file silently shipping a truncated guide - componentFiles and
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

// TestInstructionsBothComponentsPresent guards against a component file
// going missing or empty silently shipping a truncated Instructions string.
func TestInstructionsBothComponentsPresent(t *testing.T) {
	for _, component := range []string{"skillsd", "registry"} {
		got := Instructions(component, "")
		if len(got) < 500 {
			t.Errorf("Instructions(%q) looks empty or truncated (%d bytes): %q", component, len(got), got)
		}
	}
}

// TestInstructionsAreDisjointByComponent is the point of splitting the
// guide at all: the skillsd-only file should never advertise the proposal
// workflow it has no tools for, and the registry-only file should never
// explain list_skills pagination it doesn't implement.
//
// This checks the component-specific files directly, not the full
// Instructions() output - references/typical-flow.md is universal and
// legitimately mentions tools from both components as part of describing
// the end-to-end workflow, so asserting disjointness on the combined string
// would be testing the wrong thing.
func TestInstructionsAreDisjointByComponent(t *testing.T) {
	skillsdOnly, ok := Guide.ContextFile("references/skillsd.md")
	if !ok {
		t.Fatal("Guide has no references/skillsd.md")
	}
	registryOnly, ok := Guide.ContextFile("references/registry.md")
	if !ok {
		t.Fatal("Guide has no references/registry.md")
	}

	for _, tool := range []string{"list_skills(", "get_skill("} {
		if !strings.Contains(skillsdOnly.Content, tool) {
			t.Errorf("skillsd file does not mention %q", tool)
		}
		if strings.Contains(registryOnly.Content, tool) {
			t.Errorf("registry file mentions %q, which is not one of its tools", tool)
		}
	}

	for _, tool := range []string{"propose_change(", "report_outcome(", "submit_proposal("} {
		if !strings.Contains(registryOnly.Content, tool) {
			t.Errorf("registry file does not mention %q", tool)
		}
		if strings.Contains(skillsdOnly.Content, tool) {
			t.Errorf("skillsd file mentions %q, which is not one of its tools", tool)
		}
	}
}

// TestInstructionsShareCommonContent confirms the universal files - the
// intro (SKILL.md) and the typical-flow walkthrough - land in both
// components' output.
func TestInstructionsShareCommonContent(t *testing.T) {
	skillsd := Instructions("skillsd", "")
	registry := Instructions("registry", "")

	for _, phrase := range []string{
		"skillsd-registry", // from references/intro.md
		"## Typical flow",  // references/typical-flow.md
		"report_outcome",   // referenced in the "using a skill" flow
	} {
		if !strings.Contains(skillsd, phrase) {
			t.Errorf("skillsd instructions missing universal content %q", phrase)
		}
		if !strings.Contains(registry, phrase) {
			t.Errorf("registry instructions missing universal content %q", phrase)
		}
	}
}

// TestInstructionsUnknownComponentIsUniversalOnly confirms an unrecognized
// component name contributes nothing beyond SKILL.md and
// references/typical-flow.md - there is simply no entry for it in
// componentFiles.
func TestInstructionsUnknownComponentIsUniversalOnly(t *testing.T) {
	universalOnly := joinContextFiles([]string{"references/intro.md", "references/typical-flow.md"})
	unknown := Instructions("nonexistent", "")
	if universalOnly != unknown {
		t.Errorf("Instructions(\"nonexistent\") should equal the universal-only content")
	}
}

// TestInstructionsAppendsCatalog confirms a non-empty appendix string is
// appended after the guide sections, and that an empty one leaves the
// output unchanged - the two cases skillsd (a skill catalog) and
// skillsd-registry (a repo configuration section) rely on respectively.
func TestInstructionsAppendsCatalog(t *testing.T) {
	base := Instructions("skillsd", "")
	withCatalog := Instructions("skillsd", "## Skills currently served\n\n- **foo**: does foo things\n")

	if !strings.HasPrefix(withCatalog, base) {
		t.Fatalf("Instructions with an appendix should extend the base output, not replace it")
	}
	if !strings.Contains(withCatalog, "does foo things") {
		t.Errorf("appendix content missing from Instructions output: %q", withCatalog)
	}
}

// TestInstructionsAppendsRepoConfigSection confirms a registry-style
// appendix describing repo configuration shows up in Instructions output
// just like a skill catalog does - the mechanism is shared, only the
// content differs by caller.
func TestInstructionsAppendsRepoConfigSection(t *testing.T) {
	section := "## Repository configuration\n\n" +
		"- Skills are read from, and proposals are forked from, https://github.com/acme/skills.git on branch \"main\".\n" +
		"- submit_proposal opens pull requests against https://github.com/acme/skills, targeting branch \"main\".\n"
	got := Instructions("registry", section)
	if !strings.Contains(got, "https://github.com/acme/skills.git") {
		t.Errorf("expected source repo URL in registry instructions, got: %q", got)
	}
	if !strings.Contains(got, "https://github.com/acme/skills") {
		t.Errorf("expected PR repo URL in registry instructions, got: %q", got)
	}
}

// TestJoinContextFilesSkipsMissing confirms a nonexistent path is silently
// omitted rather than erroring or inserting a placeholder - Instructions
// relies on this for unrecognized components.
func TestJoinContextFilesSkipsMissing(t *testing.T) {
	got := joinContextFiles([]string{"references/intro.md", "does/not/exist.md"})
	want, _ := Guide.ContextFile("references/intro.md")
	if got != strings.TrimSpace(want.Content) {
		t.Errorf("joinContextFiles with a missing path should equal the existing file alone")
	}
}
