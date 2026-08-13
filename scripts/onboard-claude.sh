#!/usr/bin/env bash
# Onboards the current Claude Code agent onto a running skillset deployment.
#
# `claude mcp add` alone registers the servers but doesn't pre-approve their
# tools, so the agent still hits a first-use permission prompt for every
# skillsd/skillsd-registry tool. This script does three things: it registers
# the servers, merges permission rules into the target settings.json so those
# prompts never happen, and assigns the agent a stable SKILLSET_AGENT_ID (a
# UUID written to settings.json's `env`) so skillsd can identify it across
# sessions. It's read-modify-write (via jq), so re-running it, or running it
# alongside unrelated manually-set permissions, is safe — the agent ID is
# generated once and reused on subsequent runs.
#
# Usage:
#   ./onboard-claude.sh [--skillsd-url <url>] [--registry-url <url>] [--scope user|project|local]
#
# With no flags, targets a local dev deployment at scope 'project':
#   ./onboard-claude.sh
#
# Fleet rollout, no local clone needed (--scope user, since there's no
# ./.claude/ to commit to):
#   curl -fsSL https://raw.githubusercontent.com/araghukas/skillset/main/scripts/onboard-claude.sh | bash -s -- \
#     --scope user \
#     --skillsd-url http://skillsd.ns.svc.cluster.local:8080/mcp \
#     --registry-url http://skillsd-registry.ns.svc.cluster.local:8081/mcp
#
# Or with env vars instead of flags (same non-interactive shape):
#   SKILLSD_URL=http://skillsd.ns.svc.cluster.local:8080/mcp \
#   SKILLSD_REGISTRY_URL=http://skillsd-registry.ns.svc.cluster.local:8081/mcp \
#   curl -fsSL https://raw.githubusercontent.com/araghukas/skillset/main/scripts/onboard-claude.sh | bash -s -- --scope user
#
# --scope controls both where `claude mcp add` registers the servers and
# which settings.json gets the permission rules:
#   user     ~/.claude/settings.json           (applies to every project this agent touches)
#   project  ./.claude/settings.json           (default; shared, meant to be committed)
#   local    ./.claude/settings.local.json     (this checkout only, gitignored by convention)
#
# SKILLSD_URL and SKILLSD_REGISTRY_URL default to a local dev deployment
# (localhost:8080/mcp and localhost:8081/mcp).
set -euo pipefail

SCOPE="project"
SKILLSD_URL="${SKILLSD_URL:-http://localhost:8080/mcp}"
SKILLSD_REGISTRY_URL="${SKILLSD_REGISTRY_URL:-http://localhost:8081/mcp}"

usage() {
	grep '^#' "$0" | sed '1d;s/^# \{0,1\}//'
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--skillsd-url)
		SKILLSD_URL="$2"
		shift 2
		;;
	--registry-url)
		SKILLSD_REGISTRY_URL="$2"
		shift 2
		;;
	--scope)
		SCOPE="$2"
		shift 2
		;;
	-h | --help) usage ;;
	*)
		echo "unknown argument: $1" >&2
		usage
		;;
	esac
done

case "$SCOPE" in
user | project | local) ;;
*)
	echo "--scope must be one of: user, project, local (got '$SCOPE')" >&2
	exit 1
	;;
esac

command -v claude >/dev/null 2>&1 || {
	echo "claude CLI not found on PATH" >&2
	exit 1
}
command -v jq >/dev/null 2>&1 || {
	echo "jq not found on PATH (required to merge permissions into settings.json)" >&2
	exit 1
}
command -v uuidgen >/dev/null 2>&1 || {
	echo "uuidgen not found on PATH (required to generate SKILLSET_AGENT_ID)" >&2
	exit 1
}

case "$SCOPE" in
user) SETTINGS_FILE="$HOME/.claude/settings.json" ;;
project) SETTINGS_FILE="./.claude/settings.json" ;;
local) SETTINGS_FILE="./.claude/settings.local.json" ;;
esac

# `claude mcp add` exits 1 with "MCP server <name> already exists in ..." on
# a repeat run, which would otherwise trip set -e. Treat that case as a
# no-op so the whole script stays safe to re-run.
mcp_add() {
	local name="$1" url="$2" output
	if output=$(claude mcp add --transport http --scope "$SCOPE" "$name" "$url" 2>&1); then
		echo "$output"
	elif [[ "$output" == *"already exists"* ]]; then
		echo "$output (already registered, skipping)"
	else
		echo "$output" >&2
		exit 1
	fi
}

echo "==> Registering skillsd ($SKILLSD_URL) [scope: $SCOPE]"
mcp_add skillsd "$SKILLSD_URL"

ALLOW_RULES=(
	"mcp__skillsd__list_skills"
	"mcp__skillsd__get_skill"
	"mcp__skillsd__get_client_guide"
)

if [ -n "$SKILLSD_REGISTRY_URL" ]; then
	echo "==> Registering skillsd-registry ($SKILLSD_REGISTRY_URL) [scope: $SCOPE]"
	mcp_add skillsd-registry "$SKILLSD_REGISTRY_URL"
	ALLOW_RULES+=(
		"mcp__skillsd-registry__get_client_guide"
		"mcp__skillsd-registry__record_suggestion"
		"mcp__skillsd-registry__report_outcome"
		"mcp__skillsd-registry__get_suggestion"
		"mcp__skillsd-registry__get_skill_at_ref"
		"mcp__skillsd-registry__list_suggestions"
		"mcp__skillsd-registry__list_suggestion_clusters"
		"mcp__skillsd-registry__list_skill_signals"
		"mcp__skillsd-registry__list_outcome_reports"
	)
else
	echo "==> Skipping skillsd-registry (no --registry-url / SKILLSD_REGISTRY_URL set; read-only onboarding)"
fi

echo "==> Pre-approving tool permissions in $SETTINGS_FILE"
mkdir -p "$(dirname "$SETTINGS_FILE")"
[ -f "$SETTINGS_FILE" ] || echo '{}' >"$SETTINGS_FILE"

RULES_JSON=$(printf '%s\n' "${ALLOW_RULES[@]}" | jq -R . | jq -s .)
TMP=$(mktemp)
jq --argjson new "$RULES_JSON" '
  .permissions //= {} |
  .permissions.allow //= [] |
  .permissions.allow = (.permissions.allow + $new | unique)
' "$SETTINGS_FILE" >"$TMP"
mv "$TMP" "$SETTINGS_FILE"

# Keep the existing SKILLSET_AGENT_ID if this agent has already been
# onboarded before, so re-running the script doesn't churn its identity.
EXISTING_AGENT_ID=$(jq -r '.env.SKILLSET_AGENT_ID // empty' "$SETTINGS_FILE")
if [ -n "$EXISTING_AGENT_ID" ]; then
	AGENT_ID="$EXISTING_AGENT_ID"
	echo "==> Reusing existing SKILLSET_AGENT_ID ($AGENT_ID)"
else
	AGENT_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
	echo "==> Assigning new SKILLSET_AGENT_ID ($AGENT_ID)"
fi

TMP=$(mktemp)
jq --arg id "$AGENT_ID" '
  .env //= {} |
  .env.SKILLSET_AGENT_ID = $id
' "$SETTINGS_FILE" >"$TMP"
mv "$TMP" "$SETTINGS_FILE"

echo "==> Done. $(if [ -n "$SKILLSD_REGISTRY_URL" ]; then echo "skillsd + skillsd-registry"; else echo "skillsd"; fi) onboarded at scope '$SCOPE'."
