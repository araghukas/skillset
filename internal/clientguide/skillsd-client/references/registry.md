## Reporting how a skill performed

At the end of a session, report what happened to every skill you used. This
costs you one call and is the only way anyone learns that a skill is wrong:
nobody is watching your session, and a skill that quietly misleads agents
will keep doing so until someone says so.

**`report_outcome(report_id, agent_id, session_id, skills)`** — one call per
session, listing every skill the session used.

- `report_id` is a UUID **you** generate, once, before the first attempt. It
  makes the call idempotent: if it fails or times out, retry with the *same*
  ID and nothing is double-counted. Don't generate a fresh one on retry.
- `skills` is a list of `{skill_name, skill_commit, verdict, note}`. The
  `skill_commit` is the `commit` field you kept when you loaded the skill
  from `skillsd`.
- **Include the skills that worked.** They are the denominator. If you only
  report failures, every rate computed from your reports is meaningless.

Pick the verdict that describes what observably happened, not how you felt
about the skill. Each one implies a different repair:

| Verdict | When | What it says is wrong |
|---|---|---|
| `applied` | Followed it, nothing contradicted it | Nothing — this is the denominator |
| `applied_with_correction` | Followed it, but had to adjust or work around part of it | Content is stale or imprecise |
| `contradicted` | Reality disagreed: a documented command failed, a named API didn't exist | Content is **wrong** |
| `incomplete` | On-topic, but didn't cover your case; you went outside it | Content has a **gap** |
| `not_applicable` | You loaded it, then it turned out irrelevant | The `description` over-triggers |

Write a concrete `note` for anything other than `applied`: the command that
failed, the instruction that was wrong. A reviewer reads these. "Didn't
work" helps nobody.

A tool error naming a commit mismatch usually means the registry hasn't
fetched the commit you named yet. Retry in a few minutes with the same
`report_id`.

**`list_skill_signals(skill_name?, min_reported_sessions?)`** — the
aggregate, one row per `(skill, commit)`: `reported_sessions`,
`verdict_counts`, `defect_rate`, and `not_applicable_rate`. **This is the
"what should I fix next" query.** Rows come back ordered by skill, then by
when each commit was first observed, so a `defect_rate` that jumps between
two successive commits of one skill is a regression you can see by eye.

Note that `defect_rate` and `not_applicable_rate` are deliberately separate:
a high `not_applicable_rate` means the skill's *body* may be perfect and its
frontmatter `description` is pulling it into the wrong tasks. Fixing the
body would be the wrong repair.

These are rates among sessions that **reported**, never among sessions —
reporting is voluntary and a crashed session never reports. Don't present
them as true rates.

**`list_outcome_reports(skill_name, skill_commit?, verdict?,
exclude_empty_notes?, limit?)`** — the individual reports behind a signal.
Call this before proposing a fix: read what actually went wrong, then cite
those `report_id`s in `propose_change`'s `motivating_report_ids`. Set
`exclude_empty_notes: true` to skip the `applied` rows.

If the evidence tools (`report_outcome`, `list_skill_signals`,
`list_outcome_reports`) don't appear in this server's tool list at all,
evidence collection is disabled on this deployment — there's no reporting
path here, and that's a normal configuration, not an error.

## Proposing edits

Use this when asked to fix, extend, or otherwise change an existing skill —
not to create ad hoc local files, since `skillsd`'s index is read-only and
reloads only on redeploy. A proposal is a named branch
(`proposals/<agent_id>/<skill_name>/<proposal_id>`) holding one or more
commits; nothing touches the base branch until a human merges the pull
request you optionally open at the end.

**`propose_change(skill_name, agent_id, proposal_id, files,
commit_message?, source_thread_uri?, motivating_report_ids?,
allow_duplicate?)`** — commits full new file contents to a skill's proposal
branch, creating the branch from the base branch's current HEAD if it
doesn't exist yet, or appending a commit otherwise. Returns `{proposal,
deduplicated, auto_submitted}`.

- `files` is a list of `{file_path, content, deleted}` — always send the
  **complete new content** of each changed file, never a patch/diff; the
  server computes the diff itself. Set `deleted: true` to remove a file
  (content is ignored in that case).
- `agent_id`, `skill_name`, and `proposal_id` are caller-chosen and must not
  contain `/` (they form the branch name). Reuse the same `(agent_id,
  proposal_id)` to append more commits to the same in-progress proposal, or
  pick a new `proposal_id` to start a separate one.
- `motivating_report_ids` are `report_id`s from `list_outcome_reports` that
  justify this change. **Set these whenever you have them.** They're what
  turns the pull request from "an agent wants this" into "here are the
  recorded failures this fixes," and a reviewer sees them without leaving
  GitHub.
- `source_thread_uri` is an optional pointer back to the conversation that
  produced the change — set it if you have somewhere durable to point to;
  it gets folded into the pull request body by `submit_proposal`.
- Committing identical content to a branch that already has it fails with
  "cannot create empty commit" — expected, not a bug; it means your change
  is already committed.

### If another agent already proposed your exact fix

Before creating a *new* branch, the server hashes the content your change
would produce and compares it against every open proposal for that skill. If
another agent's proposal already produces identical content (whitespace
differences don't count), **you don't get a branch**. Instead:

- `deduplicated: true` comes back, and `proposal` is *their* proposal, not
  yours.
- You are recorded on it as an **endorsement**, and its `corroboration`
  count goes up.

This is intended, and it is the point: when six agents notice the same
defect, the reviewer should get one pull request signed by six agents, not
six pull requests saying the same thing. Treat a deduplicated response as
success — your finding was recorded, and it made the existing proposal more
credible than it was a moment ago.

Note there is no tool to endorse a proposal you have merely read and agreed
with. An endorsement only means anything as evidence if it was produced
*without* seeing the proposal it lands on, so the only way to create one is
to independently arrive at the same content.

Two consequences worth knowing:

- Once **your own** branch exists, dedup no longer applies to it — you can
  iterate freely without being diverted onto someone else's proposal.
- If your proposal advances to new content, earlier endorsements are kept
  but marked `stale: true` and stop counting. Agents corroborated what they
  actually saw; that agreement doesn't transfer to a revision they never
  reviewed.

`allow_duplicate: true` forces a branch of your own anyway. Rarely what you
want.

If enough agents independently converge on one proposal, the deployment may
open the pull request automatically — `auto_submitted` will be set on the
response that crossed the threshold. Most deployments leave this off.

**`list_proposals(skill_name?, agent_id?)`** — list proposals, optionally
filtered by either field, to check what's already in flight before starting
a new one. Diffs are omitted here; call `get_proposal` for one proposal's
diff. Each proposal carries its `content_hash`, its `endorsements`, and its
`corroboration` count (1 for the proposer, plus each non-stale endorser).

**`list_proposal_clusters(skill_name?, include_singletons?)`** — groups a
skill's open proposals by whether they edit *overlapping regions of the same
files*. Two agents rewriting the same passage are almost certainly answering
the same defect even when their fixes differ, so the cluster is a stronger
signal than either proposal alone. Clusters come back sorted by
`distinct_agents`, descending: it's a review queue, most-contested first.

Use it before proposing, to see whether the thing you're about to fix is
already contested — and to read the competing answers rather than adding a
third in ignorance of the first two. `contested_paths` names the files more
than one proposal in the cluster touches. Singleton clusters are omitted
unless you ask for them, since the point is to surface contention.

**`get_proposal(branch, omit_diff?, max_diff_bytes?)`** — fetch a single
proposal by its full branch name (as returned in `proposal.branch`),
including its unified `diff` against the base branch and its `commits`. Use
this to review what you (or another agent) have staged before submitting.
The diff is included by default; `omit_diff: true` skips it when you only
need metadata. A large diff is truncated at a hunk boundary and flagged with
`diff_truncated: true` — raise `max_diff_bytes` to see more of it.

**`get_skill_at_ref(skill_name, ref?, include_context_files?, paths?,
max_bytes?)`** — same shape as `get_skill`, but as of an arbitrary `ref` (a
branch name or commit SHA) instead of the base branch. Pass a proposal's
`branch` here to see the skill *with your pending edits applied*, e.g. to
sanity-check the result before submitting, or to diff two proposals against
each other. Empty `ref` resolves to the base branch HEAD (equivalent to
`get_skill`).

**`submit_proposal(branch, pr_title?, pr_body?)`** — pushes the proposal
branch upstream and opens a GitHub pull request for human review.
`pr_title`/`pr_body` default from the proposal's commits (and
`source_thread_uri`, if set) when left empty. This is the one call with a
side effect visible outside these two servers — it creates a real pull
request against a real repo (see "Repository configuration" below). Safe
to retry: if a pull request already exists for the branch, the existing
one is returned rather than a second being opened.

If `skillsd-registry` is running without a configured GitHub token, this
tool refuses with a clear error (propose/inspect still work); treat that as
"proposal is ready, but I can't open the pull request myself — hand the
branch name to a human."
