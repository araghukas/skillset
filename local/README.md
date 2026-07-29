# local/

Support files for the local dev loop (`make dev` / `tilt up`): a `ctlptl`-managed
`kind` cluster + image registry, and the Helm values override Tilt deploys with.

```
local/
├── cluster.yaml                # ctlptl spec: kind cluster "kind-skillsd" + registry "skillsd-registry"
├── values.yaml                 # Helm values override (replicaCount, logLevel, image, skillsRepo, registry)
├── git-skillsd-token           # optional, gitignored: read-only GitHub token for a private skillsRepo
└── git-skillsd-registry-token  # optional, gitignored: push + PR GitHub token enabling skillsd-registry (see below)
```

Both tokens are fine-grained GitHub personal access tokens (Settings →
Developer settings → Personal access tokens → Fine-grained tokens),
[repository access](https://github.com/settings/personal-access-tokens/new)
limited to the one skills repo:

| File | Used by | Permissions |
|---|---|---|
| `git-skillsd-token` | `skillsd`'s read-only clone | `Contents: Read-only` |
| `git-skillsd-registry-token` | `skillsd-registry`'s push + PR write path | `Contents: Read and write`, `Pull requests: Read and write` |

If fine-grained tokens aren't available (e.g. an older GitHub Enterprise
instance), a classic PAT with the `repo` scope works for either file too —
`skillsd` just won't use anything beyond read access from it.

See the root [README.md](../README.md) for the full local dev walkthrough
(`make dev`, private-repo auth, teardown). This file covers talking to the
running instance once it's up.

---

## Talking to a local deployment without a client app

`skillsd` is a plain gRPC service with [server reflection](https://github.com/grpc/grpc/blob/master/doc/server-reflection.md)
enabled (`reflection.Register(grpcServer)` in [cmd/skillsd/main.go](../cmd/skillsd/main.go)),
so you don't need the `.proto` file or a generated client on hand — `grpcurl`
can discover the service and its message shapes at runtime.

### 1. Get a port-forward

If you're running via `make dev` / `tilt up`, the Tiltfile already forwards
`localhost:8080` to the pod (`port_forwards=['8080:8080']` on the `skillsd`
resource) — nothing else to do, skip to step 2.

Outside of Tilt (e.g. Tilt UI paused, or you just want a plain kubectl
session), forward the Service directly:

```bash
kubectl --context kind-skillsd port-forward svc/skillsd 8080:8080
```

(`svc/skillsd` because the Helm release is named `skillsd` and the chart's
fullname template collapses to just the chart name when release == chart
name — see [charts/skillsd/templates/_helpers.tpl](../charts/skillsd/templates/_helpers.tpl).)

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
  "skill_name": "frontend-design",
  "include_context_files": true
}' localhost:8080 skills.v1.SkillService/GetSkill
```

Health check (via `grpc.health.v1.Health`, registered alongside `SkillService`
in main.go):

```bash
grpcurl -plaintext localhost:8080 grpc.health.v1.Health/Check
```

---

## Exercising skillsd-registry (proposals + PRs)

`skillsd-registry` is enabled by default in the chart (`registry.enabled: true`), which means `tilt up` needs `registry.github.tokenSecret` set from the start - the chart's `required` guard fails the whole render without one. The Tiltfile only wires that secret up once `local/git-skillsd-registry-token` is present, so that file isn't optional for local dev anymore. To try it locally:

1. Fill in `registry.skillsRepo.url` and `registry.github.owner`/`repo` in
   [values.yaml](values.yaml) - point them at a real (ideally throwaway)
   GitHub repo you can push branches and open PRs against.
2. Drop a GitHub token with push + pull-request write access on that repo
   at `local/git-skillsd-registry-token` (gitignored). The Tiltfile picks this up, creates
   a Secret from it, and sets `registry.enabled=true` automatically - no
   `helm_set` flags to pass by hand.
3. `tilt up`. Once `skillsd-registry` is healthy, it's forwarded at
   `localhost:8081`.

Propose a change (full new file content, not a patch - the server computes
the diff):

```bash
grpcurl -plaintext -d '{
  "skill_name": "frontend-design",
  "agent_id": "agent-1",
  "proposal_id": "fix-typo",
  "commit_message": "fix typo in description",
  "files": [{"file_path": "SKILL.md", "content": "---\nname: frontend-design\ndescription: designs frontends, fixed\n---\nbody\n"}]
}' localhost:8081 skills.v1.ProposalService/ProposeChange
```

Inspect it (includes the unified diff against base):

```bash
grpcurl -plaintext -d '{"branch": "proposals/agent-1/frontend-design/fix-typo"}' \
  localhost:8081 skills.v1.ProposalService/GetProposal
```

View the skill as of that proposal branch, or as of the base branch
(`ref` empty):

```bash
grpcurl -plaintext -d '{"skill_name": "frontend-design", "ref": "proposals/agent-1/frontend-design/fix-typo"}' \
  localhost:8081 skills.v1.ProposalService/GetSkillAtRef
```

Push the branch and open a real GitHub pull request for human review:

```bash
grpcurl -plaintext -d '{"branch": "proposals/agent-1/frontend-design/fix-typo"}' \
  localhost:8081 skills.v1.ProposalService/SubmitProposal
```
