# local/

Support files for the local dev loop (`make dev` / `tilt up`): a `ctlptl`-managed
`kind` cluster + image registry, and the Helm values override Tilt deploys with.

```
local/
├── cluster.yaml          # ctlptl spec: kind cluster "kind-skillsd" + registry "skillsd-registry"
├── values.yaml            # Helm values override (replicaCount, logLevel, image, skillsRepo)
├── git-deploy-key         # optional, gitignored: SSH deploy key for a private skillsRepo
├── git-deploy-key.pub     # optional, gitignored
└── git-known-hosts        # optional, gitignored: generated via `make git-known-hosts`
```

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
