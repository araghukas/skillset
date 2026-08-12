# Purpose

Skillset serves an evolving set of [agentskills](https://agentskills.io) to a fleet of agents. It collects proposals for updates and improvements, and [consolidates endorsements](docs/skillsd-registry.md#consolidation-how-n-agents-produce-one-pull-request) into manageable pull requests for final review. It is an API for agents, not humans, written in Go and served over [MCP](https://modelcontextprotocol.io). Curious humans should see [docs/quickstart.md](docs/quickstart.md).

## Why this exists

While `git` tracks changes, `skillset` tracks *outcomes* and enables skills to evolve over time.

Skills are how a fleet of AI agents remembers what it's learned. `skillset`
is the layer that turns that memory into something that actually improves:
it serves the current skill set to every agent, collects independent
proposals when agents find something wrong, and collapses agreement into
pull requests instead of noise.

Git remains the durable store underneath, while human reviewers stay in charge of
merging any proposal into that permanent record.

`skillset` does what git alone cannot:

- **It knows whether a skill worked.** A pull request saying *"an agent wants to
  change this"* tells a reviewer little. One saying *"this version was cited by
  142 sessions and contradicted reality in 18% of them, up from 2% on the prior
  commit"* tells them what to do next.
- **It collapses agreement instead of relaying it.** When six agents
  independently hit the same bug, a bare git integration produces six pull
  requests and a drowning reviewer. `skillset` produces one — signed by six,
  which is itself the strongest evidence available that the fix is right.
- **The service measures whether independent parties converged.** Every
  mechanism here is arithmetic over git and SQL — content hashes, line-range
  overlap, counting — never a judgment call about whether a change is *good*.
  That call always stays with whoever reviews the PR.

**No part of `skillset` itself runs an AI model, and none is planned.**

## Workloads

| Workload | Role | Scale | Does |
|---|---|---|---|
| `skillsd` | Read path | N replicas, stateless | Serves the current skill set over MCP, every skill attributed to the commit it came from |
| `skillsd-registry` | Write path (optional) | 1 replica, stateful | Turns agent proposals into git commits, deduplicates and clusters competing fixes, opens pull requests on the git forge for human review |

## Architecture

```mermaid
flowchart TB
  Agent["AI Agent"]

  subgraph K8s["Kubernetes cluster"]
    Read["skillsd\nread fleet, N replicas\nMCP: list_skills, get_skill, get_client_guide"]
    Write["skillsd-registry\nwrite path, 1 replica\nMCP: propose_change, report_outcome, …"]
  end

  Forge[("Git forge\nGitHub, Gitea, …\nskills repo · pull requests")]

  Agent -- "discover, read" --> Read
  Agent -- "propose" --> Write
  Agent -- "report, query signals" --> Write

  Forge -- "clone, read-only" --> Read
  Forge -- "fetch base" --> Write
  Write -- "push branch" --> Forge
```

Both workloads ship in one Helm chart ([charts/skillsd](charts/skillsd)) but
deploy independently: the read fleet scales horizontally and never mutates
anything; the registry is a single writer serializing git operations on its own
volume.

## Documentation

| Doc | Covers |
|---|---|
| [docs/quickstart.md](docs/quickstart.md) | Deploying to Kubernetes and pointing an agent at the running service |
| [docs/skillsd.md](docs/skillsd.md) | The read fleet, how it loads and serves skills |
| [docs/skillsd-registry.md](docs/skillsd-registry.md) | Proposals, consolidation, pull requests, and outcome reporting |
| [docs/data-stores.md](docs/data-stores.md) | What's persisted, where, and what's actually irreplaceable |
| [docs/helm-chart.md](docs/helm-chart.md) | Chart structure, full values reference, GitHub auth, installation |

Everything above is written for an operator. The agent-facing API reference is
the `get_client_guide` MCP tool itself — also delivered automatically as
server `instructions` at connect time (see
[internal/clientguide](internal/clientguide)).

## Local development

Local dev runs on a `kind` cluster provisioned by `ctlptl`, with Tilt handling
build/deploy/live-reload.

```bash
make dev
```

The default requires no GitHub account: `make dev` bootstraps a throwaway
Gitea instance in the cluster (via `gitea-up`) and points both components at
it, so the full read + propose + auto-submitted-PR path works offline.

To run against a *real* GitHub repo instead (not a Gitea clone of one), use
whichever auth you can. Both modes skip the Gitea bootstrap, since `gitea-up`
deletes token files that don't authenticate against Gitea:

```bash
make dev GITEA=0   # token auth: fine-grained PATs in local/git-skillsd-*token
make dev           # GitHub App auth: auto-detected from local/github-app.json
```

See [local/README.md](local/README.md) for complete auth options, as well as
registering the app, seeding a different skills repo, teardown, and talking
to the running deployment.

## License

See [LICENSE](LICENSE).
