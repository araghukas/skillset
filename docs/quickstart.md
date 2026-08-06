# Quickstart

Deploying `skillset` and pointing an agent at it, end to end.

## 1. Deploy the read fleet

The minimum viable deployment is just `skillsd`, pointed at a public skills
repo, with the write path off:

```yaml
# values.yaml
skillsRepo:
  url: "https://github.com/<org>/<skills-repo>.git"
  branch: main
  subPath: skills   # or "" if SKILL.md directories sit at repo root

registry:
  enabled: false
```

```bash
helm install skillsd charts/skillsd -f values.yaml
kubectl get pods -l app.kubernetes.io/instance=skillsd
kubectl port-forward svc/skillsd 8080:8080
grpcurl -plaintext localhost:8080 list          # confirm reflection + SkillService are up
```

A private repo needs credentials. The recommended route is a GitHub App:

1. [Register a GitHub App](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
   with `Contents: Read` under repository permissions.
2. Generate a private key and download the `.pem`.
3. Install the app on the skills repository. Note its **app ID** (or client ID)
   from the app's settings page, and its **installation ID** from the
   installation URL — `.../settings/installations/<installationId>`. These are
   two different numbers.

```bash
kubectl create secret generic skillsd-github-app --from-file=private-key.pem=./your-app.private-key.pem
```

```yaml
skillsRepo:
  githubApp:
    appId: "Iv23xxxxxxxxxxxxxxxx"
    installationId: "12345678"
    privateKeySecret: skillsd-github-app
```

<details> <summary>Or, a token</summary>

A fine-grained PAT still works, and is the only option against a host with no
GitHub App equivalent. Set `tokenSecret` **or** `githubApp`, not both:

```bash
kubectl create secret generic skillsd-git-auth --from-literal=token=<fine-grained PAT, Contents: read>
```

```yaml
skillsRepo:
  tokenSecret: skillsd-git-auth
```
</details>

## 2. Add the write path (optional)

Enable `skillsd-registry` once agents need to propose changes and/or report
outcomes. It needs its own, more privileged credential: `Contents: Read and
write` plus `Pull requests: Read and write`. Keeping it separate from step 1's
is the point — the read fleet has no business holding something that can push.

```yaml
registry:
  enabled: true
  skillsRepo:
    url: "https://github.com/<org>/<skills-repo>.git"
  github:
    owner: "<org>"
    repo: "<skills-repo>"
    githubApp:
      appId: "Iv23yyyyyyyyyyyyyyyy"
      installationId: "87654321"
      privateKeySecret: skillsd-registry-github-app
```

<details> <summary>Or, a token</summary>

```bash
kubectl create secret generic skillsd-registry-git-auth \
  --from-literal=token=<fine-grained PAT, Contents: read/write + Pull requests: read/write>
```

```yaml
registry:
  github:
    tokenSecret: skillsd-registry-git-auth
```
</details>

```bash
helm upgrade skillsd charts/skillsd -f values.yaml
kubectl port-forward svc/skillsd-registry 8081:8081
grpcurl -plaintext localhost:8081 list skills.v1.ProposalService
```

Full value reference: [helm-chart.md](helm-chart.md). Storage/backup
implications of turning this on: [data-stores.md](data-stores.md).

## 3. Point an agent at it

This is the only integration step — there's no SDK to install. An agent needs
exactly two things: an endpoint, and the instruction to onboard itself.

```mermaid
sequenceDiagram
    participant Op as Operator
    participant Agent as AI Agent
    participant Read as skillsd :8080
    participant Write as skillsd-registry :8081

    Op->>Agent: "skillsd is at skillsd.<ns>.svc:8080\n(and skillsd-registry at :8081, if deployed)"
    Agent->>Read: gRPC reflection: list services
    Read-->>Agent: SkillService, ProposalService, EvidenceService
    Agent->>Read: GetClientGuide({})
    Read-->>Agent: onboarding SKILL.md — every RPC,<br/>when to call it, what to send
    Note over Agent: fully onboarded — no further<br/>docs, proto files, or SDK needed
    Agent->>Read: ListSkills / GetSkill ...
    Agent->>Write: ProposeChange / ReportOutcome ...
```

In practice, "point an agent at it" means putting one or two lines in whatever
system prompt or tool config wires the agent up — something like:

```
You have access to a skillsd gRPC endpoint at skillsd.<namespace>.svc.cluster.local:8080
(and, if available, skillsd-registry at :8081). On first use, call
SkillService.GetClientGuide (no arguments) to learn the full API — which RPCs
to call, when, and with what fields. Do this before assuming any RPC's shape.
```

Everything past that point is between the agent and `GetClientGuide` — that
guide (served by the running binary itself, not this repo) is the actual API
reference. Treat it as authoritative over anything written here if the two ever
disagree.

## 4. Local development

For iterating on `skillset` itself (not just deploying it), `make dev` brings up
a local `kind` cluster with Tilt handling build/deploy/live-reload — see the
root [README.md](../README.md#local-development) for prerequisites and
[local/README.md](../local/README.md) for a full `grpcurl` walkthrough against
the local instance.
