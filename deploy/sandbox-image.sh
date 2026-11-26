#!/bin/sh
# Builds the sandbox base image and publishes it under the alias flow-base in Incus' default project, whose images
# the restricted project shares (features.images=false). Runs as root on the host after host-setup.sh; the
# restricted certificate cannot publish images, so this is an admin step.
#
#   sh deploy/sandbox-image.sh
#
# What it holds now: Debian 13 with systemd, git, ca-certificates and the agent user flow (uid and gid 65532,
# never 1000: layout.AgentUID), plus Flow's directories. Enough for flowd, session hosts and worktrees.
set -eu

ALIAS="${ALIAS:-flow-base}"
SOURCE="${SOURCE:-images:debian/13}"
BRIDGE="${BRIDGE:-incusbr0}"
BUILDER="flow-image-build"

log() { printf '%s sandbox-image: %s\n' "$(date -u +%H:%M:%S)" "$*"; }

if [ "$(id -u)" -ne 0 ]; then
	echo "sandbox-image: run as root" >&2
	exit 1
fi

incus delete --force "$BUILDER" >/dev/null 2>&1 || true
log "launching $SOURCE"
# The default project's profile has no devices (host-setup.sh equips only Flow's project): name them here.
incus init "$SOURCE" "$BUILDER" --storage default --network "$BRIDGE"
incus start "$BUILDER"

# Wait for the network: apt needs DNS and a route.
i=0
until incus exec "$BUILDER" -- getent hosts deb.debian.org >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 60 ]; then
		echo "sandbox-image: no network in $BUILDER after 60 s" >&2
		exit 1
	fi
	sleep 1
done

log "installing packages and the agent user"
incus exec "$BUILDER" -- sh -eu -c '
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
incus stop "$BUILDER"
incus image alias delete "$ALIAS" >/dev/null 2>&1 || true
incus publish "$BUILDER" --alias "$ALIAS" --reuse
incus delete "$BUILDER"
log "done: $(incus image list "$ALIAS" --format csv -c lfs)"
