#!/bin/sh
# Prepares a systemd host that already runs Docker behind firewalld for Flow's sandboxes, without touching anything
# it did not create. Run by the owner by hand, as root, never by Flow:
#
#   sh deploy/host-setup.sh --dry-run    prints what it would change, changes nothing
#   sh deploy/host-setup.sh              applies, then verifies
#   sh deploy/host-teardown.sh           the reverse (see there)
#
# What it adds (docs/architecture.md, "Sandbox network"; measured in the lab, flow-net-findings):
#   1. Docker network $DOCKER_NETWORK on bridge $BRIDGE ($SUBNET, Docker allocating only from $DOCKER_RANGE): Docker
#      provides forwarding, masquerading, the firewalld zone `docker` and the separation from other Docker networks.
#   2. firewalld policies flow-sb-host (the sandbox range may not reach the host), flow-sb-docker (nor other
#      Docker bridges, including a routed hairpin back onto $BRIDGE),
#      flow-sb-lan (nor $LAN_DESTINATIONS behind the host: LAN, cloud metadata; the internet stays reachable) and
#      flow-sb-incus (no container but Flow's, $CONTAINER_ADDRESS, reaches the Incus API at $INCUS_LISTEN: every
#      Docker bridge is in zone `docker`, and a client certificate alone was the only bar before). Matched by source
#      address: a container on $DOCKER_NETWORK with raw sockets could forge it or take the address over on the
#      bridge (not measured); Incus' client certificate stays the other bar.
#   3. One DOCKER-USER rule: sandboxes may not reach other containers' published ports (firewalld accepts DNAT'd
#      traffic before any policy). Persistence: the systemd unit flow-sandbox-published.service, bound to
#      docker.service (WantedBy + PartOf + After) and ordered before incus.service, inserts it once Docker has made
#      DOCKER-USER (at boot, before Incus autostarts sandboxes) and checks it again whenever Docker restarts.
#      Chosen as the least invasive: it owns exactly this one rule; the teardown deletes it.
#      Rejected: iptables-persistent (saves and restores the whole ruleset, fighting Docker and firewalld over
#      theirs), firewalld direct rules (deprecated; applied when firewalld starts, before Docker made the chain).
#      Docker itself never flushes DOCKER-USER (measured: restart, firewalld reload, network re-create).
#   3b. No IPv6 on $BRIDGE (host-lib.sh, IPV6_NFT): the nftables table `bridge flow_sandbox` drops every IPv6
#      frame entering it, without br_netfilter and before any route; the host's side of the bridge gets
#      disable_ipv6=1. Kept by the unit flow-sandbox-ipv6.service, bound to docker.service like the rule's unit.
#      Other Docker networks keep their IPv6 untouched (firewalld rejects no IPv6: its rules cannot name a bridge).
#   4. Incus (containers only; installed with apt-get as incus-base when missing, before any other change, elsewhere
#      install it first) without any managed network; the storage pool $BACKEND_INCUS_POOL (when missing); the API
#      on $INCUS_LISTEN (default: $BRIDGE's gateway, which Flow's container reaches and sandboxes do not). The
#      project's default profile gets its root disk on that pool; a root disk on another pool is refused, never
#      re-pointed.
#      The pool is a loop file of $POOL_SIZE, or with POOL_PATH an existing btrfs filesystem the owner made and
#      mounted himself (docs/deploy.md, "Sizing a dedicated host"), handed to Incus by path (`source=<path>`, as
#      Incus' storage pool how-to documents it). This setup never formats, wipes or mounts anything. Refused unless
#      the path exists, is a directory on btrfs, is not on a non-btrfs root filesystem, and is empty (or holds what
#      an earlier run with this manifest started); a pool made on another path is refused, never moved.
#   5. systemd drop-in: incus.service starts after Docker has made $BRIDGE (else autostart fails three times and
#      leaves the sandboxes stopped; measured), waiting at most 120 s. The unit flow-incus-listen.service (bound to
#      docker.service like the rule's unit, after incus.service) makes Incus listen on $INCUS_LISTEN once the
#      address exists: Incus started before it (Docker slow at boot) tries once more after 30 s and then never.
#   6. The restricted Incus project $PROJECT and Flow's client certificate, trusted for that project only. The
#      project needs restricted.devices.nic=allow: a restricted project refuses NICs on an unmanaged bridge
#      ("Only managed network devices are allowed", measured), and Incus has nothing narrower. $CERT_DIR (client
#      certificate and key, Incus' server certificate) belongs to $CERT_OWNER, the uid Flow's container runs as and
#      reads them with through a read-only mount; set again on every run.
#   Flow's container (not made here) joins $DOCKER_NETWORK at $CONTAINER_ADDRESS (compose.yaml) to dial flowd and
#   Incus, and must also be on the reverse proxy's network, where the proxy reaches it (Traefik: label
#   traefik.docker.network=<the proxy's network>). Flow refuses every browser request arriving over $DOCKER_NETWORK,
#   which sandboxes reach at L2; with no other network it answers nothing and logs that at start. A Flow container
#   made before flow-sb-incus existed has another address: `docker compose up -d` makes it again.
#
# Required, else it refuses before changing anything (preflight in host-lib.sh): firewalld on nftables with
# Docker's zone `docker`, Docker on iptables-nft as docker.service, FORWARD's policy DROP with DOCKER-USER before
# DOCKER-FORWARD, Incus (if installed) as incus.service without a managed network, a free $SUBNET; Incus installed
# or installable with apt-get (a candidate for each package), openssl and sha256sum, btrfs tooling for a new btrfs
# pool.
# Detected: a ban chain, a third party's chain that keeps its jump first in FORWARD (BAN_CHAIN, default MAILCOW).
# Present: it must be first now, the setup waits up to $BAN_WAIT s after applying for its owner to put it first
# again, its churn is shown in the difference, and the manifest records it, so later runs and the teardown require
# it. Absent: those steps are skipped and the output says so; refused instead when BAN_CHAIN was set explicitly
# (a typo), recorded, or BAN_REQUIRE=1 (a host that should have it).
#
# Safety: records a baseline of the whole ruleset and firewalld's zones and policies before the first firewall
# change ($STATE_DIR/baseline, written once) and a snapshot before each run (after an Incus install, to keep the
# window short); after applying (and the ban chain's wait) it prints the difference and fails on anything Flow's
# changes do not explain, except what OTHER_CHURN names (shown, not counted; default none). It writes a manifest
# of what it creates, each entry before the thing is made, so the teardown removes exactly that, also after an
# interrupted run. A re-run completes what is missing, the DOCKER-USER rule included.
#
# Settings (environment): BACKEND_SANDBOX_BRIDGE, _SUBNET, _RANGE, _GATEWAY, BACKEND_INCUS_POOL
# and BACKEND_INCUS_PROJECT as Flow reads them (PROJECT overrides the latter); DOCKER_NETWORK, DOCKER_RANGE,
# BAN_CHAIN (empty: none), BAN_REQUIRE, BAN_WAIT, INCUS_LISTEN, CONTAINER_ADDRESS, INCUS_DIR, CERT_DIR, CERT_OWNER,
# POOL_DRIVER (btrfs, or dir where loop devices are refused), POOL_SIZE, POOL_PATH, STATE_DIR, OTHER_CHURN.
# Defaults in host-lib.sh.
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
manifest_settings
ban_from_manifest
preflight
case "$POOL_DRIVER" in
btrfs | dir) ;;
*) die "POOL_DRIVER must be btrfs or dir" ;;
esac
# Incus, or what installs it, and the tools the later steps need: checked before anything changes.
pkgs="incus-base incus-client openssl"
[ "$POOL_DRIVER" = btrfs ] && pkgs="$pkgs btrfs-progs"
# apt_candidate PKG: apt's lists hold an installable version (apt-cache prints "Candidate: (none)" for a package it
# knows without one, nothing for one it does not know).
apt_candidate() {
	cand="$(apt-cache policy "$1" 2>/dev/null | awk '$1 == "Candidate:" { print $2 }')"
	[ -n "$cand" ] && [ "$cand" != "(none)" ]
}
if command -v incus >/dev/null 2>&1; then
	for c in openssl sha256sum; do
		command -v "$c" >/dev/null 2>&1 || die "$c not found (Flow's client certificate needs it)"
	done
	if incus info </dev/null >/dev/null 2>&1; then
		[ -f "$INCUS_DIR/server.crt" ] || die "no $INCUS_DIR/server.crt: set INCUS_DIR to Incus' directory"
		if [ "$POOL_DRIVER" = btrfs ] && ! incus storage show "$POOL" </dev/null >/dev/null 2>&1; then
			command -v btrfs >/dev/null 2>&1 || die "btrfs not found: install btrfs-progs for the pool $POOL, or POOL_DRIVER=dir"
		fi
		# Flow creates sandboxes from the project's default profile: its root disk must be on the configured pool.
		# Never re-pointed here: sandboxes made since keep their disks on the old pool.
		root_pool="$(incus profile device get default root pool --project "$PROJECT" </dev/null 2>/dev/null || true)"
		[ -z "$root_pool" ] || [ "$root_pool" = "$POOL" ] ||
			die "project $PROJECT's default profile puts root disks on pool $root_pool, not $POOL (BACKEND_INCUS_POOL): set BACKEND_INCUS_POOL=$root_pool, or move the project's sandboxes and the profile's root device by hand"
	fi
else
	for c in apt-get apt-cache; do
		command -v "$c" >/dev/null 2>&1 ||
			die "Incus is not installed and there is no $c to install it: install Incus (no managed network) first"
	done
	for pkg in $pkgs; do
		apt_candidate "$pkg" ||
			die "package $pkg cannot be installed (no candidate in apt's lists): run apt-get update, or install Incus (no managed network) first"
	done
fi

# The pool on the owner's btrfs filesystem (POOL_PATH), checked before anything changes, in a dry run too. Nothing
# here or later formats, wipes or mounts: the filesystem and its mount are the owner's.
pool_path_check() {
	[ "$POOL_DRIVER" = btrfs ] || die "POOL_PATH needs POOL_DRIVER=btrfs"
	command -v findmnt >/dev/null 2>&1 || die "findmnt not found (POOL_PATH needs it)"
	# One spelling for the manifest and Incus' source.
	[ "$POOL_PATH" = / ] || POOL_PATH="${POOL_PATH%/}"
	case "$POOL_PATH" in
	/*) ;;
	*) die "POOL_PATH must be an absolute path: $POOL_PATH" ;;
	esac
	rec="$(pool_path_line "$POOL")"
	[ -z "$rec" ] || [ "$rec" = "$POOL_PATH" ] ||
		die "the setup made pool $POOL on $rec (manifest $MANIFEST), POOL_PATH says $POOL_PATH: a pool is never moved"
	[ -e "$POOL_PATH" ] || die "POOL_PATH $POOL_PATH does not exist (create and mount the btrfs filesystem first: docs/deploy.md, 'Sizing a dedicated host')"
	[ -d "$POOL_PATH" ] || die "POOL_PATH $POOL_PATH is not a directory"
	fstype="$(stat -f -c %T "$POOL_PATH")"
	[ "$fstype" = btrfs ] ||
		die "POOL_PATH $POOL_PATH is on $fstype, not btrfs (nothing mounted there? a device missing at boot: docs/deploy.md, 'Sizing a dedicated host')"
	# The filesystem the path lies on: not the root filesystem, unless that is btrfs itself.
	fs_root="$(findmnt -nro TARGET -T "$POOL_PATH")"
	if [ "$fs_root" = / ] && [ "$(findmnt -nro FSTYPE --mountpoint /)" != btrfs ]; then
		die "POOL_PATH $POOL_PATH lies on the root filesystem, which is not btrfs"
	fi
	if command -v incus >/dev/null 2>&1 && incus storage show "$POOL" </dev/null >/dev/null 2>&1; then
		src="$(incus storage get "$POOL" source </dev/null)"
		[ "$(incus storage show "$POOL" </dev/null | awk '$1 == "driver:" { print $2 }')" = btrfs ] && [ "$src" = "$POOL_PATH" ] ||
			die "pool $POOL exists elsewhere (source '$src'), not on POOL_PATH $POOL_PATH: choose another BACKEND_INCUS_POOL, or leave POOL_PATH unset for this pool"
		log "pool $POOL is on $POOL_PATH (btrfs, mounted at $fs_root): nothing to create"
		return 0
	fi
	# Empty, or what an earlier run with this manifest started there (Incus' own directories, a create cut short).
	if [ -n "$(find "$POOL_PATH" -mindepth 1 -maxdepth 1 -print -quit)" ] && [ "$rec" != "$POOL_PATH" ]; then
		die "POOL_PATH $POOL_PATH is not empty: $(find "$POOL_PATH" -mindepth 1 -maxdepth 1 | head -n 5 | tr '\n' ' ')"
	fi
	log "pool $POOL: on the btrfs filesystem at $POOL_PATH (mounted at $fs_root); nothing is formatted or mounted"
}
# size_bytes SIZE prints an Incus size (40GiB, 300GB, 512MiB, a plain byte count) in bytes; status 1 for another
# spelling.
size_bytes() {
	printf '%s\n' "$1" | awk '
		match($0, /^[0-9]+(\.[0-9]+)?/) {
			n = substr($0, 1, RLENGTH); u = substr($0, RLENGTH + 1)
			m["B"] = 1; m[""] = 1
			m["kB"] = 1e3; m["KB"] = 1e3; m["MB"] = 1e6; m["GB"] = 1e9; m["TB"] = 1e12
			m["KiB"] = 2^10; m["MiB"] = 2^20; m["GiB"] = 2^30; m["TiB"] = 2^40
			if (u in m) { printf "%.0f\n", n * m[u]; ok = 1 }
		}
		END { exit !ok }'
}
# loop_space_check: the loop file Incus makes for the pool ($INCUS_DIR/disks/<pool>.img) is sparse, so it takes
# its space only as sandboxes write; a POOL_SIZE larger than the free space of the filesystem it lands on would let
# the pool outgrow it later (sandboxes failing on a full root filesystem, the host with them). Refused before
# anything changes, in a dry run too.
loop_space_check() {
	want="$(size_bytes "$POOL_SIZE")" || die "POOL_SIZE $POOL_SIZE is not a size Incus reads (e.g. 300GiB)"
	dir="$INCUS_DIR/disks"
	while [ ! -d "$dir" ]; do dir="$(dirname "$dir")"; done
	free="$(df -Pk "$dir" | awk 'NR == 2 { printf "%.0f\n", $4 * 1024 }')"
	[ -n "$free" ] || die "cannot read the free space of $dir"
	awk -v w="$want" -v f="$free" 'BEGIN { exit !(w <= f) }' ||
		die "POOL_SIZE $POOL_SIZE is more than the $((free / 1073741824)) GiB free on the filesystem of $dir, where the pool's loop file goes: a smaller POOL_SIZE, or POOL_PATH (docs/deploy.md, 'Sizing a dedicated host')"
	log "pool $POOL: a sparse loop file of $POOL_SIZE under $INCUS_DIR/disks ($((free / 1073741824)) GiB free there now)"
}
if [ -n "$POOL_PATH" ]; then
	pool_path_check
elif [ -n "$(pool_path_line "$POOL")" ]; then
	die "the setup made pool $POOL on $(pool_path_line "$POOL") (manifest $MANIFEST): run with POOL_PATH=$(pool_path_line "$POOL")"
elif [ "$POOL_DRIVER" = btrfs ] && ! { command -v incus >/dev/null 2>&1 && incus storage show "$POOL" </dev/null >/dev/null 2>&1; }; then
	loop_space_check
fi

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
# own_stale POLICY RULE: a flow-sb-incus rule an earlier run made for another CONTAINER_ADDRESS or INCUS_LISTEN;
# policy() replaces it.
own_stale() {
	[ "$1" = "$POLICY_INCUS" ] && printf '%s\n' "$2" |
		grep -Eqx 'rule family="ipv4" source NOT address="[0-9.]+"( destination address="[0-9.]+")? port port="[0-9]+" protocol="tcp" reject'
}
for p in $POLICIES; do
	policy_exists "$p" || continue
	foreign="$(firewall-cmd --permanent --policy="$p" --list-rich-rules | while IFS= read -r r; do
		[ -z "$r" ] || policy_rules "$p" | grep -qxF "$r" || own_stale "$p" "$r" || printf '%s; ' "$r"
	done)"
	[ -z "$foreign" ] || die "firewalld policy $p holds rules that are not Flow's: $foreign remove them by hand"
done

# Everything is recorded in the manifest before it is made, so an interrupted run leaves nothing the teardown does
# not know (it skips what it finds missing).
if [ "$DRY_RUN" = 0 ]; then
	mkdir -p "$STATE_DIR"
	chmod 0700 "$STATE_DIR"
fi
# From now on the teardown and every later setup require this chain (ban_from_manifest).
[ "$BAN_SEEN" = 0 ] || record "ban-chain $BAN_CHAIN"

# Incus first, containers only, no managed network (no dnsmasq): the slowest step stays out of the window the
# verification compares (others' changes in it would count), and a failing install changes no firewall.
if ! command -v incus >/dev/null 2>&1; then
	log "installing $pkgs"
	record "package incus"
	run apt-get update -qq
	# shellcheck disable=SC2086 # the package list is meant to split
	run env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $pkgs
	if [ "$DRY_RUN" = 0 ]; then
		for c in incus openssl sha256sum; do
			command -v "$c" >/dev/null 2>&1 || die "$c not found after installing $pkgs"
		done
	fi
fi

if [ "$DRY_RUN" = 0 ]; then
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
	record "docker-network $DOCKER_NETWORK"
	run docker network create --driver bridge --subnet "$SUBNET" --ip-range "$DOCKER_RANGE" --gateway "$GATEWAY" \
		-o com.docker.network.bridge.name="$BRIDGE" "$DOCKER_NETWORK"
fi

# 2. firewalld policies, permanent (survive reloads, restarts and Docker restarts; measured). Each one this run
# makes is recorded before it is made (one found is completed, not recorded) and completed piece by piece, so an
# interrupted run leaves nothing the next run or the teardown does not know: flow-sb-host (range -> host),
# flow-sb-docker (range -> other Docker bridges), flow-sb-lan (range -> private, link-local and
# shared networks behind the host, which the host would forward; the internet stays reachable), flow-sb-incus (any container but Flow's -> the Incus API).
reload=0
policy() {
	name="$1"
	egress="$(policy_egress "$name")"
	# Recorded only when this run makes it (before it does), as the network and the project: a policy found that
	# this state directory never recorded was made by another setup (another STATE_DIR), and the teardown with this
	# manifest must leave it. One recorded by an earlier run here stays ours.
	if ! policy_exists "$name"; then
		record "policy $name"
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
	elif ! recorded "policy $name"; then
		log "firewalld policy $name exists, not made by a setup with $STATE_DIR: completed if needed, not recorded (the teardown leaves it)"
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
	stale="$(firewall-cmd --permanent --policy="$name" --list-rich-rules | while IFS= read -r rule; do
		[ -z "$rule" ] || policy_rules "$name" | grep -qxF "$rule" || printf '%s\n' "$rule"
	done)"
	if [ -n "$stale" ]; then
		log "removing firewalld policy $name's rules of earlier settings"
		printf '%s\n' "$stale" | while IFS= read -r rule; do
			run firewall-cmd -q --permanent --policy="$name" --remove-rich-rule="$rule"
		done
		reload=1
	fi
}
for p in $POLICIES; do
	policy "$p"
done
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
if put_file "$RULE_UNIT_FILE" 0644 "$unit_text" "unit $RULE_UNIT"; then
	run systemctl daemon-reload
	run systemctl restart "$RULE_UNIT"
fi
if ! systemctl is-enabled --quiet "$RULE_UNIT" 2>/dev/null || ! systemctl is-active --quiet "$RULE_UNIT"; then
	log "enabling $RULE_UNIT"
	run systemctl enable --now --quiet "$RULE_UNIT"
fi
# On every run: the unit (active, RemainAfterExit) inserts the rule only when it starts, so a rule removed while
# Docker runs (iptables -F, a ruleset restore) would otherwise stay missing until Docker restarts.
# shellcheck disable=SC2086 # RULE_SPEC is a list of arguments
if ! iptables -C DOCKER-USER $RULE_SPEC 2>/dev/null; then
	log "inserting the DOCKER-USER rule"
	run iptables -I DOCKER-USER $RULE_SPEC
fi

# 3b. No IPv6 on the bridge, and its unit (header). The table needs no bridge; the sysctl waits for it. $$ is
# systemd's escape for a literal $.
if ! nft list table bridge "$IPV6_TABLE" >/dev/null 2>&1; then
	record "nft-table $IPV6_TABLE"
elif ! recorded "nft-table $IPV6_TABLE"; then
	log "nftables table bridge $IPV6_TABLE exists, not made by a setup with $STATE_DIR: completed if needed, not recorded"
fi
sysctl_file="$(ipv6_sysctl)"
# Docker's value was 0 (the teardown restores it while the bridge stays); recorded before the unit changes it.
if [ ! -e "$sysctl_file" ] || [ "$(cat "$sysctl_file")" != 1 ]; then
	record "ipv6-off $BRIDGE"
fi
unit6_text="[Unit]
Description=Flow: no IPv6 on $BRIDGE (nftables table bridge $IPV6_TABLE, disable_ipv6)
After=docker.service
PartOf=docker.service
Before=incus.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=nft '$IPV6_NFT'
ExecStart=/bin/sh -c 'i=0; until [ -e $sysctl_file ]; do [ \$\$i -lt 120 ] || { echo \"$BRIDGE missing after 120s\" >&2; exit 1; }; i=\$\$((i+1)); sleep 1; done; echo 1 >$sysctl_file'

[Install]
WantedBy=docker.service"
if put_file "$IPV6_UNIT_FILE" 0644 "$unit6_text" "unit $IPV6_UNIT"; then
	run systemctl daemon-reload
	run systemctl restart "$IPV6_UNIT"
fi
if ! systemctl is-enabled --quiet "$IPV6_UNIT" 2>/dev/null || ! systemctl is-active --quiet "$IPV6_UNIT"; then
	log "enabling $IPV6_UNIT"
	run systemctl enable --now --quiet "$IPV6_UNIT"
fi
# On every run, as for the DOCKER-USER rule: a ruleset flushed or a bridge re-created since the unit started.
if ! ipv6_table_ok; then
	log "loading nftables table bridge $IPV6_TABLE (IPv6 dropped on $BRIDGE)"
	run nft "$IPV6_NFT"
fi
if [ -e "$sysctl_file" ] && [ "$(cat "$sysctl_file")" != 1 ]; then
	log "switching IPv6 off on $BRIDGE"
	run sh -c 'echo 1 >"$1"' sh "$sysctl_file"
fi

# 5. Incus after Docker's bridge: wait up to 120 s for it, then start anyway (Flow reports the missing bridge).
# $$ is systemd's escape for a literal $ (it substitutes $NAME itself).
dropin_text="[Unit]
After=docker.service
Wants=docker.service

[Service]
ExecStartPre=-/bin/sh -c 'i=0; while [ \$\$i -lt 120 ]; do ip link show $BRIDGE >/dev/null 2>&1 && exit 0; i=\$\$((i+1)); sleep 1; done; echo \"$BRIDGE missing after 120s\" >&2; exit 1'"
if put_file "$DROPIN" 0644 "$dropin_text" "dropin $DROPIN"; then
	run systemctl daemon-reload
fi
run systemctl enable --now --quiet incus.socket

# 6. Storage pool, API listener, project, certificate.
if [ "$DRY_RUN" = 1 ] && ! command -v incus >/dev/null 2>&1; then
	log "dry run: Incus is not installed yet, its steps follow the install"
else
	# The settings this setup works with: the teardown refuses others.
	record "uses project $PROJECT"
	record "uses pool $POOL"
	if ! incus storage show "$POOL" </dev/null >/dev/null 2>&1; then
		record "pool $POOL"
		if [ -n "$POOL_PATH" ]; then
			# The owner's btrfs filesystem by path (checked above): recorded first, so a re-run after an interrupted
			# create takes what Incus started there as its own; every later run needs the same path.
			record "pool-path $POOL $POOL_PATH"
			run incus storage create "$POOL" btrfs source="$POOL_PATH" </dev/null
		elif [ "$POOL_DRIVER" = btrfs ]; then
			run incus storage create "$POOL" btrfs size="$POOL_SIZE" </dev/null
		else
			run incus storage create "$POOL" dir </dev/null
		fi
	fi
	have="$(incus config get core.https_address </dev/null)"
	if [ "$have" != "$INCUS_LISTEN" ]; then
		log "Incus API on $INCUS_LISTEN (was '${have}')"
		# Only the first value seen is kept: the one the teardown restores.
		grep -q '^https_address[= ]' "$MANIFEST" 2>/dev/null || record "https_address=$have"
		run incus config set core.https_address "$INCUS_LISTEN" </dev/null
	fi
	# 5b. The listener once the address exists (see the header). Only for a specific address: a wildcard always binds.
	# It leaves alone a listen address someone set since ("exit 0"); $$ is systemd's escape for a literal $.
	lip="$(listen_ip)"
	if [ -n "$lip" ]; then
		lport="${INCUS_LISTEN##*:}"
		listen_text="[Unit]
Description=Flow: Incus API on $INCUS_LISTEN once Docker has made $BRIDGE
After=docker.service incus.service
PartOf=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'i=0; until ip -4 -o addr show to $lip/32 | grep -q .; do [ \$\$i -lt 120 ] || { echo \"$lip missing after 120s\" >&2; exit 1; }; i=\$\$((i+1)); sleep 1; done; [ \"\$\$(incus config get core.https_address </dev/null)\" = $INCUS_LISTEN ] || exit 0; ss -Hltn \"src $lip and sport = :$lport\" | grep -q . && exit 0; echo \"Incus does not listen on $INCUS_LISTEN: setting it again\"; incus config unset core.https_address </dev/null && incus config set core.https_address $INCUS_LISTEN </dev/null'

[Install]
WantedBy=docker.service"
		if put_file "$LISTEN_UNIT_FILE" 0644 "$listen_text" "unit $LISTEN_UNIT"; then
			run systemctl daemon-reload
			run systemctl restart "$LISTEN_UNIT"
		fi
		if ! systemctl is-enabled --quiet "$LISTEN_UNIT" 2>/dev/null || ! systemctl is-active --quiet "$LISTEN_UNIT"; then
			log "enabling $LISTEN_UNIT"
			run systemctl enable --now --quiet "$LISTEN_UNIT"
		fi
	fi
	if ! incus project show "$PROJECT" </dev/null >/dev/null 2>&1; then
		log "creating project $PROJECT"
		record "project $PROJECT"
		run incus project create "$PROJECT" -c features.images=false -c features.profiles=true \
			-c features.storage.volumes=true -c features.networks=false -c restricted=true </dev/null
	fi
	run incus project set "$PROJECT" restricted.containers.nesting=allow restricted.snapshots=allow \
		restricted.devices.nic=allow </dev/null
	# A root device on another pool was refused above. Recorded also in a project this setup did not make: the
	# teardown removes it, or the pool it made could not be deleted.
	if ! incus profile device get default root pool --project "$PROJECT" </dev/null >/dev/null 2>&1; then
		record "root-device $PROJECT $POOL"
		run incus profile device add default root disk path=/ pool="$POOL" --project "$PROJECT" </dev/null
	fi
	if [ ! -f "$CERT_DIR/client.crt" ]; then
		log "generating Flow's client certificate in $CERT_DIR"
		record "cert $CERT_DIR"
		run mkdir -p "$CERT_DIR"
		run chmod 0700 "$CERT_DIR"
		run openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:secp384r1 -nodes -days 3650 \
			-keyout "$CERT_DIR/client.key" -out "$CERT_DIR/client.crt" -subj /CN=flow-backend
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
		[ -f "$INCUS_DIR/server.crt" ] || die "no $INCUS_DIR/server.crt: set INCUS_DIR to Incus' directory"
		cp "$INCUS_DIR/server.crt" "$CERT_DIR/server.crt"
		chmod 0600 "$CERT_DIR"/*
		# Flow's container reads them as CERT_OWNER (mounted read-only); root keeps access. Also on a re-run over
		# certificates an earlier setup left to root.
		chown "$CERT_OWNER" "$CERT_DIR" "$CERT_DIR/client.crt" "$CERT_DIR/client.key" "$CERT_DIR/server.crt"
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
for p in $POLICIES; do
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
ipv6_table_ok || fail "nftables table bridge $IPV6_TABLE does not drop IPv6 on $BRIDGE"
[ "$(nft list chain bridge "$IPV6_TABLE" ipv6 2>/dev/null | grep -c 'ether type ip6 drop')" = 1 ] || fail "table bridge $IPV6_TABLE does not hold its rule exactly once"
[ "$(cat "$sysctl_file" 2>/dev/null)" = 1 ] || fail "IPv6 is not off on $BRIDGE ($sysctl_file)"
systemctl is-active --quiet "$IPV6_UNIT" || fail "$IPV6_UNIT is not active"
systemctl show -p After incus.service | grep -qw docker.service || fail "incus.service is not ordered after docker.service"
if [ -n "$(listen_ip)" ]; then
	systemctl is-active --quiet "$LISTEN_UNIT" || fail "$LISTEN_UNIT is not active"
	ss -Hltn "src $(listen_ip) and sport = :${INCUS_LISTEN##*:}" | grep -q . || fail "Incus does not listen on $INCUS_LISTEN"
fi
user_first || fail "FORWARD jumps to DOCKER-FORWARD before DOCKER-USER: the DOCKER-USER rule never fires (restart Docker)"
if [ "$FAILED" = 1 ]; then
	log "verification failed; snapshots in $RUN_DIR"
	exit 1
fi
log "done: Docker network $DOCKER_NETWORK ($BRIDGE), policies, DOCKER-USER rule, no IPv6 on $BRIDGE, project $PROJECT, API $INCUS_LISTEN (for $CONTAINER_ADDRESS only), certificates in $CERT_DIR; snapshots in $RUN_DIR"
