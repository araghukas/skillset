package suggestions

import (
	"context"
	"strings"
	"testing"
)

// descriptionPatch renames the seeded skill's description, the same change
// validSkillMD(name, "designs frontends, fixed") produces as full content.
const descriptionPatch = `--- a/SKILL.md
+++ b/SKILL.md
@@ -1,5 +1,5 @@
 ---
 name: frontend-design
-description: designs frontends
+description: designs frontends, fixed
 ---
 body
`

func suggestPatch(t *testing.T, svc *Service, agentID, suggestionID, patch string) (*SuggestResult, error) {
	t.Helper()
	return svc.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:    "frontend-design",
		AgentID:      agentID,
		SuggestionID: suggestionID,
		Patch:        patch,
	})
}

func newSeededService(t *testing.T) *Service {
	t.Helper()
	svc, _ := newTestService(t, "frontend-design", validSkillMD("frontend-design", "designs frontends"))
	return svc
}

// TestPatchProducesTheSameContentAsFullFiles is the property the whole input
// rests on: how a change was described must not affect what is recorded.
func TestPatchProducesTheSameContentAsFullFiles(t *testing.T) {
	viaFiles := newSeededService(t)
	fromFiles, err := viaFiles.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:    "frontend-design",
		AgentID:      "agent-1",
		SuggestionID: "fix-description",
		Files: []FileEdit{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "designs frontends, fixed")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	viaPatch := newSeededService(t)
	fromPatch, err := suggestPatch(t, viaPatch, "agent-1", "fix-description", descriptionPatch)
	if err != nil {
		t.Fatal(err)
	}

	if fromPatch.Suggestion.ContentHash != fromFiles.Suggestion.ContentHash {
		t.Errorf("content hash from patch = %s, from files = %s; want them equal",
			fromPatch.Suggestion.ContentHash, fromFiles.Suggestion.ContentHash)
	}
	if fromPatch.Suggestion.Diff == "" {
		t.Error("expected a non-empty diff")
	}
}

// TestPatchDedupesLikeFiles is the load-bearing test that nothing downstream
// of the expansion can tell the two inputs apart.
func TestPatchDedupesLikeFiles(t *testing.T) {
	svc := newSeededService(t)

	if _, err := svc.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:    "frontend-design",
		AgentID:      "agent-1",
		SuggestionID: "fix-description",
		Files: []FileEdit{
			{FilePath: "SKILL.md", Content: validSkillMD("frontend-design", "designs frontends, fixed")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := suggestPatch(t, svc, "agent-2", "same-fix", descriptionPatch)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Deduplicated {
		t.Fatal("expected the patch to be deduplicated onto agent-1's suggestion")
	}
	if res.Suggestion.AgentID != "agent-1" {
		t.Errorf("returned suggestion belongs to %q, want agent-1", res.Suggestion.AgentID)
	}
	if res.Suggestion.Corroboration != 2 {
		t.Errorf("corroboration = %d, want 2", res.Suggestion.Corroboration)
	}
}

// TestPatchAppliesToOwnBranchTip covers the case an iterating agent hits: the
// second patch has to be against what the first one left behind.
func TestPatchAppliesToOwnBranchTip(t *testing.T) {
	svc := newSeededService(t)

	if _, err := suggestPatch(t, svc, "agent-1", "fix-description", descriptionPatch); err != nil {
		t.Fatal(err)
	}

	res, err := suggestPatch(t, svc, "agent-1", "fix-description", `--- a/SKILL.md
+++ b/SKILL.md
@@ -1,5 +1,5 @@
 ---
 name: frontend-design
-description: designs frontends, fixed
+description: designs frontends, fixed twice
 ---
 body
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suggestion.Commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(res.Suggestion.Commits))
	}

	md, err := svc.GetSkillAtRef(context.Background(), "frontend-design", res.Suggestion.Branch, false)
	if err != nil {
		t.Fatal(err)
	}
	if md.Description != "designs frontends, fixed twice" {
		t.Errorf("description = %q, want the twice-patched one", md.Description)
	}

	// The original patch no longer applies, and the error has to say what it
	// was applied to.
	_, err = suggestPatch(t, svc, "agent-1", "fix-description", descriptionPatch)
	if err == nil {
		t.Fatal("expected the stale patch to be rejected")
	}
	for _, want := range []string{"SKILL.md", "hunk #1", res.Suggestion.Branch, "get_skill_at_ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestPatchDeletesAFile(t *testing.T) {
	svc := newSeededService(t)

	res, err := suggestPatch(t, svc, "agent-1", "drop-reference", `--- a/references/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-old reference content
`)
	if err != nil {
		t.Fatal(err)
	}

	md, err := svc.GetSkillAtRef(context.Background(), "frontend-design", res.Suggestion.Branch, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := md.ContextFile("references/old.txt"); ok {
		t.Error("references/old.txt still exists after a deleting patch")
	}
}

func TestPatchCreatesAFile(t *testing.T) {
	svc := newSeededService(t)

	res, err := suggestPatch(t, svc, "agent-1", "add-reference", `--- /dev/null
+++ b/references/new.md
@@ -0,0 +1,2 @@
+alpha
+beta
`)
	if err != nil {
		t.Fatal(err)
	}

	md, err := svc.GetSkillAtRef(context.Background(), "frontend-design", res.Suggestion.Branch, true)
	if err != nil {
		t.Fatal(err)
	}
	cf, ok := md.ContextFile("references/new.md")
	if !ok {
		t.Fatal("references/new.md was not created")
	}
	if cf.Content != "alpha\nbeta\n" {
		t.Errorf("content = %q, want the two added lines", cf.Content)
	}
}

// TestPatchAcceptsMirroredScratchDirs covers the recipe callers are given:
// the skill mirrored under scratch roots named a and b, diffed from their
// parent. The tab-separated timestamps are what "diff -ru" emits.
func TestPatchAcceptsMirroredScratchDirs(t *testing.T) {
	svc := newSeededService(t)

	if _, err := suggestPatch(t, svc, "agent-1", "fix-description", `--- a/SKILL.md	2026-08-13 10:00:00
+++ b/SKILL.md	2026-08-13 10:01:00
@@ -1,5 +1,5 @@
 ---
 name: frontend-design
-description: designs frontends
+description: designs frontends, fixed
 ---
 body
`); err != nil {
		t.Fatal(err)
	}
}

// TestPatchRejectsUnresolvablePath pins the decision to reject rather than
// guess: two differently-named scratch copies name no file of the skill, and
// inferring one from the filename could land the edit on a same-named file.
// The error has to carry both what the skill does have and how to diff it.
func TestPatchRejectsUnresolvablePath(t *testing.T) {
	svc := newSeededService(t)

	_, err := suggestPatch(t, svc, "agent-1", "fix-description", `--- a/scratch/orig/SKILL.md	2026-08-13 10:00:00
+++ b/scratch/edited/SKILL.md	2026-08-13 10:01:00
@@ -1,5 +1,5 @@
 ---
 name: frontend-design
-description: designs frontends
+description: designs frontends, fixed
 ---
 body
`)
	if err == nil {
		t.Fatal("expected mismatched scratch paths to be rejected")
	}
	for _, want := range []string{"scratch/edited/SKILL.md", "SKILL.md", "git diff --no-index a b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %s", want, err)
		}
	}
}

// TestPatchResolvesLiteralTopLevelADirectory covers a skill whose own file
// sits under a top-level "a/". A header written without git's prefix has that
// directory stripped as if it were one, leaving a path the skill does not
// have; the file is found by re-prepending it, never by matching on filename.
func TestPatchResolvesLiteralTopLevelADirectory(t *testing.T) {
	svc := newSeededService(t)

	if _, err := svc.RecordSuggestion(context.Background(), SuggestInput{
		SkillName:    "frontend-design",
		AgentID:      "agent-1",
		SuggestionID: "add-notes",
		Files:        []FileEdit{{FilePath: "a/notes.md", Content: "one\ntwo\n"}},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := suggestPatch(t, svc, "agent-1", "add-notes", `--- a/notes.md
+++ b/notes.md
@@ -1,2 +1,2 @@
 one
-two
+three
`)
	if err != nil {
		t.Fatal(err)
	}

	md, err := svc.GetSkillAtRef(context.Background(), "frontend-design", res.Suggestion.Branch, true)
	if err != nil {
		t.Fatal(err)
	}
	cf, ok := md.ContextFile("a/notes.md")
	if !ok {
		t.Fatal("a/notes.md is missing from the suggestion")
	}
	if cf.Content != "one\nthree\n" {
		t.Errorf("content = %q, want the edit applied to a/notes.md", cf.Content)
	}
}

func TestPatchErrors(t *testing.T) {
	tests := []struct {
		name  string
		in    SuggestInput
		wants []string
	}{
		{
			name:  "neither input",
			in:    SuggestInput{},
			wants: []string{"a change is required"},
		},
		{
			name: "both inputs",
			in: SuggestInput{
				Files: []FileEdit{{FilePath: "SKILL.md", Content: "x"}},
				Patch: descriptionPatch,
			},
			wants: []string{"either files or patch, not both"},
		},
		{
			name:  "not a patch at all",
			in:    SuggestInput{Patch: "please change the description\n"},
			wants: []string{"no file diffs"},
		},
		{
			name: "unknown file",
			in: SuggestInput{Patch: `--- a/references/missing.md
+++ b/references/missing.md
@@ -1 +1 @@
-a
+b
`},
			wants: []string{"not a file of skill", "SKILL.md", "references/old.txt"},
		},
		{
			name: "changes nothing",
			in: SuggestInput{Patch: `--- a/SKILL.md
+++ b/SKILL.md
@@ -1,2 +1,2 @@
 ---
 name: frontend-design
`},
			wants: []string{"changed nothing"},
		},
		{
			name: "escapes the skill directory",
			in: SuggestInput{Patch: `--- a/../../.github/workflows/ci.yml
+++ b/../../.github/workflows/ci.yml
@@ -1 +1 @@
-a
+b
`},
			wants: []string{"not a file of skill"},
		},
		{
			name: "context does not match",
			in: SuggestInput{Patch: `--- a/SKILL.md
+++ b/SKILL.md
@@ -1,5 +1,5 @@
 ---
 name: frontend-design
-description: something else entirely
+description: designs frontends, fixed
 ---
 body
`},
			wants: []string{"does not apply", "hunk #1", "base branch", "get_skill_at_ref"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newSeededService(t)

			in := tt.in
			in.SkillName, in.AgentID, in.SuggestionID = "frontend-design", "agent-1", "attempt"
			_, err := svc.RecordSuggestion(context.Background(), in)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q is missing %q", err, want)
				}
			}
		})
	}
}

// TestPatchAlreadyAppliedIsCalledOut: an agent that lost track of what it
// already recorded gets told so, rather than a bare context mismatch.
func TestPatchAlreadyAppliedIsCalledOut(t *testing.T) {
	svc := newSeededService(t)

	if _, err := suggestPatch(t, svc, "agent-1", "fix-description", descriptionPatch); err != nil {
		t.Fatal(err)
	}
	_, err := suggestPatch(t, svc, "agent-1", "fix-description", descriptionPatch)
	if err == nil {
		t.Fatal("expected the repeated patch to be rejected")
	}
	if !strings.Contains(err.Error(), "already be present") {
		t.Errorf("error %q does not point out that the change is already there", err)
	}
}

// TestPatchAgainstServedSkillNamesTheFooter: the likeliest way to compute an
// unapplyable patch is to diff against skillsd's served copy, whose SKILL.md
// carries a footer that is not in the repository.
func TestPatchAgainstServedSkillNamesTheFooter(t *testing.T) {
	svc := newSeededService(t)

	_, err := suggestPatch(t, svc, "agent-1", "fix-description", `--- a/SKILL.md
+++ b/SKILL.md
@@ -5,4 +5,4 @@
 body

 ## Improving this skill
-This skill was served by skillsd.
+This skill was served by skillsd, allegedly.
`)
	if err == nil {
		t.Fatal("expected the patch to be rejected")
	}
	if !strings.Contains(err.Error(), onboardingMarker) {
		t.Errorf("error %q does not name the footer", err)
	}
}

func TestPatchRespectsMaxFileBytes(t *testing.T) {
	svc := newSeededService(t)
	svc.maxFileBytes = 32

	_, err := suggestPatch(t, svc, "agent-1", "too-big", descriptionPatch)
	if err == nil {
		t.Fatal("expected an oversized patch to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeding the 32 byte limit") {
		t.Errorf("error %q does not name the limit", err)
	}
}

// TestRecordSuggestionRejectsEscapingFilePath: a file path is joined onto the
// skill's directory before it is committed, so one that climbs out of it would
// otherwise stage a change to something the caller was never editing.
func TestRecordSuggestionRejectsEscapingFilePath(t *testing.T) {
	svc := newSeededService(t)

	for _, filePath := range []string{
		"../../.github/workflows/ci.yml",
		"/etc/passwd",
		".git/config",
		"",
	} {
		_, err := svc.RecordSuggestion(context.Background(), SuggestInput{
			SkillName:    "frontend-design",
			AgentID:      "agent-1",
			SuggestionID: "escape",
			Files:        []FileEdit{{FilePath: filePath, Content: "x"}},
		})
		if err == nil {
			t.Errorf("file path %q was accepted", filePath)
			continue
		}
		if !strings.Contains(err.Error(), "file path") {
			t.Errorf("file path %q: error %q does not name the path", filePath, err)
		}
	}
}
