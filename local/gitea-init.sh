#!/usr/bin/env bash
# One-time (idempotent) bootstrap for the local Gitea stand-in: applies
# local/gitea.yaml, waits for it to come up, creates an admin user + a
# "skills" repo, mints a read-only and a read/write token, seeds the repo
# with a private copy of an upstream skills repo's skills/ tree on `main`
# (anthropics/skills by default; set GITEA_SEED_URL to use another - a
# fresh commit with no shared history or remote, see the seeding step), and
# writes the tokens to local/git-skillsd-token / local/git-skillsd-registry-token
# - the same gitignored files the Tiltfile already looks for.
#
# Safe to re-run: if git-skillsd-registry-token already exists AND still
# authenticates against the currently running Gitea, it exits immediately.
# Otherwise (first run, a stale token left over from a wiped Gitea pod, or a
# leftover real-GitHub token from local/README.md's "point at a real repo"
# path) it discards the unusable token file(s) and bootstraps from scratch.
set -euo pipefail

KIND_CONTEXT="kind-skillsd"
NAMESPACE="default"
ADMIN_USER="skillset-admin"
REPO_NAME="skills"
UPSTREAM_URL="${GITEA_SEED_URL:-https://github.com/anthropics/skills.git}"
UPSTREAM_REF="${GITEA_SEED_REF:-main}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCAL_DIR="$ROOT_DIR/local"
READ_TOKEN_FILE="$LOCAL_DIR/git-skillsd-token"
WRITE_TOKEN_FILE="$LOCAL_DIR/git-skillsd-registry-token"

echo "gitea-init: applying local/gitea.yaml"
kubectl --context "$KIND_CONTEXT" apply -f "$LOCAL_DIR/gitea.yaml"
kubectl --context "$KIND_CONTEXT" rollout status deployment/gitea -n "$NAMESPACE" --timeout=180s

PF_PID=""
SEED_DIR=""
UPSTREAM_DIR=""
cleanup() {
  [[ -n "$PF_PID" ]] && kill "$PF_PID" >/dev/null 2>&1 || true
  [[ -n "$SEED_DIR" ]] && rm -rf "$SEED_DIR" || true
  [[ -n "$UPSTREAM_DIR" ]] && rm -rf "$UPSTREAM_DIR" || true
}
trap cleanup EXIT

echo "gitea-init: starting temporary port-forward to gitea:3000"
kubectl --context "$KIND_CONTEXT" port-forward svc/gitea 3000:3000 -n "$NAMESPACE" >/tmp/gitea-port-forward.log 2>&1 &
PF_PID=$!

for i in $(seq 1 30); do
  if curl -fsS "http://localhost:3000/api/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! curl -fsS "http://localhost:3000/api/healthz" >/dev/null 2>&1; then
  echo "gitea-init: gitea did not become healthy in time" >&2
  exit 1
fi

# If a token file already exists, only trust it if it still authenticates
# against *this* Gitea instance - otherwise discard it (no .stale- copies
# left behind) and fall through to a full rebootstrap, which overwrites
# both files with freshly minted tokens further down.
if [[ -f "$WRITE_TOKEN_FILE" ]]; then
  if curl -fsS -H "Authorization: token $(cat "$WRITE_TOKEN_FILE")" \
      "http://localhost:3000/api/v1/user" >/dev/null 2>&1; then
    echo "gitea-init: $WRITE_TOKEN_FILE already authenticates against this Gitea instance; nothing to do."
    exit 0
  fi
  echo "gitea-init: $WRITE_TOKEN_FILE doesn't authenticate against this Gitea instance (stale or from a different remote) - discarding it and rebootstrapping." >&2
  rm -f "$WRITE_TOKEN_FILE" "$READ_TOKEN_FILE"
fi

# Create an admin user
ADMIN_PASS="$(openssl rand -hex 16)"

echo "gitea-init: creating admin user"
# kubectl exec lands as root in this image (unlike the container's own
# entrypoint, which starts as root only to fix up permissions, then drops
# to the "git" user before ever running gitea itself). Gitea refuses to run
# as root, so run the CLI through su-exec exactly as the entrypoint would.
if ! kubectl \
    --context "$KIND_CONTEXT" \
    exec deploy/gitea \
    -n "$NAMESPACE" \
    -- \
    su-exec git gitea admin user create \
    --admin \
    --username "$ADMIN_USER" \
    --password "$ADMIN_PASS" \
    --email "admin@skillset.local" \
    --must-change-password=false; then
  echo "gitea-init: admin user create failed - it may already exist from a partial previous run." >&2
  echo "gitea-init: run 'make gitea-down' to reset and try again." >&2
  exit 1
fi

echo "gitea-init: creating repo $ADMIN_USER/$REPO_NAME"
curl -fsS -u "$ADMIN_USER:$ADMIN_PASS" -X POST "http://localhost:3000/api/v1/user/repos" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$REPO_NAME\",\"auto_init\":false,\"private\":false,\"default_branch\":\"main\"}" >/dev/null

# Mint PAT tokens for admin user
mint_token() {
  local name="$1" scope="$2" resp token
  resp="$(curl -fsS -u "$ADMIN_USER:$ADMIN_PASS" -X POST "http://localhost:3000/api/v1/users/$ADMIN_USER/tokens" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$name\",\"scopes\":[\"$scope\"]}")"
  token="$(jq -r '.sha1 // empty' <<<"$resp")"
  if [[ -z "$token" ]]; then
    echo "gitea-init: minting token '$name' failed - unexpected response: $resp" >&2
    exit 1
  fi
  printf '%s' "$token"
}

echo "gitea-init: minting read-only token"
READ_TOKEN="$(mint_token skillsd-read read:repository)"
echo "gitea-init: minting read/write token"
WRITE_TOKEN="$(mint_token skillsd-registry-write write:repository)"

# Seed the gitea repo with a *private copy* of the upstream repo's skills/
# tree: a real, varied set of skills to test against, but with no shared
# git history and no remote pointing back at the real GitHub repo. This
# shallow-clones upstream into a scratch dir, copies just the file
# contents into a brand-new git repo (SEED_DIR, its own `git init` below),
# and discards the scratch clone (and its .git, and its history) entirely
# before anything is committed or pushed.
echo "gitea-init: fetching $UPSTREAM_URL ($UPSTREAM_REF) to copy its skills/ tree"

# Shallow-clone upstream into a throwaway dir - just to read its files.
UPSTREAM_DIR="$(mktemp -d)"
git clone --depth 1 --branch "$UPSTREAM_REF" -q "$UPSTREAM_URL" "$UPSTREAM_DIR"

# Copy only the file contents out, then discard the clone (and its .git,
# and every trace of upstream history) before it touches SEED_DIR.
SEED_DIR="$(mktemp -d)"
if [[ ! -d "$UPSTREAM_DIR/skills" ]]; then
  echo "gitea-init: $UPSTREAM_URL ($UPSTREAM_REF) has no skills/ directory to seed from." >&2
  exit 1
fi
cp -R "$UPSTREAM_DIR/skills" "$SEED_DIR/skills"
rm -rf "$UPSTREAM_DIR"
UPSTREAM_DIR=""

echo "gitea-init: seeding $REPO_NAME with that copy on main"

# SEED_DIR is a brand-new repo from here on - no relation to upstream's.
git -C "$SEED_DIR" init -q -b main

# Stage the copied files as this new repo's first (and only) commit.
git -C "$SEED_DIR" \
  -c user.name="gitea-init" \
  -c user.email="gitea-init@skillset.local" \
  add -A

git -C "$SEED_DIR" \
  -c user.name="gitea-init" \
  -c user.email="gitea-init@skillset.local" \
  commit -q -m "seed: private copy of $UPSTREAM_URL's skills/ tree ($UPSTREAM_REF, $(date -u +%FT%TZ))"

# Push that single commit to Gitea's main - the only history it will ever have.
git -C "$SEED_DIR" push -q \
  "http://${ADMIN_USER}:${WRITE_TOKEN}@localhost:3000/${ADMIN_USER}/${REPO_NAME}.git" \
  main

printf '%s' "$READ_TOKEN" > "$READ_TOKEN_FILE"
printf '%s' "$WRITE_TOKEN" > "$WRITE_TOKEN_FILE"

echo "gitea-init: done."
echo "gitea-init: repo is at http://gitea:3000/${ADMIN_USER}/${REPO_NAME}.git (in-cluster)"
echo "gitea-init: wrote $READ_TOKEN_FILE and $WRITE_TOKEN_FILE"
