#!/usr/bin/env bash
#
# Writes the release version everywhere it is stated, as one step of cutting a release
# (see .claude/skills/changelog/SKILL.md): frontend/package.json, and the install
# instructions in README.md and README.fa.md - the marked "currently vX.Y.Z" in the
# prose and every `--version vX.Y.Z` example - so the README always shows the release
# the installer gives you.
#   bash deploy/set-version.sh 0.2.0      # write it
#   bash deploy/set-version.sh --check    # fail if a README disagrees with package.json
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readmes=("$root/README.md" "$root/README.fa.md")
semver_ere='[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?'
semver_pcre='[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]+)?'

# readme_versions prints the distinct versions a README states.
readme_versions() {
  grep -oE "<!-- release-version -->\*\*v${semver_ere}\*\*<!-- /release-version -->|--version v${semver_ere}" "$1" \
    | grep -oE "v${semver_ere}" | sort -u
}

check() {
  local version found f status=0
  version="v$(sed -n 's/^  "version": "\([^"]*\)".*/\1/p' "$root/frontend/package.json" | head -n1)"
  for f in "${readmes[@]}"; do
    found="$(readme_versions "$f" | tr '\n' ' ')"
    if [[ "$found" != "$version " ]]; then
      echo "$(basename "$f") states ${found:-no version}- package.json is $version" >&2
      status=1
    fi
  done
  [[ $status -eq 0 ]] && echo "README.md and README.fa.md match $version"
  return $status
}

if [[ "${1:-}" == --check ]]; then
  check
  exit
fi

version="${1:-}"
version="${version#v}"
if [[ ! "$version" =~ ^${semver_ere}$ ]]; then
  echo "usage: bash deploy/set-version.sh X.Y.Z | --check" >&2
  exit 1
fi

(cd "$root/frontend" && npm version "$version" --no-git-tag-version --allow-same-version >/dev/null)
for f in "${readmes[@]}"; do
  perl -pi -e "s{(<!-- release-version -->\*\*)v${semver_pcre}(\*\*<!-- /release-version -->)}{\${1}v${version}\${2}}g; s{--version v${semver_pcre}}{--version v${version}}g" "$f"
done
check
