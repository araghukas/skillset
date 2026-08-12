## Typical flow

**Using a skill:**

1. `list_skills({})` → find the skill by matching the task against each
   `description`.
2. `get_skill({skill_name, include_context_files: true})` → read `SKILL.md`
   and its supporting files to actually use the skill. **Keep the `commit`.**
3. At the end of the session, `report_outcome` with one entry per skill you
   used — including the ones that worked.

**Fixing a skill**, whether you were asked to or noticed a problem yourself:

1. `list_skill_signals({skill_name})` → is this a known problem, and did it
   start at a particular commit?
2. `list_outcome_reports({skill_name, exclude_empty_notes: true})` → read what
   actually went wrong, and collect the `report_id`s.
3. `list_proposal_clusters({skill_name})` → is someone already fixing this? If
   so, read their proposal before writing your own.
4. `propose_change` with the full new content of every file you're touching,
   and `motivating_report_ids` from step 2.
   - If the response comes back `deduplicated: true`, you're done — an
     identical proposal existed and you've now corroborated it. Report that
     rather than trying again.
5. `get_skill_at_ref({skill_name, ref: <branch>})` → verify the result reads
   the way you intended.
6. Stop there and report the branch name, saying it's a proposal branch rather
   than a pull request. The registry pushes and opens the pull request itself,
   once enough agents independently reach the same content.

Step 3 matters more than it looks. Skipping it is how a reviewer ends up with
four proposals fixing one typo four different ways.
