#!/bin/sh
# Builds the sandbox base image and publishes it under the alias flow-base in Incus' default project, whose images
# the restricted project shares (features.images=false). Runs as root on the host after host-setup.sh; the
# restricted certificate cannot publish images, so this is an admin step.
#
#   sh deploy/sandbox-image.sh
#
# What it holds now: Debian 13 with systemd, git, ca-certificates and the agent user flow (uid and gid 65532,
# never 1000: layout.AgentUID), plus Flow's directories. Enough for flowd, session hosts and worktrees. No network
# configuration of its own: Flow writes eth0's before each start (layout.NetworkConfig), and the image's DHCP unit
# is removed (Docker bridges have no DHCP).
#
# The build instance sits on Flow's bridge like a sandbox, isolated and filtered, at BUILDER_ADDRESS: the highest
# usable address of the sandbox range, which Flow never assigns (sandbox.Network.BuilderAddress).
set -eu

SCRIPT=sandbox-image
# Bridge, subnet, range and gateway as host-setup.sh and Flow read them (BACKEND_SANDBOX_*).
# shellcheck source=deploy/host-lib.sh
. "$(dirname "$0")/host-lib.sh"

ALIAS="${ALIAS:-flow-base}"
SOURCE="${SOURCE:-images:debian/13}"
RESOLVERS="${BACKEND_SANDBOX_RESOLVERS:-1.1.1.1,9.9.9.9}"
BUILDER="flow-image-build"
NETWORK_UNIT=/etc/systemd/network/00-flow-eth0.network

check_settings
BUILDER_ADDRESS="${BUILDER_ADDRESS:-$(builder_address)}"
ip_in "$BUILDER_ADDRESS" "$RANGE" || die "builder address $BUILDER_ADDRESS is outside the sandbox range $RANGE"

[ "$(id -u)" -eq 0 ] || die "run as root"
ip link show "$BRIDGE" >/dev/null 2>&1 || die "bridge $BRIDGE missing: run host-setup.sh first"

incus delete --force "$BUILDER" </dev/null >/dev/null 2>&1 || true
log "creating $BUILDER from $SOURCE at $BUILDER_ADDRESS"
incus init "$SOURCE" "$BUILDER" --storage default --no-profiles </dev/null
incus config device add "$BUILDER" eth0 nic nictype=bridged parent="$BRIDGE" name=eth0 \
	security.port_isolation=true security.mac_filtering=true security.ipv4_filtering=true \
	ipv4.address="$BUILDER_ADDRESS" </dev/null >/dev/null
unit="$(mktemp /tmp/flow-image-net.XXXXXX)"
{
	printf '[Match]\nName=eth0\n\n[Network]\nAddress=%s/%s\nGateway=%s\n' "$BUILDER_ADDRESS" "${SUBNET#*/}" "$GATEWAY"
	printf '%s\n' "$RESOLVERS" | tr ',' '\n' | sed 's/^ *//; s/ *$//; /^$/d; s/^/DNS=/'
	printf 'LinkLocalAddressing=no\nIPv6AcceptRA=no\n'
} >"$unit"
incus file push "$unit" "$BUILDER$NETWORK_UNIT" --mode 0644 </dev/null
rm -f "$unit"
incus start "$BUILDER" </dev/null

# Wait for the network: apt needs DNS and a route.
i=0
until incus exec "$BUILDER" -- getent hosts deb.debian.org </dev/null >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 60 ]; then
		echo "sandbox-image: no network in $BUILDER after 60 s" >&2
		exit 1
	fi
	sleep 1
done

log "installing packages and the agent user"
incus exec "$BUILDER" </dev/null -- sh -eu -c '
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y -qq --no-install-recommends git ca-certificates
	groupadd --gid 65532 flow
	useradd --uid 65532 --gid 65532 --create-home --home-dir /home/flow --shell /bin/bash flow
	install -d -m 0755 /opt/flow/releases /var/lib/flow
	install -d -m 0700 /etc/flow
	install -d -o flow -g flow -m 0755 /home/flow/work /home/flow/work/.sessions
	apt-get clean
	rm -rf /var/lib/apt/lists/*
'

# LATER (the full toolchain, a separate step once agents run for real): Docker Engine with the containerd
# snapshotter and flow in the docker group, Node 26 and the Claude Code CLI, OpenCode, Go 1.27, gcc and libc6-dev,
# make, shellcheck 0.11 (Debian ships 0.10), gh, Postgres client, Playwright Chromium and its system dependencies.

log "publishing as $ALIAS"
incus stop "$BUILDER" </dev/null
# Neither the builder's address nor the image's DHCP unit belong in the image.
incus file delete "$BUILDER$NETWORK_UNIT" </dev/null
incus file delete "$BUILDER/etc/systemd/network/eth0.network" </dev/null 2>/dev/null || true
# The alias stays on the old image until the new one is published (--reuse moves it then), so Flow always has a
# base image, also when publishing fails. Incus 6.0.4's --reuse also deletes the superseded image (measured); one
# that is left is deleted here. Instances made from it do not need it.
image_fp() { incus image info "$1" </dev/null 2>/dev/null | awk '/^Fingerprint:/ { print $2 }'; }
old_fp="$(image_fp "$ALIAS")"
incus publish "$BUILDER" --alias "$ALIAS" --reuse </dev/null
new_fp="$(image_fp "$ALIAS")"
if [ -n "$old_fp" ] && [ "$old_fp" != "$new_fp" ] && [ -n "$(image_fp "$old_fp")" ]; then
	log "deleting the superseded image ${old_fp%"${old_fp#????????????}"}"
	incus image delete "$old_fp" </dev/null
fi
incus delete "$BUILDER" </dev/null
log "done: $(incus image list "$ALIAS" --format csv -c lfs </dev/null)"
