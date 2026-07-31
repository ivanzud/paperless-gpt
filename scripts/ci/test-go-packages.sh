#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

declare -A package_dirs=()
while IFS= read -r -d '' go_file; do
  package_dir="$(dirname "$go_file")"
  if [[ "$package_dir" == "." ]]; then
    package_dirs["."]=1
  else
    package_dirs["./${package_dir}"]=1
  fi
done < <(git ls-files -z --cached -- '*.go')

if (( ${#package_dirs[@]} == 0 )); then
  echo "No repository-owned Go packages found." >&2
  exit 1
fi

mapfile -t packages < <(printf '%s\n' "${!package_dirs[@]}" | LC_ALL=C sort)
go test "$@" "${packages[@]}"
