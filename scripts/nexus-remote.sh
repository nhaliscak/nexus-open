#!/usr/bin/env bash
# Open the settings UI on this machine against a nexus-open daemon running on
# another host (e.g. a headless cluster node).
#
# The daemon's API only listens on 127.0.0.1 and checks the Host header, so
# this forwards local port 1985 to the remote daemon over SSH rather than
# exposing it on the network. It also installs the remote daemon's API token
# where the UI looks for it, restoring any local token on exit.
#
# Usage: scripts/nexus-remote.sh user@host
#   Then start the settings UI (nexus-open-ui, or the tray's "Open settings").
#   Ctrl-C closes the tunnel and restores the local token.
#
# Environment:
#   NEXUS_REMOTE_TOKEN  token path on the remote host
#                       (default: headless service path under /var/lib)
set -euo pipefail

if [[ $# -ne 1 ]]; then
	echo "usage: $0 user@host" >&2
	exit 2
fi
host=$1
remote_token=${NEXUS_REMOTE_TOKEN:-/var/lib/nexus-open/config/nexus-open/token}
token_dir="${XDG_CONFIG_HOME:-$HOME/.config}/nexus-open"
token="$token_dir/token"
backup="$token_dir/token.local"

if ss -ltn 2>/dev/null | grep -q '127.0.0.1:1985 '; then
	echo "Port 1985 is in use, probably by a local nexus-open daemon." >&2
	echo "Stop it first: systemctl --user stop nexus-open" >&2
	exit 1
fi

mkdir -p "$token_dir"
if [[ -f $token && ! -f $backup ]]; then
	mv "$token" "$backup"
fi
restore() {
	rm -f "$token"
	if [[ -f $backup ]]; then
		mv "$backup" "$token"
	fi
}
trap restore EXIT

# The headless service's state dir is root-owned, so read it with sudo.
(umask 077 && ssh -t "$host" "sudo cat '$remote_token'" | tr -d '\r' >"$token")
if [[ ! -s $token ]]; then
	echo "Could not read the API token from $host:$remote_token" >&2
	exit 1
fi

echo "Forwarding localhost:1985 to $host. Open the settings UI now; Ctrl-C to stop."
ssh -N -o ExitOnForwardFailure=yes -L 1985:127.0.0.1:1985 "$host"
