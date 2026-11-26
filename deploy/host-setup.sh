#!/bin/sh
# Prepares a Debian 13 host for Flow's sandboxes: Incus with a btrfs pool and a managed bridge, the restricted
# project Flow drives, Flow's project-restricted client certificate, and the egress ACL that keeps sandboxes away
# from the host, the LAN and each other. Idempotent: every step checks before it changes anything.
#
# Run as root on the host:  sh deploy/host-setup.sh
# Measured settings: docs of the spike (restricted project, relaxed nesting and snapshots, ACL rules).
#
# Not done here yet (later steps): Docker for Flow's container with "ip-forward-no-drop": true in
# /etc/docker/daemon.json (Flow runs as a plain binary in the dev setup), Traefik, pool quotas.
set -eu

POOL_DRIVER="${POOL_DRIVER:-btrfs}"
POOL_SIZE="${POOL_SIZE:-40GiB}"
BRIDGE="${BRIDGE:-incusbr0}"
BRIDGE_CIDR="${BRIDGE_CIDR:-10.77.0.1/24}"
PROJECT="${PROJECT:-flow}"
# Where Flow reaches the Incus API. Loopback while Flow runs on the host; the Docker bridge gateway once Flow runs
# in a container (never a public address).
INCUS_LISTEN="${INCUS_LISTEN:-127.0.0.1:8443}"
CERT_DIR="${CERT_DIR:-/etc/flow-host/incus}"
ACL="${ACL:-sbx-egress}"

log() { printf '%s host-setup: %s\n' "$(date -u +%H:%M:%S)" "$*"; }

if [ "$(id -u)" -ne 0 ]; then
	echo "host-setup: run as root" >&2
	exit 1
fi

# 1. Packages. incus-base: containers only, no QEMU pulled in through Recommends; dnsmasq-base is only a
# Recommends of it, but a managed bridge hangs on create without it.
if ! command -v incus >/dev/null 2>&1 || ! command -v dnsmasq >/dev/null 2>&1; then
	log "installing incus-base, dnsmasq-base, btrfs-progs"
	apt-get update -qq
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends incus-base incus-client \
		dnsmasq-base btrfs-progs openssl ca-certificates
fi
systemctl enable --now incus.socket incus.service >/dev/null 2>&1 || true

# 2. Storage pool and bridge. A loop-file btrfs pool gives copy-on-write clones; quotas could not be enabled on
# the spike kernel, so no size= on volumes. POOL_DRIVER=dir is the fallback for hosts that refuse loop devices
# and btrfs ioctls (an OrbStack machine with a seccomp filter: losetup fails with EPERM): no copy-on-write, every
# sandbox a full copy of the image.
if ! incus storage show default >/dev/null 2>&1; then
	case "$POOL_DRIVER" in
	btrfs)
		log "creating btrfs pool default ($POOL_SIZE)"
		incus storage create default btrfs size="$POOL_SIZE"
		;;
	dir)
		log "creating dir pool default (no copy-on-write)"
		incus storage create default dir
		;;
	*)
		echo "host-setup: POOL_DRIVER must be btrfs or dir" >&2
		exit 1
		;;
	esac
fi
if ! incus network show "$BRIDGE" >/dev/null 2>&1; then
	log "creating bridge $BRIDGE ($BRIDGE_CIDR)"
	incus network create "$BRIDGE" ipv4.address="$BRIDGE_CIDR" ipv4.nat=true ipv6.address=none
fi

# 3. The API listener Flow uses with its client certificate.
if [ "$(incus config get core.https_address)" != "$INCUS_LISTEN" ]; then
	log "Incus API on $INCUS_LISTEN"
	incus config set core.https_address "$INCUS_LISTEN"
fi

# 4. The restricted project: images shared from the default project (the base image is published there), its own
# profiles and volumes, no networks of its own. Relaxed: nesting (Docker inside) and snapshots.
if ! incus project show "$PROJECT" >/dev/null 2>&1; then
	log "creating project $PROJECT"
	incus project create "$PROJECT" -c features.images=false -c features.profiles=true \
		-c features.storage.volumes=true -c features.networks=false -c restricted=true
fi
incus project set "$PROJECT" restricted.containers.nesting=allow restricted.snapshots=allow
if ! incus profile device get default root pool --project "$PROJECT" >/dev/null 2>&1; then
	incus profile device add default root disk path=/ pool=default --project "$PROJECT"
fi
if ! incus profile device get default eth0 network --project "$PROJECT" >/dev/null 2>&1; then
	incus profile device add default eth0 nic network="$BRIDGE" name=eth0 --project "$PROJECT"
fi

# 5. Flow's client certificate, trusted for the project only. The server certificate is copied beside it: Flow
# pins it, since its SAN never names the address Flow dials.
mkdir -p "$CERT_DIR"
chmod 0700 "$CERT_DIR"
if [ ! -f "$CERT_DIR/client.crt" ]; then
	log "generating Flow's client certificate"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:secp384r1 -nodes -days 3650 \
		-keyout "$CERT_DIR/client.key" -out "$CERT_DIR/client.crt" -subj /CN=flow-backend 2>/dev/null
fi
# csv columns of incus 6.0: name,type,description,fingerprint,expiry
if ! incus config trust list --format csv | grep -q '^flow-backend,client,'; then
	log "trusting the client certificate for project $PROJECT"
	incus config trust add-certificate "$CERT_DIR/client.crt" --restricted --projects "$PROJECT" --name flow-backend
fi
cp /var/lib/incus/server.crt "$CERT_DIR/server.crt"
chmod 0600 "$CERT_DIR"/*

# 6. Egress ACL on the bridge: sandboxes keep DNS and the internet, lose the host (all but DNS on the bridge
# address), private networks and each other. Incus applies rejects before allows.
uplink_ip="$(ip -4 -o addr show scope global | awk -v br="$BRIDGE" '$2 != br {split($4, a, "/"); print a[1]; exit}')"
bridge_ip="${BRIDGE_CIDR%/*}"
if ! incus network acl show "$ACL" >/dev/null 2>&1; then
	log "creating ACL $ACL (uplink $uplink_ip)"
	incus network acl create "$ACL"
	incus network acl rule add "$ACL" egress action=reject destination="$bridge_ip/32" protocol=tcp destination_port=1-52,54-65535
	incus network acl rule add "$ACL" egress action=reject destination="$uplink_ip/32,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
	incus network acl rule add "$ACL" egress action=allow
	incus network acl rule add "$ACL" ingress action=allow
fi
incus network set "$BRIDGE" security.acls="$ACL"

log "done: project $PROJECT, API $INCUS_LISTEN, certificates in $CERT_DIR"
