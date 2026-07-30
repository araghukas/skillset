#!/usr/bin/env bash
# Confirms both gRPC endpoints are reachable, reflection is on, and the
# standard health service reports SERVING.
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

# Reflection: confirm skillsd is up and SkillService is registered on it.
echo "== health: skillsd ($SKILLSD_ADDR) =="
list="$(grpcurl -plaintext "$SKILLSD_ADDR" list)"
check "reflection lists skills.v1.SkillService" "$(jq -n --arg l "$list" '$l | test("skills.v1.SkillService")')" '. == true'

# grpc.health.v1.Health: confirm skillsd reports itself SERVING.
health="$(rpc "$SKILLSD_ADDR" grpc.health.v1.Health/Check)"
check "skillsd health is SERVING" "$health" '.status == "SERVING"'

# Reflection: confirm skillsd-registry is up with both its services registered.
echo "== health: skillsd-registry ($REGISTRY_ADDR) =="
list_reg="$(grpcurl -plaintext "$REGISTRY_ADDR" list)"
check "reflection lists skills.v1.ProposalService" "$(jq -n --arg l "$list_reg" '$l | test("skills.v1.ProposalService")')" '. == true'
check "reflection lists skills.v1.EvidenceService" "$(jq -n --arg l "$list_reg" '$l | test("skills.v1.EvidenceService")')" '. == true'

# grpc.health.v1.Health: confirm skillsd-registry reports itself SERVING.
health_reg="$(rpc "$REGISTRY_ADDR" grpc.health.v1.Health/Check)"
check "skillsd-registry health is SERVING" "$health_reg" '.status == "SERVING"'

summarize
