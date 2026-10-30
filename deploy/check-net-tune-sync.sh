#!/usr/bin/env bash
#
# The host network tuning block and the release version block live, identically, in
# install.sh, install-node.sh and the wgpanel CLI (install-node.sh is fetched
# standalone, so it cannot source a shared file). This check fails if the copies of
# either block drift apart.
#   bash deploy/check-net-tune-sync.sh
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
extract() {
  sed -n "/^# >>> wgpanel $2/,/^# <<< wgpanel $2/p" "$dir/$1"
}

status=0
for block in net-tune version; do
  ref="$(extract wgpanel "$block")"
  if [[ -z "$ref" ]]; then
    echo "$block block not found in wgpanel" >&2
    status=1
    continue
  fi
  for f in install.sh install-node.sh; do
    if [[ "$(extract "$f" "$block")" != "$ref" ]]; then
      echo "$block block in $f differs from wgpanel" >&2
      status=1
    fi
  done
done
[[ $status -eq 0 ]] && echo "net-tune and version blocks are in sync"
exit $status
