// Package submit turns a proposal into a pull request: pushing its branch
// and endorsement refs upstream, opening the pull request, and recording
// that it was opened.
//
// It sits between internal/proposals (which owns git) and internal/githubpr
// (which owns the forge API) because it needs both, and because the same
// sequence is reached two ways - an explicit submit call, and the
// auto-submit path that fires when enough agents corroborate a proposal.
// Neither should be able to open a second pull request for a branch that
// already has one.
package submit

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/go-git/go-git/v5/plumbing"
)

// Submitter opens pull requests for proposals.
type Submitter struct {
	proposals  *proposals.Service
	github     *githubpr.Client
	baseBranch string
}

// New returns a Submitter that opens pull requests against baseBranch.
func New(svc *proposals.Service, gh *githubpr.Client, baseBranch string) *Submitter {
	return &Submitter{proposals: svc, github: gh, baseBranch: baseBranch}
}

// Submit pushes p's branch and opens a pull request for it, or returns the
// pull request it already has.
//
// The already-submitted check is what makes this safe to call from both the
// explicit path and the auto-submit path without either opening a second
// pull request for the same branch. Like every other fact about a proposal,
// the record of submission is a ref in the repository, not a row somewhere.
func (s *Submitter) Submit(ctx context.Context, p *proposals.Proposal, title, body string) (*proposals.Submission, error) {
	if existing, ok, err := s.proposals.Submission(p.Branch); err != nil {
		return nil, fmt.Errorf("checking for an existing pull request: %w", err)
	} else if ok {
		return existing, nil
	}

	if err := s.proposals.Push(ctx, p.Branch); err != nil {
		return nil, fmt.Errorf("pushing branch: %w", err)
	}

	if title == "" {
		title = DefaultTitle(p)
	}
	if body == "" {
		body = DefaultBody(p)
	}

	pr, err := s.github.CreatePullRequest(ctx, githubpr.PullRequestInput{
		Title: title,
		Body:  body,
		Head:  p.Branch,
		Base:  s.baseBranch,
	})
	if err != nil {
		return nil, fmt.Errorf("opening pull request: %w", err)
	}

	if err := s.proposals.MarkSubmitted(p.Branch, plumbing.NewHash(p.HeadSHA), pr.URL, pr.Number); err != nil {
		// The pull request exists; failing the call now would tell the
		// caller nothing happened when something did. Log instead - the
		// cost of a lost marker is a duplicate-PR attempt later, which
		// GitHub itself rejects.
		slog.Error("could not record submission marker", "branch", p.Branch, "error", err)
	}

	return &proposals.Submission{
		PullRequestURL:    pr.URL,
		PullRequestNumber: pr.Number,
	}, nil
}

// DefaultTitle is used when the caller supplies no pull request title.
func DefaultTitle(p *proposals.Proposal) string {
	return fmt.Sprintf("skillsd: propose changes to %s (%s)", p.SkillName, p.AgentID)
}

// DefaultBody writes not just what changed, but how many independent
// agents arrived at it and which recorded failures it claims to fix. That
// corroboration is the reason the pull request is worth a reviewer's time,
// and it is invisible from the diff alone.
func DefaultBody(p *proposals.Proposal) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Proposed by agent `%s` via skillsd-registry.\n\n", p.AgentID)

	if n := p.Corroboration; n > 1 {
		fmt.Fprintf(&b, "**Independently proposed by %d agents.** Each arrived at identical "+
			"content without seeing the others' work:\n\n- `%s` (opened this proposal)\n", n, p.AgentID)
		for _, e := range p.Endorsements {
			if !e.Stale {
				fmt.Fprintf(&b, "- `%s`\n", e.AgentID)
			}
		}
		b.WriteString("\n")
	}

	if ids := p.MotivatingReportIDs; len(ids) > 0 {
		fmt.Fprintf(&b, "Motivated by %d recorded outcome report(s): %s\n\n",
			len(ids), "`"+strings.Join(ids, "`, `")+"`")
	}

	b.WriteString("Commits:\n")
	for _, c := range p.Commits {
		sha := c.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		fmt.Fprintf(&b, "- %s: %s\n", sha, firstLine(c.Message))
	}

	if p.SourceThreadURI != "" {
		fmt.Fprintf(&b, "\nSource conversation: %s\n", p.SourceThreadURI)
	}
	return b.String()
}

// firstLine keeps commit trailers (Source-Thread, Motivated-By) out of the
// bulleted commit list, where they'd repeat what the sections above say.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
