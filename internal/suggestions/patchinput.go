package suggestions

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/araghukas/skillset/internal/patch"
	"github.com/go-git/go-git/v5/plumbing"
)

// onboardingMarker is the heading skillsd appends to every SKILL.md it serves.
// It is not in the repository, so a patch computed against a served copy
// carries context no base content can match. Naming it in the error turns the
// likeliest patch failure into one the caller can fix on its own.
const onboardingMarker = "## Improving this skill"

// expandPatch turns a unified diff into the same full-content file set the
// files input carries, so everything downstream - the commit, clustering -
// sees one kind of request.
//
// at is the commit the patch applies to: the caller's own suggestion branch
// when they are iterating on one, the base branch otherwise. onOwnBranch says
// which, and shapes the error a failed apply returns, since the two cases have
// different answers to "what should I have diffed against".
//
// The tree is read here and committed later, outside one lock. A suggestion
// branch has a single writer - it is namespaced by the agent's own ID - so the
// window only opens if one agent runs two calls against the same suggestion at
// once, where the second would expand against content the first has since
// replaced and quietly undo it. Closing it needs a repo API that reads and
// commits together.
func (s *Service) expandPatch(ctx context.Context, req SuggestInput, branch string, at plumbing.Hash, onOwnBranch bool) ([]FileEdit, error) {
	current, err := s.skillContentAt(ctx, req.SkillName, at)
	if err != nil {
		return nil, fmt.Errorf("suggestions: a patch needs existing content to apply to, and skill %q does not exist at %s; send files with full content to create it",
			req.SkillName, at.String()[:7])
	}

	files, err := patch.Parse(req.Patch)
	if err != nil {
		return nil, fmt.Errorf("suggestions: %w", err)
	}
	if err := s.resolvePatchPaths(files, current, req.SkillName); err != nil {
		return nil, err
	}

	changes, err := patch.Apply(files, current)
	if err != nil {
		return nil, s.applyFailed(err, files, current, req, branch, at, onOwnBranch)
	}

	edits := make([]FileEdit, 0, len(changes))
	var changed bool
	for _, c := range changes {
		if c.Deleted || current[c.Path] != c.Content {
			changed = true
		}
		edits = append(edits, FileEdit{FilePath: c.Path, Deleted: c.Deleted, Content: c.Content})
	}
	if !changed {
		return nil, fmt.Errorf("suggestions: the patch applied cleanly but changed nothing; skill %q already has this content", req.SkillName)
	}

	return edits, nil
}

// skillContentAt reads a skill's files at a commit, keyed relative to the
// skill directory - the same paths a caller's patch and file edits use.
func (s *Service) skillContentAt(ctx context.Context, skillName string, at plumbing.Hash) (map[string]string, error) {
	byRepoPath, err := s.skillFilesAt(ctx, skillName, at)
	if err != nil {
		return nil, err
	}
	if len(byRepoPath) == 0 {
		return nil, fmt.Errorf("suggestions: skill %q has no files at %s", skillName, at)
	}

	dirPrefix := path.Join(s.subPath, skillName) + "/"
	out := make(map[string]string, len(byRepoPath))
	for repoPath, content := range byRepoPath {
		out[strings.TrimPrefix(repoPath, dirPrefix)] = string(content)
	}
	return out, nil
}

// patchPathRecipe is restated in every path error: an agent that got a path
// wrong is exactly the one that needs the layout spelled out again.
const patchPathRecipe = `Mirror the skill under two directories named "a" and "b" - each file at its path relative to the skill directory - edit only under "b", and diff from their parent with "git diff --no-index a b" or "diff -ru a b". Paths then arrive relative to the skill directory.`

// resolvePatchPaths rewrites each file section's paths to be relative to the
// skill directory.
//
// Callers mirror the skill under roots named a and b, which is the layout git's
// own prefixes describe, so paths arrive skill-relative once the one-letter
// prefix is dropped. Every candidate below is checked against content that
// exists rather than inferred from the shape of the path: a patch either names
// a file of this skill or is rejected. Guessing would let a diff of a scratch
// copy land on a same-named file of a different skill.
func (s *Service) resolvePatchPaths(files []patch.File, current map[string]string, skillName string) error {
	for i := range files {
		f := &files[i]
		if f.Created() {
			// A new file has no content to be matched against, so its path is
			// taken as given once the skill directory is trimmed off, and
			// validated by cleanSkillRelPath before anything is written.
			trimmed, _ := s.trimSkillDir(f.Path(), skillName)
			setPaths(f, trimmed)
			continue
		}
		resolved, err := s.resolveExisting(f.Path(), current, skillName)
		if err != nil {
			return err
		}
		setPaths(f, resolved)
	}

	return nil
}

// setPaths rewrites a section's paths to resolved, preserving which sides the
// section says the file exists on.
func setPaths(f *patch.File, resolved string) {
	if f.OldPath != "" {
		f.OldPath = resolved
	}
	if f.NewPath != "" {
		f.NewPath = resolved
	}
}

func (s *Service) resolveExisting(p string, current map[string]string, skillName string) (string, error) {
	for _, cand := range s.pathCandidates(p, skillName) {
		if _, ok := current[cand]; ok {
			return cand, nil
		}
	}
	return "", fmt.Errorf("suggestions: the patch changes %q, which is not a file of skill %q (its files: %s).\n%s",
		p, skillName, strings.Join(sortedKeys(current), ", "), patchPathRecipe)
}

// pathCandidates lists the skill-relative paths p could name, most literal
// first.
func (s *Service) pathCandidates(p, skillName string) []string {
	out := []string{p}
	if trimmed, ok := s.trimSkillDir(p, skillName); ok {
		out = append(out, trimmed)
	}
	// patch.Parse strips git's one-letter prefix, which also strips a real
	// leading directory named "a" or "b". Restore it if that is what p was.
	out = append(out, "a/"+p, "b/"+p)
	return out
}

// trimSkillDir drops a leading skill directory, with or without the registry's
// skills subpath in front of it.
func (s *Service) trimSkillDir(p, skillName string) (string, bool) {
	for _, prefix := range []string{path.Join(s.subPath, skillName) + "/", skillName + "/"} {
		if trimmed, ok := strings.CutPrefix(p, prefix); ok {
			return trimmed, true
		}
	}
	return p, false
}

// applyFailed turns a failed apply into a message that says what the patch was
// applied to and what to do about it. The caller diffed against *something*;
// everything useful here is about naming what the server has instead.
func (s *Service) applyFailed(err error, files []patch.File, current map[string]string, req SuggestInput, branch string, at plumbing.Hash, onOwnBranch bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "suggestions: %s", err)

	if onOwnBranch {
		fmt.Fprintf(&b, "\nThe patch was applied to skill %q as it stands on your own suggestion branch %s (head %s), which is what your previous record_suggestion call left there.",
			req.SkillName, branch, at.String()[:7])
	} else {
		fmt.Fprintf(&b, "\nThe patch was applied to skill %q as it stands on the base branch (commit %s).",
			req.SkillName, at.String()[:7])
	}

	for _, hint := range applyHints(err, files, current) {
		b.WriteString("\n")
		b.WriteString(hint)
	}

	ref := ""
	if onOwnBranch {
		ref = fmt.Sprintf(", ref: %q", branch)
	}
	fmt.Fprintf(&b, "\nRe-read it with get_skill_at_ref({skill_name: %q%s, include_context_files: true}), diff against that, or send files with full content instead.\n%s",
		req.SkillName, ref, patchPathRecipe)

	return errors.New(b.String())
}

// applyHints names the specific causes worth guessing at, each of which has a
// different fix and none of which a caller can see from the diff alone.
func applyHints(err error, files []patch.File, current map[string]string) []string {
	var hints []string

	if patch.AlreadyApplied(files, current) {
		hints = append(hints, "This change appears to already be present, so there may be nothing left to record.")
	}

	var aerr *patch.ApplyError
	if errors.As(err, &aerr) {
		if containsMarker(aerr.Expected, onboardingMarker) && !hasMarker(current, onboardingMarker) {
			hints = append(hints, "The patch's context includes skillsd's \""+onboardingMarker+"\" footer, which skillsd appends when serving a skill and the registry does not store. Strip it from your copy before diffing.")
		}
	}

	for _, content := range current {
		if strings.Contains(content, "\r\n") {
			hints = append(hints, "This file uses CRLF line endings; a patch with LF endings will not match it.")
			break
		}
	}

	return hints
}

func containsMarker(lines []string, marker string) bool {
	for _, l := range lines {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}

func hasMarker(files map[string]string, marker string) bool {
	for _, content := range files {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
