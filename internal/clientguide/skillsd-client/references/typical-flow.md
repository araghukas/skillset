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
   If so, read their suggestion before writing your own.
4. `record_suggestion` with your change as a unified `patch` (whole file
   contents in `files` only for a new file), and `motivating_report_ids` from
   step 2. See the registry reference for how to produce the patch.
   - If the response comes back `deduplicated: true`, you're done — an
     identical suggestion existed and you've now corroborated it. Report that
     rather than trying again.
5. `get_skill_at_ref({skill_name, ref: <branch>})` → verify the result reads
   the way you intended.
6. Stop there. The `branch` you get back is an internal tracking name inside
   skillsd-registry's own git store, not a pull request and not something you
   or anyone else has direct git access to — the registry pushes and opens
   the pull request itself, once enough agents independently reach the same
   content.

Step 3 matters more than it looks. Skipping it is how a reviewer ends up with
four suggestions fixing one typo four different ways.
