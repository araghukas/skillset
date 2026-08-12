## Reporting how a skill performed

Report every skill you used at the end of the session. It's one call, and the
only way anyone learns a skill is wrong — nobody else is watching, and a skill
that quietly misleads agents keeps doing so until someone says so.

**`report_outcome(report_id, agent_id, session_id, skills)`** — one call per
session, listing every skill the session used.

- `report_id` is a UUID **you** generate once, before the first attempt. It
  makes the call idempotent: on failure or timeout, retry with the *same* ID
  and nothing is double-counted. Don't generate a fresh one on retry.
- `skills` is a list of `{skill_name, skill_commit, verdict, note}`.
  `skill_commit` is the `commit` you kept when you loaded the skill.
- **Include the skills that worked.** They are the denominator. Report only
  failures and every rate computed from your reports is meaningless.

Pick the verdict that describes what observably happened, not how you felt
about the skill. Each implies a different repair:

| Verdict | When | What it says is wrong |
|---|---|---|
| `applied` | Followed it, nothing contradicted it | Nothing — this is the denominator |
| `applied_with_correction` | Followed it, but had to adjust part of it | Content is stale or imprecise |
| `contradicted` | A documented command failed, a named API didn't exist | Content is **wrong** |
| `incomplete` | On-topic, but didn't cover your case | Content has a **gap** |
| `not_applicable` | You loaded it, then it turned out irrelevant | The `description` over-triggers |

Write a concrete `note` for anything other than `applied`: the command that
failed, the instruction that was wrong. A reviewer reads these. "Didn't work"
helps nobody.

An error naming a commit mismatch usually means the registry hasn't fetched
that commit yet. Retry in a few minutes with the same `report_id`.

**`list_skill_signals(skill_name?, min_reported_sessions?)`** — the aggregate,
one row per `(skill, commit)`: `reported_sessions`, `verdict_counts`,
`defect_rate`, `not_applicable_rate`. **This is the "what should I fix next"
query.** Rows are ordered by skill, then by when each commit was first seen,
so a `defect_rate` that jumps between successive commits is a regression you
can spot by eye.

`not_applicable_rate` is separate on purpose: a high one means the skill's
*body* may be perfect and its `description` is pulling it into the wrong
tasks, so fixing the body would be the wrong repair. Both are rates among
sessions that **reported**, never among sessions — reporting is voluntary and
a crashed session never reports. Don't present them as true rates.

**`list_outcome_reports(skill_name, skill_commit?, verdict?,
exclude_empty_notes?, limit?)`** — the individual reports behind a signal.
Read these before proposing a fix, then cite their `report_id`s in
`propose_change`'s `motivating_report_ids`. Set `exclude_empty_notes: true` to
skip the `applied` rows.

If these three tools aren't in this server's tool list, evidence collection is
disabled on this deployment. That's a normal configuration, not an error.

## Proposing edits

Use this when asked to fix, extend, or change an existing skill — not to
create ad hoc local files, since `skillsd`'s index is read-only and reloads
only on redeploy.

**A proposal is not a pull request.** It's a commit on a branch
(`proposals/<agent_id>/<skill_name>/<proposal_id>`) inside the registry's own
working copy — not pushed, not on GitHub. No tool here pushes a branch or
opens one; the only code that writes to the forge runs inside the registry,
when a proposal's `corroboration` count reaches a configured threshold.

So your job ends at a good proposal: correct content, cited evidence, honest
commit message. Report the branch name to whoever asked, and say plainly it's
a proposal branch, not a pull request.

**`propose_change(skill_name, agent_id, proposal_id, files, commit_message?,
source_thread_uri?, motivating_report_ids?, allow_duplicate?)`** — commits
full new file contents to a skill's proposal branch, creating it from the base
branch's current HEAD if it doesn't exist, or appending a commit. Returns
`{proposal, deduplicated, auto_submitted}`.

- `files` is a list of `{file_path, content, deleted}` — always send each
  changed file's **complete new content**, never a patch; the server computes
  the diff. `deleted: true` removes a file (content is then ignored).
- `agent_id`, `skill_name`, and `proposal_id` are caller-chosen and must not
  contain `/`, since they form the branch name. Reuse `(agent_id,
  proposal_id)` to append commits to a proposal in progress; pick a new
  `proposal_id` for a separate one.
- `motivating_report_ids` are `report_id`s from `list_outcome_reports` that
  justify this change. **Set these whenever you have them.** They turn the
  pull request from "an agent wants this" into "here are the recorded failures
  this fixes." They ride along as commit trailers, so they survive even if
  those reports are later aged out.
- `source_thread_uri` optionally points back to the conversation behind the
  change; the registry folds it into the pull request body.
- Committing content a branch already has fails with "cannot create empty
  commit". That's expected — it means your change is already committed.

### If another agent already proposed your exact fix

Before creating a *new* branch, the server hashes the content your change
would produce and compares it against every open proposal for that skill. On a
match (whitespace aside) you get no branch: `deduplicated: true` comes back,
`proposal` is *theirs*, and you're recorded on it as an **endorsement**,
raising its `corroboration` count.

Treat that as success. When six agents notice the same defect, the reviewer
should get one pull request signed by six agents, not six saying the same
thing.

There's no tool to endorse a proposal you've read and agreed with: an
endorsement is only evidence if it was produced *without* seeing the proposal
it lands on. The only way to make one is to independently reach the same
content.

- Once **your own** branch exists, dedup no longer applies to it — iterate
  freely without being diverted onto someone else's proposal.
- If your proposal advances to new content, earlier endorsements are kept but
  marked `stale: true` and stop counting. Agents corroborated what they
  actually saw; that doesn't transfer to a revision they never reviewed.
- `allow_duplicate: true` forces a branch of your own anyway. Rarely what you
  want.

### What opens a pull request

`corroboration` is 1 for the proposing agent plus each non-stale endorsement.
The registry pushes the branch and opens the pull request on the
`propose_change` call that lifts that count to the threshold —
`auto_submitted` comes back on that response with the URL.

It's arithmetic over content hashes, so nothing judges it: not you, not a
model. A persuasive commit message counts for nothing, and neither does
proposing the same fix twice under different names — endorsements are keyed by
`agent_id`. You can't read the threshold, so propose your best change and
stop.

**`list_proposals(skill_name?, agent_id?)`** — list proposals, optionally
filtered by either field, to see what's already in flight before starting
something new. Diffs are omitted; each proposal carries its `content_hash`,
`endorsements`, and `corroboration` count.

**`list_proposal_clusters(skill_name?, include_singletons?)`** — groups a
skill's open proposals by whether they edit *overlapping regions of the same
files*. Two agents rewriting one passage are almost certainly answering the
same defect even when their fixes differ, so a cluster is a stronger signal
than either proposal alone. Sorted by `distinct_agents` descending: a review
queue, most-contested first. `contested_paths` names the files more than one
proposal touches, and singletons are omitted unless you ask.

Read it before proposing, so you don't add a third answer in ignorance of the
first two.

**`get_proposal(branch, omit_diff?, max_diff_bytes?)`** — fetch one proposal
by its full branch name (from `proposal.branch`), including its unified `diff`
against the base branch and its `commits`. Use it to review what another agent
staged before duplicating it. The diff is included by default; `omit_diff:
true` skips it. A large diff is cut at a hunk boundary and flagged with
`diff_truncated: true` — raise `max_diff_bytes` for more.

**`get_skill_at_ref(skill_name, ref?, include_context_files?, paths?,
max_bytes?)`** — the same shape as `get_skill` on `skillsd`, but as of any
`ref` (branch name or commit SHA). Pass a proposal's `branch` to see the skill
with those edits applied; pass the `skill_commit` from an outcome report to
read the version that report is about. Empty `ref` means the base branch HEAD.
