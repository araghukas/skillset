#!/usr/bin/env bash
# Exercises EvidenceService: report an outcome, then confirm it shows up
# both in the raw report listing and in the aggregated per-(skill,commit)
# signal. Skips (not fails) if EvidenceService is disabled on this
# deployment.
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

SKILL_NAME="${SKILL_NAME:-internal-comms}"
REPORT_ID="verify-$(date +%s)-$$"
SESSION_ID="verify-session-$$"

# A report needs a real skill_commit to attach to - read it off skillsd first.
echo "== resolving current commit for $SKILL_NAME =="
skill="$(rpc "$SKILLSD_ADDR" skills.v1.SkillService/GetSkill "{\"skill_name\": \"$SKILL_NAME\"}")"
COMMIT="$(echo "$skill" | jq -r '.skill.commit // empty')"
if [[ -z "$COMMIT" ]]; then
  echo "  skip - could not resolve a commit for $SKILL_NAME; is skillsd deployed and seeded?"
  exit 0
fi

# Report one outcome for that (skill, commit) - only if EvidenceService is enabled.
echo "== EvidenceService.ReportOutcome =="
report_req=$(jq -n --arg rid "$REPORT_ID" --arg sid "$SESSION_ID" --arg skill "$SKILL_NAME" --arg commit "$COMMIT" \
  '{report_id: $rid, agent_id: "verify-agent", session_id: $sid,
    skills: [{skill_name: $skill, skill_commit: $commit, verdict: "VERDICT_APPLIED_WITH_CORRECTION", note: "verify script exercising ReportOutcome"}]}')
report_out="$(mktemp)"
if grpcurl -plaintext -d "$report_req" "$REGISTRY_ADDR" skills.v1.EvidenceService/ReportOutcome >"$report_out" 2>&1; then
  reported="$(cat "$report_out")"
  check "report was recorded (not a dedup replay)" "$reported" '.recorded == true'
else
  # Unimplemented means the service is off on this deployment - not a failure.
  if grep -qi "unimplemented"  "$report_out"; then
    echo "  skip - EvidenceService is disabled on this deployment (registry.evidence.enabled=false)"
    rm -f "$report_out"
    exit 0
  fi
  fail "ReportOutcome failed unexpectedly: $(cat "$report_out")"
fi
rm -f "$report_out"

# report_id makes retries idempotent - re-sending it must be a no-op, not a duplicate.
echo "== EvidenceService.ReportOutcome (idempotent replay) =="
replay="$(rpc "$REGISTRY_ADDR" skills.v1.EvidenceService/ReportOutcome "$report_req")"
check "re-sending the same report_id is a no-op" "$replay" '(.recorded // false) == false'

# Raw report listing - confirms our report is individually retrievable.
echo "== EvidenceService.ListOutcomeReports =="
reports="$(rpc "$REGISTRY_ADDR" skills.v1.EvidenceService/ListOutcomeReports "{\"skill_name\": \"$SKILL_NAME\"}")"
check "our report shows up in the listing" "$reports" ".reports // [] | map(.reportId) | index(\"$REPORT_ID\") != null"

# Aggregated per-(skill,commit) signal - confirms our report rolled up into it.
echo "== EvidenceService.ListSkillSignals =="
signals="$(rpc "$REGISTRY_ADDR" skills.v1.EvidenceService/ListSkillSignals "{\"skill_name\": \"$SKILL_NAME\"}")"
check "a signal exists for $SKILL_NAME at $COMMIT" "$signals" \
  ".signals // [] | map(select(.skillCommit == \"$COMMIT\")) | length > 0"
check "reported_sessions counts our report" "$signals" \
  ".signals // [] | map(select(.skillCommit == \"$COMMIT\")) | .[0].reportedSessions >= 1"

summarize
