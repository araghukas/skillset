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

// TestInstructionsBothMarkersPresent guards against a mistyped or removed
// HTML-comment marker in skillsd-client/SKILL.md silently shipping an
// empty Instructions string - the source of the bug is a markdown edit,
// far from any Go code that would otherwise catch it.
func TestInstructionsBothMarkersPresent(t *testing.T) {
	for _, component := range []string{"skillsd", "registry"} {
		got := Instructions(component)
		if len(got) < 500 {
			t.Errorf("Instructions(%q) looks empty or truncated (%d bytes): %q", component, len(got), got)
		}
	}
}

// TestInstructionsAreDisjointByComponent is the point of splitting the
// guide at all: the skillsd-only section should never advertise the
// proposal workflow it has no tools for, and the registry-only section
// should never explain list_skills pagination it doesn't implement.
//
// This checks the component-specific sections directly (via
// extractSections), not the full Instructions() output - the "shared"
// block legitimately mentions tools from both components as part of
// describing the end-to-end workflow, so asserting disjointness on the
// combined string would be testing the wrong thing.
func TestInstructionsAreDisjointByComponent(t *testing.T) {
	cf, ok := Guide.ContextFile("SKILL.md")
	if !ok {
		t.Fatal("Guide has no SKILL.md")
	}
	skillsdOnly := strings.Join(extractSections(cf.Content, "skillsd"), "\n")
	registryOnly := strings.Join(extractSections(cf.Content, "registry"), "\n")

	for _, tool := range []string{"list_skills(", "get_skill("} {
		if !strings.Contains(skillsdOnly, tool) {
			t.Errorf("skillsd section does not mention %q", tool)
		}
		if strings.Contains(registryOnly, tool) {
			t.Errorf("registry section mentions %q, which is not one of its tools", tool)
		}
	}

	for _, tool := range []string{"propose_change(", "report_outcome(", "submit_proposal("} {
		if !strings.Contains(registryOnly, tool) {
			t.Errorf("registry section does not mention %q", tool)
		}
		if strings.Contains(skillsdOnly, tool) {
			t.Errorf("skillsd section mentions %q, which is not one of its tools", tool)
		}
	}
}

// TestInstructionsShareCommonContent confirms the "shared" sections - the
// intro and the typical-flow walkthrough - land in both, rather than only
// in whichever section happened to come first in the source document.
func TestInstructionsShareCommonContent(t *testing.T) {
	skillsd := Instructions("skillsd")
	registry := Instructions("registry")

	for _, phrase := range []string{
		"skillsd-registry", // from the shared intro
		"## Typical flow",  // the second shared block
		"report_outcome",   // referenced in the "using a skill" flow
	} {
		if !strings.Contains(skillsd, phrase) {
			t.Errorf("skillsd instructions missing shared content %q", phrase)
		}
		if !strings.Contains(registry, phrase) {
			t.Errorf("registry instructions missing shared content %q", phrase)
		}
	}
}

// TestInstructionsUnknownComponentIsSharedOnly confirms an unrecognized
// component name contributes nothing beyond the shared sections - there is
// simply no "nonexistent:start" marker in the document for it to match.
// (The shared "Typical flow" walkthrough legitimately mentions tool names
// from both components in prose, so this checks section count rather than
// scanning the combined text for tool names - see
// TestInstructionsAreDisjointByComponent for that check, done against the
// isolated per-component sections instead.)
func TestInstructionsUnknownComponentIsSharedOnly(t *testing.T) {
	cf, ok := Guide.ContextFile("SKILL.md")
	if !ok {
		t.Fatal("Guide has no SKILL.md")
	}
	if got := extractSections(cf.Content, "nonexistent"); len(got) != 0 {
		t.Errorf("expected no sections for an unrecognized component, got %d", len(got))
	}

	sharedOnly := strings.Join(extractSections(cf.Content, "shared"), "\n\n")
	unknown := Instructions("nonexistent")
	if sharedOnly != unknown {
		t.Errorf("Instructions(\"nonexistent\") should equal the shared-only content")
	}
}

func TestExtractSectionsHandlesMultipleOccurrences(t *testing.T) {
	doc := "before\n<!-- x:start -->first<!-- x:end -->\nmiddle\n<!-- x:start -->second<!-- x:end -->\nafter"
	got := extractSections(doc, "x")
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("extractSections = %v, want [first second]", got)
	}
}

func TestExtractSectionsNoMatchIsEmpty(t *testing.T) {
	got := extractSections("no markers here", "x")
	if len(got) != 0 {
		t.Errorf("expected no sections, got %v", got)
	}
}

func TestExtractSectionsUnterminatedIsDropped(t *testing.T) {
	got := extractSections("<!-- x:start -->never closed", "x")
	if len(got) != 0 {
		t.Errorf("an unterminated section should be dropped, not returned partially: %v", got)
	}
}
