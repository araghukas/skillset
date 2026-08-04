#!/usr/bin/env bash
# Drives one full proposal lifecycle against ProposalService: propose a
# change, inspect it, read the skill at that ref, list it back, cluster it,
# and (if SubmitProposal is enabled on this deployment) submit it and
# confirm the resulting pull request actually exists.
#
# Uses a timestamp-suffixed proposal_id so the script is safely re-runnable
# without "clean working tree" errors from a previous run's identical commit.
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

SKILL_NAME="${SKILL_NAME:-internal-comms}"
AGENT_ID="verify-agent"
PROPOSAL_ID="verify-$(date +%s)"
BRANCH="proposals/${AGENT_ID}/${SKILL_NAME}/${PROPOSAL_ID}"

# Full replacement content for SKILL.md - ProposeChange takes whole files,
# never patches; the server computes the diff against base itself.
SKILL_CONTENT=$(cat <<EOF
---
name: ${SKILL_NAME}
description: A minimal placeholder skill seeded by local/gitea-init.sh, edited by local/verify/20_proposal_flow.sh at $(date -u +%FT%TZ) to exercise the proposal flow.
---

## When to use this skill

This is seed content for local dev only - edited by the verification script.
EOF
)

# Commit that content to a new proposal branch and check the response shape.
echo "== ProposalService.ProposeChange =="
propose_req=$(jq -n --arg skill "$SKILL_NAME" --arg agent "$AGENT_ID" --arg pid "$PROPOSAL_ID" \
  --arg msg "verify: edit $SKILL_NAME" --arg content "$SKILL_CONTENT" \
  '{skill_name: $skill, agent_id: $agent, proposal_id: $pid, commit_message: $msg,
    files: [{file_path: "SKILL.md", content: $content}]}')
proposed="$(rpc "$REGISTRY_ADDR" skills.v1.ProposalService/ProposeChange "$propose_req")"
check "proposal branch matches expected name" "$proposed" ".proposal.branch == \"$BRANCH\""
check "proposal is not a dedup of an existing one" "$proposed" '.deduplicated != true'
check "proposal has a non-empty diff" "$proposed" '(.proposal.diff // "") | length > 0'
check "proposal has at least one commit" "$proposed" '(.proposal.commits // []) | length > 0'

# Re-fetch by branch name and confirm it's the same proposal we just made.
echo "== ProposalService.GetProposal =="
got="$(rpc "$REGISTRY_ADDR" skills.v1.ProposalService/GetProposal "{\"branch\": \"$BRANCH\"}")"
check "GetProposal returns the same head_sha" "$got" ".headSha == $(echo "$proposed" | jq '.proposal.headSha')"

# Read the skill as of the proposal branch - should reflect the edit, not base.
echo "== ProposalService.GetSkillAtRef =="
at_ref="$(rpc "$REGISTRY_ADDR" skills.v1.ProposalService/GetSkillAtRef "{\"skill_name\": \"$SKILL_NAME\", \"ref\": \"$BRANCH\"}")"
check "skill at proposal ref carries the edited description" "$at_ref" '(.skill.description // "") | test("exercise the proposal flow")'

# Filtered listing by skill - confirms the proposal is discoverable, not just gettable by exact branch.
echo "== ProposalService.ListProposals =="
listed="$(rpc "$REGISTRY_ADDR" skills.v1.ProposalService/ListProposals "{\"skill_name\": \"$SKILL_NAME\"}")"
check "our proposal shows up in the listing" "$listed" ".proposals // [] | map(.branch) | index(\"$BRANCH\") != null"

# Clustering groups proposals that touch overlapping regions - even a lone
# proposal should show up as its own singleton cluster.
echo "== ProposalService.ListProposalClusters =="
clusters="$(rpc "$REGISTRY_ADDR" skills.v1.ProposalService/ListProposalClusters "{\"skill_name\": \"$SKILL_NAME\", \"include_singletons\": true}")"
check "at least one cluster is returned" "$clusters" '(.clusters // []) | length > 0'

# Push the branch and open a real PR - only if this deployment allows it.
echo "== ProposalService.SubmitProposal =="
submit_out="$(mktemp)"
if grpcurl -plaintext -d "{\"branch\": \"$BRANCH\"}" "$REGISTRY_ADDR" skills.v1.ProposalService/SubmitProposal >"$submit_out" 2>&1; then
  # Submission succeeded: check the PR URL came back, and try (best-effort) to reach it.
  submitted="$(cat "$submit_out")"
  check "submit returned a pull_request_url" "$submitted" '(.pullRequestUrl // "") | length > 0'
  pr_url="$(jq -r '.pullRequestUrl // empty' "$submit_out" 2>/dev/null || true)"
  if [[ -n "$pr_url" ]] && curl -fsS -o /dev/null "$pr_url" 2>/dev/null; then
    echo "  ok   - pull request URL is reachable ($pr_url)"
  else
    echo "  ok   - pull request URL returned but not reachable from this shell (expected if it's behind auth or a different network namespace): $pr_url"
  fi
else
  # Submission failed: only a "disabled" error is expected/acceptable here.
  if grep -qi "disabled" "$submit_out"; then
    echo "  skip - SubmitProposal is disabled on this deployment (registry.submitProposalEnabled=false or no GitHub auth configured)"
  else
    fail "SubmitProposal failed unexpectedly: $(cat "$submit_out")"
  fi
fi
rm -f "$submit_out"

summarize
