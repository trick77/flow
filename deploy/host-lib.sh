# shellcheck shell=sh disable=SC2034 # the settings are used by the scripts that source this file
# Shared by host-setup.sh and host-teardown.sh: settings, dry run, preflight checks, firewall snapshots and the
# check that a diff holds only what Flow's setup explains. Sourced, never run.

# Flow's own settings (backend/internal/config/network.go), same names and defaults: Flow and the host must agree.
BRIDGE="${BACKEND_SANDBOX_BRIDGE:-flowbr0}"
SUBNET="${BACKEND_SANDBOX_SUBNET:-172.30.0.0/16}"
RANGE="${BACKEND_SANDBOX_RANGE:-172.30.128.0/17}"
GATEWAY="${BACKEND_SANDBOX_GATEWAY:-$(printf '%s\n' "$SUBNET" | awk -F'[./]' '{print $1"."$2"."$3"."$4+1}')}"
MEMORY_TOTAL_MIB="${BACKEND_SANDBOX_MEMORY_TOTAL_MIB:-14336}"
# Host-side settings.
DOCKER_NETWORK="${DOCKER_NETWORK:-flow}"
DOCKER_RANGE="${DOCKER_RANGE:-172.30.0.0/24}" # Docker's own allocations (Flow's container); the rest is RANGE's
BAN_CHAIN="${BAN_CHAIN-MAILCOW}"              # must stay first in FORWARD; empty: there is none
BAN_WAIT="${BAN_WAIT:-60}"                    # seconds its owner gets to put it first again
PROJECT="${PROJECT:-flow}"
INCUS_LISTEN="${INCUS_LISTEN:-127.0.0.1:8443}"
CERT_DIR="${CERT_DIR:-/etc/flow-host/incus}"
POOL_DRIVER="${POOL_DRIVER:-btrfs}"
POOL_SIZE="${POOL_SIZE:-40GiB}"
STATE_DIR="${STATE_DIR:-/var/lib/flow-host/host-setup}"

# Destinations behind the host sandboxes may not reach: private networks, link-local (cloud metadata
# 169.254.169.254), shared address space. The host forwards them otherwise (measured: SYNs to each left the uplink).
LAN_DESTINATIONS="${LAN_DESTINATIONS:-10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 100.64.0.0/10}"

POLICY_HOST=flow-sb-host
POLICY_DOCKER=flow-sb-docker
POLICY_LAN=flow-sb-lan
RULE_UNIT=flow-sandbox-published.service
RULE_UNIT_FILE=/etc/systemd/system/$RULE_UNIT
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

# put_file PATH MODE CONTENT writes a file unless it already holds CONTENT; status 0 when it wrote (or would).
put_file() {
	if [ -f "$1" ] && [ "$(cat "$1")" = "$3" ]; then
		return 1
	fi
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

# The rich rules of each policy, one per line, in firewalld's own spelling. family ipv6 needs a source
# ("INVALID_RULE: no element, no source, no destination"), hence ::/0.
policy_rules() {
	case "$1" in
	"$POLICY_HOST")
		printf 'rule family="ipv4" source address="%s" reject\n' "$RANGE"
		printf 'rule family="ipv6" source address="::/0" reject\n'
		;;
	"$POLICY_DOCKER") printf 'rule family="ipv4" source address="%s" reject\n' "$RANGE" ;;
	"$POLICY_LAN")
		for d in $LAN_DESTINATIONS; do
			printf 'rule family="ipv4" source address="%s" destination address="%s" reject\n' "$RANGE" "$d"
		done
		;;
	esac
}
policy_egress() {
	case "$1" in
	"$POLICY_HOST") echo HOST ;;
	"$POLICY_DOCKER") echo docker ;;
	"$POLICY_LAN") echo ANY ;;
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
}

# --- state of the firewall managers ---

ban_first() {
	[ -z "$BAN_CHAIN" ] && return 0
	[ "$(iptables -S FORWARD 2>/dev/null | awk '$1 == "-A" { print; exit }')" = "-A FORWARD -j $BAN_CHAIN" ]
}

# wait_ban_first gives the ban chain's owner BAN_WAIT seconds to put it first in FORWARD again (Docker and
# firewalld reloads move it; the mail stack re-inserts it). It only looks: it never moves a rule it did not make.
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

# preflight refuses unless the firewall managers are in the state the setup was measured against: firewalld on
# nftables, Docker on iptables-nft with its zone and DOCKER-USER chain, FORWARD dropping, the ban chain first, and
# Incus (when installed) without a managed network.
preflight() {
	for c in docker firewall-cmd iptables nft ip systemctl awk comm sort; do
		command -v "$c" >/dev/null 2>&1 || die "$c not found"
	done
	[ "$(firewall-cmd --state 2>/dev/null)" = running ] || die "firewalld is not running"
	backend="$(awk -F= '/^FirewallBackend=/ { print $2 }' /etc/firewalld/firewalld.conf 2>/dev/null)"
	[ "${backend:-nftables}" = nftables ] || die "firewalld's backend is $backend, not nftables"
	iptables -V 2>/dev/null | grep -q nf_tables || die "iptables is not the nf_tables variant: $(iptables -V 2>&1)"
	docker info >/dev/null 2>&1 || die "Docker is not running"
	dfw="$(docker info --format '{{.FirewallBackend.Driver}}' 2>/dev/null || true)"
	case "$dfw" in
	*iptables*) ;;
	"") warn "Docker does not report its firewall backend; assuming iptables" ;;
	*) die "Docker's firewall backend is $dfw, not iptables" ;;
	esac
	firewall-cmd --get-zones | tr ' ' '\n' | grep -qx docker || die "firewalld has no zone docker"
	iptables -S DOCKER-USER >/dev/null 2>&1 || die "iptables has no DOCKER-USER chain"
	[ "$(iptables -S FORWARD 2>/dev/null | head -n 1)" = "-P FORWARD DROP" ] || die "FORWARD's policy is not DROP"
	user_first || die "FORWARD jumps to DOCKER-FORWARD before DOCKER-USER: restart Docker to restore its order"
	if [ -n "$BAN_CHAIN" ]; then
		iptables -S "$BAN_CHAIN" >/dev/null 2>&1 || die "ban chain $BAN_CHAIN does not exist (BAN_CHAIN= for none)"
		ban_first || die "$BAN_CHAIN is not first in FORWARD now: wait for its owner to restore it"
	fi
	if command -v incus >/dev/null 2>&1; then
		managed="$(incus network list --format csv -c nm </dev/null | awk -F, '$2 == "YES" { print $1 }' | tr '\n' ' ')"
		[ -z "$managed" ] || die "Incus manages networks ($managed): Flow's sandboxes use Docker's bridge only"
	fi
	if [ "$(sysctl -n net.bridge.bridge-nf-call-iptables 2>/dev/null)" = 1 ]; then
		warn "br_netfilter is active: bridged traffic passes iptables, unlike the production host measured"
	fi
	for n in $(docker network ls -q --filter driver=bridge); do
		if [ "$(docker network inspect -f '{{.EnableIPv6}}' "$n")" = true ]; then
			warn "Docker network $(docker network inspect -f '{{.Name}}' "$n") has IPv6: the zone-wide IPv6 reject affects it"
		fi
	done
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

# explained LINE: a difference Flow's setup accounts for (its bridge, subnet, range, policies, rule).
explained() {
	case "$1" in
	*"$BRIDGE"* | *flow-sb-* | *"$SUBNET"* | *"$RANGE"*) return 0 ;;
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
# allowed). Ban-chain churn is shown, never counted.
compare() {
	LC_ALL=C comm -13 "$1/rules" "$2/rules" | sed 's/^/+ /' >"$2/diff"
	LC_ALL=C comm -23 "$1/rules" "$2/rules" | sed 's/^/- /' >>"$2/diff"
	: >"$2/unexpected"
	: >"$2/expected"
	while IFS= read -r line; do
		if others "$line"; then
			printf '%s\n' "$line" >>"$2/expected"
		elif allowed "$3" "$line"; then
			printf '%s\n' "$line" >>"$2/expected"
		else
			printf '%s\n' "$line" >>"$2/unexpected"
		fi
	done <"$2/diff"
	printf '=== differences %s -> %s: %s expected, %s unexpected\n' "$1" "$2" \
		"$(wc -l <"$2/expected" | tr -d ' ')" "$(wc -l <"$2/unexpected" | tr -d ' ')"
	sed 's/^/  ok  /' "$2/expected"
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
