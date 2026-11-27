#!/bin/sh
# Prepares a Debian 13 host that already runs Docker behind firewalld for Flow's sandboxes, without touching
# anything it did not create. Run by the owner by hand, as root, never by Flow:
#
#   sh deploy/host-setup.sh --dry-run    prints what it would change, changes nothing
#   sh deploy/host-setup.sh              applies, then verifies
#   sh deploy/host-teardown.sh           the reverse (see there)
#
# What it adds (docs/architecture.md, "Sandbox network"; measured in the lab, flow-net-findings):
#   1. Docker network $DOCKER_NETWORK on bridge $BRIDGE ($SUBNET, Docker allocating only from $DOCKER_RANGE): Docker
#      provides forwarding, masquerading, the firewalld zone `docker` and the separation from other Docker networks.
#   2. firewalld policies flow-sb-host (the sandbox range may not reach the host; IPv6 from the zone `docker` is
#      rejected), flow-sb-docker (nor other Docker bridges, including a routed hairpin back onto $BRIDGE) and
#      flow-sb-lan (nor $LAN_DESTINATIONS behind the host: LAN, cloud metadata; the internet stays reachable).
#   3. One DOCKER-USER rule: sandboxes may not reach other containers' published ports (firewalld accepts DNAT'd
#      traffic before any policy). Persistence: the systemd unit flow-sandbox-published.service, bound to
#      docker.service (WantedBy + PartOf + After) and ordered before incus.service, inserts it once Docker has made
#      DOCKER-USER (at boot, before Incus autostarts sandboxes) and checks it again whenever Docker restarts.
#      Chosen as the least invasive: it owns exactly this one rule; the teardown deletes it.
#      Rejected: iptables-persistent (saves and restores the whole ruleset, fighting Docker and firewalld over
#      theirs), firewalld direct rules (deprecated; applied when firewalld starts, before Docker made the chain).
#      Docker itself never flushes DOCKER-USER (measured: restart, firewalld reload, network re-create).
#   4. Incus (incus-base, containers only) without any managed network; a storage pool; the API on $INCUS_LISTEN.
#   5. systemd drop-in: incus.service starts after Docker has made $BRIDGE (else autostart fails three times and
#      leaves the sandboxes stopped; measured).
#   6. The restricted Incus project $PROJECT and Flow's client certificate, trusted for that project only. The
#      project needs restricted.devices.nic=allow: a restricted project refuses NICs on an unmanaged bridge
#      ("Only managed network devices are allowed", measured), and Incus has nothing narrower.
#   Flow's container (not made here) joins $DOCKER_NETWORK to dial flowd and must also be on the reverse proxy's
#   network, where the proxy reaches it (Traefik: label traefik.docker.network=<the proxy's network>). Flow refuses
#   every browser request arriving over $DOCKER_NETWORK, which sandboxes reach at L2; with no other network it
#   answers nothing and logs that at start.
#   Optional (INCUS_PROJECT_MEMORY=1): limits.memory on the project = BACKEND_SANDBOX_MEMORY_TOTAL_MIB. Incus counts
#   every instance of the project in it, stopped ones too (measured), so it caps how many sandboxes exist, not how
#   many run; Flow enforces the same ceiling on running sandboxes itself. Off by default.
#
# Safety: refuses unless firewalld (nftables), Docker (iptables-nft) and the ban chain are as expected; records a
# baseline of the whole ruleset and firewalld's zones and policies before the first change ($STATE_DIR/baseline,
# written once) and a snapshot before each run; after applying it waits up to $BAN_WAIT s for $BAN_CHAIN to be
# first in FORWARD again, then prints the difference and fails on anything Flow's changes do not explain. It
# writes a manifest of what it created, so the teardown removes exactly that.
#
# Settings (environment): BACKEND_SANDBOX_BRIDGE, _SUBNET, _RANGE, _GATEWAY, _MEMORY_TOTAL_MIB as Flow reads them;
# DOCKER_NETWORK, DOCKER_RANGE, BAN_CHAIN (empty: none), BAN_WAIT, PROJECT, INCUS_LISTEN, CERT_DIR, POOL_DRIVER
# (btrfs, or dir where loop devices are refused), POOL_SIZE, STATE_DIR, INCUS_PROJECT_MEMORY. Defaults in
# host-lib.sh.
set -eu

SCRIPT=host-setup
# shellcheck source=deploy/host-lib.sh
. "$(dirname "$0")/host-lib.sh"

for arg in "$@"; do
	case "$arg" in
	--dry-run) DRY_RUN=1 ;;
	*) die "unknown argument $arg (only --dry-run)" ;;
	esac
done
[ "$(id -u)" -eq 0 ] || die "run as root"

check_settings
preflight

# The Docker network: either ours with exactly these settings, or absent with its subnet free.
net_want="$SUBNET $DOCKER_RANGE $GATEWAY $BRIDGE"
if docker network inspect "$DOCKER_NETWORK" >/dev/null 2>&1; then
	net_have="$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{.IPRange}} {{.Gateway}}{{end}} {{index .Options "com.docker.network.bridge.name"}}' "$DOCKER_NETWORK")"
	[ "$net_have" = "$net_want" ] || die "Docker network $DOCKER_NETWORK exists as '$net_have', not '$net_want'"
	net_new=0
else
	[ -z "$(ip -4 route show root "$SUBNET")" ] || die "$SUBNET is in use on this host: $(ip -4 route show root "$SUBNET" | head -n 1)"
	covering="$(ip -4 route show match "$SUBNET" | grep -v '^default' || true)"
	[ -z "$covering" ] || die "a route covers $SUBNET: $covering"
	! ip link show "$BRIDGE" >/dev/null 2>&1 || die "an interface $BRIDGE exists already"
	net_new=1
fi
# An existing policy of ours may be incomplete (a run interrupted while making it) but must hold no rule of
# anyone else's: what is missing is added below.
policy_exists() { firewall-cmd --permanent --get-policies | tr ' ' '\n' | grep -qx "$1"; }
for p in "$POLICY_HOST" "$POLICY_DOCKER" "$POLICY_LAN"; do
	policy_exists "$p" || continue
	foreign="$(firewall-cmd --permanent --policy="$p" --list-rich-rules | while IFS= read -r r; do
		[ -z "$r" ] || policy_rules "$p" | grep -qxF "$r" || printf '%s; ' "$r"
	done)"
	[ -z "$foreign" ] || die "firewalld policy $p holds rules that are not Flow's: $foreign remove them by hand"
done

if [ "$DRY_RUN" = 0 ]; then
	mkdir -p "$STATE_DIR"
	chmod 0700 "$STATE_DIR"
	RUN_DIR="$STATE_DIR/run-$(date -u +%Y%m%dT%H%M%SZ)"
	snapshot "$RUN_DIR/before"
	if [ ! -d "$BASELINE" ]; then
		cp -R "$RUN_DIR/before" "$BASELINE"
		log "baseline recorded: $BASELINE/rules ($(wc -l <"$BASELINE/rules" | tr -d ' ') lines)"
	fi
fi

# 1. Docker network.
if [ "$net_new" = 1 ]; then
	log "creating Docker network $DOCKER_NETWORK ($SUBNET on $BRIDGE, Docker allocates from $DOCKER_RANGE)"
	run docker network create --driver bridge --subnet "$SUBNET" --ip-range "$DOCKER_RANGE" --gateway "$GATEWAY" \
		-o com.docker.network.bridge.name="$BRIDGE" "$DOCKER_NETWORK"
	record "docker-network $DOCKER_NETWORK"
fi

# 2. firewalld policies, permanent (survive reloads, restarts and Docker restarts; measured). Each is recorded
# before it is made and completed piece by piece, so an interrupted run leaves nothing the next run or the
# teardown does not know: flow-sb-host (range -> host; IPv6 from zone docker), flow-sb-docker (range -> other
# Docker bridges), flow-sb-lan (range -> private, link-local and shared networks behind the host, which the host
# would forward; the internet stays reachable).
reload=0
policy() {
	name="$1"
	egress="$(policy_egress "$name")"
	record "policy $name"
	if ! policy_exists "$name"; then
		log "creating firewalld policy $name (zone docker -> $egress)"
		run firewall-cmd -q --permanent --new-policy="$name"
		reload=1
		if [ "$DRY_RUN" = 1 ]; then
			run firewall-cmd -q --permanent --policy="$name" --set-priority=-10
			run firewall-cmd -q --permanent --policy="$name" --add-ingress-zone=docker
			run firewall-cmd -q --permanent --policy="$name" --add-egress-zone="$egress"
			policy_rules "$name" | while IFS= read -r rule; do
				run firewall-cmd -q --permanent --policy="$name" --add-rich-rule="$rule"
			done
			return 0
		fi
	fi
	if [ "$(firewall-cmd --permanent --policy="$name" --get-priority)" != -10 ]; then
		run firewall-cmd -q --permanent --policy="$name" --set-priority=-10
		reload=1
	fi
	if ! firewall-cmd -q --permanent --policy="$name" --query-ingress-zone=docker; then
		run firewall-cmd -q --permanent --policy="$name" --add-ingress-zone=docker
		reload=1
	fi
	if ! firewall-cmd -q --permanent --policy="$name" --query-egress-zone="$egress"; then
		run firewall-cmd -q --permanent --policy="$name" --add-egress-zone="$egress"
		reload=1
	fi
	missing="$(policy_rules "$name" | while IFS= read -r rule; do
		firewall-cmd -q --permanent --policy="$name" --query-rich-rule="$rule" || printf '%s\n' "$rule"
	done)"
	if [ -n "$missing" ]; then
		log "completing firewalld policy $name"
		printf '%s\n' "$missing" | while IFS= read -r rule; do
			run firewall-cmd -q --permanent --policy="$name" --add-rich-rule="$rule"
		done
		reload=1
	fi
}
policy "$POLICY_HOST"
policy "$POLICY_DOCKER"
policy "$POLICY_LAN"
if [ "$reload" = 1 ]; then
	log "reloading firewalld (Docker re-applies its rules)"
	run firewall-cmd -q --reload
fi

# 3. The DOCKER-USER rule and its unit.
# No ExecStop: stopping Docker must not take the rule away (Docker keeps DOCKER-USER, and starts containers before
# its unit is active); host-teardown.sh deletes it. Before incus.service: sandboxes autostarted at boot find it.
unit_text="[Unit]
Description=Flow: sandboxes may not reach other containers' published ports (DOCKER-USER)
After=docker.service
PartOf=docker.service
Before=incus.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'iptables -C DOCKER-USER $RULE_SPEC 2>/dev/null || iptables -I DOCKER-USER $RULE_SPEC'

[Install]
WantedBy=docker.service"
if put_file "$RULE_UNIT_FILE" 0644 "$unit_text"; then
	record "unit $RULE_UNIT"
	run systemctl daemon-reload
	run systemctl restart "$RULE_UNIT"
fi
if ! systemctl is-enabled --quiet "$RULE_UNIT" 2>/dev/null || ! systemctl is-active --quiet "$RULE_UNIT"; then
	log "enabling $RULE_UNIT"
	run systemctl enable --now --quiet "$RULE_UNIT"
fi

# 4. Incus, containers only, no managed network (no dnsmasq).
if ! command -v incus >/dev/null 2>&1; then
	log "installing incus-base and incus-client"
	pkgs="incus-base incus-client openssl"
	[ "$POOL_DRIVER" = btrfs ] && pkgs="$pkgs btrfs-progs"
	run apt-get update -qq
	# shellcheck disable=SC2086 # the package list is meant to split
	run env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $pkgs
	record "package incus"
fi

# 5. Incus after Docker's bridge: wait up to 120 s for it, then start anyway (Flow reports the missing bridge).
# $$ is systemd's escape for a literal $ (it substitutes $NAME itself).
dropin_text="[Unit]
After=docker.service
Wants=docker.service

[Service]
ExecStartPre=-/bin/sh -c 'i=0; while [ \$\$i -lt 120 ]; do ip link show $BRIDGE >/dev/null 2>&1 && exit 0; i=\$\$((i+1)); sleep 1; done; echo \"$BRIDGE missing after 120s\" >&2; exit 1'"
if put_file "$DROPIN" 0644 "$dropin_text"; then
	record "dropin $DROPIN"
	run systemctl daemon-reload
fi
run systemctl enable --now --quiet incus.socket

# 6. Storage pool, API listener, project, certificate.
if [ "$DRY_RUN" = 1 ] && ! command -v incus >/dev/null 2>&1; then
	log "dry run: Incus is not installed yet, its steps follow the install"
else
	if ! incus storage show default </dev/null >/dev/null 2>&1; then
		case "$POOL_DRIVER" in
		btrfs) run incus storage create default btrfs size="$POOL_SIZE" </dev/null ;;
		dir) run incus storage create default dir </dev/null ;;
		*) die "POOL_DRIVER must be btrfs or dir" ;;
		esac
		record "pool default"
	fi
	have="$(incus config get core.https_address </dev/null)"
	if [ "$have" != "$INCUS_LISTEN" ]; then
		log "Incus API on $INCUS_LISTEN (was '${have}')"
		# Only the first value seen is kept: the one the teardown restores.
		grep -q '^https_address[= ]' "$MANIFEST" 2>/dev/null || record "https_address=$have"
		run incus config set core.https_address "$INCUS_LISTEN" </dev/null
	fi
	if ! incus project show "$PROJECT" </dev/null >/dev/null 2>&1; then
		log "creating project $PROJECT"
		run incus project create "$PROJECT" -c features.images=false -c features.profiles=true \
			-c features.storage.volumes=true -c features.networks=false -c restricted=true </dev/null
		record "project $PROJECT"
	fi
	run incus project set "$PROJECT" restricted.containers.nesting=allow restricted.snapshots=allow \
		restricted.devices.nic=allow </dev/null
	if [ "${INCUS_PROJECT_MEMORY:-0}" = 1 ]; then
		run incus project set "$PROJECT" limits.memory="${MEMORY_TOTAL_MIB}MiB" </dev/null
	fi
	if ! incus profile device get default root pool --project "$PROJECT" </dev/null >/dev/null 2>&1; then
		run incus profile device add default root disk path=/ pool=default --project "$PROJECT" </dev/null
	fi
	if [ ! -f "$CERT_DIR/client.crt" ]; then
		log "generating Flow's client certificate in $CERT_DIR"
		record "cert $CERT_DIR"
		run mkdir -p "$CERT_DIR"
		run chmod 0700 "$CERT_DIR"
		run openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:secp384r1 -nodes -days 3650 \
			-keyout "$CERT_DIR/client.key" -out "$CERT_DIR/client.crt" -subj /CN=flow-backend 2>/dev/null
	fi
	# The trust entry must be this certificate: after CERT_DIR changed, the old entry names another one.
	have_fp="$(trusted_fp)"
	want_fp=""
	[ -f "$CERT_DIR/client.crt" ] && want_fp="$(cert_fp "$CERT_DIR/client.crt")"
	if [ -z "$have_fp" ] || [ -z "$want_fp" ] || [ "${want_fp#"$have_fp"}" = "$want_fp" ]; then
		if [ -n "$have_fp" ]; then
			log "the trusted flow-backend certificate ($have_fp) is not $CERT_DIR/client.crt: replacing it"
			run incus config trust remove "$have_fp" </dev/null
		fi
		log "trusting $CERT_DIR/client.crt for project $PROJECT only"
		record "trust flow-backend"
		run incus config trust add-certificate "$CERT_DIR/client.crt" --restricted --projects "$PROJECT" \
			--name flow-backend </dev/null
	fi
	if [ "$DRY_RUN" = 0 ]; then
		cp /var/lib/incus/server.crt "$CERT_DIR/server.crt"
		chmod 0600 "$CERT_DIR"/*
	fi
fi

[ "$DRY_RUN" = 1 ] && {
	log "dry run: nothing changed"
	exit 0
}

# Verify.
wait_ban_first || true
snapshot "$RUN_DIR/after"
compare "$RUN_DIR/before" "$RUN_DIR/after" setup
firewall-cmd -q --zone=docker --query-interface="$BRIDGE" || fail "$BRIDGE is not in firewalld zone docker"
for p in "$POLICY_HOST" "$POLICY_DOCKER" "$POLICY_LAN"; do
	firewall-cmd --get-policies | tr ' ' '\n' | grep -qx "$p" || fail "policy $p is not active"
	policy_rules "$p" | while IFS= read -r rule; do
		firewall-cmd -q --policy="$p" --query-rich-rule="$rule" || echo "$p: $rule"
	done | grep . && fail "policy $p lacks the rules above"
done
want_fp="$(cert_fp "$CERT_DIR/client.crt")"
have_fp="$(trusted_fp)"
[ -n "$have_fp" ] && [ "${want_fp#"$have_fp"}" != "$want_fp" ] ||
	fail "the trusted flow-backend certificate (${have_fp:-none}) is not $CERT_DIR/client.crt (${want_fp%"${want_fp#????????????}"})"
# shellcheck disable=SC2086 # RULE_SPEC is a list of arguments
iptables -C DOCKER-USER $RULE_SPEC 2>/dev/null || fail "the DOCKER-USER rule is missing"
[ "$(iptables -S DOCKER-USER 2>/dev/null | grep -cF -- "-s $RANGE -i $BRIDGE")" = 1 ] || fail "the DOCKER-USER rule is not there exactly once"
systemctl show -p After incus.service | grep -qw docker.service || fail "incus.service is not ordered after docker.service"
user_first || fail "FORWARD jumps to DOCKER-FORWARD before DOCKER-USER: the DOCKER-USER rule never fires (restart Docker)"
if [ "$FAILED" = 1 ]; then
	log "verification failed; snapshots in $RUN_DIR"
	exit 1
fi
log "done: Docker network $DOCKER_NETWORK ($BRIDGE), policies, DOCKER-USER rule, project $PROJECT, API $INCUS_LISTEN, certificates in $CERT_DIR; snapshots in $RUN_DIR"
