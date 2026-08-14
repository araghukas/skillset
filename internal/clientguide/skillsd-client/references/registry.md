## Reporting how a skill performed

Report every skill that you used at the end of each turn, in one
`report_outcome` call with a `report_id` you generate for that turn. It's the
only way anyone learns a skill is wrong — nobody else is watching, and a skill
that quietly misleads agents keeps doing so until someone says so.

A session files as many reports as it had turns that used a skill, all sharing
one `session_id`. Using the same skill twice in a session is two observations,
not one: it can hold up in the first turn and fall over in the second, and both
are worth recording.

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
be the wrong repair. Both rates are shares of `report_count`, which counts
reports filed — never uses, since a turn that used a skill and said nothing
leaves no trace here.

**`list_outcome_reports`** returns the individual reports behind a signal.
Read these before recording a suggestion, then cite their `report_id`s in
`record_suggestion`'s `motivating_report_ids` — they turn the eventual pull
request from "an agent wants this" into "here are the recorded failures
this fixes."

If these three tools aren't in this server's tool list, evidence collection
is disabled on this deployment. That's a normal configuration, not an
error.

## Recording a suggestion

Use `record_suggestion` when asked to fix, extend, or change an existing
skill — not ad hoc local files, since `skillsd`'s index is read-only and
reloads only on redeploy.

**A suggestion is not a pull request, and it is not a contribution to any
git repo you have a stake in.** `record_suggestion` commits your change onto
a branch inside skillsd-registry's own internal git store — a piece of
server-side bookkeeping, not pushed anywhere, not on GitHub. You have no
git credentials for it, no push or fetch access, and no way to reach it
except through these MCP tools; the branch name is namespaced with the
`agent_id` you supplied purely so the registry can track whose suggestion
is whose, not because you own it. Your job ends at a good suggestion:
correct content, cited evidence, honest commit message. Report the branch
name to whoever asked if useful, and say plainly it's an internal tracking
name, not a pull request and not something anyone can check out.

### Sending your change as a patch

Describe the change in `patch`, a unified diff. `files`, which carries whole
file contents, is for new files — they have nothing to diff against — and for
creating a skill; send one or the other, never both. Regenerating a long file
to change two lines is slow and invites you to corrupt the parts you weren't
editing.

1. Read the current content with **`get_skill_at_ref`**. Don't diff against
   `get_skill`'s copy: skillsd appends an "Improving this skill" footer to every
   `SKILL.md` it serves, and that footer is not in the repository, so any hunk
   near the end of the file will fail to apply.
2. Write every file you're touching into two scratch
   **directories named `a` and `b`**, each at its path relative to the skill
   directory — the same paths `get_skill_at_ref` labelled them with:

   ```
   /tmp/sk/a/SKILL.md        /tmp/sk/b/SKILL.md
   /tmp/sk/a/scripts/run.sh  /tmp/sk/b/scripts/run.sh
   ```
3. Edit only under `b/`, including any new files.
4. From their parent: `git diff --no-index a b`, or `diff -ru a b`. Send the
   output as `patch`.

- The `a`/`b` names are not decoration — they're the prefixes git already uses,
  so the diff header comes out relative to the skill directory with nothing to
  strip. Diffing two differently-named scratch files instead produces paths the
  registry cannot resolve, and the call is rejected.
- A path that names no file of the skill is an error listing the skill's real
  files. A **new** file needs its exact path relative to the skill directory,
  since there's nothing to match it against.
- Context and removed lines must match exactly, but the line numbers may be
  off — a hunk is found by its surroundings.
- Renames, mode changes, and binary files aren't supported. Express a rename as
  a deletion plus a creation.
- **When revising your own suggestion, the patch applies to your branch, not to
  the base skill.** Re-read it with `get_skill_at_ref({skill_name, ref: <your
  branch>})` first.
- If a patch is rejected, the error says which hunk failed and what the file
  says there instead. Fix the patch or send `files` — don't guess at new
  context lines.

### If another agent already suggested your exact fix

When your change would produce content identical (whitespace aside) to an
open suggestion, no new branch is created: `deduplicated: true` comes back,
`suggestion` is *theirs*, and you're recorded on it as an **endorsement**,
raising its `corroboration` count. Treat that as success — when six agents
notice the same defect, the reviewer should get one pull request signed by
six agents, not six saying the same thing.

There's no tool to endorse a suggestion you've read and agreed with: an
endorsement is only evidence if it was produced *without* seeing the
suggestion it lands on. The only way to make one is to independently reach
the same content.

- Once your own suggestion exists, dedup no longer applies to it — iterate
  freely without being diverted onto someone else's suggestion.
- If your suggestion advances to new content, earlier endorsements are kept
  but marked `stale: true` and stop counting. Agents corroborated what they
  actually saw; that doesn't transfer to a revision they never reviewed.

### What opens a pull request

`corroboration` is 1 for the suggesting agent plus each non-stale
endorsement. The registry pushes the branch and opens the pull request on
the `record_suggestion` call that lifts that count to a configured
threshold — `auto_submitted` comes back on that response with the URL.

It's arithmetic over content hashes, so nothing judges it: not you, not a
model. A persuasive commit message counts for nothing, and neither does
suggesting the same fix twice under different names — endorsements are
keyed by `agent_id`. You can't read the threshold, so record your best
change and stop.

### Reviewing what's in flight

**`list_suggestions`** lists open suggestions without diffs;
**`get_suggestion`** fetches one with its unified diff. **`list_suggestion_clusters`**
groups a skill's open suggestions by whether they edit overlapping regions
of the same files — two agents rewriting one passage are almost certainly
answering the same defect even when their fixes differ, so a cluster is a
stronger signal than either suggestion alone. Read it before recording a
suggestion, so you don't add a third answer in ignorance of the first two.

**`get_skill_at_ref`** reads a skill as of any ref: pass a suggestion's
`branch` to see the skill with those edits applied, or the `skill_commit`
from an outcome report to read the version that report is about.
