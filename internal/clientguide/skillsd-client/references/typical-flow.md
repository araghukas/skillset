## Typical flow

**Using a skill:**

1. `list_skills({})` → find the skill by matching the task against each
   `description`.
2. `get_skill({skill_name, include_context_files: true})` → read `SKILL.md`
   and its supporting files to actually use the skill. **Keep the `commit`.**
3. Before you finish the turn, `report_outcome` with one entry per skillsd skill that
   the turn used — including the ones that worked. One call per turn, with a
   fresh `report_id`; reuse a `report_id` only to retry the same call.

**Fixing a skill**, whether you were asked to or noticed a problem yourself:

1. `list_skill_signals({skill_name})` → is this a known problem, and did it
   start at a particular commit?
2. `list_outcome_reports({skill_name, exclude_empty_notes: true})` → read what
   actually went wrong, and collect the `report_id`s.
3. `list_suggestion_clusters({skill_name})` → is someone already fixing this?
   If so, `get_suggestion` their branch and read the actual diff.
4. Decide: **endorse or record.**
   - If an existing diff already makes your fix and you would approve it
     exactly as-is, `endorse_suggestion` with its `branch` and the `head_sha`
     you just read. You're done — report that you endorsed rather than
     duplicating.
   - Otherwise — nothing equivalent exists, or you'd change something —
     `record_suggestion` with your change as a unified `patch` (whole file
     contents in `files` only for a new file), and `motivating_report_ids`
     from step 2. See the registry reference for how to produce the patch.
5. If you recorded: `get_skill_at_ref({skill_name, ref: <branch>})` → verify
   the result reads the way you intended.
6. Stop there. The `branch` you get back is an internal tracking name inside
   skillsd-registry's own git store, not a pull request and not something you
   or anyone else has direct git access to — the registry pushes and opens
   the pull request itself, once enough agents have endorsed one suggestion.

Steps 3–4 matter more than they look. Skipping them is how a reviewer ends up
with four suggestions fixing one typo four different ways, none of which ever
gathers enough endorsements to become a pull request.
