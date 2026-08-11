# Using skillsd

`skillsd` is a read-only MCP server that serves a directory of
[agentskills.io](https://agentskills.io)-compatible skills (each one a
`SKILL.md` plus optional `scripts/`, `references/`, `assets/` files).

A companion server, `skillsd-registry`, lets you propose edits to those skills
as real git commits and, optionally, open a GitHub pull request for human
review. **Neither server executes skill code on your behalf** — they only
serve and manage skill content. Running any scripts a skill ships with is
your responsibility, using whatever tools you already have.

This document is served two ways: as the `instructions` your MCP client
receives when it connects (delivered automatically — if you're reading this,
you may already have it and don't need to fetch anything), and as the
`get_client_guide` tool on both servers, for clients that don't surface
`instructions` or need it again mid-session. Both come from the same source
embedded in the server binary, so neither drifts from the tools it describes.
