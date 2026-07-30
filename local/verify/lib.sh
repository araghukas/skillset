# Shared helpers for the local/verify/*.sh scripts. Not directly executable
# - sourced by each check script and by run-all.sh.
#
# Assumes a local deployment reachable at SKILLSD_ADDR (default
# localhost:8080) and, for the registry-dependent scripts, REGISTRY_ADDR
# (default localhost:8081) - i.e. the port-forwards `make dev` / `tilt up`
# already set up.
set -euo pipefail

# Where the two services are reachable, overridable per-invocation.
SKILLSD_ADDR="${SKILLSD_ADDR:-localhost:8080}"
REGISTRY_ADDR="${REGISTRY_ADDR:-localhost:8081}"

# grpcurl defaults to a 4 MiB max response size, but skillsd/skillsd-registry
# are configured (grpcMaxRecvMsgSizeMiB/grpcMaxSendMsgSizeMiB in
# charts/skillsd/values.yaml) to allow up to 8 MiB - ListSkills with
# include_context_files routinely exceeds 4 MiB. Match the client cap to the
# server's, overridable if a deployment's values differ.
GRPC_MAX_MSG_SIZE_BYTES="${GRPC_MAX_MSG_SIZE_BYTES:-8388608}"

# Running tally, printed by summarize() at the end of each script.
CHECKS_RUN=0
CHECKS_FAILED=0

# rpc <addr> <full.method.Name> [json-data]
# Calls grpcurl in plaintext mode and echoes the response body.
rpc() {
  local addr="$1" method="$2" data="${3:-}"
  [[ -z "$data" ]] && data='{}'
  grpcurl -plaintext -max-msg-sz "$GRPC_MAX_MSG_SIZE_BYTES" -d "$data" "$addr" "$method"
}

# check <description> <actual> <jq-filter-expecting-boolean>
# Runs a jq boolean filter over actual (JSON text); prints pass/fail.
check() {
  local desc="$1" json="$2" filter="$3"
  CHECKS_RUN=$((CHECKS_RUN + 1))
  if echo "$json" | jq -e "$filter" >/dev/null 2>&1; then
    echo "  ok   - $desc"
  else
    echo "  FAIL - $desc"
    echo "$json" | jq . >&2 || echo "$json" >&2
    CHECKS_FAILED=$((CHECKS_FAILED + 1))
  fi
}

# fail <description>
# Records an unconditional failure (e.g. an RPC that should have errored,
# or a shell command that itself failed).
fail() {
  CHECKS_RUN=$((CHECKS_RUN + 1))
  CHECKS_FAILED=$((CHECKS_FAILED + 1))
  echo "  FAIL - $1" >&2
}

# summarize
# Prints the pass/fail tally for the calling script and exits non-zero if
# anything failed. Call this once, at the end of each check script.
summarize() {
  echo
  if [[ "$CHECKS_FAILED" -gt 0 ]]; then
    echo "$(basename "$0"): $CHECKS_FAILED/$CHECKS_RUN checks failed"
    exit 1
  fi
  echo "$(basename "$0"): all $CHECKS_RUN checks passed"
}
