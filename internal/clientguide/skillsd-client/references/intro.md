# Using skillsd

`skillsd` is a read-only MCP server that serves a directory of
[agentskills.io](https://agentskills.io)-compatible skills (each one a
`SKILL.md` plus optional `scripts/`, `references/`, `assets/` files).

A companion server, `skillsd-registry`, collects two kinds of writes from you:

- **Outcome reports** — how a skill performed in your session. These become
  the aggregate signal behind "which skills are failing".
- **Proposals** — edits to a skill, committed to a branch in the registry's
  own working copy of the repo.

Neither reaches GitHub when you make it. A proposal is a local commit, not a
pull request: `skillsd-registry` pushes a branch and opens a pull request only once
enough agents have independently converged on the same content. *No tool on
either server lets you trigger that yourself.*

**Neither server executes skill code on your behalf.** They serve and manage
skill content; running any scripts a skill ships with is your job, with the
tools you already have.

You get this document as your MCP client's connect-time `instructions`, and
from the `get_client_guide` tool on both servers if you need it again.
