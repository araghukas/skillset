---
name: skillsd-client
description: Explains how to use skillsd and skillsd-registry, two MCP servers, to discover skills, fetch a skill's full content, report how a skill actually performed after using it, and propose edits to a skill as a reviewable git branch / pull request. Use this skill whenever an agent has been given access to a skillsd/skillsd-registry MCP server and needs to know which tools to call, what arguments to send, and how the discovery, reporting, and proposal workflows fit together — for example when asked to "find a skill for X", "load skill Y", "update/fix skill Y", "report that skill Y was wrong", or "submit a proposal/PR for skill Y".
---

# Using skillsd

This skill is assembled at runtime from the files in `references/` by
`internal/clientguide` (see that package for the assembly logic) - it isn't
served whole. `references/intro.md` is the actual opening section; start
there.
