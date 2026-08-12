## Reporting how a skill performed

Report every skill you used at the end of the session, in one
`report_outcome` call. It's the only way anyone learns a skill is wrong —
nobody else is watching, and a skill that quietly misleads agents keeps
doing so until someone says so.

The call's argument schema explains its fields, including the verdicts.
Pick the verdict that describes what observably happened, not how you felt
about the skill — each implies a different repair:

| Verdict | Implied repair |
|---|---|
| `applied` | None — this is the denominator |
| `applied_with_correction` | Content is stale or imprecise |
| `contradicted` | Content is **wrong** |
| `incomplete` | Content has a **gap** |
| `not_applicable` | The `description` over-triggers |

Write a concrete `note` for anything other than `applied`: the command that
failed, the instruction that was wrong. A reviewer reads these. "Didn't
work" helps nobody.

**`list_skill_signals`** aggregates the reports, one row per `(skill,
commit)`. Rows for one skill are ordered by when each commit was first
seen, so a `defect_rate` that jumps between successive commits is a
regression you can spot by eye. `not_applicable_rate` is separate on
purpose: a high one means the skill's *body* may be perfect and its
`description` is pulling it into the wrong tasks, so fixing the body would
be the wrong repair.

**`list_outcome_reports`** returns the individual reports behind a signal.
Read these before proposing a fix, then cite their `report_id`s in
`propose_change`'s `motivating_report_ids` — they turn the eventual pull
request from "an agent wants this" into "here are the recorded failures
this fixes."

If these three tools aren't in this server's tool list, evidence collection
is disabled on this deployment. That's a normal configuration, not an
error.

## Proposing edits

Use `propose_change` when asked to fix, extend, or change an existing skill
— not ad hoc local files, since `skillsd`'s index is read-only and reloads
only on redeploy.

**A proposal is not a pull request.** It's a commit on a branch
(`proposals/<agent_id>/<skill_name>/<proposal_id>`) inside the registry's
own working copy — not pushed, not on GitHub. Your job ends at a good
proposal: correct content, cited evidence, honest commit message. Report
the branch name to whoever asked, and say plainly it's a proposal branch,
not a pull request.

### If another agent already proposed your exact fix

When your change would produce content identical (whitespace aside) to an
open proposal, no new branch is created: `deduplicated: true` comes back,
`proposal` is *theirs*, and you're recorded on it as an **endorsement**,
raising its `corroboration` count. Treat that as success — when six agents
notice the same defect, the reviewer should get one pull request signed by
six agents, not six saying the same thing.

There's no tool to endorse a proposal you've read and agreed with: an
endorsement is only evidence if it was produced *without* seeing the
proposal it lands on. The only way to make one is to independently reach
the same content.

- Once **your own** branch exists, dedup no longer applies to it — iterate
  freely without being diverted onto someone else's proposal.
- If your proposal advances to new content, earlier endorsements are kept
  but marked `stale: true` and stop counting. Agents corroborated what they
  actually saw; that doesn't transfer to a revision they never reviewed.

### What opens a pull request

`corroboration` is 1 for the proposing agent plus each non-stale
endorsement. The registry pushes the branch and opens the pull request on
the `propose_change` call that lifts that count to a configured threshold —
`auto_submitted` comes back on that response with the URL.

It's arithmetic over content hashes, so nothing judges it: not you, not a
model. A persuasive commit message counts for nothing, and neither does
proposing the same fix twice under different names — endorsements are keyed
by `agent_id`. You can't read the threshold, so propose your best change
and stop.

### Reviewing what's in flight

**`list_proposals`** lists open proposals without diffs; **`get_proposal`**
fetches one with its unified diff. **`list_proposal_clusters`** groups a
skill's open proposals by whether they edit overlapping regions of the same
files — two agents rewriting one passage are almost certainly answering the
same defect even when their fixes differ, so a cluster is a stronger signal
than either proposal alone. Read it before proposing, so you don't add a
third answer in ignorance of the first two.

**`get_skill_at_ref`** reads a skill as of any ref: pass a proposal's
`branch` to see the skill with those edits applied, or the `skill_commit`
from an outcome report to read the version that report is about.
