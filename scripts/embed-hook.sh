#!/usr/bin/env bash
# Regenerates the copy of skillset-hook.sh embedded in onboard-claude.sh.
#
# onboard-claude.sh has to carry the hook script rather than reference it:
# the fleet rollout pipes onboard-claude.sh straight from curl, so there is
# no checkout on the target machine to read scripts/skillset-hook.sh from.
# Carrying it means the two can drift, which is what `--check` is for — CI
# runs it, and a drift fails the build rather than shipping agents a stale
# hook.
#
# Usage:
#   ./embed-hook.sh            # rewrite the embedded block in place
#   ./embed-hook.sh --check    # exit 1 if the block is out of date
set -euo pipefail

cd "$(dirname "$0")"

SOURCE="skillset-hook.sh"
TARGET="onboard-claude.sh"
BEGIN='# >>> BEGIN EMBEDDED skillset-hook.sh — auto-generated, do not edit <<<'
END='# >>> END EMBEDDED skillset-hook.sh <<<'
DELIM="SKILLSET_HOOK_SCRIPT_EOF"

# A heredoc ends at a line equal to its delimiter, so a hook script
# containing that line would silently truncate the embedded copy.
if grep -qx "$DELIM" "$SOURCE"; then
	echo "$SOURCE contains a line equal to the heredoc delimiter ($DELIM)" >&2
	exit 1
fi

render() {
	printf '%s\n' "$BEGIN"
	printf '%s\n' "write_hook_script() {"
	printf '\tcat >\"$1\" <<'"'"'%s'"'"'\n' "$DELIM"
	cat "$SOURCE"
	printf '%s\n' "$DELIM"
	printf '%s\n' "	chmod +x \"\$1\""
	printf '%s\n' "}"
	printf '%s\n' "$END"
}

# Splice the rendered block between the markers, leaving the rest of the
# file untouched. awk rather than sed: the block is multi-line and contains
# every character sed would need escaping for. The block goes through a file
# and the markers through the environment, because awk's -v mangles
# backslashes and the awk macOS ships rejects newlines in a -v value.
splice() {
	local block
	block=$(mktemp)
	render >"$block"
	BEGIN_MARK="$BEGIN" END_MARK="$END" BLOCK_FILE="$block" awk '
		$0 == ENVIRON["BEGIN_MARK"] {
			while ((getline line < ENVIRON["BLOCK_FILE"]) > 0) print line
			skipping = 1
			next
		}
		$0 == ENVIRON["END_MARK"] { skipping = 0; next }
		!skipping { print }
	' "$TARGET"
	rm -f "$block"
}

if ! grep -qxF "$BEGIN" "$TARGET" || ! grep -qxF "$END" "$TARGET"; then
	echo "$TARGET is missing the embed markers; add them back before regenerating" >&2
	exit 1
fi

if [ "${1:-}" = "--check" ]; then
	if ! splice | diff -u "$TARGET" - >/dev/null; then
		echo "$TARGET's embedded copy of $SOURCE is out of date; run 'make hook-embed'" >&2
		splice | diff -u "$TARGET" - || true
		exit 1
	fi
	echo "embedded $SOURCE is up to date"
	exit 0
fi

TMP=$(mktemp)
splice >"$TMP"
mv "$TMP" "$TARGET"
chmod +x "$TARGET"
echo "embedded $SOURCE into $TARGET"
