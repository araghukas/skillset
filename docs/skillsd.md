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
hand-written integration doc. Onboarding arrives automatically as this
server's `instructions` when an MCP client connects, and the full guide is
available on demand as `get_client_guide()` (a tool) and
`skillsd://client-guide` (a resource). Both read from one embedded source,
so neither can drift from what the server actually implements.

The two delivery paths carry different amounts on purpose. Connect-time
`instructions` are paid by every session that has the server attached, so
they hold only the universal sections — the mental model and the typical
call flow — plus a one-line count of the skills the running instance's
`registry.Registry` holds (`registry.Registry.Catalog`), pointing at
`list_skills` for the actual listing. The per-tool reference for both
servers lives in `get_client_guide` and the resource, where an agent
that's actually working with the tools fetches it. Everything else an
agent needs per tool — argument shapes, verdict meanings, constraints —
rides on the tools themselves, in their descriptions and schemas. The
count is built once from the same in-memory index `list_skills`/`get_skill`
read, right after `registry.Load` at startup — so it's current as of
process start, and (per "No runtime refresh, by design" above) stays fixed
until the pod restarts, same as the rest of the index.

The guide's static content is
[internal/clientguide/skillsd-client/SKILL.md](../internal/clientguide/skillsd-client/SKILL.md)
and its `references/`, embedded into the server binary via `go:embed` —
deliberately *not* read from the skills repo `list_skills` indexes, since
it documents the API itself: it ships versioned with the server binary,
stays available even if the skills repo is empty or misconfigured, and
never appears in `list_skills`'s output.

**This README/docs tree and that guide serve different readers.** The guide is
the API reference for an agent (or a human integrating one) — this doc
and its siblings cover deployment and operations.

## Configuration

`skillsd` is configured entirely through environment variables set by the Helm
chart. See [helm-chart.md](helm-chart.md#skillsd-values) for the full
values/env-var reference and the GitHub auth it needs for its init container's
clone.
