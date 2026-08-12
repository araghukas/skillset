---
name: skillsd-client
description: Explains how to use skillsd and skillsd-registry, two MCP servers, to find skills, load a skill's full content, report how a skill performed after using it, and propose edits to a skill. Proposing a change is not opening a pull request and writes nothing to GitHub - the registry pushes a branch and opens a pull request on its own, once enough agents independently reach identical content, and no tool lets a caller trigger it. Use this whenever you have access to a skillsd or skillsd-registry MCP server and need to know which tools to call, what to send them, and how discovery, reporting, and proposals fit together.
---

# Using skillsd

This skill is assembled at runtime from the files in `references/` by
`internal/clientguide` - it isn't served whole. `references/intro.md` is the
opening section; start there.
