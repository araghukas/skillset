# local/

## Deploying local dev

Three modes, all launched with `make dev`. They differ only in what backs the
skills repo and how the two components authenticate to it:

| # | Mode | Backing repo | Auth | Setup |
|---|---|---|---|---|
| 1 | Gitea stand-in (default) | throwaway in-cluster Gitea, seeded from a source repo | tokens minted by `gitea-up` | none |
| 2 | Real repo, tokens | your GitHub repo | fine-grained PATs you supply | [below](#2-real-repo-with-token-auth) |
| 3 | Real repo, GitHub App | your GitHub repo | GitHub App installation | [below](#3-real-repo-with-github-app-auth) |

```bash
make dev                 # 1. Gitea stand-in
make dev GITEA=0         # 2. real repo, tokens (skips the Gitea bootstrap)
make dev                 # 3. real repo, GitHub App - auto-skips Gitea when
                         #    local/github-app.json exists
```

Mode 3 is the only way to exercise the GitHub App path at all, since Gitea
doesn't support GitHub Apps. Modes 2 and 3 must skip the Gitea bootstrap
because `gitea-up` deletes token files that don't authenticate against Gitea —
`local/github-app.json` does that automatically, `GITEA=0` does it by hand.

Teardown, any mode: `make dev-down` (stop Tilt, keep the cluster and Gitea up
for next time) or `make cluster-down` (tear down everything). Full target
list: `make help`.

## 1. Gitea stand-in (default)

The default local mode is (almost) fully sandboxed. While it does clone
a Github repository on startup, **it does not push/pull nor make PRs against any actual Github repos.**
This mode is for development and testing.

```bash
make dev
```

1. Applies `local/gitea.yaml` (a single-replica, SQLite-backed Gitea) to the
   kind cluster and waits for it to come up.
2. Creates an admin user, a `skills` repo, and two tokens (read-only and
   read/write) via Gitea's API.
3. Seeds the repo on `main` with a private copy of the `skills/` tree from
   whatever `GITEA_SEED_URL` points at. It's a fresh commit with no shared git
   history or remote pointing back at the source: upstream is shallow-cloned
   into a scratch directory, its `skills/` contents are copied out, and the
   scratch clone (`.git` included) is discarded before anything is committed
   to Gitea.
4. Writes the tokens to `git-skillsd-token` / `git-skillsd-registry-token`.

Which repo gets seeded is set by `GITEA_SEED_URL` (a full clone URL) and
`GITEA_SEED_REF` (branch/tag), declared with their current defaults at the top
of the [Makefile](../Makefile). Override them on the `make dev` line and they
reach `gitea-up` as usual:

```bash
make dev GITEA_SEED_URL=https://github.com/me/my-skills.git
```

Since `gitea-up` no-ops against an already-bootstrapped instance, switching seed
repos on an existing cluster means tearing Gitea down first:

```bash
make gitea-down
make dev GITEA_SEED_URL=https://github.com/me/my-other-skills.git
```

Gitea's Deployment is owned by `gitea-up`, *not* Tilt — the Tiltfile only
port-forwards it. So `make dev-down` leaves Gitea and its tokens up for the
next `make dev`, while `make gitea-down` / `make cluster-down` destroy the pod
and wipe the now-unusable token files with it.

Gitea is reachable at `localhost:3000` once Tilt has forwarded it (`admin`
user `skillset-admin`; its password is known only to the `gitea-up` run that
created it — reset rather than try to recover it).

## 2. Real repo with token auth

Token-based Github auth it not recommended unless GitHub App is not possible
(e.g. no admin rights to install one).

Prepare two fine-grained tokens with [repository access](https://github.com/settings/personal-access-tokens/new)
limited to the one skills repo. A classic PAT with the `repo` scope works for
either file too, e.g. against an older GitHub Enterprise instance.

| File | Used by | Permissions |
|---|---|---|
| `git-skillsd-token` | `skillsd`'s read-only clone | `Contents: Read-only` |
| `git-skillsd-registry-token` | `skillsd-registry`'s push + PR write path | `Contents: Read and write`, `Pull requests: Read and write` |

This is the one mode needing [values.yaml](values.yaml) edits: point
`skillsRepo.url` and `registry.skillsRepo.url` at the repo,
`registry.github.owner`/`repo` at its coordinates, and
`registry.github.apiBaseURL` at `https://api.github.com`. Then:

```bash
# Write the two tokens to local/git-skillsd-token and
# local/git-skillsd-registry-token (both gitignored).

make dev GITEA=0
```

## 3. Real repo with GitHub App auth

Prepare:
[register an app](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
with `Contents: Read and write` + `Pull requests: Read and write`, install it
on the repo (e.g. a private throwaway), and download its private key.

```bash
mv ~/Downloads/<app-name>.*.private-key.pem local/github-app.pem

cat > local/github-app.json <<'EOF'
{
  "appId": "<app id>",
  "installationId": <installation id>,
  "privateKeyPath": "local/github-app.pem",
  "owner": "<repo owner>",
  "repo": "<repo name>",
  "branch": "main"
}
EOF

make dev
```

`owner`/`repo`/`branch` point both components at the repo, so no
[values.yaml](values.yaml) edits are needed; omit them to use whatever it
already has. Both components share the one installation — the read/write split
is a production concern the local loop doesn't model.

---

# Talking to a local deployment

This section describes manual interaction with the skillset API across
`skillsd` and `skillsd-registry`. Both are plain gRPC services with
[server reflection](https://github.com/grpc/grpc/blob/master/doc/server-reflection.md)
enabled to help guide AI agents, along with a `GetClientGuide` method
which serves a meta-skill about the API.

*Any skill names in the examples below (e.g. `internal-comms`, `algorithmic-art`)*
*correspond to the default public seed repo of the Gitea-backed deployment.*


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

`GetClientGuide` fetches the embedded usage guide for the API itself (not part
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

All three modes above supply the registry's write credential, so it comes up
without extra work and is forwarded at `localhost:8081`. `SubmitProposal`
opens a real PR against whichever remote is configured (e.g. the Gitea `skills`
repo in mode 1, since its REST API mirrors GitHub's for pull requests).

Propose a change (full new file content, not a patch - the server computes the diff):

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
