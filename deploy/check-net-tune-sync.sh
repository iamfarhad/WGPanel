#!/usr/bin/env bash
#
# The host network tuning block lives, identically, in install.sh, install-node.sh and
# the wgpanel CLI (install-node.sh is fetched standalone, so it cannot source a shared
# file). This check fails if the three copies drift apart.
#   bash deploy/check-net-tune-sync.sh
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
extract() {
  sed -n '/^# >>> wgpanel net-tune/,/^# <<< wgpanel net-tune/p' "$dir/$1"
}

ref="$(extract wgpanel)"
if [[ -z "$ref" ]]; then
  echo "net-tune block not found in wgpanel" >&2
  exit 1
fi
status=0
for f in install.sh install-node.sh; do
  if [[ "$(extract "$f")" != "$ref" ]]; then
    echo "net-tune block in $f differs from wgpanel" >&2
    status=1
  fi
done
[[ $status -eq 0 ]] && echo "net-tune blocks are in sync"
exit $status
