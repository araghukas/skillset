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
