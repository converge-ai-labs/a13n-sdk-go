#!/usr/bin/env bash
# Shared input selection for local imports and automatic proposals.
files='{
  "openapi.json": "proto/a13n-service/openapi.json",
  "thread-stream.schema.json": "proto/a13n-service/thread-stream.schema.json",
  "session-stream.schema.json": "proto/a13n-service/session-stream.schema.json",
  "semantics/api-conventions.md": "spec/api-conventions.md",
  "semantics/runs.md": "spec/a13n-service/05-runs.md",
  "semantics/facts-and-delivery.md": "spec/a13n-service/07-facts-and-delivery.md",
  "semantics/api.md": "spec/a13n-service/10-api.md"
}'

# Input-set extensions require an import even at the same source SHA. Extra
# historical inputs are retired by the next import, not mistaken for additions.
contract_input_set_changed() {
  ! jq -e --argjson selected "$files" '
    .files as $owned | $selected | to_entries | all(.[]; $owned[.key].source_path == .value)
  ' "$1" >/dev/null
}

contract_inputs_changed() {
  local upstream=$1 previous=$2 incoming=$3 status=0
  if contract_input_set_changed contract/source.json; then return 0; fi
  local -a paths
  mapfile -t paths < <(jq -r '.[]' <<< "$files")
  git -C "$upstream" diff --quiet --no-ext-diff "$previous" "$incoming" -- "${paths[@]}" || status=$?
  case "$status" in
    0) return 1 ;;
    1) return 0 ;;
    *) echo 'Contract sync: cannot compare source inputs' >&2; exit "$status" ;;
  esac
}
