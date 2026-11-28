#!/bin/sh
# Builds the sandbox base image and publishes it under the alias flow-base (BACKEND_SANDBOX_IMAGE) in Incus' default
# project, whose images the restricted project shares (features.images=false). Runs as root on the host after
# host-setup.sh; the restricted certificate cannot publish images, so this is an admin step. Repeatable: every run
# builds from scratch and replaces the image under the alias.
#
#   sh deploy/sandbox-image.sh
#
# What the image holds: the versions in image/versions.sh (the one place to bump them), installed by
# image/provision.sh: Debian 13 with systemd; Go, Node with npm, Claude Code's CLI (/usr/local/bin/claude),
# OpenCode, gh, shellcheck, Playwright with Chromium and WebKit (/opt/ms-playwright), Docker Engine with compose and
# buildx; git, make, gcc, PostgreSQL client tools and the rest of versions.sh's Debian list; the agent user flow
# (uid and gid 65532, never 1000: layout.AgentUID) in the docker group, Flow's directories, and one environment
# for units, PAM sessions and shells (/etc/environment, DefaultEnvironment, profile.d). Inside the image,
# /usr/local/share/flow/ keeps versions.sh, check.sh and the inventory (tool, pinned, reported version).
# Not in it: flowd and the sidecar, which Flow pushes into each sandbox. No network configuration of its own: Flow
# writes eth0's before each start (layout.NetworkConfig), and the image's DHCP unit is removed (Docker bridges have
# no DHCP).
#
# Check a sandbox made from it (image/check.sh: versions, Docker with nested NAT, Chromium and WebKit, the race
# detector, npm ci):
#
#   incus exec --project flow <sandbox> -- systemd-run --quiet --wait --pipe --collect --uid=flow \
#     -p MemorySwapMax=0 sh /usr/local/share/flow/check.sh </dev/null
#
# The build instance sits on Flow's bridge like a sandbox, isolated and filtered, at BUILDER_ADDRESS: the highest
# usable address of the sandbox range, which Flow never assigns (sandbox.Network.BuilderAddress). It is deleted
# when the script ends, also when it fails.
set -eu

SCRIPT=sandbox-image
DIR="$(dirname "$0")"
# Bridge, subnet, range and gateway as host-setup.sh and Flow read them (BACKEND_SANDBOX_*).
# shellcheck source=deploy/host-lib.sh
. "$DIR/host-lib.sh"
for f in versions.sh provision.sh check.sh; do
	[ -f "$DIR/image/$f" ] || die "$DIR/image/$f missing"
done
# shellcheck source=deploy/image/versions.sh
. "$DIR/image/versions.sh"

ALIAS="${ALIAS:-${BACKEND_SANDBOX_IMAGE:-flow-base}}"
SOURCE="${SOURCE:-$SOURCE_IMAGE}"
RESOLVERS="${BACKEND_SANDBOX_RESOLVERS:-1.1.1.1,9.9.9.9}"
BUILDER="flow-image-build"
NETWORK_UNIT=/etc/systemd/network/00-flow-eth0.network
SHARE=/usr/local/share/flow

check_settings
BUILDER_ADDRESS="${BUILDER_ADDRESS:-$(builder_address)}"
ip_in "$BUILDER_ADDRESS" "$RANGE" || die "builder address $BUILDER_ADDRESS is outside the sandbox range $RANGE"

[ "$(id -u)" -eq 0 ] || die "run as root"
ip link show "$BRIDGE" >/dev/null 2>&1 || die "bridge $BRIDGE missing: run host-setup.sh first"

started="$(date +%s)"
unit="$(mktemp /tmp/flow-image-net.XXXXXX)"
# The builder never outlives the script: not after success, not after a failure or an interrupt.
cleanup() {
	rm -f "$unit"
	incus delete --force "$BUILDER" </dev/null >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# A builder left by a killed run (SIGKILL skips the trap).
incus delete --force "$BUILDER" </dev/null >/dev/null 2>&1 || true
log "creating $BUILDER from $SOURCE at $BUILDER_ADDRESS"
incus init -q "$SOURCE" "$BUILDER" --storage "$POOL" --no-profiles </dev/null
incus config device add "$BUILDER" eth0 nic nictype=bridged parent="$BRIDGE" name=eth0 \
	security.port_isolation=true security.mac_filtering=true security.ipv4_filtering=true \
	ipv4.address="$BUILDER_ADDRESS" </dev/null >/dev/null
{
	printf '[Match]\nName=eth0\n\n[Network]\nAddress=%s/%s\nGateway=%s\n' "$BUILDER_ADDRESS" "${SUBNET#*/}" "$GATEWAY"
	printf '%s\n' "$RESOLVERS" | tr ',' '\n' | sed 's/^ *//; s/ *$//; /^$/d; s/^/DNS=/'
	printf 'LinkLocalAddressing=no\nIPv6AcceptRA=no\n'
} >"$unit"
# Pushed files are root's: without --uid/--gid Incus keeps the source file's owner (measured: a checkout owned by
# uid 1234 lands as 1234).
incus file push -q --uid 0 --gid 0 --mode 0644 "$unit" "$BUILDER$NETWORK_UNIT" </dev/null
incus start "$BUILDER" </dev/null

# Wait for the network: apt needs DNS and a route.
i=0
until incus exec "$BUILDER" -- getent hosts deb.debian.org </dev/null >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -le 60 ] || die "no network in $BUILDER after 60 s"
	sleep 1
done

log "provisioning (image/provision.sh)"
incus exec "$BUILDER" -- install -d -m 0755 "$SHARE" </dev/null
for f in versions.sh provision.sh check.sh; do
	incus file push -q --uid 0 --gid 0 --mode 0644 "$DIR/image/$f" "$BUILDER$SHARE/$f" </dev/null
done
incus exec "$BUILDER" -- sh "$SHARE/provision.sh" </dev/null
incus exec "$BUILDER" -- rm -f "$SHARE/provision.sh" </dev/null

log "publishing as $ALIAS"
incus stop "$BUILDER" </dev/null
# Neither the builder's address nor the image's DHCP unit belong in the image.
incus file delete "$BUILDER$NETWORK_UNIT" </dev/null
incus file delete "$BUILDER/etc/systemd/network/eth0.network" </dev/null 2>/dev/null || true
# Nor the builder's machine identity (measured: every sandbox of an earlier image had the builder's machine id) or
# its journal, which is filed under that id. An empty /etc/machine-id makes systemd generate a new id on each
# sandbox's first boot.
machine_id="$(incus file pull "$BUILDER/etc/machine-id" - </dev/null 2>/dev/null | tr -cd '0-9a-f')"
empty="$(mktemp /tmp/flow-image-empty.XXXXXX)"
incus file push -q --uid 0 --gid 0 --mode 0444 "$empty" "$BUILDER/etc/machine-id" </dev/null
rm -f "$empty"
if [ -n "$machine_id" ]; then
	incus file delete -f "$BUILDER/var/log/journal/$machine_id" </dev/null 2>/dev/null || true
fi
# Other per-machine secrets systemd makes at boot, so no two sandboxes share them.
for f in /var/lib/systemd/random-seed /var/lib/systemd/credential.secret; do
	incus file delete "$BUILDER$f" </dev/null 2>/dev/null || true
done
# The alias stays on the old image until the new one is published (--reuse moves it then), so Flow always has a
# base image, also when publishing fails. Incus 6.0.4's --reuse also deletes the superseded image (measured); one
# that is left is deleted here. Instances made from it do not need it.
image_fp() { incus image info "$1" </dev/null 2>/dev/null | awk '/^Fingerprint:/ { print $2 }'; }
old_fp="$(image_fp "$ALIAS")"
incus publish -q "$BUILDER" --alias "$ALIAS" --reuse \
	description="Flow sandbox base ($(date -u +%Y-%m-%d), Go $GO_VERSION, Node $NODE_VERSION, Claude Code $CLAUDE_VERSION, OpenCode $OPENCODE_VERSION)" \
	</dev/null
new_fp="$(image_fp "$ALIAS")"
if [ -n "$old_fp" ] && [ "$old_fp" != "$new_fp" ] && [ -n "$(image_fp "$old_fp")" ]; then
	log "deleting the superseded image ${old_fp%"${old_fp#????????????}"}"
	incus image delete "$old_fp" </dev/null
fi
log "done in $(($(date +%s) - started)) s: $(incus image list "$ALIAS" --format csv -c lfs </dev/null)"
