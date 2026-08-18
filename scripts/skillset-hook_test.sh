#!/usr/bin/env bash
# Tests scripts/skillset-hook.sh against sample Claude Code hook events,
# and the settings.json merge onboard-claude.sh performs with them.
set -uo pipefail

cd "$(dirname "$0")"
HOOK="./skillset-hook.sh"
FIXTURES="testdata"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1"
	shift
	[ $# -gt 0 ] && printf '       %s\n' "$@"
	FAILURES=$((FAILURES + 1))
}

setup() {
	SKILLSET_HOOK_STATE_DIR="$(mktemp -d)"
	export SKILLSET_HOOK_STATE_DIR
}
teardown() { rm -rf "$SKILLSET_HOOK_STATE_DIR"; }

pending() { cat "$SKILLSET_HOOK_STATE_DIR/$1.json" 2>/dev/null || printf '[]'; }
pending_count() { pending "$1" | jq 'length'; }

# fixture renders one of testdata/*.json with @@SESSION@@ and any KEY=VALUE
# substitutions applied, so a test can vary the session or a single field
# without a fixture file per case.
fixture() {
	local name="$1" session="$2" out
	shift 2
	out="$(cat "$FIXTURES/$name.json")"
	out="${out//@@SESSION@@/$session}"
	for sub in "$@"; do
		out="${out//@@${sub%%=*}@@/${sub#*=}}"
	done
	printf '%s' "$out"
}

echo "skillset-hook.sh"

# --- record ------------------------------------------------------------

setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
if [ "$(pending "s1" | jq -c '.')" = '[{"skill_name":"deploy","commit":"abc123"}]' ]; then
	pass "record stores the skill and its commit"
else
	fail "record stores the skill and its commit" "got: $(pending s1 | jq -c .)"
fi

# The same skill at the same commit twice in a turn is one thing to report.
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
[ "$(pending_count s1)" = "1" ] &&
	pass "record does not duplicate an identical load" ||
	fail "record does not duplicate an identical load" "got $(pending_count s1) entries"

# A re-read after the registry moved on is a genuinely different version.
fixture get_skill "s1" COMMIT=def456 SKILL=deploy | $HOOK record
[ "$(pending_count s1)" = "2" ] &&
	pass "record keeps a second commit of the same skill" ||
	fail "record keeps a second commit of the same skill" "got $(pending_count s1) entries"
teardown

# An MCP result can reach the hook as JSON inside a text block rather than
# as structured output; the commit has to survive that.
setup
fixture get_skill_text "s1" COMMIT=deadbee SKILL=lint | $HOOK record
[ "$(pending s1 | jq -r '.[0].commit')" = "deadbee" ] &&
	pass "record finds a commit in a text-block response" ||
	fail "record finds a commit in a text-block response" "got: $(pending s1 | jq -c .)"
teardown

setup
fixture get_skill_no_commit "s1" SKILL=deploy | $HOOK record
[ "$(pending s1 | jq -r '.[0].skill_name')" = "deploy" ] &&
	pass "record still owes a report when no commit could be read" ||
	fail "record still owes a report when no commit could be read" "got: $(pending s1 | jq -c .)"
teardown

# session_id lands in a filename, so it must not be able to escape the
# state directory. Exercises sanitize()'s path-traversal defense: a
# session_id of "../../escaped" must not let record() write outside
# SKILLSET_HOOK_STATE_DIR.
setup
ESCAPE_TARGET="$(dirname "$(dirname "$SKILLSET_HOOK_STATE_DIR")")/escaped.json"
rm -f "$ESCAPE_TARGET"
fixture get_skill "../../escaped" COMMIT=abc SKILL=deploy | $HOOK record
if [ -e "$ESCAPE_TARGET" ]; then
	fail "record cannot write outside the state directory" "wrote $ESCAPE_TARGET"
	rm -f "$ESCAPE_TARGET"
elif [ -z "$(find "$SKILLSET_HOOK_STATE_DIR" -name '*.json')" ]; then
	# Without this the case passes for the wrong reason: a record that
	# silently did nothing also writes nothing outside the directory.
	fail "record cannot write outside the state directory" "recorded nothing at all"
else
	pass "record cannot write outside the state directory"
fi
teardown

# --- clear -------------------------------------------------------------

setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
fixture report_outcome "s1" COMMIT=abc123 SKILL=deploy | $HOOK clear
[ "$(pending_count s1)" = "0" ] &&
	pass "clear drops a reported skill" ||
	fail "clear drops a reported skill" "got: $(pending s1 | jq -c .)"
teardown

# A partial report leaves the rest owed - that is the whole point of
# matching on the pair rather than emptying the file.
setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
fixture get_skill "s1" COMMIT=abc123 SKILL=lint | $HOOK record
fixture report_outcome "s1" COMMIT=abc123 SKILL=deploy | $HOOK clear
if [ "$(pending s1 | jq -r '.[0].skill_name')" = "lint" ] && [ "$(pending_count s1)" = "1" ]; then
	pass "clear leaves unreported skills owed"
else
	fail "clear leaves unreported skills owed" "got: $(pending s1 | jq -c .)"
fi
teardown

# Reporting a different commit than the one loaded clears nothing: the
# outcome recorded is about a version the agent did not read.
setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
fixture report_outcome "s1" COMMIT=999999 SKILL=deploy | $HOOK clear
[ "$(pending_count s1)" = "1" ] &&
	pass "clear ignores a report naming another commit" ||
	fail "clear ignores a report naming another commit"
teardown

# Except when the hook never captured a commit - otherwise the agent is
# stuck owing something it has no way to clear.
setup
fixture get_skill_no_commit "s1" SKILL=deploy | $HOOK record
fixture report_outcome "s1" COMMIT=whatever SKILL=deploy | $HOOK clear
[ "$(pending_count s1)" = "0" ] &&
	pass "clear drops an entry whose commit was never captured" ||
	fail "clear drops an entry whose commit was never captured"
teardown

setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
fixture report_outcome_error "s1" COMMIT=abc123 SKILL=deploy | $HOOK clear
[ "$(pending_count s1)" = "1" ] &&
	pass "clear keeps the outcome owed when report_outcome failed" ||
	fail "clear keeps the outcome owed when report_outcome failed"
teardown

# --- check -------------------------------------------------------------

setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
OUT="$(fixture stop "s1" ACTIVE=false | $HOOK check)"
if [ "$(printf '%s' "$OUT" | jq -r '.decision')" = "block" ] &&
	printf '%s' "$OUT" | jq -r '.reason' | grep -q 'deploy @ abc123'; then
	pass "check blocks and names the skill and commit"
else
	fail "check blocks and names the skill and commit" "got: $OUT"
fi

# The reason has to say that "applied" is expected, or the block reads as
# an invitation to invent a defect to get past it.
fixture stop "s1" ACTIVE=false | $HOOK check | jq -r '.reason' | grep -q 'applied' &&
	pass "check asks for an honest verdict, not a defect" ||
	fail "check asks for an honest verdict, not a defect"

# Blocking re-enters the agent, which stops again: without this guard that
# is an infinite loop.
[ -z "$(fixture stop "s1" ACTIVE=true | $HOOK check)" ] &&
	pass "check yields when stop_hook_active is set" ||
	fail "check yields when stop_hook_active is set"
teardown

setup
[ -z "$(fixture stop "s1" ACTIVE=false | $HOOK check)" ] &&
	pass "check allows a turn that used no skills" ||
	fail "check allows a turn that used no skills"
teardown

setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
fixture report_outcome "s1" COMMIT=abc123 SKILL=deploy | $HOOK clear
[ -z "$(fixture stop "s1" ACTIVE=false | $HOOK check)" ] &&
	pass "check allows a turn that reported" ||
	fail "check allows a turn that reported"
teardown

# One session, several turns: reporting in turn one must not excuse turn
# two. This is the behaviour the whole per-turn design rests on.
setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
fixture report_outcome "s1" COMMIT=abc123 SKILL=deploy | $HOOK clear
fixture get_skill "s1" COMMIT=abc123 SKILL=lint | $HOOK record
[ -n "$(fixture stop "s1" ACTIVE=false | $HOOK check)" ] &&
	pass "check blocks again on a later turn in the same session" ||
	fail "check blocks again on a later turn in the same session"
teardown

# Sessions must not read each other's state.
setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
[ -z "$(fixture stop "s2" ACTIVE=false | $HOOK check)" ] &&
	pass "check is scoped to one session" ||
	fail "check is scoped to one session"
teardown

# --- cleanup -----------------------------------------------------------

setup
fixture get_skill "s1" COMMIT=abc123 SKILL=deploy | $HOOK record
fixture session_end "s1" | $HOOK cleanup
[ ! -e "$SKILLSET_HOOK_STATE_DIR/s1.json" ] &&
	pass "cleanup discards the session's state" ||
	fail "cleanup discards the session's state"
teardown

# --- fail-open ---------------------------------------------------------

# A hook that errors must not take the session with it. Every subcommand
# exits 0 on garbage.
setup
OPEN=1
for sub in record clear check cleanup; do
	if ! printf 'not json' | $HOOK "$sub" >/dev/null 2>&1; then
		fail "$sub exits 0 on malformed input"
		OPEN=0
	fi
done
[ "$OPEN" -eq 1 ] && pass "every subcommand exits 0 on malformed input"
teardown

# --- settings.json merge -----------------------------------------------

echo "onboard-claude.sh hook merge"

# Lift install_hook out of onboard-claude.sh and run the real thing rather
# than a copy of its jq program
eval "$(awk '/^install_hook\(\) \{$/,/^\}$/' onboard-claude.sh)"

SETTINGS_FILE=$(mktemp)
HOOK_SCRIPT="/hooks/skillset-hook.sh"
printf '%s' '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/bin/my-notify"}]}]}}' >"$SETTINGS_FILE"

install_hook Stop "" check
install_hook Stop "" check

if [ "$(jq '.hooks.Stop | length' "$SETTINGS_FILE")" = "2" ]; then
	pass "a re-run does not duplicate a hook entry"
else
	fail "a re-run does not duplicate a hook entry" "got: $(jq -c '.hooks.Stop' "$SETTINGS_FILE")"
fi
if jq -e '.hooks.Stop[] | select(.hooks[].command == "/usr/local/bin/my-notify")' "$SETTINGS_FILE" >/dev/null; then
	pass "an unrelated hook survives the merge"
else
	fail "an unrelated hook survives the merge" "got: $(jq -c '.hooks.Stop' "$SETTINGS_FILE")"
fi

# Re-pointing the install path must move the hook, not leave one behind
# calling a script that is no longer there.
HOOK_SCRIPT="/elsewhere/skillset-hook.sh"
install_hook Stop "" check
if [ "$(jq '[.hooks.Stop[].hooks[] | select(.command | endswith("check"))] | length' "$SETTINGS_FILE")" = "1" ]; then
	pass "a changed install path replaces the old entry"
else
	fail "a changed install path replaces the old entry" "got: $(jq -c '.hooks.Stop' "$SETTINGS_FILE")"
fi

printf '%s' '{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/local/bin/audit"},{"type":"command","command":"/hooks/skillset-hook.sh record"}]}]}}' >"$SETTINGS_FILE"
HOOK_SCRIPT="/hooks/skillset-hook.sh"
install_hook PostToolUse '^mcp__skillsd__get_skill$' record
if jq -e '.hooks.PostToolUse[] | select(.matcher == "Bash") | .hooks | length == 1' "$SETTINGS_FILE" >/dev/null; then
	pass "a co-located command in the same entry survives"
else
	fail "a co-located command in the same entry survives" "got: $(jq -c '.hooks.PostToolUse' "$SETTINGS_FILE")"
fi

# A matcher that goes missing is silent and expensive: the hook then runs
# after every tool call instead of after get_skill.
if jq -e '.hooks.PostToolUse[] | select(.hooks[].command == "/hooks/skillset-hook.sh record") | .matcher == "^mcp__skillsd__get_skill$"' "$SETTINGS_FILE" >/dev/null; then
	pass "the matcher lands on the entry"
else
	fail "the matcher lands on the entry" "got: $(jq -c '.hooks.PostToolUse' "$SETTINGS_FILE")"
fi
if [ "$(jq -r '[keys[] | select(. != "hooks")] | join(",")' "$SETTINGS_FILE")" = "" ]; then
	pass "the merge adds nothing at the settings root"
else
	fail "the merge adds nothing at the settings root" "stray keys: $(jq -c 'keys' "$SETTINGS_FILE")"
fi
rm -f "$SETTINGS_FILE"

echo
if [ "$FAILURES" -eq 0 ]; then
	echo "all tests passed"
	exit 0
fi
echo "$FAILURES test(s) failed"
exit 1
