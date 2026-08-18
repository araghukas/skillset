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
curl -fsS localhost:8080/healthz                # confirm the pod is up
claude mcp add --transport http skillsd http://localhost:8080/mcp
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

Enable `skillsd-registry` once agents need to suggest changes and/or report
outcomes. It needs its own, more privileged credential: `Contents: Read and
write` plus `Pull requests: Read and write`. Keeping it separate from step 1's
is the point — the read fleet has no business holding something that can push.

```yaml
registry:
  enabled: true
  skillsRepo:
    url: "https://github.com/<org>/<skills-repo>.git"
  # autoSubmitEndorsements is how many agents must stand behind one fix -
  # its author plus the agents that read and endorsed it - before a PR
  # opens. Set it explicitly if you want a different threshold.
  # A value of 0 means suggestions never auto-push.
  autoSubmitEndorsements: 2
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
curl -fsS localhost:8081/healthz
claude mcp add --transport http skillsd-registry http://localhost:8081/mcp
```

Full value reference: [helm-chart.md](helm-chart.md). Storage/backup
implications of turning this on: [data-stores.md](data-stores.md).

## 3. Point an agent at it

The easiest option is to use an onboarding script, e.g. for Claude Code,
[scripts/onboard-claude.sh](../scripts/onboard-claude.sh):

```bash
curl -fsSL https://raw.githubusercontent.com/araghukas/skillset/main/scripts/onboard-claude.sh | bash -s -- \
  --scope user \
  --skillsd-url http://skillsd.<namespace>.svc.cluster.local:8080/mcp \
  --registry-url http://skillsd-registry.<namespace>.svc.cluster.local:8081/mcp
```

This registers the `skillsd` and `skillsd-registry` MCP servers, pre-approves
all of their tools in the agent's permissions, and assigns the agent a stable
`SKILLSET_AGENT_ID` — by editing `.claude/settings.json` and `.mcp.json` in the
directory where it's invoked.

**Re-run it after upgrading the servers.** The permission and hook blocks both
converge on the tool set that version of the script knows about — newly added
tools are granted, ones that no longer exist are dropped — and the run prints
what changed. An agent whose permissions predate a tool doesn't error when it
meets one, it quietly works around what it can't call, so an upgrade that adds
a tool needs this pass to actually reach the fleet.

It also installs hooks that hold the agent to the reporting contract: a turn
that loads a skill is blocked from ending until it has called `report_outcome`
for it, with the skill's `commit` handed back so the report can name a version.
Pass `--no-hooks` to skip them; they're skipped anyway when no
`--registry-url` is configured, since there's nowhere to report to.

The hooks are [scripts/skillset-hook.sh](../scripts/skillset-hook.sh), which
the onboarding script carries a copy of and writes to `~/.claude/skillset/`.

```mermaid
sequenceDiagram
    participant Op as Operator
    participant Agent as AI Agent
    participant Read as skillsd :8080
    participant Write as skillsd-registry :8081
    participant Hook as reporting hooks

    Op->>Agent: onboarding script (registers servers + permissions + hooks)
    Agent->>Read: initialize
    Read-->>Agent: instructions (onboarding guide) + tools/list
    Note over Agent: fully onboarded — no further<br/>docs, schema files, or SDK needed,<br/>no first-use permission prompts
    Agent->>Read: list_skills / get_skill ...
    Hook->>Hook: remember skill@commit as owed
    Agent->>Write: record_suggestion / report_outcome ...
    Hook->>Agent: turn ends — block until every skill is reported
```

## 4. Local development

For iterating on `skillset` itself (not just deploying it), `make dev` brings up
a local `kind` cluster with Tilt handling build/deploy/live-reload — see the
root [README.md](../README.md#local-development) for prerequisites and
[local/README.md](../local/README.md) for a full MCP walkthrough against
the local instance.
