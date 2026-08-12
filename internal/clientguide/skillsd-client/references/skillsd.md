## Discovering and reading skills

**`list_skills(category?, cursor?, page_size?)`** — the entry point for "what
skills exist" or "find a skill for X". Returns a summary per skill: `name`,
`description`, `compatibility`, `metadata` (a free-form map), and
`context_files` (a count, not the content). This guide isn't in that index —
fetch it with `get_client_guide`.

- Leave `category` empty to list everything; set it to match
  `metadata["category"]` exactly.
- Results are paginated, ordered by name. Pass `next_cursor` back as `cursor`
  to get the next page; no `next_cursor` means you're on the last one.
- Call `list_skills({})` first — it's cheap — and pick by matching the request
  against each `description`, which is written to be matched against the way a
  system prompt describes when to reach for a tool.

**`get_skill(skill_name, include_context_files?, paths?, max_bytes?)`** —
fetch one skill by directory name (which always equals its frontmatter
`name`). This is what actually loads a skill: set `include_context_files:
true`, read `SKILL.md`'s body for instructions, then follow any references it
makes to its other files by path.

- `paths` returns only the files you name, when you already know which ones.
- `max_bytes` caps total content (default 256 KiB). Oversized file sets drop
  whole files — never a partial file — and the reply names what was omitted
  and how to re-fetch it with `paths`.
- Binary and non-UTF-8 files are silently omitted, since content is carried as
  text. Expect a skill's `assets/` to be incomplete if it holds binaries.

**Keep the `commit` field** from every skill you fetch. You need it for
`report_outcome` on `skillsd-registry`: a report that names a skill but not a
version can't tell anyone whether a recent edit broke something or it was
always broken.

**`get_client_guide()`** — fetches this document, as of the running server's
build. No arguments.
