# `skillsd-registry` — the write path

`skillsd-registry` is an optional, single-replica component that gives agents a write path onto skills — proposing changes, opening pull requests, and reporting how a skill actually performed — without ever handing them raw git or GitHub credentials. It owns a real git working copy and (optionally) a SQLite database, both on their own persistent volumes.

It's a single writer by construction, not by convention: exactly one replica ever mounts the repo volume, git operations serialize on an in-process mutex, and the Deployment uses `Recreate` (not `RollingUpdate`) so a second pod can never start — and try to mount the same `ReadWriteOnce` volume — before the outgoing one has fully terminated.

It serves two independent gRPC services on one endpoint: `ProposalService` and `EvidenceService`.

## ProposalService — proposals, clustering, and pull requests

| RPC | Purpose |
|---|---|
| `ProposeChange` | Commits full new file content to a skill's proposal branch — creates it if this is the first call, appends if the agent is iterating. Deduplicates against existing proposals (below). |
| `ListProposals` | Lists proposals, filterable by skill and/or agent. |
| `ListProposalClusters` | Groups a skill's open proposals into clusters of competing answers to the same defect, most-contested first. |
| `GetProposal` | Fetches one proposal by branch name: its diff against base, commit history, and endorsements. |
| `GetSkillAtRef` | Fetches a skill's metadata as of an arbitrary ref — a branch or a commit SHA. |
| `SubmitProposal` | Pushes the branch upstream and opens a GitHub pull request for human review. |

Branches are namespaced `proposals/<agent_id>/<skill_name>/<proposal_id>` — that's also the lookup key for `ListProposals`. There's no separate status field or database row for a proposal: its state *is* whatever's on its branch, and once `SubmitProposal` opens a PR, GitHub's own merge mechanism takes over. Agents send full file content rather than a patch, so the server computes the diff itself and callers never need a base they may not have in sync.

Deploying it requires an HTTPS clone URL with push access and a GitHub token — see [helm-chart.md](helm-chart.md#github-authentication).

### Consolidation: how N agents produce one pull request

The problem shows up the moment more than a handful of agents run at once: they hit the same defect independently, and — left alone — a reviewer gets N pull requests describing one bug. Three deterministic mechanisms fix this, all of them arithmetic; no model is involved.

**Content-hash deduplication.** Before creating a branch, `ProposeChange` hashes the caller's *prospective* file set and compares it against every open proposal for that skill (files normalized first — CRLF, trailing whitespace, blank lines at EOF — so cosmetic differences don't split a cluster). On a match, no branch is created: the caller is recorded as an **endorsement** on the existing proposal instead.

```mermaid
flowchart TD
  Start(["ProposeChange(agent, skill,\nproposal_id, files)"]) --> OwnBranch{"Agent's own\nbranch exists?"}

  OwnBranch -- yes --> Commit["Commit onto that branch\n(iteration, never diverted)"]
  OwnBranch -- no --> AllowDup{"allow_duplicate\nset?"}

  AllowDup -- yes --> Commit
  AllowDup -- no --> Hash["Hash the prospective\nfile set (normalized)"]

  Hash --> Match{"Matches another agent's\nopen proposal?"}

  Match -- no --> Commit
  Match -- yes --> Endorse["Record caller as endorser\n(no branch created)"]

  Commit --> Validate{"skillparse\nvalidates result"}
  Validate -- fails --> Invalid["Error — commit stands,\nfix with a follow-up call"]
  Validate -- ok --> OwnResult(["deduplicated: false"])

  Endorse --> DupResult(["deduplicated: true\ncorroboration += 1"])

  OwnResult --> AutoCheck{"corroboration ≥\nautoSubmitEndorsements?"}
  DupResult --> AutoCheck
  AutoCheck -- "yes, threshold > 0" --> AutoPR["Push branch + open PR"]
  AutoCheck -- no --> Done(["Return to caller"])
  AutoPR --> Done
```

There's deliberately **no RPC to endorse a proposal you've merely read and agreed with** — an endorsement is only meaningful as evidence if it was produced without knowledge of the proposal it lands on. Endorsements are git refs (`refs/endorsements/<agent>/<skill>/<id>/<endorser>`), pushed alongside the branch on submission; when a proposal advances, earlier endorsements are kept but marked `stale` and stop counting.

**Clustering by contested region.** Proposals that aren't identical may still be competing answers to one defect. `ListProposalClusters` diffs each proposal against its fork point, extracts the base-side line ranges touched, and unions proposals whose ranges overlap within a diff-context window — a stand-in for three-way conflict detection that arguably improves on it, since edits that would merge cleanly can still be rival answers. Clusters are computed per call, never stored.

**Auto-submission at a threshold.** `autoSubmitEndorsements` is how many independent agents must reach identical content before a PR opens unprompted. It defaults to `0` (off) — deliberately, since this is the one behavior here that acts on its own, and it's exactly as trustworthy as `agent_id` is. **Authenticate callers before turning it on**; with self-asserted identity, one misconfigured caller can manufacture a threshold's worth of agreement by itself.

Three agents independently hitting the same defect, `autoSubmitEndorsements: 2`:

```mermaid
sequenceDiagram
    participant A1 as Agent 1
    participant A2 as Agent 2
    participant A3 as Agent 3
    participant Reg as skillsd-registry
    participant GH as GitHub

    A1->>Reg: ProposeChange(fix X)
    Reg-->>A1: deduplicated: false, corroboration: 1

    A2->>Reg: ProposeChange(fix X, same content)
    Reg->>Reg: record endorsement (agent-2)
    Reg-->>A2: deduplicated: true, corroboration: 2

    A3->>Reg: ProposeChange(fix X, same content)
    Note over Reg: threshold (2) now met
    Reg->>Reg: record endorsement (agent-3)
    Reg->>GH: push branch + endorsement refs
    Reg->>GH: open PR (3 agents, endorsers named)
    GH-->>Reg: PR #42
    Reg-->>A3: deduplicated: true, corroboration: 3, auto_submitted: PR #42

    Note over GH: one pull request, signed by three agents,<br/>reviewed once by a human
```

## EvidenceService — outcome reporting

The one thing a git host can't see is whether a skill actually worked. `EvidenceService` collects that — the only data in `skillset` not derived from git.

| RPC | Purpose |
|---|---|
| `ReportOutcome` | Records one session's outcome for every skill it used. Idempotent on a caller-supplied `report_id`. |
| `ListSkillSignals` | Aggregates reports into one row per `(skill, commit)`: sessions, verdict counts, defect rate. The "what should I fix next" query. |
| `ListOutcomeReports` | The individual reports behind a signal, so a proposing agent can cite report IDs. |

Two decisions carry most of the weight:

- **The agent reports usage; the server doesn't observe it.** A `GetSkill` call isn't usage — a skill that influenced a task is. Reporting is one call per *session*, not per fetch, which keeps the read fleet stateless and gives a meaningful denominator.
- **Verdicts are observable outcomes, not satisfaction ratings.** Each one implies a different repair:

| Verdict | Implies |
|---|---|
| `APPLIED` | Nothing — this is the denominator |
| `APPLIED_WITH_CORRECTION` | Content is stale or imprecise |
| `CONTRADICTED` | Content is **wrong** |
| `INCOMPLETE` | Content has a **gap** |
| `NOT_APPLICABLE` | The frontmatter `description` **over-triggers** |

`NOT_APPLICABLE` is easy to overlook and expensive to ignore: a skill repeatedly loaded for the wrong tasks burns context fleet-wide, and no human is positioned to notice. It's tracked as its own rate, separate from `defect_rate`, because fixing the body would be the wrong repair. Grouping by commit is what makes regressions visible — a defect rate that jumps between successive commits of one skill is a merge that made things worse.

**On rates.** `reported_sessions` counts sessions that *reported*, never sessions that ran — a crashed session never reports. Every rate is "among sessions that reported"; don't let a dashboard quietly drop that qualifier.

**On durability and storage**, including the retention/backup policy for the SQLite database backing this service, see [data-stores.md](data-stores.md#evidence-data).

### A note on identity

Both consolidation and evidence rest on `agent_id`, which is currently **self-asserted**. Corroboration counts and defect rates are only as trustworthy as the callers are — fine for a trusted fleet, not for an open one. Fix this before enabling auto-submission or treating signals as authoritative.

## Configuration

`skillsd-registry` is configured entirely through environment variables set by the Helm chart. See [helm-chart.md](helm-chart.md#skillsd-registry-values) for the full values/env-var reference and required GitHub authentication.
