# local/

Support files for the local dev loop (`make dev` / `tilt up`): a
`ctlptl`-managed `kind` cluster + image registry, a throwaway Gitea instance
standing in for GitHub, and the Helm values override Tilt deploys with.

```
local/
├── cluster.yaml                # ctlptl spec: kind cluster "kind-skillsd" + registry "skillsd-registry"
├── values.yaml                 # Helm values override (replicaCount, logLevel, image, skillsRepo, registry)
├── gitea.yaml                  # Deployment+Service for the local Gitea stand-in for GitHub
├── gitea-init.sh               # bootstraps Gitea: seed content from GITEA_SEED_URL (run by `make gitea-up`)
├── verify/                     # gRPC check scripts exercising a running local deployment (see below)
├── git-skillsd-token           # gitignored, written by gitea-init.sh: read-only token for skillsd's clone
├── git-skillsd-registry-token  # gitignored, written by gitea-init.sh: push + PR token for skillsd-registry
└── github-app.json             # gitignored, written by you: optional GitHub App auth against a real repo (see below)
```

## Using the local Gitea stand-in (default)

`make dev` runs `make gitea-up` first, which:

1. Applies `local/gitea.yaml` (a single-replica, SQLite-backed Gitea) to the
   kind cluster and waits for it to come up.
2. Creates an admin user, a `skills` repo, and two tokens (read-only and
   read/write) via Gitea's API.
3. Seeds the repo on `main` with a private copy of the `skills/` tree from
   whatever `GITEA_SEED_URL` points at (see below). It's a fresh commit with no
   shared git history or remote pointing back at the source: upstream is
   shallow-cloned into a scratch directory, its `skills/` contents are copied
   out, and the scratch clone (`.git` included) is discarded before anything is
   committed to Gitea.
4. Writes the tokens to `git-skillsd-token` / `git-skillsd-registry-token`.

Which repo gets seeded is set by `GITEA_SEED_URL` (a full clone URL) and
`GITEA_SEED_REF` (branch/tag), declared with their current defaults at the top
of the [Makefile](../Makefile). It needs a `skills/` directory at its root -
that's the tree that gets copied:

```bash
make gitea-up GITEA_SEED_URL=https://github.com/me/my-skills.git
```

They're read at bootstrap only, so switching repos means `make gitea-down &&
make gitea-up`. Skill names in the examples below (`internal-comms`,
`algorithmic-art`) are whatever the seeded repo happens to contain.

`local/values.yaml` already points `skillsRepo.url`, `registry.skillsRepo.url`,
and `registry.github.{owner,repo,apiBaseURL}` at this Gitea instance - no GitHub
account or repo needed for local dev apart from the initial clone. Gitea's REST
API deliberately mirrors GitHub's for pull requests, so `SubmitProposal` (which
opens a PR via that API) works against it unmodified.

Re-running `make gitea-up` is a no-op once bootstrapped: it checks
`git-skillsd-registry-token` against the *currently running* Gitea, not just
that the file exists. Gitea's storage is an `emptyDir` (gone on every pod
restart), so a token left over from a since-wiped instance is detected as stale
and replaced by a fresh bootstrap.

Gitea's Deployment is owned by `gitea-up`, *not* Tilt - the Tiltfile only
port-forwards it.

Accordingly, `make dev-down` leaves Gitea and its tokens up for the next `make
dev`. Teardown paths that *do* destroy the pod wipe the now-unusable token files
with it:

```bash
make gitea-down    # delete just Gitea, for a clean re-bootstrap
make cluster-down  # ctlptl delete (whole kind cluster)
```

A raw `kubectl delete -f local/gitea.yaml` leaves those token files behind -
harmless, since the next `make gitea-up` detects them as stale.

Gitea is reachable at `localhost:3000` once Tilt has forwarded it (`admin` user
`skillset-admin`; its password is known only to the `gitea-up` run that created
it - reset rather than try to recover it).

## Pointing at a real GitHub repo instead

Point `skillsRepo.url` / `registry.skillsRepo.url` / `registry.github.*` in
[values.yaml](values.yaml) back at a real (or throwaway public) GitHub repo,
then supply credentials yourself *instead* of running `make gitea-up`.

### GitHub App authentication

Gitea does not support Github App authentication, so a real repo is still
required to exercise this auth feature locally.
[Register an app](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
with `Contents: Read and write` + `Pull requests: Read and write`, install it on
the repo (e.g. a private throwaway), then create `github-app.json` (gitignored)
in the same location as app key:

```json
{
  "appId": "Iv23xxxxxxxxxxxxxxxx",
  "installationId": 12345678,
  "privateKeyPath": "local/github-app.pem"
}
```

The Tiltfile creates a Secret from the key, switches both components to
`githubApp` mode, and enables the registry. It takes precedence over the token
files below, and both components share the one installation - the read/write
split is a production concern the local loop doesn't model.

### Tokens

Drop fine-grained personal access tokens at `git-skillsd-token` /
`git-skillsd-registry-token` (Settings → Developer settings → Personal access
tokens → Fine-grained tokens,
[repository access](https://github.com/settings/personal-access-tokens/new)
limited to the one skills repo):

| File | Used by | Permissions |
|---|---|---|
| `git-skillsd-token` | `skillsd`'s read-only clone | `Contents: Read-only` |
| `git-skillsd-registry-token` | `skillsd-registry`'s push + PR write path | `Contents: Read and write`, `Pull requests: Read and write` |

If fine-grained tokens aren't available (e.g. an older GitHub Enterprise
instance), a classic PAT with the `repo` scope works for either file too —
`skillsd` just won't use anything beyond read access from it. Also revert
`registry.github.apiBaseURL` to `https://api.github.com`.

See the root [README.md](../README.md) for the full local dev walkthrough (`make
dev`, private-repo auth, teardown). This file covers talking to the running
instance once it's up.

---

## Talking to a local deployment without a client app

`skillsd` is a plain gRPC service with
[server reflection](https://github.com/grpc/grpc/blob/master/doc/server-reflection.md)
enabled (`reflection.Register(grpcServer)` in
[cmd/skillsd/main.go](../cmd/skillsd/main.go)), so you don't need the `.proto`
file or a generated client on hand — `grpcurl` can discover the service and its
message shapes at runtime.

### 1. Get a port-forward

If you're running via `make dev` / `tilt up`, the Tiltfile already forwards
`localhost:8080` to the pod (`port_forwards=['8080:8080']` on the `skillsd`
resource) — nothing else to do, skip to step 2.

Outside of Tilt (e.g. Tilt UI paused, or you just want a plain kubectl session),
forward the Service directly:

```bash
kubectl --context kind-skillsd port-forward svc/skillsd 8080:8080
```

(`svc/skillsd` because the Helm release is named `skillsd` and the chart's
fullname template collapses to just the chart name when release == chart name —
see
[charts/skillsd/templates/_helpers.tpl](../charts/skillsd/templates/_helpers.tpl).)

### 2. Call it with grpcurl

List services/methods via reflection (confirms the port-forward is live and
reflection is working):

```bash
grpcurl -plaintext localhost:8080 list
grpcurl -plaintext localhost:8080 list skills.v1.SkillService
```

`ListSkills` — metadata only, no context files:

```bash
grpcurl -plaintext -d '{}' localhost:8080 skills.v1.SkillService/ListSkills
```

Filtered by category, with context files included:

```bash
grpcurl -plaintext -d '{
  "category": "data",
  "include_context_files": true
}' localhost:8080 skills.v1.SkillService/ListSkills
```

`GetSkill` — fetch one skill by name:

```bash
grpcurl -plaintext -d '{
  "skill_name": "algorithmic-art",
  "include_context_files": true
}' localhost:8080 skills.v1.SkillService/GetSkill
```

An unknown `skill_name` is a `NotFound` error, not an empty/zero-value response:

```bash
grpcurl -plaintext -d '{"skill_name": "does-not-exist"}' localhost:8080 skills.v1.SkillService/GetSkill
```

`GetClientGuide` — fetches the embedded usage guide for the API itself (not part
of `ListSkills`):

```bash
grpcurl -plaintext -d '{}' localhost:8080 skills.v1.SkillService/GetClientGuide
```

Health check (via `grpc.health.v1.Health`, registered alongside `SkillService`
in main.go):

```bash
grpcurl -plaintext localhost:8080 grpc.health.v1.Health/Check
```

---

## Exercising skillsd-registry (proposals + PRs)

`skillsd-registry` is enabled by default in the chart (`registry.enabled:
true`), but the Tiltfile only sets `registry.enabled=true` once it has a
credential to give it - either `local/git-skillsd-registry-token` or
`local/github-app.json`. Without one, `tilt up` brings up the read fleet alone.
To try the write path locally:

1. Fill in `registry.skillsRepo.url` and `registry.github.owner`/`repo` in
   [values.yaml](values.yaml) - point them at a real (ideally throwaway) GitHub
   repo you can push branches and open PRs against.
2. Drop a GitHub token with push + pull-request write access on that repo at
   `local/git-skillsd-registry-token` (gitignored). The Tiltfile picks this up,
   creates a Secret from it, and sets `registry.enabled=true` automatically - no
   `helm_set` flags to pass by hand.
3. `tilt up`. Once `skillsd-registry` is healthy, it's forwarded at
   `localhost:8081`.

Propose a change (full new file content, not a patch - the server computes the
diff):

```bash
grpcurl -plaintext -d '{
  "skill_name": "internal-comms",
  "agent_id": "agent-1",
  "proposal_id": "fix-typo",
  "commit_message": "fix typo in description",
  "files": [{"file_path": "SKILL.md", "content": "---\nname: internal-comms\ndescription: A set of resources to help me write all kinds of internal communications, using the formats that my company likes to use. Claude should use this skill whenever asked to write some sort of internal communications (status reports, leadership updates, 3P updates, company newsletters, FAQs, incident reports, project updates, etc.).\nlicense: Complete terms in LICENSE.txt\n---\n\n## When to use this skill\nTo write internal communications, use this skill for:\n- 3P updates (Progress, Plans, Problems)\n- Company newsletters\n- FAQ responses\n- Status reports\n- Leadership updates\n- Project updates\n- Incident reports\n\n## How to use this skill\n\nTo write any internal communication:\n\n1. **Identify the communication type** from the request\n2. **Load the appropriate guideline file** from the `examples/` directory:\n    - `examples/3p-updates.md` - For Progress/Plans/Problems team updates\n    - `examples/company-newsletter.md` - For company-wide newsletters\n    - `examples/faq-answers.md` - For answering frequently asked questions\n    - `examples/general-comms.md` - For anything else that doesn'\''t explicitly match one of the above\n3. **Follow the specific instructions** in that file for formatting, tone, and content gathering\n\nIf the communication type doesn'\''t match any existing guideline, ask for clarification or more context about the desired format.\n\n## Keywords\n3P updates, company newsletter, company comms, weekly update, FAQs, common questions, updates, internal comms\n"}]
}' localhost:8081 skills.v1.ProposalService/ProposeChange
```

(Re-running this exact call fails with `cannot create empty commit: clean
working tree` once the branch already has this content committed - expected, not
a bug.)

Inspect it (includes the unified diff against base):

```bash
grpcurl -plaintext -d '{"branch": "proposals/agent-1/internal-comms/fix-typo"}' \
  localhost:8081 skills.v1.ProposalService/GetProposal
```

View the skill as of that proposal branch, or as of the base branch (`ref`
empty):

```bash
grpcurl -plaintext -d '{"skill_name": "internal-comms", "ref": "proposals/agent-1/internal-comms/fix-typo"}' \
  localhost:8081 skills.v1.ProposalService/GetSkillAtRef
```

List open proposals for a skill (or drop `skill_name` to list everything):

```bash
grpcurl -plaintext -d '{"skill_name": "internal-comms"}' \
  localhost:8081 skills.v1.ProposalService/ListProposals
```

Group a skill's open proposals by whether they touch overlapping regions of the
same files (`include_singletons` also surfaces proposals with no overlap,
otherwise only contested groups are returned):

```bash
grpcurl -plaintext -d '{"skill_name": "internal-comms", "include_singletons": true}' \
  localhost:8081 skills.v1.ProposalService/ListProposalClusters
```

Push the branch and open a real GitHub pull request for human review:

```bash
grpcurl -plaintext -d '{"branch": "proposals/agent-1/internal-comms/fix-typo"}' \
  localhost:8081 skills.v1.ProposalService/SubmitProposal
```

---

## Reporting skill outcomes (skillsd-registry evidence)

`EvidenceService` shares `skillsd-registry`'s endpoint (`localhost:8081`) and
lets clients report how a skill performed in a session, then read that back as
either raw reports or an aggregated per-`(skill, commit)` signal. Like
`SubmitProposal`, this is guarded by a chart flag (`registry.evidence.enabled`)
- an `Unimplemented` error here means it's off on this deployment, not a bug.

Report an outcome. `report_id` is caller-generated and makes the call idempotent
- re-sending the same one is a no-op, not a duplicate report:

```bash
grpcurl -plaintext -d '{
  "report_id": "verify-example-1",
  "agent_id": "agent-1",
  "session_id": "session-1",
  "skills": [{
    "skill_name": "internal-comms",
    "skill_commit": "<commit from GetSkill/ListSkills>",
    "verdict": "VERDICT_APPLIED_WITH_CORRECTION",
    "note": "worked, but had to reformat the FAQ section by hand"
  }]
}' localhost:8081 skills.v1.EvidenceService/ReportOutcome
```

(Re-running this exact call returns `{"recorded": false}` the second time -
expected: the `report_id` already exists, so it's treated as a retry, not a new
report.)

List the individual reports behind a skill (useful before writing a proposal -
read what actually went wrong and cite the `report_id`s in
`ProposeChange.motivating_report_ids`):

```bash
grpcurl -plaintext -d '{"skill_name": "internal-comms"}' \
  localhost:8081 skills.v1.EvidenceService/ListOutcomeReports
```

Get the aggregated signal - one row per `(skill, commit)` with
`reported_sessions`, `verdict_counts`, and `defect_rate`:

```bash
grpcurl -plaintext -d '{"skill_name": "internal-comms"}' \
  localhost:8081 skills.v1.EvidenceService/ListSkillSignals
```

---

## Automated verification (`local/verify/`)

The manual `grpcurl` calls above are also codified as re-runnable check scripts,
against whatever's currently deployed (the Gitea stand-in by default, or a real
GitHub repo if you've pointed `values.yaml` at one):

```bash
make verify
# or directly:
./local/verify/run-all.sh
```

This runs, in order:

| Script | Covers |
|---|---|
| `00_health.sh` | reflection + `grpc.health.v1.Health` on both `skillsd` and `skillsd-registry` |
| `10_skillservice.sh` | `ListSkills`, `GetSkill` (found + not-found), `GetClientGuide` |
| `20_proposal_flow.sh` | `ProposeChange` → `GetProposal` → `GetSkillAtRef` → `ListProposals` → `ListProposalClusters` → `SubmitProposal` |
| `30_evidence.sh` | `ReportOutcome` (including idempotent replay), `ListOutcomeReports`, `ListSkillSignals` |

Each script is independently runnable (`./local/verify/10_skillservice.sh`) and
safely repeatable: `20_proposal_flow.sh` mints a timestamp-suffixed
`proposal_id` each run, so it never hits the "clean working tree" error above.
Scripts covering an optional feature (`SubmitProposal` when
`submitProposalEnabled: false`, `EvidenceService` when disabled) print a `skip`
line and exit 0, so `make verify` reflects what's actually enabled.

They default to `SKILL_NAME=internal-comms` and the standard 8080/8081
port-forwards - override `SKILL_NAME` if your seed repo has no such skill, and
`SKILLSD_ADDR`/`REGISTRY_ADDR` to point elsewhere.
