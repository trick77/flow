# shellcheck shell=sh disable=SC2034 # the settings are used by the scripts that source this file
# Shared by host-setup.sh and host-teardown.sh: settings, dry run, preflight checks, firewall snapshots and the
# check that a diff holds only what Flow's setup explains. Sourced, never run.

# Flow's own settings (backend/internal/config/network.go), same names and defaults: Flow and the host must agree.
BRIDGE="${BACKEND_SANDBOX_BRIDGE:-flowbr0}"
SUBNET="${BACKEND_SANDBOX_SUBNET:-172.30.0.0/16}"
RANGE="${BACKEND_SANDBOX_RANGE:-172.30.128.0/17}"
GATEWAY="${BACKEND_SANDBOX_GATEWAY:-$(printf '%s\n' "$SUBNET" | awk -F'[./]' '{print $1"."$2"."$3"."$4+1}')}"
PROJECT="${PROJECT:-${BACKEND_INCUS_PROJECT:-flow}}"
POOL="${BACKEND_INCUS_POOL:-default}"
# Host-side settings.
DOCKER_NETWORK="${DOCKER_NETWORK:-flow}"
DOCKER_RANGE="${DOCKER_RANGE:-172.30.0.0/24}" # Docker's own allocations (Flow's container); the rest is RANGE's
# A third party's iptables chain that keeps its jump first in FORWARD (mailcow's netfilter container: MAILCOW). If it
# exists, it must be first before and after every change. If the default does not exist, its checks are skipped and
# the output says so; a name set explicitly, or recorded by an earlier setup, must exist (a typo or a chain that is
# being restarted never switches the checks off). BAN_REQUIRE=1: the default is required too. Empty BAN_CHAIN: none.
BAN_CHAIN_SET="${BAN_CHAIN+1}"
BAN_CHAIN="${BAN_CHAIN-MAILCOW}"
BAN_REQUIRE="${BAN_REQUIRE:-0}"
BAN_WAIT="${BAN_WAIT:-60}" # seconds its owner gets to put it first again
# An extended regular expression for snapshot lines that others change at any time (fail2ban's or CrowdSec's bans,
# containers starting): a difference it matches is shown, never counted. Never excuses a line of Flow's own. Empty
# (the default): every difference outside Flow's and the ban chain's counts. Example: 'f2b-|crowdsec'.
OTHER_CHURN="${OTHER_CHURN:-}"
# The Incus API on the sandbox network's gateway: Flow's container reaches it there (loopback is the container's
# own); sandboxes (policy flow-sb-host) and every other container (flow-sb-incus: only CONTAINER_ADDRESS passes) do
# not. An address missing when Incus starts is bound once more after 30 s, then never (measured, Incus 6.0.4: "Still
# unable to listen on https socket"; setting the same value again does not bind either, unset and set does): the
# unit flow-incus-listen.service does that once Docker has made the bridge.
INCUS_LISTEN="${INCUS_LISTEN:-$GATEWAY:8443}"
# Flow's container's static address on DOCKER_NETWORK (compose.yaml, the same name): the one container that reaches
# the Incus API. Outside DOCKER_RANGE (Docker could hand it to another container) and RANGE. Default: the second
# address after DOCKER_RANGE, 172.30.1.2.
CONTAINER_ADDRESS="${CONTAINER_ADDRESS:-$(awk -v r="$DOCKER_RANGE" '
	function ip(s,  a) { split(s, a, "."); return ((a[1]*256 + a[2])*256 + a[3])*256 + a[4] }
	BEGIN { split(r, p, "/"); n = int(ip(p[1]) / 2^(32 - p[2])) * 2^(32 - p[2]) + 2^(32 - p[2]) + 2
		printf "%d.%d.%d.%d\n", int(n / 16777216) % 256, int(n / 65536) % 256, int(n / 256) % 256, n % 256 }')}"
INCUS_DIR="${INCUS_DIR:-/var/lib/incus}" # Incus' own variable: where its server.crt lies
CERT_DIR="${CERT_DIR:-/etc/flow-host/incus}"
# Owner of CERT_DIR and its files: the uid:gid Flow's container runs as (compose.yaml `user`), never 1000.
CERT_OWNER="${CERT_OWNER:-65532:65532}"
POOL_DRIVER="${POOL_DRIVER:-btrfs}"
POOL_SIZE="${POOL_SIZE:-40GiB}" # the loop file's size; unused with POOL_PATH
# The pool on a btrfs filesystem the owner made and mounted himself (docs/deploy.md, "Sizing a dedicated host"):
# its mount point or a directory on it, handed to Incus as `source=`. The scripts never format, wipe or mount
# anything. Empty (the default): the loop file.
POOL_PATH="${POOL_PATH:-}"
STATE_DIR="${STATE_DIR:-/var/lib/flow-host/host-setup}"

# Destinations behind the host sandboxes may not reach: private networks, link-local (cloud metadata
# 169.254.169.254), shared address space. The host forwards them otherwise (measured: SYNs to each left the uplink).
LAN_DESTINATIONS="${LAN_DESTINATIONS:-10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 100.64.0.0/10}"

POLICY_HOST=flow-sb-host
POLICY_DOCKER=flow-sb-docker
POLICY_LAN=flow-sb-lan
POLICY_INCUS=flow-sb-incus
POLICIES="$POLICY_HOST $POLICY_DOCKER $POLICY_LAN $POLICY_INCUS"
RULE_UNIT=flow-sandbox-published.service
RULE_UNIT_FILE=/etc/systemd/system/$RULE_UNIT
IPV6_UNIT=flow-sandbox-ipv6.service
IPV6_UNIT_FILE=/etc/systemd/system/$IPV6_UNIT
IPV6_TABLE=flow_sandbox
LISTEN_UNIT=flow-incus-listen.service
LISTEN_UNIT_FILE=/etc/systemd/system/$LISTEN_UNIT
DROPIN=/etc/systemd/system/incus.service.d/flow-order.conf
MANIFEST=$STATE_DIR/manifest
BASELINE=$STATE_DIR/baseline

DRY_RUN=0
FAILED=0

log() { printf '%s %s: %s\n' "$(date -u +%H:%M:%S)" "$SCRIPT" "$*"; }
warn() { printf '%s %s: WARNING: %s\n' "$(date -u +%H:%M:%S)" "$SCRIPT" "$*" >&2; }
die() {
	printf '%s: refusing: %s\n' "$SCRIPT" "$*" >&2
	exit 1
}
fail() {
	printf '%s: FAILED: %s\n' "$SCRIPT" "$*" >&2
	FAILED=1
}

# run executes a command that changes the host, or prints it in a dry run.
run() {
	if [ "$DRY_RUN" = 1 ]; then
		printf 'would run:'
		printf ' %s' "$@"
		printf '\n'
	else
		"$@"
	fi
}

# reload_firewalld reloads firewalld, then waits (at most 30 s) until every interface zone docker held before is in
# it again: Docker re-adds its bridges only after the reload (measured: 1 of 7 right after it, all 7 about 350 ms
# later), and a snapshot taken in between misses the zone's chains (once 460 "unexpected" lines in a teardown).
reload_firewalld() {
	held="$(firewall-cmd --zone=docker --list-interfaces)"
	run firewall-cmd -q --reload
	[ "$DRY_RUN" = 1 ] && return 0
	waited=0
	for i in $held; do
		until firewall-cmd -q --zone=docker --query-interface="$i"; do
			if [ "$waited" -ge 30 ]; then
				warn "$i is not back in firewalld zone docker 30 s after the reload"
				return 0
			fi
			sleep 1
			waited=$((waited + 1))
		done
	done
}

# put_file PATH MODE CONTENT RECORD writes a file unless it already holds CONTENT, recording RECORD before it writes;
# status 0 when it wrote (or would).
put_file() {
	if [ -f "$1" ] && [ "$(cat "$1")" = "$3" ]; then
		return 1
	fi
	record "$4"
	if [ "$DRY_RUN" = 1 ]; then
		printf 'would write %s (mode %s):\n%s\n' "$1" "$2" "$3"
		return 0
	fi
	mkdir -p "$(dirname "$1")"
	printf '%s\n' "$3" >"$1.flow-tmp"
	chmod "$2" "$1.flow-tmp"
	mv -f "$1.flow-tmp" "$1"
}

# record notes in the manifest that this setup created something, so the teardown removes exactly that.
record() {
	[ "$DRY_RUN" = 1 ] && return 0
	mkdir -p "$STATE_DIR"
	grep -qxF "$*" "$MANIFEST" 2>/dev/null || printf '%s\n' "$*" >>"$MANIFEST"
}
recorded() { grep -qxF "$*" "$MANIFEST" 2>/dev/null; }

# The manifest line a setup writes before it creates a pool on POOL_PATH: `pool-path <pool> <path>`.
# pool_path_line POOL prints the path.
pool_path_line() { sed -n "s/^pool-path $1 //p" "$MANIFEST" 2>/dev/null | tail -n 1; }

# The rich rules of each policy, one per line, in firewalld's own spelling. IPv6 has none: it never gets past the
# bridge (IPV6_NFT). A zone-wide IPv6 reject here would hit every Docker network (firewalld matches no interface,
# all bridges are in zone docker): measured, it cut containers on an IPv6 network off from the host over IPv6.
policy_rules() {
	case "$1" in
	"$POLICY_HOST") printf 'rule family="ipv4" source address="%s" reject\n' "$RANGE" ;;
	"$POLICY_DOCKER") printf 'rule family="ipv4" source address="%s" reject\n' "$RANGE" ;;
	"$POLICY_LAN")
		for d in $LAN_DESTINATIONS; do
			printf 'rule family="ipv4" source address="%s" destination address="%s" reject\n' "$RANGE" "$d"
		done
		;;
	# Every container (all Docker bridges are in zone docker) but Flow's: the Incus API's port, on the listen
	# address (any host address for a wildcard listener). A wildcard listens on IPv6 too, whichever spelling
	# (measured, Incus 6.0.4: 0.0.0.0, [::] and :port all bind *:port), and Flow dials over IPv4: then IPv6 to the
	# port is refused for every container.
	"$POLICY_INCUS")
		lip="$(listen_ip)"
		printf 'rule family="ipv4" source NOT address="%s"%s port port="%s" protocol="tcp" reject\n' \
			"$CONTAINER_ADDRESS" "${lip:+ destination address=\"$lip\"}" "${INCUS_LISTEN##*:}"
		[ -n "$lip" ] || printf 'rule family="ipv6" port port="%s" protocol="tcp" reject\n' "${INCUS_LISTEN##*:}"
		;;
	esac
}

# own_stale POLICY RULE: a rule of Flow's that an earlier run made and these settings no longer want; policy()
# replaces or removes it instead of refusing it as someone else's. flow-sb-incus: rules for another
# CONTAINER_ADDRESS or INCUS_LISTEN. flow-sb-host: the zone-wide IPv6 reject of setups before IPV6_NFT.
own_stale() {
	case "$1" in
	"$POLICY_INCUS")
		printf '%s\n' "$2" | grep -Eqx \
			'rule family="ipv4" source NOT address="[0-9.]+"( destination address="[0-9.]+")? port port="[0-9]+" protocol="tcp" reject|rule family="ipv6" port port="[0-9]+" protocol="tcp" reject'
		;;
	"$POLICY_HOST") [ "$2" = 'rule family="ipv6" source address="::/0" reject' ] ;;
	*) return 1 ;;
	esac
}
policy_egress() {
	case "$1" in
	"$POLICY_HOST" | "$POLICY_INCUS") echo HOST ;;
	"$POLICY_DOCKER") echo docker ;;
	"$POLICY_LAN") echo ANY ;;
	esac
}

# listen_ip prints INCUS_LISTEN's IPv4 address, nothing for a wildcard (0.0.0.0, [::], empty).
listen_ip() {
	h="${INCUS_LISTEN%:*}"
	case "$h" in
	"" | 0.0.0.0 | "[::]") ;;
	*) printf '%s\n' "$h" ;;
	esac
}

# builder_address is what sandbox.Network.BuilderAddress computes: the highest address of RANGE that does not end
# in .0 or .255 and is neither the subnet's broadcast nor the gateway. Flow never assigns it.
builder_address() {
	awk -v r="$RANGE" -v s="$SUBNET" -v g="$GATEWAY" "$cidr_awk"'
		function str(n) { return sprintf("%d.%d.%d.%d", int(n / 16777216) % 256, int(n / 65536) % 256, int(n / 256) % 256, n % 256) }
		BEGIN {
			for (a = hi(r); a >= lo(r); a--) {
				o = a % 256
				if (o != 0 && o != 255 && a != hi(s) && a != ip(g)) { print str(a); exit 0 }
			}
			exit 1
		}'
}

# cert_fp prints the SHA-256 fingerprint of a PEM certificate, as Incus names trusted certificates.
cert_fp() { openssl x509 -in "$1" -outform der 2>/dev/null | sha256sum | cut -c1-64; }

# trusted_fp prints the (shortened) fingerprint of the trust entry flow-backend; csv columns of incus 6.0:
# name,type,description,fingerprint,expiry.
trusted_fp() {
	incus config trust list --format csv </dev/null | awk -F, '$1 == "flow-backend" && $2 == "client" { print $4; exit }'
}

# The DOCKER-USER rule: sandboxes may not open connections that Docker DNATs to a published port of another
# container (firewalld accepts `ct status dnat` before any policy, so only DOCKER-USER can say no).
RULE_SPEC="-i $BRIDGE -s $RANGE -m conntrack --ctstate DNAT -j REJECT --reject-with icmp-port-unreachable"

# Sandboxes have no IPv6: Flow's table drops every IPv6 frame that enters BRIDGE from any port, before the bridge
# forwards it to another port (Flow's container) or hands it to the host (its link-local address, multicast, routes
# to other Docker networks, published ports). The bridge family's hooks see bridged frames without br_netfilter;
# matched by the bridge's name, so a re-created bridge is covered too. One nft command is one transaction: loading
# it again replaces the table, never doubles the rule. Measured without it (lab, bridge-nf-call sysctls 0): a
# sandbox reached the host over link-local (ping, published ports through docker-proxy) once the zone-wide reject
# was gone, and Flow's container at L2 whenever its interface on BRIDGE has IPv6. Rejected: Incus'
# security.ipv6_filtering (refuses to start a NIC without br_netfilter: "security.ipv6_filtering requires bridge
# netfilter: sysctl net.bridge.bridge-nf-call-ip6tables not enabled", Incus 6.0.4), narrowing the firewalld reject
# (it can only match addresses; the open path used a link-local source, which every bridge's containers share).
IPV6_NFT="add table bridge $IPV6_TABLE; flush table bridge $IPV6_TABLE; add chain bridge $IPV6_TABLE ipv6 { type filter hook prerouting priority filter; policy accept; }; add rule bridge $IPV6_TABLE ipv6 meta ibrname \"$BRIDGE\" ether type ip6 drop"
ipv6_table_ok() {
	nft list chain bridge "$IPV6_TABLE" ipv6 2>/dev/null | grep -qF "meta ibrname \"$BRIDGE\" ether type ip6 drop"
}
# The second layer: no IPv6 on the host's side of BRIDGE (no link-local address; the kernel discards IPv6 received
# there before netfilter: measured, table gone and bridge-nf-call 0, nothing reached prerouting). Docker leaves it on for an IPv4-only network (measured: link-local present, disable_ipv6 0) and keeps it
# across its restarts (measured); a re-created bridge has it on again until Docker next restarts, the table holds.
ipv6_sysctl() { printf '/proc/sys/net/ipv6/conf/%s/disable_ipv6\n' "$BRIDGE"; }

# --- address arithmetic (awk: POSIX sh has no 32-bit integers to rely on) ---

# cidr_inside A B: prefix A lies within prefix B. cidr_overlap A B: they share an address. ip_in ADDR P.
cidr_awk='
function ip(s,  a) { split(s, a, "."); return ((a[1]*256 + a[2])*256 + a[3])*256 + a[4] }
function lo(p,  a, n) { split(p, a, "/"); n = 2^(32 - a[2]); return int(ip(a[1]) / n) * n }
function hi(p,  a) { split(p, a, "/"); return lo(p) + 2^(32 - a[2]) - 1 }'
cidr_inside() { awk -v a="$1" -v b="$2" "$cidr_awk"' BEGIN { exit !(lo(a) >= lo(b) && hi(a) <= hi(b)) }'; }
cidr_overlap() { awk -v a="$1" -v b="$2" "$cidr_awk"' BEGIN { exit !(lo(a) <= hi(b) && lo(b) <= hi(a)) }'; }
ip_in() { cidr_inside "$1/32" "$2"; }
cidr_valid() {
	printf '%s\n' "$1" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$' &&
		awk -v a="$1" "$cidr_awk"' BEGIN { split(a, p, "/"); exit !(lo(a) == ip(p[1])) }'
}

check_settings() {
	for p in "$SUBNET" "$RANGE" "$DOCKER_RANGE"; do
		cidr_valid "$p" || die "$p is not an IPv4 network address with prefix length"
	done
	cidr_inside "$RANGE" "$SUBNET" || die "sandbox range $RANGE is not inside $SUBNET"
	cidr_inside "$DOCKER_RANGE" "$SUBNET" || die "Docker's range $DOCKER_RANGE is not inside $SUBNET"
	! cidr_overlap "$RANGE" "$DOCKER_RANGE" || die "sandbox range $RANGE overlaps Docker's range $DOCKER_RANGE"
	ip_in "$GATEWAY" "$SUBNET" || die "gateway $GATEWAY is not in $SUBNET"
	! ip_in "$GATEWAY" "$RANGE" || die "gateway $GATEWAY lies in the sandbox range $RANGE"
	[ "${#BRIDGE}" -le 15 ] || die "bridge name $BRIDGE is longer than 15 characters"
	printf '%s\n' "$INCUS_LISTEN" | grep -Eq '^((([0-9]{1,3}\.){3}[0-9]{1,3})|\[::\])?:[0-9]{1,5}$' ||
		die "INCUS_LISTEN $INCUS_LISTEN is not <IPv4 address>:<port> (or a wildcard: 0.0.0.0:<port>, [::]:<port>, :<port>)"
	cidr_valid "$CONTAINER_ADDRESS/32" || die "CONTAINER_ADDRESS $CONTAINER_ADDRESS is not an IPv4 address"
	ip_in "$CONTAINER_ADDRESS" "$SUBNET" || die "CONTAINER_ADDRESS $CONTAINER_ADDRESS is not in $SUBNET"
	! ip_in "$CONTAINER_ADDRESS" "$RANGE" || die "CONTAINER_ADDRESS $CONTAINER_ADDRESS lies in the sandbox range $RANGE"
	! ip_in "$CONTAINER_ADDRESS" "$DOCKER_RANGE" ||
		die "CONTAINER_ADDRESS $CONTAINER_ADDRESS lies in Docker's range $DOCKER_RANGE (Docker may give it to another container)"
	[ "$CONTAINER_ADDRESS" != "$GATEWAY" ] || die "CONTAINER_ADDRESS is the gateway $GATEWAY"
	if [ -n "$OTHER_CHURN" ]; then
		rc=0
		printf 'x\n' | grep -Eq -- "$OTHER_CHURN" || rc=$?
		[ "$rc" -ne 2 ] || die "OTHER_CHURN is not an extended regular expression: $OTHER_CHURN"
	fi
}

# --- state of the firewall managers ---

ban_first() {
	[ -z "$BAN_CHAIN" ] && return 0
	[ "$(iptables -S FORWARD 2>/dev/null | awk '$1 == "-A" { print; exit }')" = "-A FORWARD -j $BAN_CHAIN" ]
}

# wait_ban_first gives the ban chain's owner BAN_WAIT seconds to put it first in FORWARD again (Docker and
# firewalld reloads move it; its owner re-inserts it). It only looks: it never moves a rule it did not make.
wait_ban_first() {
	[ -z "$BAN_CHAIN" ] && return 0
	[ "$DRY_RUN" = 1 ] && return 0
	waited=0
	while ! ban_first; do
		if [ "$waited" -ge "$BAN_WAIT" ]; then
			fail "$BAN_CHAIN is not first in FORWARD after ${BAN_WAIT}s: $(iptables -S FORWARD 2>/dev/null | grep -- '^-A' | tr '\n' ';')"
			return 1
		fi
		sleep 2
		waited=$((waited + 2))
	done
	log "$BAN_CHAIN is first in FORWARD (waited ${waited}s)"
}

# user_first: FORWARD jumps to DOCKER-USER before DOCKER-FORWARD, else DOCKER-FORWARD accepts sandbox traffic
# before the DOCKER-USER rule sees it (seen in the lab after a firewalld reload plus `docker network rm`; a Docker
# restart puts it right). Sorted snapshots do not show order, so this is checked on its own.
user_first() {
	iptables -S FORWARD 2>/dev/null | awk '
		$1 == "-A" && $NF == "DOCKER-USER" && !fwd { user = 1 }
		$1 == "-A" && $NF == "DOCKER-FORWARD" { fwd = 1 }
		END { exit !user }'
}

# other_jumps prints the chains FORWARD jumps to unconditionally that are not Docker's: a hint when BAN_CHAIN does
# not exist. Never taken as the ban chain by itself: a VPN's or hypervisor's chain may sit first just as well.
other_jumps() {
	iptables -S FORWARD 2>/dev/null | awk '$1 == "-A" && NF == 4 && $3 == "-j" && $4 !~ /^(DOCKER|ACCEPT$|DROP$|REJECT$|RETURN$|LOG$)/ { print $4 }' |
		tr '\n' ' ' | sed 's/ $//'
}

# manifest_settings refuses a PROJECT or POOL other than the one an earlier setup worked with (manifest lines
# `project`, `uses project`, `root-device`; `uses pool`): a setup re-run would leave the old one behind, and the
# teardown would look for sandboxes in the wrong place. Lines a manifest lacks (older setups) are not compared.
manifest_settings() {
	[ -f "$MANIFEST" ] || return 0
	used_projects="$(sed -n -e 's/^project //p' -e 's/^uses project //p' -e 's/^root-device \([^ ]*\) .*/\1/p' "$MANIFEST" | sort -u)"
	for mp in $used_projects; do
		[ "$mp" = "$PROJECT" ] ||
			die "the setup worked with Incus project $mp (manifest $MANIFEST), the settings say $PROJECT: run with BACKEND_INCUS_PROJECT=$mp"
	done
	used_pools="$(sed -n 's/^uses pool //p' "$MANIFEST" | sort -u)"
	if [ -n "$used_pools" ] && ! printf '%s\n' "$used_pools" | grep -qxF "$POOL"; then
		die "the setup worked with pool $(printf '%s' "$used_pools" | tr '\n' ' ' | sed 's/ $//') (manifest $MANIFEST), the settings say $POOL: run with BACKEND_INCUS_POOL set as for the setup"
	fi
}

# ban_from_manifest: a ban chain an earlier setup found (manifest line `ban-chain NAME`) is required from then on, by
# setup re-runs and the teardown alike; a BAN_CHAIN that names another one (empty included) is refused.
ban_from_manifest() {
	seen="$(sed -n 's/^ban-chain //p' "$MANIFEST" 2>/dev/null | tail -n 1)"
	[ -n "$seen" ] || return 0
	if [ -z "$BAN_CHAIN_SET" ]; then
		BAN_CHAIN="$seen"
		BAN_CHAIN_SET=1
	elif [ "$BAN_CHAIN" != "$seen" ]; then
		die "the setup found ban chain $seen (manifest $MANIFEST), BAN_CHAIN says '$BAN_CHAIN': run with BAN_CHAIN=$seen, or, if the host no longer has it, delete the line 'ban-chain $seen' from the manifest"
	fi
}

# resolve_ban decides once per run whether the host has the ban chain: it exists → it must be first now (and again
# after each change); it does not → refused when it was named explicitly, recorded or BAN_REQUIRE=1, else BAN_CHAIN
# is emptied, which skips every ban-chain step. Never re-decided later: a chain that vanishes during the run fails
# the verification instead. BAN_SEEN=1: it exists (host-setup.sh records that).
BAN_SEEN=0
resolve_ban() {
	case "$BAN_REQUIRE" in
	0) ;;
	1) [ -n "$BAN_CHAIN" ] || die "BAN_REQUIRE=1 needs the chain's name in BAN_CHAIN" ;;
	*) die "BAN_REQUIRE must be 0 or 1" ;;
	esac
	if [ -n "$BAN_CHAIN" ] && iptables -S "$BAN_CHAIN" >/dev/null 2>&1; then
		ban_first || die "$BAN_CHAIN is not first in FORWARD now: wait for its owner to restore it"
		log "ban chain $BAN_CHAIN is first in FORWARD; it must be again after the changes"
		BAN_SEEN=1
		return 0
	fi
	if [ "$BAN_REQUIRE" = 1 ]; then
		die "ban chain $BAN_CHAIN does not exist, and BAN_REQUIRE=1 says this host has one"
	fi
	if [ -n "$BAN_CHAIN_SET" ] && [ -n "$BAN_CHAIN" ]; then
		die "ban chain $BAN_CHAIN does not exist (named in BAN_CHAIN or found by an earlier setup): fix the name or wait for its owner to restore it; a host without one: BAN_CHAIN= (and, if the manifest $MANIFEST has a line 'ban-chain $BAN_CHAIN', delete that line)"
	fi
	jumps="$(other_jumps)"
	log "no ban chain${BAN_CHAIN:+ ($BAN_CHAIN does not exist)}: its checks are skipped${jumps:+; FORWARD also jumps to $jumps (BAN_CHAIN=<name> if one of them must stay first)}"
	BAN_CHAIN=""
}

# preflight refuses unless the host has what the isolation design was measured with: firewalld on nftables, Docker
# on iptables-nft with its firewalld zone `docker` and DOCKER-USER chain, FORWARD dropping and jumping to DOCKER-USER
# before DOCKER-FORWARD, the units docker.service and (when Incus is installed) incus.service, and Incus without a
# managed network. A ban chain is checked when the host has one (resolve_ban).
preflight() {
	for c in docker firewall-cmd iptables nft ip ss systemctl awk comm sort; do
		command -v "$c" >/dev/null 2>&1 || die "$c not found"
	done
	[ "$(firewall-cmd --state 2>/dev/null)" = running ] || die "firewalld is not running"
	# Its nftables backend owns `table inet firewalld`; the iptables backend makes none.
	nft list table inet firewalld >/dev/null 2>&1 || die "firewalld does not run on nftables (no table inet firewalld): set FirewallBackend=nftables"
	iptables -V 2>/dev/null | grep -q nf_tables || die "iptables is not the nf_tables variant: $(iptables -V 2>&1)"
	docker info >/dev/null 2>&1 || die "Docker is not running"
	dfw="$(docker info --format '{{.FirewallBackend.Driver}}' 2>/dev/null || true)"
	case "$dfw" in
	*iptables*) ;;
	"") warn "Docker does not report its firewall backend; assuming iptables" ;;
	*) die "Docker's firewall backend is $dfw, not iptables" ;;
	esac
	# Docker's names, fixed in Docker: the zone its bridges join and the chain left to the admin.
	firewall-cmd --get-zones | tr ' ' '\n' | grep -qx docker || die "firewalld has no zone docker: Docker's firewalld integration is off"
	iptables -S DOCKER-USER >/dev/null 2>&1 || die "iptables has no DOCKER-USER chain"
	[ "$(iptables -S FORWARD 2>/dev/null | head -n 1)" = "-P FORWARD DROP" ] ||
		die "FORWARD's policy is not DROP (the isolation was measured with DROP; Docker leaves ACCEPT when IP forwarding was on before it)"
	user_first || die "FORWARD jumps to DOCKER-FORWARD before DOCKER-USER: restart Docker to restore its order"
	# The DOCKER-USER rule's unit is bound to docker.service, Incus' drop-in names incus.service.
	systemctl cat docker.service >/dev/null 2>&1 || die "Docker does not run as docker.service"
	if command -v incus >/dev/null 2>&1; then
		systemctl cat incus.service >/dev/null 2>&1 || die "Incus does not run as incus.service"
		managed="$(incus network list --format csv -c nm </dev/null | awk -F, '$2 == "YES" { print $1 }' | tr '\n' ' ')"
		[ -z "$managed" ] || die "Incus manages networks ($managed): Flow's sandboxes use Docker's bridge only"
	fi
	resolve_ban
	[ -z "$OTHER_CHURN" ] || log "differences matching OTHER_CHURN ($OTHER_CHURN) are shown, not counted"
	if [ "$(sysctl -n net.bridge.bridge-nf-call-iptables 2>/dev/null)" = 1 ]; then
		warn "br_netfilter is active: bridged traffic passes iptables, so same-bridge results differ from a host without it"
	fi
}

# --- snapshots ---

# snapshot DIR writes DIR/rules: the whole ruleset (iptables-nft, legacy tables, nft) and firewalld's runtime and
# permanent zones and policies, one fact per line with its context, counters dropped, sorted. Order is not
# compared: Docker and firewalld reorder on every reload; the ban chain's position is checked on its own.
snapshot() {
	mkdir -p "$1"
	{
		for t in iptables-nft-save ip6tables-nft-save iptables-legacy-save ip6tables-legacy-save; do
			command -v "$t" >/dev/null 2>&1 || continue
			"$t" 2>/dev/null | awk -v tool="${t%-save}" '
				/^#/ || /^COMMIT/ { next }
				/^\*/ { table = substr($0, 2); next }
				{ gsub(/ \[[0-9]+:[0-9]+\]/, ""); print tool " " table " " $0 }'
		done
		# Declarations get a line too: an empty table or chain is part of the state.
		nft -s list ruleset 2>/dev/null | awk '
			/^table / { tbl = $2 " " $3; print "nft " tbl " | table"; next }
			/^\t(chain|set|map|flowtable) / { obj = $1 " " $2; sub(/^\t/, "", obj); print "nft " tbl " | " obj; next }
			/^\t}$/ { obj = ""; next }
			/^}$/ { tbl = ""; next }
			{ line = $0; gsub(/^[ \t]+/, "", line); if (line != "") print "nft " tbl " | " obj " | " line }'
		for mode in "" --permanent; do
			for z in $(firewall-cmd ${mode:+"$mode"} --get-zones); do
				firewall-cmd ${mode:+"$mode"} --zone="$z" --list-all | fw_lines "firewalld${mode:+ permanent} zone $z"
			done
			for p in $(firewall-cmd ${mode:+"$mode"} --get-policies); do
				firewall-cmd ${mode:+"$mode"} --info-policy="$p" | fw_lines "firewalld${mode:+ permanent} policy $p"
			done
		done
	} | LC_ALL=C sort >"$1/rules"
}

# fw_lines splits firewall-cmd's listing into one value per line (interface lists come in any order).
fw_lines() {
	awk -v pfx="$1" '
		NR == 1 { next }
		/^[ \t]+[a-z0-9 -]+:/ {
			key = $0; sub(/:.*/, "", key); gsub(/^[ \t]+/, "", key)
			val = $0; sub(/^[^:]*:[ \t]*/, "", val)
			rich = (key == "rich rules")
			n = split(val, v, " ")
			if (n == 0) print pfx " " key
			for (i = 1; i <= n; i++) print pfx " " key " " v[i]
			next
		}
		rich { line = $0; gsub(/^[ \t]+/, "", line); if (line != "") print pfx " rich " line }'
}

# explained LINE: a difference Flow's setup accounts for (its bridge, subnet, range, policies, rule, table).
explained() {
	case "$1" in
	*"$BRIDGE"* | *flow-sb-* | *"$SUBNET"* | *"$RANGE"* | *"nft bridge $IPV6_TABLE "*) return 0 ;;
	esac
	return 1
}

# others LINE: churn of the ban chain, whose owner adds and removes bans at any time.
others() {
	[ -n "$BAN_CHAIN" ] || return 1
	case "$1" in
	*" -A $BAN_CHAIN "* | *"chain $BAN_CHAIN |"*) return 0 ;;
	esac
	return 1
}

# churn LINE: a change OTHER_CHURN names, never one of Flow's own lines.
churn() {
	[ -n "$OTHER_CHURN" ] || return 1
	explained "$1" && return 1
	printf '%s\n' "$1" | grep -Eq -- "$OTHER_CHURN"
}

# allowed MODE LINE: whether a difference is Flow's doing. setup: anything explained appears or disappears.
# teardown: only explained lines and Incus' per-NIC chains (`table bridge incus`) disappear; nothing appears.
allowed() {
	case "$1" in
	setup) explained "$2" ;;
	teardown)
		case "$2" in
		"- nft bridge incus "*) return 0 ;;
		"- "*) explained "$2" ;;
		*) return 1 ;;
		esac
		;;
	*) return 1 ;;
	esac
}

# compare FROM TO MODE prints the difference of two snapshots and fails on any line MODE does not allow (see
# allowed). Ban-chain churn and what OTHER_CHURN names are shown, never counted.
compare() {
	LC_ALL=C comm -13 "$1/rules" "$2/rules" | sed 's/^/+ /' >"$2/diff"
	LC_ALL=C comm -23 "$1/rules" "$2/rules" | sed 's/^/- /' >>"$2/diff"
	: >"$2/unexpected"
	: >"$2/expected"
	: >"$2/churn"
	while IFS= read -r line; do
		if allowed "$3" "$line" || others "$line"; then
			printf '%s\n' "$line" >>"$2/expected"
		elif churn "$line"; then
			printf '%s\n' "$line" >>"$2/churn"
		else
			printf '%s\n' "$line" >>"$2/unexpected"
		fi
	done <"$2/diff"
	printf '=== differences %s -> %s: %s expected, %s churn of others (OTHER_CHURN, not counted), %s unexpected\n' \
		"$1" "$2" "$(wc -l <"$2/expected" | tr -d ' ')" "$(wc -l <"$2/churn" | tr -d ' ')" \
		"$(wc -l <"$2/unexpected" | tr -d ' ')"
	sed 's/^/  ok  /' "$2/expected"
	sed 's/^/  churn  /' "$2/churn"
	if [ -s "$2/unexpected" ]; then
		sed 's/^/  UNEXPECTED  /' "$2/unexpected"
		fail "the ruleset changed in ways the $3 does not explain (above, also $2/unexpected)"
	fi
}

# residue DIR [BASELINE] fails on any line in DIR/rules that is Flow's (explained) and not in BASELINE: what the
# teardown should have removed and did not.
residue() {
	if [ -n "${2:-}" ] && [ -f "$2/rules" ]; then
		LC_ALL=C comm -23 "$1/rules" "$2/rules"
	else
		cat "$1/rules"
	fi | while IFS= read -r line; do
		if explained "$line"; then printf '%s\n' "$line"; fi
	done >"$1/residue"
	if [ -s "$1/residue" ]; then
		sed 's/^/  LEFT  /' "$1/residue"
		fail "Flow's changes are still there (above, also $1/residue)"
	else
		log "nothing of Flow's setup is left in the ruleset"
	fi
}
