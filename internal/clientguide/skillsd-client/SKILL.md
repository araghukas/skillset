---
name: skillsd-client
description: Explains how to call skillsd's gRPC API (SkillService, ProposalService, EvidenceService) to discover skills, fetch a skill's full content, report how a skill actually performed after using it, and propose edits to a skill as a reviewable git branch / pull request. Use this skill whenever an agent has been given access to a skillsd/skillsd-registry endpoint and needs to know which RPCs to call, what fields to send, and how the discovery, reporting, and proposal workflows fit together — for example when asked to "find a skill for X", "load skill Y", "update/fix skill Y", "report that skill Y was wrong", or "submit a proposal/PR for skill Y".
---

# Using skillsd

`skillsd` is a read-only gRPC service that serves a directory of
[agentskills.io](https://agentskills.io)-compatible skills (each one a
`SKILL.md` plus optional `scripts/`, `references/`, `assets/` files).

A companion service, `skillsd-registry`, lets you propose edits to those
skills as real git commits and, optionally, open a GitHub pull request for
human review. **Neither service executes skill code on your behalf** — they
only serve and manage skill content. Running any scripts a skill ships with
is your responsibility, using whatever tools you already have.

This document is served by `skillsd` itself, via `SkillService.GetClientGuide`
— a dedicated RPC, separate from `ListSkills`/`GetSkill`. It ships embedded in
the server binary rather than coming from the skills repo `ListSkills` reads,
so it's always available and never drifts from the API it describes,
regardless of how the skills repo is configured. If you can read this, you
already have a working connection to `skillsd` and don't need to call
`ListSkills` to confirm that.

## Two services, two endpoints

| Service | Typical port | Purpose |
|---|---|---|
| `skills.v1.SkillService` | e.g. `:8080` | Discover skills, fetch their content, and fetch this guide (`GetClientGuide`). Always available. |
| `skills.v1.ProposalService` | e.g. `:8081`, often a separate deployment (`skillsd-registry`) | Propose edits to a skill's files as a branch, inspect the diff, and submit it as a pull request. May not be deployed at all — treat `Unimplemented`/connection failures as "read-only environment, no proposal path available." |
| `skills.v1.EvidenceService` | same endpoint as `ProposalService` | Report how skills performed in your session, and read the aggregated signal back to decide what's worth fixing. May be disabled; treat `Unimplemented` as "no reporting path here." |

Both are plain gRPC with server reflection enabled, so `grpcurl` (or any
gRPC client) can discover methods and message shapes at runtime without a
copy of the `.proto` files:

```bash
grpcurl -plaintext <host:port> list
grpcurl -plaintext <host:port> list skills.v1.SkillService
grpcurl -plaintext <host:port> list skills.v1.ProposalService
```

If reflection isn't available in your environment, ask the operator for the
`.proto` files or a generated client; the RPC/field names below won't change.

## Discovering and reading skills (`SkillService`)

**`ListSkills(category, include_context_files)`** — the entry point for "what
skills exist" or "find a skill for X". Returns `SkillMetadata` for every
skill: `name`, `description`, `license`, `compatibility`, `metadata` (a
free-form map), `allowed_tools`, and `json_schema` (a convenience projection
of `metadata["json_schema"]`, when a skill defines one for a strict
function-call interface). This guide itself is not part of that index —
fetch it with `GetClientGuide` instead.

- Leave `category` empty to list everything; set it to filter to skills whose
  `metadata["category"]` matches exactly.
- `include_context_files` defaults to leaving `context_files` empty — set it
  `true` only once you actually need file contents, since it inflates the
  response with every file in every skill directory. A full listing this way
  can exceed most gRPC clients' default 4 MiB max-receive-message-size
  (`skillsd` itself is typically configured to allow responses up to 8 MiB —
  check `grpcMaxRecvMsgSizeMiB` on your deployment). If you hit
  `ResourceExhausted: received message larger than max`, raise your client's
  max message size (e.g. `grpcurl -max-msg-sz <bytes>`) rather than assuming
  the server misbehaved.
- In practice: call `ListSkills({})` first (metadata only, cheap) to decide
  *which* skill you want, matching the request against each skill's
  `description` — that field is written to be matched against, the way a
  system prompt would describe when to reach for a tool. Then fetch that one
  skill's content with `GetSkill`, rather than setting
  `include_context_files: true` on `ListSkills` up front.

```bash
grpcurl -plaintext -d '{}' <host:port> skills.v1.SkillService/ListSkills
```

**`GetSkill(skill_name, include_context_files)`** — fetch one skill by its
directory name (== its frontmatter `name`, they're required to match).
Set `include_context_files: true` to get `SKILL.md` and everything alongside
it (`scripts/`, `references/`, `assets/`) as `SkillContextFile{file_path,
content, mime_type}` entries, `file_path` relative to the skill directory.
This is what actually loads the skill: read `SKILL.md`'s body for
instructions, then follow any references it makes to its other files by
`file_path`.

```bash
grpcurl -plaintext -d '{
  "skill_name": "internal-comms",
  "include_context_files": true
}' <host:port> skills.v1.SkillService/GetSkill
```

Binary/non-UTF-8 files (images, etc.) are silently omitted from
`context_files` since the field is a proto3 string — expect a skill's
`assets/` directory to be incomplete if it holds binaries. The same
max-message-size caveat as `ListSkills` applies here, though it's less
likely to bite since this is one skill's files rather than every skill's.

**Keep `SkillMetadata.commit`.** Every skill you fetch carries the git commit
its content came from. You need it later, in `ReportOutcome` — a report that
names a skill but not a version can't tell anyone whether a recent edit broke
something or it was always broken. Hold onto it for every skill you load.

**`GetClientGuide({})`** — fetches this document, always as of the running
server's build (no arguments; there's only ever one). Same response shape as
`GetSkill` (`GetSkillResponse{skill}`) so a client that already knows how to
render a `SkillMetadata`/`context_files` pair can render this one too.

```bash
grpcurl -plaintext -d '{}' <host:port> skills.v1.SkillService/GetClientGuide
```

## Reporting how a skill performed (`EvidenceService`)

At the end of a session, report what happened to every skill you used. This
costs you one call and is the only way anyone learns that a skill is wrong:
nobody is watching your session, and a skill that quietly misleads agents
will keep doing so until someone says so.

**`ReportOutcome(report_id, agent_id, session_id, skills)`** — one call per
session, listing every skill the session used.

- `report_id` is a UUID **you** generate, once, before the first attempt. It
  makes the call idempotent: if it fails or times out, retry with the *same*
  ID and nothing is double-counted. Don't generate a fresh one on retry.
- `skills` is a list of `{skill_name, skill_commit, verdict, note}`. The
  `skill_commit` is the `SkillMetadata.commit` you kept when you loaded it.
- **Include the skills that worked.** They are the denominator. If you only
  report failures, every rate computed from your reports is meaningless.

Pick the verdict that describes what observably happened, not how you felt
about the skill. Each one implies a different repair:

| Verdict | When | What it says is wrong |
|---|---|---|
| `VERDICT_APPLIED` | Followed it, nothing contradicted it | Nothing — this is the denominator |
| `VERDICT_APPLIED_WITH_CORRECTION` | Followed it, but had to adjust or work around part of it | Content is stale or imprecise |
| `VERDICT_CONTRADICTED` | Reality disagreed: a documented command failed, a named API didn't exist | Content is **wrong** |
| `VERDICT_INCOMPLETE` | On-topic, but didn't cover your case; you went outside it | Content has a **gap** |
| `VERDICT_NOT_APPLICABLE` | You loaded it, then it turned out irrelevant | The `description` over-triggers |

Write a concrete `note` for anything other than `APPLIED`: the command that
failed, the instruction that was wrong. A reviewer reads these. "Didn't work"
helps nobody.

```bash
grpcurl -plaintext -d '{
  "report_id": "b1f0c8e2-4a1d-4f3a-9c77-2b1e6d0a55d1",
  "agent_id": "agent-1",
  "session_id": "sess-2291",
  "skills": [
    {"skill_name": "postgres-migrations", "skill_commit": "9a3c1f2...",
     "verdict": "VERDICT_CONTRADICTED",
     "note": "Skill says run `migrate up` before seeding; that fails with \"relation exists\" unless --baseline is passed first."},
    {"skill_name": "incident-comms", "skill_commit": "9a3c1f2...",
     "verdict": "VERDICT_NOT_APPLICABLE"}
  ]
}' <host:port> skills.v1.EvidenceService/ReportOutcome
```

A `FailedPrecondition` here usually means the registry hasn't fetched the
commit you named yet. Retry in a few minutes with the same `report_id`.

**`ListSkillSignals(skill_name?, min_reported_sessions?)`** — the aggregate,
one row per `(skill, commit)`: `reported_sessions`, `verdict_counts`,
`defect_rate`, and `not_applicable_rate`. **This is the "what should I fix
next" query.** Rows come back ordered by skill, then by when each commit was
first observed, so a `defect_rate` that jumps between two successive commits
of one skill is a regression you can see by eye.

Note that `defect_rate` and `not_applicable_rate` are deliberately separate:
a high `not_applicable_rate` means the skill's *body* may be perfect and its
frontmatter `description` is pulling it into the wrong tasks. Fixing the body
would be the wrong repair.

These are rates among sessions that **reported**, never among sessions —
reporting is voluntary and a crashed session never reports. Don't present
them as true rates.

**`ListOutcomeReports(skill_name, skill_commit?, verdict?, exclude_empty_notes?, limit?)`**
— the individual reports behind a signal. Call this before proposing a fix:
read what actually went wrong, then cite those `report_id`s in
`ProposeChange.motivating_report_ids`. Set `exclude_empty_notes: true` to
skip the `APPLIED` rows.

## Proposing edits (`ProposalService`)

Use this when asked to fix, extend, or otherwise change an existing skill —
not to create ad hoc local files, since `skillsd`'s index is read-only and
reloads only on redeploy. A proposal is a named branch
(`proposals/<agent_id>/<skill_name>/<proposal_id>`) holding one or more
commits; nothing touches the base branch until a human merges the PR you
optionally open at the end.

**`ProposeChange(skill_name, agent_id, proposal_id, files, commit_message,
source_thread_uri?, motivating_report_ids?, allow_duplicate?)`** — commits
full new file contents to a skill's proposal branch, creating the branch from
the base branch's current HEAD if it doesn't exist yet, or appending a commit
otherwise. Returns `ProposeChangeResponse{proposal, deduplicated,
auto_submitted}`.

- `files` is a list of `{file_path, content, deleted}` — always send the
  **complete new content** of each changed file, never a patch/diff; the
  server computes the diff itself. Set `deleted: true` to remove a file
  (content is ignored in that case).
- `agent_id`, `skill_name`, and `proposal_id` are caller-chosen and must not
  contain `/` (they form the branch name). Reuse the same `(agent_id,
  proposal_id)` to append more commits to the same in-progress proposal, or
  pick a new `proposal_id` to start a separate one.
- `motivating_report_ids` are `report_id`s from `ListOutcomeReports` that
  justify this change. **Set these whenever you have them.** They're what
  turns the pull request from "an agent wants this" into "here are the
  recorded failures this fixes," and a reviewer sees them without leaving
  GitHub.
- `source_thread_uri` is an optional pointer back to the conversation that
  produced the change — set it if you have somewhere durable to point to;
  it gets folded into the PR body by `SubmitProposal`.
- Committing identical content to a branch that already has it fails with
  "cannot create empty commit" — expected, not a bug; it means your change
  is already committed.

### If another agent already proposed your exact fix

Before creating a *new* branch, the server hashes the content your change
would produce and compares it against every open proposal for that skill.
If another agent's proposal already produces identical content (whitespace
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

Note there is no RPC to endorse a proposal you have merely read and agreed
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

```bash
grpcurl -plaintext -d '{
  "skill_name": "internal-comms",
  "agent_id": "agent-1",
  "proposal_id": "fix-typo",
  "commit_message": "fix typo in description",
  "files": [{"file_path": "SKILL.md", "content": "<full new file content>"}]
}' <host:port> skills.v1.ProposalService/ProposeChange
```

**`ListProposals(skill_name?, agent_id?)`** — list proposals, optionally
filtered by either field, to check what's already in flight before starting
a new one. Each `Proposal` carries its `content_hash`, its `endorsements`,
and its `corroboration` count (1 for the proposer, plus each non-stale
endorser).

**`ListProposalClusters(skill_name?, include_singletons?)`** — groups a
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

**`GetProposal(branch)`** — fetch a single proposal by its full branch name
(as returned in `Proposal.branch`), including its unified `diff` against the
base branch and its `commits`. Use this to review what you (or another
agent) have staged before submitting.

**`GetSkillAtRef(skill_name, ref, include_context_files)`** — same shape as
`GetSkill`, but as of an arbitrary `ref` (a branch name or commit SHA)
instead of the base branch. Pass a proposal's `branch` here to see the skill
*with your pending edits applied*, e.g. to sanity-check the result before
submitting, or to diff two proposals against each other. Empty `ref`
resolves to the base branch HEAD (equivalent to `GetSkill`).

**`SubmitProposal(branch, pr_title?, pr_body?)`** — pushes the proposal
branch upstream and opens a GitHub pull request for human review.
`pr_title`/`pr_body` default from the proposal's commits (and
`source_thread_uri`, if set) when left empty. This is the one call with a
side effect visible outside the two services — it creates a real PR against
a real repo. If `skillsd-registry` is running without a configured GitHub
token, this RPC is disabled (propose/inspect still work); treat that as
"proposal is ready, but I can't open the PR myself — hand the branch name to
a human."

```bash
grpcurl -plaintext -d '{"branch": "proposals/agent-1/internal-comms/fix-typo"}' \
  <host:port> skills.v1.ProposalService/SubmitProposal
```

## Typical flow

**Using a skill:**

1. `ListSkills({})` → find the skill by matching the task against each
   `description`.
2. `GetSkill({skill_name, include_context_files: true})` → read `SKILL.md`
   and its supporting files to actually use the skill. **Keep the
   `commit`.**
3. At the end of the session, `ReportOutcome` with one entry per skill you
   used — including the ones that worked.

**Fixing a skill**, whether you were asked to or noticed a problem yourself:

1. `ListSkillSignals({skill_name})` → is this a known problem, and did it
   start at a particular commit?
2. `ListOutcomeReports({skill_name, exclude_empty_notes: true})` → read what
   actually went wrong across sessions, and collect the `report_id`s.
3. `ListProposalClusters({skill_name})` → is someone already fixing this?
   If so, read their proposal before writing your own.
4. `ProposeChange` with the full new content of every file you're touching,
   and `motivating_report_ids` from step 2.
   - If the response comes back `deduplicated: true`, you're done — an
     identical proposal existed and you've now corroborated it. Report that
     outcome rather than trying again.
5. `GetSkillAtRef({skill_name, ref: <branch>})` → verify the result reads
   the way you intended.
6. `SubmitProposal` once you're confident — or stop and report the branch
   name if you'd rather a human take the review step from there.

Step 3 matters more than it looks. Skipping it is how a reviewer ends up
with four proposals fixing one typo four different ways.
