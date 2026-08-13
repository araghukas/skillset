#!/usr/bin/env bash
# Onboards the current Claude Code agent onto a running skillset deployment.
#
# `claude mcp add` alone registers the servers but doesn't pre-approve their
# tools, so the agent still hits a first-use permission prompt for every
# skillsd/skillsd-registry tool. This script does four things: it registers
# the servers, merges permission rules into the target settings.json so those
# prompts never happen, assigns the agent a stable SKILLSET_AGENT_ID (a UUID
# written to settings.json's `env`) so skillsd can identify it across
# sessions, and installs the outcome-reporting hooks. It's read-modify-write
# (via jq), so re-running it, or running it alongside unrelated manually-set
# permissions and hooks, is safe — the agent ID is generated once and reused
# on subsequent runs.
#
# The hooks hold the agent to the reporting contract the client guide
# describes: a turn that loads a skill can't end until it has called
# report_outcome for it. Without them, reporting is advisory and an agent
# deep in a task simply forgets. They need skillsd-registry, so they're
# skipped when no registry URL is configured. See scripts/skillset-hook.sh,
# a copy of which is embedded in this file and written to
# ~/.claude/skillset/ at install time — the fleet rollout below pipes this
# script straight from curl, with no clone to reference it from.
#
# Usage:
#   ./onboard-claude.sh [--skillsd-url <url>] [--registry-url <url>] [--scope user|project|local] [--no-hooks]
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
# --no-hooks skips the reporting hooks, leaving permissions and the agent ID
# alone. Re-running without it installs them; it does not remove hooks a
# previous run installed.
#
# SKILLSD_URL and SKILLSD_REGISTRY_URL default to a local dev deployment
# (localhost:8080/mcp and localhost:8081/mcp).
set -euo pipefail

SCOPE="project"
SKILLSD_URL="${SKILLSD_URL:-http://localhost:8080/mcp}"
SKILLSD_REGISTRY_URL="${SKILLSD_REGISTRY_URL:-http://localhost:8081/mcp}"
INSTALL_HOOKS=1
HOOK_DIR="$HOME/.claude/skillset"
HOOK_SCRIPT="$HOOK_DIR/skillset-hook.sh"

usage() {
	# Only the header block, which ends at the first line that isn't a
	# comment - the embedded hook script further down has comments of its
	# own and they are not help text.
	awk 'NR > 1 { if (!/^#/) exit; sub(/^# ?/, ""); print }' "$0"
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
	--no-hooks)
		INSTALL_HOOKS=0
		shift
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

# >>> BEGIN EMBEDDED skillset-hook.sh — auto-generated, do not edit <<<
write_hook_script() {
	cat >"$1" <<'SKILLSET_HOOK_SCRIPT_EOF'
#!/usr/bin/env bash
# Claude Code hook that holds an agent to skillsd's reporting contract:
# every turn that loads a skill files a report_outcome before it ends.
#
# The guide asks for this in prose, but prose is advisory - an agent deep in
# a task forgets, and the skill's `commit` (which report_outcome requires)
# is exactly the sort of detail that falls out of context first. This script
# keeps the loaded-but-unreported set on disk and blocks the turn from
# ending while it is non-empty, handing the commits back at the same time.
#
# Installed by onboard-claude.sh, which embeds a copy of this file. The
# two are kept in sync in the skillset repo.
#
# Usage: skillset-hook.sh <record|clear|check|cleanup>
#
# Each subcommand reads its Claude Code hook event as JSON on stdin:
#
#   record   PostToolUse on mcp__skillsd__get_skill - remembers
#            {skill_name, commit} as owed.
#   clear    PostToolUse on mcp__skillsd-registry__report_outcome - drops
#            the skills that call actually reported.
#   check    Stop / SubagentStop - blocks the turn if anything is still
#            owed, naming each skill and commit.
#   cleanup  SessionEnd - discards the session's state.
#
# State lives one file per session under $SKILLSET_HOOK_STATE_DIR (default
# ~/.claude/skillset/pending). Override it to test, or to relocate state.
#
# Every failure path here exits 0. A hook that can't do its job must not
# take the session down with it: unreported outcomes are a lost signal, a
# wedged agent is a lost user.

set -uo pipefail

STATE_DIR="${SKILLSET_HOOK_STATE_DIR:-$HOME/.claude/skillset/pending}"

command -v jq >/dev/null 2>&1 || exit 0

# jq helpers shared by record and clear. find_commit walks the whole event
# looking for a "commit" string, rather than reading a fixed path, because
# an MCP tool result reaches a hook wrapped differently depending on
# transport and client version - structured output, a content array, or a
# JSON string inside a text block. texts feeds that last case back through
# fromjson.
JQ_HELPERS='
def find_commit:
  [ .. | objects | .commit? ] | map(select(type == "string" and . != "")) | first;
def texts:
  [ .. | objects | select(.type? == "text") | .text? ] | map(select(type == "string"));
'

# session_id reaches the filesystem as a filename, so keep it to characters
# that cannot escape STATE_DIR.
sanitize() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_'; }

pending_file() {
	local session="$1"
	[ -n "$session" ] || return 1
	printf '%s/%s.json' "$STATE_DIR" "$(sanitize "$session")"
}

# with_lock serializes read-modify-write on one session's pending file.
# Claude Code can run several tool calls, and so several hooks, at once; an
# mkdir lock is the portable way to make the update atomic.
# It gives up after ~2s and proceeds anyway, since a dropped record beats
# a hung tool call.
with_lock() {
	local lock="$1" i
	shift
	for ((i = 0; i < 20; i++)); do
		if mkdir "$lock" 2>/dev/null; then
			"$@"
			rmdir "$lock" 2>/dev/null
			return 0
		fi
		sleep 0.1
	done
	"$@"
}

write_pending() {
	local file="$1" content="$2" tmp
	tmp="$(mktemp "${file}.XXXXXX")" || return 0
	printf '%s\n' "$content" >"$tmp" && mv "$tmp" "$file" || rm -f "$tmp"
}

read_pending() {
	local file="$1"
	if [ -s "$file" ]; then cat "$file"; else printf '[]'; fi
}

cmd_record() {
	local event file session skill commit updated
	event="$(cat)"
	session="$(printf '%s' "$event" | jq -r '.session_id // ""' 2>/dev/null)"
	file="$(pending_file "$session")" || exit 0

	skill="$(printf '%s' "$event" | jq -r '.tool_input.skill_name // ""' 2>/dev/null)"
	[ -n "$skill" ] || exit 0

	# An empty commit is still worth recording: the agent owes a report
	# either way, and check says so rather than silently letting it go.
	commit="$(printf '%s' "$event" | jq -r "$JQ_HELPERS"'
		(.tool_response | find_commit)
		// ([ (.tool_response | texts)[] | (try fromjson catch empty) | find_commit ] | first)
		// ""' 2>/dev/null)"

	mkdir -p "$STATE_DIR" 2>/dev/null || exit 0

	append() {
		updated="$(read_pending "$file" | jq --arg s "$skill" --arg c "$commit" '
			. + [{skill_name: $s, commit: $c}] | unique_by([.skill_name, .commit])' 2>/dev/null)"
		[ -n "$updated" ] && write_pending "$file" "$updated"
	}
	with_lock "${file}.lock" append
	exit 0
}

cmd_clear() {
	local event file session updated failed
	event="$(cat)"
	session="$(printf '%s' "$event" | jq -r '.session_id // ""' 2>/dev/null)"
	file="$(pending_file "$session")" || exit 0
	[ -e "$file" ] || exit 0

	# A rejected report_outcome (an unknown commit, a bad verdict) clears
	# nothing - the outcome is still owed.
	failed="$(printf '%s' "$event" | jq -r '
		[ .. | objects | (.isError? // .is_error?) ] | any(. == true)' 2>/dev/null)"
	[ "$failed" = "true" ] && exit 0

	drop() {
		# Drop an entry when the report names its (skill, commit) pair, and
		# also when it names the skill and we never captured a commit -
		# otherwise a commit this script failed to read would wedge the
		# turn with something the agent cannot clear.
		updated="$(read_pending "$file" | jq --argjson reported \
			"$(printf '%s' "$event" | jq -c '[ .tool_input.skills[]? | {skill_name, commit: .skill_commit} ]' 2>/dev/null || printf '[]')" '
			map(select(
				(. as $p | $reported | any(.skill_name == $p.skill_name and .commit == $p.commit)) or
				(.commit == "" and (. as $p | $reported | any(.skill_name == $p.skill_name)))
			| not))' 2>/dev/null)"
		[ -n "$updated" ] && write_pending "$file" "$updated"
	}
	with_lock "${file}.lock" drop
	exit 0
}

cmd_check() {
	local event file session pending count reason
	event="$(cat)"

	# The block below re-enters the agent, which will eventually stop
	# again. Without this guard that is an infinite loop.
	[ "$(printf '%s' "$event" | jq -r '.stop_hook_active // false' 2>/dev/null)" = "true" ] && exit 0

	session="$(printf '%s' "$event" | jq -r '.session_id // ""' 2>/dev/null)"
	file="$(pending_file "$session")" || exit 0
	[ -s "$file" ] || exit 0

	pending="$(read_pending "$file")"
	count="$(printf '%s' "$pending" | jq 'length' 2>/dev/null)"
	[ -n "$count" ] && [ "$count" != "0" ] || exit 0

	reason="$(printf '%s' "$pending" | jq -r --arg session "$session" '
		"You loaded these skills this turn and have not reported on them yet:\n" +
		(map("  - " + .skill_name + " @ " + (if .commit == "" then "<commit unknown - re-read it from get_skill>" else .commit end)) | join("\n")) +
		"\n\nCall report_outcome on skillsd-registry before finishing, with a report_id " +
		"you generate for this turn, agent_id from $SKILLSET_AGENT_ID, and session_id " +
		$session + ". One entry per skill above.\n\n" +
		"Report what observably happened. \"applied\" is the expected verdict when a " +
		"skill worked, and reporting it is what makes every defect rate meaningful - " +
		"do not invent problems, and do not skip the skills that were fine."' 2>/dev/null)"
	[ -n "$reason" ] || exit 0

	jq -n --arg reason "$reason" '{decision: "block", reason: $reason}'
	exit 0
}

cmd_cleanup() {
	local file session
	session="$(cat | jq -r '.session_id // ""' 2>/dev/null)"
	file="$(pending_file "$session")" || exit 0
	rm -f "$file" 2>/dev/null
	rmdir "${file}.lock" 2>/dev/null
	exit 0
}

case "${1:-}" in
record) cmd_record ;;
clear) cmd_clear ;;
check) cmd_check ;;
cleanup) cmd_cleanup ;;
*)
	echo "usage: $(basename "$0") <record|clear|check|cleanup>" >&2
	exit 0
	;;
esac
SKILLSET_HOOK_SCRIPT_EOF
	chmod +x "$1"
}
# >>> END EMBEDDED skillset-hook.sh <<<

# install_hook adds the hook entry for one subcommand. Any existing entry
# running that subcommand is stripped first, so a re-run updates in place
# rather than appending a second copy.
#
# The strip matches on "skillset-hook.sh <subcommand>" rather than the whole
# command string, so a run that installs to a different path (a changed
# HOME, a relocated HOOK_DIR) replaces the old entry instead of leaving one
# behind pointing at a script that is no longer there. Entries that share a
# slot with unrelated commands keep them: this removes only the one hook it
# owns, never a whole event's array.
install_hook() {
	local event="$1" matcher="$2" sub="$3" tmp
	tmp=$(mktemp)
	jq --arg event "$event" --arg matcher "$matcher" \
		--arg cmd "$HOOK_SCRIPT $sub" --arg marker "skillset-hook.sh $sub" '
	  def strip($m):
	    map(.hooks = ((.hooks // []) | map(select((.command // "") | contains($m) | not))))
	    | map(select((.hooks | length) > 0));

	  # Parenthesized as a whole: jq binds "as" to the term immediately
	  # before it, so without these the matcher would fall out of the entry
	  # and be merged into the settings root instead.
	  (($matcher | if . == "" then {} else {matcher: .} end)
	    + {hooks: [{type: "command", command: $cmd}]}) as $entry |

	  .hooks //= {} |
	  .hooks[$event] = (((.hooks[$event] // []) | strip($marker)) + [$entry])
	' "$SETTINGS_FILE" >"$tmp"
	mv "$tmp" "$SETTINGS_FILE"
}

if [ "$INSTALL_HOOKS" -eq 1 ] && [ -n "$SKILLSD_REGISTRY_URL" ]; then
	echo "==> Installing outcome-reporting hooks ($HOOK_SCRIPT)"
	mkdir -p "$HOOK_DIR"
	write_hook_script "$HOOK_SCRIPT"

	# Matchers are regexes over tool names, anchored so a future tool whose
	# name merely starts with one of these doesn't inherit its hook.
	install_hook PostToolUse '^mcp__skillsd__get_skill$' record
	install_hook PostToolUse '^mcp__skillsd-registry__report_outcome$' clear
	# Stop fires once per turn, SubagentStop once per subagent that
	# finishes - a subagent's skill use never reaches the main Stop.
	install_hook Stop '' check
	install_hook SubagentStop '' check
	install_hook SessionEnd '' cleanup
elif [ "$INSTALL_HOOKS" -eq 1 ]; then
	echo "==> Skipping reporting hooks (no registry to report to)"
else
	echo "==> Skipping reporting hooks (--no-hooks)"
fi

echo "==> Done. $(if [ -n "$SKILLSD_REGISTRY_URL" ]; then echo "skillsd + skillsd-registry"; else echo "skillsd"; fi) onboarded at scope '$SCOPE'."
