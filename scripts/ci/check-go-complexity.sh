#!/usr/bin/env bash
set -euo pipefail

threshold="${GO_COMPLEXITY_THRESHOLD:-15}"
baseline_file="scripts/ci/go-complexity-baseline.txt"
output_file="$(mktemp)"
trap 'rm -f "$output_file"' EXIT

mapfile -d '' go_files < <(
  git ls-files -z --cached --others --exclude-standard -- '*.go'
)
if (( ${#go_files[@]} == 0 )); then
  echo "No repository-owned Go files found."
  exit 1
fi

"$(go env GOPATH)/bin/gocyclo" -over "$threshold" "${go_files[@]}" >"$output_file" || true

declare -A baseline=()
while IFS='|' read -r func_name file_path max_complexity; do
  [[ -z "${func_name}" || "${func_name}" == \#* ]] && continue
  baseline["${func_name}|${file_path}"]="${max_complexity}"
done <"$baseline_file"

status=0
while read -r complexity _package func_name location; do
  [[ -z "${complexity}" ]] && continue

  file_path="${location%%:*}"
  key="${func_name}|${file_path}"
  allowed="${baseline[$key]:-}"

  if [[ -z "${allowed}" ]]; then
    echo "New complexity hotspot: ${func_name} in ${file_path} (${complexity} > ${threshold})"
    status=1
    continue
  fi

  if (( complexity > allowed )); then
    echo "Complexity regression: ${func_name} in ${file_path} rose to ${complexity} (baseline ${allowed})"
    status=1
  fi
done <"$output_file"

if (( status != 0 )); then
  echo "Complexity baseline file: ${baseline_file}"
  exit "${status}"
fi

echo "Go complexity is within the recorded baseline."
