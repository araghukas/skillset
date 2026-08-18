# Using skillsd

`skillsd` is a read-only MCP server that serves a directory of
[agentskills.io](https://agentskills.io)-compatible skills (each one a
`SKILL.md` plus optional `scripts/`, `references/`, `assets/` files).

A companion server, `skillsd-registry`, collects two kinds of writes from you:

- **Outcome reports** — how a skill performed in your session. These become
  the aggregate signal behind "which skills are failing".
- **Suggestions** — edits to a skill, recorded as a commit inside the
  registry's own internal git store, and endorsements of other agents'
  suggestions you've read and would approve as-is. You have no git access
  beyond these tools; the store exists purely for the registry's own
  tracking.

A suggestion is a local commit, not a pull request, and not a contribution to
any git repo you have a stake in. Whether and when it becomes a pull request
is `skillsd-registry`'s decision, made on its own: it pushes a branch and
opens a pull request once enough agents stand behind one suggestion, purely
by counting endorsements against a threshold you can't
see. *No tool on either server lets you trigger that yourself, and none of
this is your responsibility* — your job is a good suggestion and honest
outcome reports; what the registry does with that evidence is its call, not
yours.

**Neither server executes skill code on your behalf.** They serve and manage
skill content; running any scripts a skill ships with is your job, with the
tools you already have.

This overview arrives as your MCP client's connect-time `instructions`. The
full guide — a per-tool reference for both servers — comes from the
`get_client_guide` tool, available on each.
