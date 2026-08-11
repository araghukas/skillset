## Discovering and reading skills

**`list_skills(category?, cursor?, page_size?)`** — the entry point for "what
skills exist" or "find a skill for X". Returns a summary for every matching
skill: `name`, `description`, `compatibility`, `metadata` (a free-form map),
and `context_files` (a count, not the content itself). This guide is not part
of that index — fetch it with `get_client_guide` instead.

- Leave `category` empty to list everything; set it to filter to skills whose
  `metadata["category"]` matches exactly.
- Results are paginated, ordered by name. Pass back `next_cursor` from one
  call as `cursor` on the next to page through; an absent `next_cursor` means
  you're on the last page.
- In practice: call `list_skills({})` first (metadata only, cheap) to decide
  *which* skill you want, matching the request against each skill's
  `description` — that field is written to be matched against, the way a
  system prompt would describe when to reach for a tool. Then fetch that one
  skill's content with `get_skill`.

**`get_skill(skill_name, include_context_files?, paths?, max_bytes?)`** —
fetch one skill by its directory name (== its frontmatter `name`, they're
required to match). Set `include_context_files: true` to get `SKILL.md` and
everything alongside it (`scripts/`, `references/`, `assets/`) as one text
block per file. This is what actually loads the skill: read `SKILL.md`'s body
for instructions, then follow any references it makes to its other files by
path.

- `paths` restricts the returned files to the ones you name, when you already
  know which you need.
- `max_bytes` caps the total size of returned file content (default 256 KiB
  server-side). If a file set is too large to return whole, files are
  dropped entirely — never truncated mid-file — and the reply names what was
  omitted and how to re-fetch it with `paths`.
- Binary/non-UTF-8 files (images, etc.) are silently omitted from
  `context_files`, since file content is carried as text — expect a skill's
  `assets/` directory to be incomplete if it holds binaries.

**Keep the `commit` field** from every skill you fetch. You need it later, in
`report_outcome` on `skillsd-registry` — a report that names a skill but not
a version can't tell anyone whether a recent edit broke something or it was
always broken. Hold onto it for every skill you load.

**`get_client_guide()`** — fetches this document, always as of the running
server's build. No arguments; there's only ever one.
