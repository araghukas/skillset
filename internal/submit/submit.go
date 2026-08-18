// Package submit turns a suggestion into a pull request: pushing its branch
// and endorsement refs upstream, opening the pull request, and recording
// that it was opened.
//
// It sits between internal/suggestions (which owns git) and
// internal/githubpr (which owns the forge API) because it needs both. The
// whole sequence is driven by the registry itself, once enough agents stand
// behind a suggestion - its author plus its endorsers; no tool opens a pull
// request directly.
package submit

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/suggestions"
	"github.com/go-git/go-git/v5/plumbing"
)

// Submitter opens pull requests for suggestions.
type Submitter struct {
	suggestions *suggestions.Service
	github      *githubpr.Client
	baseBranch  string
}

// New returns a Submitter that opens pull requests against baseBranch.
func New(svc *suggestions.Service, gh *githubpr.Client, baseBranch string) *Submitter {
	return &Submitter{suggestions: svc, github: gh, baseBranch: baseBranch}
}

// Submit pushes sg's branch and opens a pull request for it, or returns the
// pull request it already has.
//
// The already-submitted check makes this idempotent: a suggestion sits at
// or above the corroboration threshold for every subsequent call that
// touches it, so this is reached repeatedly for a branch that already has a
// pull request. Like every other fact about a suggestion, the record of
// submission is a ref in the repository, not a row somewhere.
func (s *Submitter) Submit(ctx context.Context, sg *suggestions.Suggestion) (*suggestions.Submission, error) {
	if existing, ok, err := s.suggestions.Submission(sg.Branch); err != nil {
		return nil, fmt.Errorf("checking for an existing pull request: %w", err)
	} else if ok {
		return existing, nil
	}

	if err := s.suggestions.Push(ctx, sg.Branch); err != nil {
		return nil, fmt.Errorf("pushing branch: %w", err)
	}

	pr, err := s.github.CreatePullRequest(ctx, githubpr.PullRequestInput{
		Title: title(sg),
		Body:  body(sg),
		Head:  sg.Branch,
		Base:  s.baseBranch,
	})
	if err != nil {
		return nil, fmt.Errorf("opening pull request: %w", err)
	}

	if err := s.suggestions.MarkSubmitted(sg.Branch, plumbing.NewHash(sg.HeadSHA), pr.URL, pr.Number); err != nil {
		// The pull request exists; failing the call now would tell the
		// caller nothing happened when something did. Log instead - the
		// cost of a lost marker is a duplicate-PR attempt later, which
		// GitHub itself rejects.
		slog.Error("could not record submission marker", "branch", sg.Branch, "error", err)
	}

	return &suggestions.Submission{
		PullRequestURL:    pr.URL,
		PullRequestNumber: pr.Number,
	}, nil
}

// title names the skill and the agent that opened the suggestion.
func title(sg *suggestions.Suggestion) string {
	return fmt.Sprintf("skillsd: suggested changes to %s (%s)", sg.SkillName, sg.AgentID)
}

// body writes not just what changed, but how many agents stand behind it
// and which recorded failures it claims to fix. That corroboration is the
// reason the pull request is worth a reviewer's time, and it is invisible
// from the diff alone.
func body(sg *suggestions.Suggestion) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Suggested by agent `%s` via skillsd-registry.\n\n", sg.AgentID)

	if n := sg.Corroboration; n > 1 {
		fmt.Fprintf(&b, "**Backed by %d agents.** Each endorser read this exact diff and "+
			"approved it as-is:\n\n- `%s` (recorded this suggestion)\n", n, sg.AgentID)
		for _, e := range sg.Endorsements {
			if !e.Stale {
				fmt.Fprintf(&b, "- `%s`\n", e.AgentID)
			}
		}
		b.WriteString("\n")
	}

	if ids := sg.MotivatingReportIDs; len(ids) > 0 {
		fmt.Fprintf(&b, "Motivated by %d recorded outcome report(s): %s\n\n",
			len(ids), "`"+strings.Join(ids, "`, `")+"`")
	}

	b.WriteString("Commits:\n")
	for _, c := range sg.Commits {
		sha := c.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		fmt.Fprintf(&b, "- %s: %s\n", sha, firstLine(c.Message))
	}

	if sg.SourceThreadURI != "" {
		fmt.Fprintf(&b, "\nSource conversation: %s\n", sg.SourceThreadURI)
	}
	return b.String()
}

// firstLine keeps commit trailers (Source-Thread, Motivated-By) out of the
// bulleted commit list, where they'd repeat what the sections above say.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
