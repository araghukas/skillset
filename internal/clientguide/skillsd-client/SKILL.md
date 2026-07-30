---
name: skillsd-client
description: Explains how to call skillsd's gRPC API (SkillService, ProposalService) to discover skills, fetch a skill's full content, and propose edits to a skill as a reviewable git branch / pull request. Use this skill whenever an agent has been given access to a skillsd/skillsd-registry endpoint and needs to know which RPCs to call, what fields to send, and how the discovery and proposal workflows fit together — for example when asked to "find a skill for X", "load skill Y", "update/fix skill Y", or "submit a proposal/PR for skill Y".
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
  response with every file in every skill directory.
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
`assets/` directory to be incomplete if it holds binaries.

**`GetClientGuide({})`** — fetches this document, always as of the running
server's build (no arguments; there's only ever one). Same response shape as
`GetSkill` (`GetSkillResponse{skill}`) so a client that already knows how to
render a `SkillMetadata`/`context_files` pair can render this one too.

```bash
grpcurl -plaintext -d '{}' <host:port> skills.v1.SkillService/GetClientGuide
```

## Proposing edits (`ProposalService`)

Use this when asked to fix, extend, or otherwise change an existing skill —
not to create ad hoc local files, since `skillsd`'s index is read-only and
reloads only on redeploy. A proposal is a named branch
(`proposals/<agent_id>/<skill_name>/<proposal_id>`) holding one or more
commits; nothing touches the base branch until a human merges the PR you
optionally open at the end.

**`ProposeChange(skill_name, agent_id, proposal_id, files, commit_message,
source_thread_uri?)`** — commits full new file contents to a skill's
proposal branch, creating the branch from the base branch's current HEAD if
it doesn't exist yet, or appending a commit otherwise.

- `files` is a list of `{file_path, content, deleted}` — always send the
  **complete new content** of each changed file, never a patch/diff; the
  server computes the diff itself. Set `deleted: true` to remove a file
  (content is ignored in that case).
- `agent_id` and `proposal_id` are caller-chosen; together with `skill_name`
  they determine the branch name, so reuse the same `(agent_id,
  proposal_id)` to append more commits to the same in-progress proposal,
  or pick a new `proposal_id` to start a separate one.
- `source_thread_uri` is an optional pointer back to the conversation that
  produced the change — set it if you have somewhere durable to point to;
  it gets folded into the PR body by `SubmitProposal`.
- Committing identical content to a branch that already has it fails with
  "cannot create empty commit" — expected, not a bug; it means your change
  is already committed.

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
a new one.

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

1. `ListSkills({})` → find the skill by matching the task against each
   `description`.
2. `GetSkill({skill_name, include_context_files: true})` → read `SKILL.md`
   and its supporting files to actually use the skill.
3. If the task is to *change* a skill rather than use it: `ProposeChange`
   with the full new content of every file you're touching, then
   `GetProposal` or `GetSkillAtRef` to verify the result, then
   `SubmitProposal` once you're confident — or stop after `ProposeChange`
   and report the branch name if you'd rather a human take the review step
   from there.
