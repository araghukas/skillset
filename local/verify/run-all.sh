#!/usr/bin/env bash
# Runs every local/verify/*.sh check script in order against a running
# local deployment (`make dev` / `tilt up`, with the usual 8080/8081
# port-forwards). Requires grpcurl, jq, and curl on PATH.
#
#   ./local/verify/run-all.sh
#   SKILLSD_ADDR=localhost:8080 REGISTRY_ADDR=localhost:8081 ./local/verify/run-all.sh
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

# Fail fast with a clear message rather than letting the first script's cryptic "command not found" do it.
for bin in grpcurl jq curl; do
  command -v "$bin" >/dev/null 2>&1 || { echo "run-all: missing required tool: $bin" >&2; exit 1; }
done

# Run every check script in order; keep going after a failure so one broken
# area doesn't hide failures elsewhere, but remember which ones failed.
FAILED_SCRIPTS=()
for script in 00_health.sh 10_skillservice.sh 20_proposal_flow.sh 30_evidence.sh; do
  echo
  echo "########## $script ##########"
  if ! ./"$script"; then
    FAILED_SCRIPTS+=("$script")
  fi
done

# Final rollup across all scripts, exit non-zero if anything failed.
echo
if [[ "${#FAILED_SCRIPTS[@]}" -gt 0 ]]; then
  echo "run-all: FAILED: ${FAILED_SCRIPTS[*]}"
  exit 1
fi
echo "run-all: all check scripts passed"
