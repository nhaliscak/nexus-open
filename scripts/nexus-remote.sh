#!/usr/bin/env bash
# nexus-remote.sh — open the Nexus Open settings UI for a daemon on another host.
#
# Usage: scripts/nexus-remote.sh <ssh-host> [ui-binary]
#
# The daemon's API only accepts localhost requests carrying its token, so this
# forwards the remote API port over SSH and points the UI at a private config
# dir holding the remote token. No change to the daemon's exposure is needed,
# and the local token in ~/.config is never touched.
#
# If the UI binary isn't installed, the tunnel stays open until Ctrl-C so the
# REST API can be used directly (token printed path: $cfg/nexus-open/token).
#
# Assumes the remote runs packaging/systemd/nexus-open-headless.service and
# that the SSH user can sudo to read its token. Any local nexus-open daemon
# must be stopped first: both use port 1985.
#
# Environment:
#   NEXUS_REMOTE_TOKEN  token path on the remote host
#                       (default: headless service path under /var/lib)

set -euo pipefail

host="${1:?usage: $0 <ssh-host> [ui-binary]}"
ui="${2:-/usr/lib/nexus-open/ui-bundle/ui}"
port=1985
remote_token=${NEXUS_REMOTE_TOKEN:-/var/lib/nexus-open/config/nexus-open/token}

if ss -ltn "sport = :$port" | grep -q LISTEN; then
  echo "port $port is already in use locally (is a local nexus-open running?)" >&2
  echo "stop it first: systemctl --user stop nexus-open" >&2
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
(umask 077 && ssh "$host" sudo -n cat "$remote_token" > "$cfg/nexus-open/token")
if [[ ! -s $cfg/nexus-open/token ]]; then
  echo "could not read the API token from $host:$remote_token" >&2
  exit 1
fi

ssh -f -N -M -S "$ctl" -o ExitOnForwardFailure=yes \
  -L "127.0.0.1:$port:127.0.0.1:$port" "$host"

curl -fsS "http://127.0.0.1:$port/api/health" > /dev/null
echo "connected to nexus-open on $host"

if [[ -x $ui ]]; then
  XDG_CONFIG_HOME="$cfg" "$ui"
else
  echo "UI not found at $ui; tunnel open on localhost:$port, Ctrl-C to close."
  echo "token: $cfg/nexus-open/token"
  while ssh -S "$ctl" -O check "$host" 2>/dev/null; do sleep 2; done
fi
