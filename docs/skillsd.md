# `skillsd` — the read fleet

`skillsd` is the service agents actually read skills from: a stateless,
horizontally scalable MCP fleet that serves a static, versioned snapshot of a
skills repository over Streamable HTTP.

## What it serves

| Tool | Purpose |
|---|---|
| `list_skills(category?, cursor?, page_size?)` | Metadata for every skill, paginated — the "what exists" query. |
| `get_skill(skill_name, include_context_files?, paths?, max_bytes?)` | One skill's full content: `SKILL.md` plus `scripts/`, `references/`, `assets/`. |
| `get_client_guide()` | The agent-facing onboarding doc for the whole API — see below. |

`GET /healthz` (plain HTTP, not MCP) backs liveness/readiness probes. Any
MCP client discovers the tools above, their schemas, and the server's
onboarding `instructions` at connect time (`initialize` + `tools/list`) —
no schema file or generated stub needed.

## How a pod comes up

```mermaid
sequenceDiagram
    participant Init as init container<br/>(skillsd-init)
    participant Vol as skills-data<br/>(emptyDir)
    participant Main as skillsd
    participant GH as Git forge

    Init->>GH: authenticate, then<br/>git clone --depth 1 --branch main
    GH-->>Init: shallow clone
    Init->>Vol: write cloned tree
    Note over Init: init container exits
    Main->>Vol: mount read-only
    Main->>Main: registry.Load() — walk once,<br/>build in-memory index, stamp HEAD commit
    Note over Main: index is now immutable<br/>for the life of the process
    Main->>Main: serve the skill tools over MCP
```

Every replica repeats this independently — there's no leader, no shared cache,
no coordination between pods. `N` replicas means `N` independent clones of the
same ref, each serving from its own in-memory copy.

## No runtime refresh, by design

`registry.Load` runs exactly once, at startup, and a failed load is fatal (no
retry loop, no partial index). This is a deliberate trade for the property that
matters most here: reads never block, never hit disk, and never coordinate —
which is what lets the fleet scale horizontally by adding replicas with no other
change.

The consequence: **picking up new skill content means restarting the pod**, so
the init container re-clones. In practice that's a rolling restart of the
Deployment (`kubectl rollout restart`), not a config change or an RPC call —
there is intentionally no `RefreshSkillIndex` RPC.

`SKILLS_COMMIT` exists for the one case where a git working copy isn't what's
mounted (a baked image layer, a ConfigMap): it overrides the commit stamped onto
every served skill. Leave it unset when `SKILLS_DIR` is a real clone — `skillsd`
reads `HEAD` itself.

## Why the commit matters

Every skill carries the git commit its content came from. That's what lets a
downstream outcome report (see [skillsd-registry.md](skillsd-registry.md))
say *this exact version* of a skill was cited and whether it held up — not
just "the skill," which can't distinguish a fix from the bug it fixed.

## The client guide (`get_client_guide`, and connect-time `instructions`)

An agent isn't expected to have read this file, a schema, or any
hand-written integration doc. The onboarding guide arrives automatically as
this server's `instructions` when an MCP client connects — most clients
fold that straight into the agent's context with nothing else required. For
a client that doesn't surface `instructions`, or an agent that wants it
again mid-session, the same content is also `get_client_guide()` (a tool)
and `skillsd://client-guide` (a resource). All three read from one source,
so none of them can drift from what the server actually implements.

Its content is
[internal/clientguide/skillsd-client/SKILL.md](../internal/clientguide/skillsd-client/SKILL.md),
embedded into the server binary via `go:embed` — deliberately *not* read from
the skills repo `list_skills` indexes, since it documents the API itself: it
ships versioned with the server binary, stays available even if the skills
repo is empty or misconfigured, and never appears in `list_skills`'s output.
`skillsd`'s `instructions` carry only the tools this server actually has
(`list_skills`, `get_skill`, `get_client_guide`) — the proposal and evidence
workflow content lives in the same source file but is filtered out for this
server; see [skillsd-registry.md](skillsd-registry.md) for where it goes
instead.

**This README/docs tree and that guide serve different readers.** The guide is
the API reference for an agent (or a human integrating one) — this doc
and its siblings cover deployment and operations.

## Configuration

`skillsd` is configured entirely through environment variables set by the Helm
chart. See [helm-chart.md](helm-chart.md#skillsd-values) for the full
values/env-var reference and the GitHub auth it needs for its init container's
clone.
