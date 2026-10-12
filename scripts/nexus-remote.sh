#!/usr/bin/env bash
# nexus-remote.sh — open the Nexus Open settings UI for a daemon on another host.
#
# Usage: scripts/nexus-remote.sh <ssh-host> [ui-binary]
#
# The daemon's API only accepts localhost requests carrying its token, so this
# forwards the remote API port over SSH and points the UI at a private config
# dir holding the remote token. No change to the daemon's exposure is needed.
#
# Assumes the remote runs packaging/systemd/nexus-open-headless.service (token
# under /var/lib/nexus-open) and that the SSH user can sudo to read it. Any
# local nexus-open daemon must be stopped first: both use port 1985.

set -euo pipefail

host="${1:?usage: $0 <ssh-host> [ui-binary]}"
ui="${2:-/usr/lib/nexus-open/ui-bundle/ui}"
port=1985
remote_token=/var/lib/nexus-open/config/nexus-open/token

if ss -ltn "sport = :$port" | grep -q LISTEN; then
  echo "port $port is already in use locally (is a local nexus-open running?)" >&2
  exit 1
fi

cfg="$(mktemp -d)"
ctl="$cfg/ssh.sock"
cleanup() {
  ssh -S "$ctl" -O exit "$host" 2>/dev/null || true
  rm -rf "$cfg"
}
trap cleanup EXIT

mkdir -m 700 "$cfg/nexus-open"
ssh "$host" sudo cat "$remote_token" > "$cfg/nexus-open/token"
chmod 600 "$cfg/nexus-open/token"

ssh -f -N -M -S "$ctl" -o ExitOnForwardFailure=yes \
  -L "127.0.0.1:$port:127.0.0.1:$port" "$host"

curl -fsS "http://127.0.0.1:$port/api/health" > /dev/null
echo "connected to nexus-open on $host"

XDG_CONFIG_HOME="$cfg" "$ui"
