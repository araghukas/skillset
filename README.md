# skillset

`skillset` is a gRPC registry service for [agentskills.io](https://agentskills.io)-compatible agent skills. It discovers, indexes, and serves skill metadata and content — it does **not** execute skills. Skill definitions live in a git repository and are cloned into a pod-local volume by an init container at startup; `skillsd` reads that volume once and serves it over gRPC for the lifetime of the process.

---

## Workspace Structure

```
├── buf.gen.yaml           # Protobuf generation config using Buf
├── buf.yaml                # Protobuf module definition
├── Dockerfile               # Container definition for building skillsd
├── Makefile                 # Entrypoint for build/test/local-cluster/deploy tasks (see `make help`)
├── Tiltfile                  # Local dev loop (build, deploy, port-forward) via Tilt
├── go.mod
├── cmd/
│   └── skillsd/
│       └── main.go         # gRPC server entry point & signal orchestrator
├── charts/
│   └── skillsd/             # Helm chart: Deployment (with git-clone init container), Service
├── local/
│   ├── cluster.yaml         # ctlptl spec: local kind cluster + image registry
│   └── values.yaml          # Helm values override used for local dev
├── gen/                     # Generated gRPC code (DO NOT EDIT)
│   └── skills/v1/
│       ├── skills.pb.go
│       └── skills_grpc.pb.go
├── internal/
│   ├── config/               # Environment-variable configuration loading
│   ├── registry/              # In-memory, atomically-swapped skill index; parses SKILL.md
│   ├── server/                 # gRPC SkillService implementation
│   └── storage/                # Backend abstraction; FSBackend reads the mounted skills volume
└── proto/
    └── skills/v1/
        └── skills.proto      # Canonical API definition (ListSkills, GetSkill)
```

---

## Skill format

Each skill is a directory containing a `SKILL.md` file with YAML frontmatter (`name`, `description` required; `license`, `compatibility`, `metadata`, `allowed-tools` optional), plus any supporting `scripts/`, `references/`, or `assets/` files. See the [agentskills.io specification](https://agentskills.io/specification) for the full format.

`registry.Load` walks the mounted skills directory once at startup, requires each skill's directory name to match its frontmatter `name`, and builds an in-memory index served over gRPC. There is no runtime re-indexing — picking up new skills means restarting the pod so the init container re-clones the repo.

---

## Configuration

Configuration is loaded from environment variables on startup:

| Environment Variable | Default   | Description                                                                 |
|-----------------------|-----------|-------------------------------------------------------------------------------|
| `GRPC_ADDR`            | `:8080`   | Address the gRPC server listens on                                            |
| `SKILLS_DIR`           | `/skills` | Local directory the skills volume is mounted at (populated by the init container) |
| `SKILLS_SUBPATH`       | `""`      | Optional subdirectory within `SKILLS_DIR` where skill directories actually live |

---

## Local development

Local dev runs on a `kind` cluster provisioned by `ctlptl`, with `Tilt` handling build/deploy/live-reload. Requires `go`, `docker`, `kind`, `ctlptl`, `kubectl`, `helm`, `tilt`, and `buf` on `PATH`.

```bash
make dev          # runs cluster-up, then `tilt up`
```

For a private skills repo, drop an SSH deploy key and its host's `known_hosts` at `local/git-deploy-key` / `local/git-known-hosts` (both gitignored) before running `make dev` — the Tiltfile creates a Secret from them and wires it into the chart automatically.

Run `make help` for the full list of targets (build, test, vet, proto codegen, docker build, helm lint/template, cluster teardown, log tailing, etc).

Tear down the cluster with:

```bash
make cluster-down
```
