#!/usr/bin/env bash
# Exercises the read-only SkillService against whatever skillsRepo skillsd
# was deployed with. Assumes the seed content from local/gitea-init.sh (a
# private copy of anthropics/skills' skills/ tree, which includes
# "internal-comms") unless SKILL_NAME is overridden.
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

SKILL_NAME="${SKILL_NAME:-internal-comms}"

# Baseline listing: metadata only, no context files - confirms the index loaded.
echo "== SkillService.ListSkills =="
list="$(rpc "$SKILLSD_ADDR" skills.v1.SkillService/ListSkills)"
check "response has an indexed_at timestamp" "$list" '.indexedAt != null'
check "skills list is non-empty" "$list" '(.skills // []) | length > 0'
check "seed skill $SKILL_NAME is present" "$list" ".skills // [] | map(.name) | index(\"$SKILL_NAME\") != null"

# Same call, but with context files pulled in - confirms that opt-in path works too.
echo "== SkillService.ListSkills (include_context_files) =="
list_ctx="$(rpc "$SKILLSD_ADDR" skills.v1.SkillService/ListSkills '{"include_context_files": true}')"
check "at least one skill carries context files" "$list_ctx" '(.skills // []) | map((.contextFiles // []) | length) | max > 0'

# Fetch one skill by name and check its shape in detail.
echo "== SkillService.GetSkill =="
skill="$(rpc "$SKILLSD_ADDR" skills.v1.SkillService/GetSkill "{\"skill_name\": \"$SKILL_NAME\", \"include_context_files\": true}")"
check "returned skill name matches" "$skill" ".skill.name == \"$SKILL_NAME\""
check "returned skill has a non-empty description" "$skill" '(.skill.description // "") | length > 0'
check "returned skill has a commit SHA" "$skill" '(.skill.commit // "") | length > 0'
check "returned skill has SKILL.md among context files" "$skill" '.skill.contextFiles // [] | map(.filePath) | index("SKILL.md") != null'
check "SKILL.md content carries the onboarding footer" "$skill" \
  '.skill.contextFiles // [] | map(select(.filePath == "SKILL.md")) | first | .content | contains("served by skillsd")'

# Negative case: a name that isn't in the index should error, not return empty.
echo "== SkillService.GetSkill (unknown skill) =="
if grpcurl -plaintext -d '{"skill_name": "does-not-exist"}' "$SKILLSD_ADDR" skills.v1.SkillService/GetSkill >/tmp/verify_getskill_err 2>&1; then
  fail "GetSkill on an unknown skill should have returned an error"
else
  check "error mentions not found" "$(jq -Rs . </tmp/verify_getskill_err)" 'test("NotFound|not found"; "i")'
fi

# The embedded onboarding guide - served outside ListSkills, never absent.
echo "== SkillService.GetClientGuide =="
guide="$(rpc "$SKILLSD_ADDR" skills.v1.SkillService/GetClientGuide)"
check "client guide has non-empty content" "$guide" '(.skill.contextFiles // []) | map(.content) | join("") | length > 0'
check "client guide is not returned by ListSkills" "$list" '(.skills // []) | map(.name) | index("skillsd-client") == null'
check "client guide is not double-stamped with the served-skill onboarding footer" "$guide" \
  '(.skill.contextFiles // []) | map(.content) | join("") | contains("served by skillsd") | not'

summarize
