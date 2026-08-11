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
a GitHub repository on startup, **it does not push/pull or make PRs against any actual GitHub repos.**
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

Token-based GitHub auth is not recommended unless GitHub App is not possible
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

`skillsd` and `skillsd-registry` are MCP servers over Streamable HTTP.

*Any skill names in the examples below (e.g. `internal-comms`, `algorithmic-art`)*
*correspond to the default public seed repo of the Gitea-backed deployment.*

### 1. Get a port-forward

If you're running via `make dev` / `tilt up`, the Tiltfile already forwards
`localhost:8080` (skillsd) and `localhost:8081` (skillsd-registry) to their
pods — nothing else to do, skip to step 2.

Outside of Tilt (e.g. Tilt UI paused, or you just want a plain kubectl session),
forward the Services directly:

```bash
kubectl --context kind-skillsd port-forward svc/skillsd 8080:8080
kubectl --context kind-skillsd port-forward svc/skillsd-registry 8081:8081
```

(`svc/skillsd` because the Helm release is named `skillsd` and the chart's
fullname template collapses to just the chart name when release == chart name —
see
[charts/skillsd/templates/_helpers.tpl](../charts/skillsd/templates/_helpers.tpl).)

### 2. Connect an MCP client

With, e.g., the Claude Code CLI:

```bash
claude mcp add --transport http skillsd http://localhost:8080/mcp
claude mcp add --transport http skillsd-registry http://localhost:8081/mcp
```

After this, `skillsd`'s tools (`list_skills`, `get_skill`,
`get_client_guide`) and `skillsd-registry`'s (`propose_change`,
`get_proposal`, and the rest) should be available in the session.

### 3. Or drive it directly

For scripting or debugging without a full client, use `curl` against the raw
JSON-RPC endpoint (the streamable transport, so include the SSE `Accept`
header even for a plain request/response call):

```bash
curl -s http://localhost:8080/healthz   # readiness/liveness probe target, not MCP

curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

`get_skill` — fetch one skill by name, with its context files:

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{
    "name": "get_skill",
    "arguments": {"skill_name": "algorithmic-art", "include_context_files": true}
  }}'
```

An unknown `skill_name` comes back as a tool error (`"isError": true` in the
result), not a protocol-level failure — the call still succeeds at the
JSON-RPC level.

`propose_change` against `skillsd-registry` — full new file content, not a
patch; the server computes the diff:

```bash
curl -s -X POST http://localhost:8081/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{
    "name": "propose_change",
    "arguments": {
      "skill_name": "internal-comms",
      "agent_id": "agent-1",
      "proposal_id": "fix-typo",
      "commit_message": "fix typo in description",
      "files": [{"file_path": "SKILL.md", "content": "---\nname: internal-comms\ndescription: fixed description\n---\nbody\n"}]
    }
  }}'
```

(Re-running this exact call fails with `cannot create empty commit: clean
working tree` once the branch already has this content committed - expected,
not a bug.)

Every other tool follows the same `tools/call` shape with its own
`name`/`arguments` — `get_proposal`, `get_skill_at_ref`, `list_proposals`,
`list_proposal_clusters`, `submit_proposal`, and (if evidence collection is
enabled) `report_outcome`, `list_outcome_reports`, `list_skill_signals`. Call
`get_client_guide` on either server, or read a connected client's onboarding
instructions, for the full argument shape and workflow each tool expects — the
guide is generated from the same source both servers advertise at connect
time, so it never drifts from what's actually registered.

---

## Automated verification (`local/verify/`)

The manual calls above are also codified as a re-runnable Go test suite,
against whatever's currently deployed (the Gitea stand-in by default, or a
real GitHub repo if you've pointed `values.yaml` at one):

```bash
make verify
# or directly:
go test -tags e2e -count=1 -v ./local/verify/...
```

It's gated behind the `e2e` build tag specifically so `go test ./...` (and
CI's regular test job) never touches it — this package talks to a real
deployment over the network, not to the Go API, and has nothing to run
against outside `make dev` / a real cluster.

| File | Covers |
|---|---|
| `health_test.go` | `/healthz` on both servers, `initialize`'s `instructions`, `tools/list` naming the expected tools |
| `skills_test.go` | `list_skills`, `get_skill` (found + not-found), `get_client_guide` |
| `proposals_test.go` | `propose_change` → `get_proposal` → `get_skill_at_ref` → `list_proposals` → `list_proposal_clusters` → `submit_proposal` |
| `evidence_test.go` | `report_outcome` (including idempotent replay), `list_outcome_reports`, `list_skill_signals` |

Each file is independently runnable (`go test -tags e2e -run TestGetSkill
./local/verify/...`) and safely repeatable: the proposal test mints a
timestamp-suffixed `proposal_id` each run, so it never hits the "clean
working tree" error above. Tests covering an optional feature
(`submit_proposal` when `submitProposalEnabled: false`, the evidence tools
when disabled) call `t.Skip`, so `make verify` reflects what's actually
enabled rather than failing on it.

They default to `SKILL_NAME=internal-comms` and the standard 8080/8081
port-forwards - override `SKILL_NAME` if your seed repo has no such skill, and
`SKILLSD_ADDR`/`REGISTRY_ADDR` to point elsewhere.
