#!/bin/sh
# The reverse of host-setup.sh: removes what its manifest says it created, nothing else. Run by the owner by hand,
# as root, with the same settings as the setup:
#
#   sh deploy/host-teardown.sh --dry-run                prints what it would remove
#   sh deploy/host-teardown.sh [--delete-sandboxes]     removes; refuses while sandboxes exist unless told
#                                                       to delete them (instances and custom volumes of $PROJECT)
#   sh deploy/host-teardown.sh --purge-incus            also purges Incus, if host-setup installed it
#
# Every step checks first, so a teardown that stopped half way can simply be run again. Verification compares
# with a snapshot taken at the start of this run, not with the setup's old baseline: only Flow's own lines may
# disappear and nothing may appear, so firewall changes the host made since the setup (a container, a ban) are
# neither reported nor undone (OTHER_CHURN as in host-setup.sh). Then nothing of Flow's may be left, a third
# party's chain kept first in FORWARD (when the host has one; detected and required as in host-setup.sh, and always
# when the setup recorded it) must be first again and DOCKER-USER before DOCKER-FORWARD.
#
# Refuses while a Docker container is attached to the Docker network (Flow's own: `docker network disconnect`
# it first), and when the project or pool settings differ from those the manifest records. Sandboxes and volumes
# are looked for on every pool the setups made or used. Left in place on purpose: anything the manifest does not
# list (an Incus that was there before, its pool, a project it did not make; the root device the setup added to
# that project's default profile goes), a Flow policy another setup made (another STATE_DIR; the setup records
# only those it creates, though manifests written before that record every policy), firewalld's own *.xml.old
# backups other than those of Flow's policies.
# A pool on the owner's btrfs filesystem (POOL_PATH): Incus deletes the pool (refused while anything is on it); the
# filesystem, its mount and its fstab line stay as the owner made them. Nothing here formats, wipes or unmounts.
set -eu

SCRIPT=host-teardown
# shellcheck source=deploy/host-lib.sh
. "$(dirname "$0")/host-lib.sh"

DELETE_SANDBOXES=0
PURGE_INCUS=0
for arg in "$@"; do
	case "$arg" in
	--dry-run) DRY_RUN=1 ;;
	--delete-sandboxes) DELETE_SANDBOXES=1 ;;
	--purge-incus) PURGE_INCUS=1 ;;
	*) die "unknown argument $arg (--dry-run, --delete-sandboxes, --purge-incus)" ;;
	esac
done
[ "$(id -u)" -eq 0 ] || die "run as root"
[ -f "$MANIFEST" ] || die "no manifest at $MANIFEST: nothing host-setup.sh created is known"
# With other project or pool settings the teardown would look for sandboxes in the wrong place and then fail half
# way.
manifest_settings
ban_from_manifest
preflight

has_incus=0
command -v incus >/dev/null 2>&1 && has_incus=1
instances=""
volumes=""
# Every pool the project may have volumes on: those the setups made or used, and the configured one.
pools="$({
	sed -n -e 's/^pool //p' -e 's/^uses pool //p' -e 's/^root-device [^ ]* //p' "$MANIFEST"
	printf '%s\n' "$POOL"
} | sort -u)"
if [ "$has_incus" = 1 ] && incus project show "$PROJECT" </dev/null >/dev/null 2>&1; then
	instances="$(incus list --project "$PROJECT" --format csv -c n </dev/null | tr '\n' ' ')"
	# Without features.storage.volumes a project lists the default project's volumes: those are not Flow's.
	if [ "$(incus project get "$PROJECT" features.storage.volumes </dev/null)" = true ]; then
		for pl in $pools; do
			incus storage show "$pl" </dev/null >/dev/null 2>&1 || continue
			list="$(incus storage volume list "$pl" --project "$PROJECT" --format csv -c tn </dev/null)" ||
				die "cannot list project $PROJECT's volumes on pool $pl"
			volumes="$volumes$(printf '%s\n' "$list" | awk -F, -v pl="$pl" '$1 == "custom" { printf "%s/%s ", pl, $2 }')"
		done
	fi
fi
if [ -n "$instances$volumes" ] && [ "$DELETE_SANDBOXES" = 0 ]; then
	die "project $PROJECT still has sandboxes ($instances) or volumes ($volumes): --delete-sandboxes removes them"
fi
if recorded "docker-network $DOCKER_NETWORK" && docker network inspect "$DOCKER_NETWORK" >/dev/null 2>&1; then
	attached="$(docker network inspect -f '{{range .Containers}}{{.Name}} {{end}}' "$DOCKER_NETWORK")"
	[ -z "$attached" ] || die "containers are attached to $DOCKER_NETWORK: $attached (docker network disconnect them first)"
fi

if [ "$DRY_RUN" = 0 ]; then
	RUN_DIR="$STATE_DIR/teardown-$(date -u +%Y%m%dT%H%M%SZ)"
	snapshot "$RUN_DIR/before"
fi

# Sandboxes first: their NICs hang on the bridge and their chains in `table bridge incus`.
for i in $instances; do
	log "deleting sandbox $i"
	run incus delete --force --project "$PROJECT" "$i" </dev/null
done
for v in $volumes; do
	log "deleting volume ${v#*/} on pool ${v%%/*}"
	run incus storage volume delete "${v%%/*}" "custom/${v#*/}" --project "$PROJECT" </dev/null
done

if recorded "unit $RULE_UNIT"; then
	if [ -f "$RULE_UNIT_FILE" ]; then
		log "removing $RULE_UNIT"
		run systemctl disable --now --quiet "$RULE_UNIT"
		run rm -f "$RULE_UNIT_FILE"
		run systemctl daemon-reload
	fi
	# The unit has no ExecStop: the rule goes here.
	# shellcheck disable=SC2086 # RULE_SPEC is a list of arguments
	while iptables -C DOCKER-USER $RULE_SPEC 2>/dev/null; do
		log "deleting the DOCKER-USER rule"
		run iptables -D DOCKER-USER $RULE_SPEC
		[ "$DRY_RUN" = 1 ] && break
	done
fi
# No IPv6 on the bridge: the unit (no ExecStop), its table, the bridge's sysctl back to Docker's 0 while the bridge
# stays (a bridge the teardown removes below goes with it).
if recorded "unit $IPV6_UNIT" && [ -f "$IPV6_UNIT_FILE" ]; then
	log "removing $IPV6_UNIT"
	run systemctl disable --now --quiet "$IPV6_UNIT"
	run rm -f "$IPV6_UNIT_FILE"
	run systemctl daemon-reload
fi
if recorded "nft-table $IPV6_TABLE" && nft list table bridge "$IPV6_TABLE" >/dev/null 2>&1; then
	log "deleting nftables table bridge $IPV6_TABLE"
	run nft delete table bridge "$IPV6_TABLE"
fi
if recorded "ipv6-off $BRIDGE" && [ -e "$(ipv6_sysctl)" ] && [ "$(cat "$(ipv6_sysctl)")" = 1 ]; then
	log "switching IPv6 on $BRIDGE back on (Docker's default)"
	run sh -c 'echo 0 >"$1"' sh "$(ipv6_sysctl)"
fi
# Stopping it changes nothing (no ExecStop): Incus keeps listening where it does.
if recorded "unit $LISTEN_UNIT" && [ -f "$LISTEN_UNIT_FILE" ]; then
	log "removing $LISTEN_UNIT"
	run systemctl disable --now --quiet "$LISTEN_UNIT"
	run rm -f "$LISTEN_UNIT_FILE"
	run systemctl daemon-reload
fi

reload=0
for p in $POLICIES; do
	if recorded "policy $p" && firewall-cmd --permanent --get-policies | tr ' ' '\n' | grep -qx "$p"; then
		log "deleting firewalld policy $p"
		run firewall-cmd -q --permanent --delete-policy="$p"
		reload=1
	fi
done
if [ "$reload" = 1 ]; then
	reload_firewalld
fi
for p in $POLICIES; do
	if recorded "policy $p" && [ -f "/etc/firewalld/policies/$p.xml.old" ]; then
		run rm -f "/etc/firewalld/policies/$p.xml.old"
	fi
done

if recorded "docker-network $DOCKER_NETWORK" && docker network inspect "$DOCKER_NETWORK" >/dev/null 2>&1; then
	log "removing Docker network $DOCKER_NETWORK ($BRIDGE)"
	run docker network rm "$DOCKER_NETWORK"
fi

if recorded "dropin $DROPIN"; then
	if [ -f "$DROPIN" ]; then
		run rm -f "$DROPIN"
		run systemctl daemon-reload
	fi
	if [ -d "$(dirname "$DROPIN")" ]; then
		run rmdir --ignore-fail-on-non-empty "$(dirname "$DROPIN")"
	fi
fi

if [ "$has_incus" = 1 ]; then
	if recorded "trust flow-backend"; then
		fp="$(trusted_fp)"
		if [ -n "$fp" ]; then
			run incus config trust remove "$fp" </dev/null
		fi
	fi
	# The root device a setup added to the project's default profile, also in a project it did not make: no pool
	# can be deleted while a profile uses it. Only while it is still on the pool the setup put it on.
	rd_pools="$(sed -n "s/^root-device $PROJECT //p" "$MANIFEST")"
	if [ -n "$rd_pools" ] && incus project show "$PROJECT" </dev/null >/dev/null 2>&1; then
		rd_pool="$(incus profile device get default root pool --project "$PROJECT" </dev/null 2>/dev/null || true)"
		if [ -n "$rd_pool" ] && printf '%s\n' "$rd_pools" | grep -qxF "$rd_pool"; then
			log "removing the root device (pool $rd_pool) from project $PROJECT's default profile"
			run incus profile device remove default root --project "$PROJECT" </dev/null
		fi
	fi
	if recorded "project $PROJECT" && incus project show "$PROJECT" </dev/null >/dev/null 2>&1; then
		log "deleting project $PROJECT"
		if incus profile device get default root pool --project "$PROJECT" </dev/null >/dev/null 2>&1; then
			run incus profile device remove default root --project "$PROJECT" </dev/null
		fi
		run incus project delete "$PROJECT" </dev/null
	fi
	# The listen address Incus had before the first setup: the first one recorded (older manifests wrote
	# "https_address <value>").
	https_line="$(grep -m 1 '^https_address[= ]' "$MANIFEST" || true)"
	if [ -n "$https_line" ]; then
		https="${https_line#https_address?}"
		if [ "$(incus config get core.https_address </dev/null)" != "$https" ]; then
			log "restoring Incus' API address to '$https'"
			if [ -n "$https" ]; then
				run incus config set core.https_address "$https" </dev/null
			else
				run incus config unset core.https_address </dev/null
			fi
		fi
	fi
	# Every pool a setup made (BACKEND_INCUS_POOL may have changed between runs).
	sed -n 's/^pool //p' "$MANIFEST" | while IFS= read -r pool; do
		if incus storage show "$pool" </dev/null >/dev/null 2>&1; then
			run incus storage delete "$pool" </dev/null
		fi
	done
	# Incus keeps an empty `table bridge incus` after its last filtered NIC; remove it only when empty and absent
	# from the baseline.
	if nft list table bridge incus >/dev/null 2>&1 && ! nft list table bridge incus | grep -q chain &&
		! grep -q '^nft bridge incus ' "$BASELINE/rules" 2>/dev/null; then
		run nft delete table bridge incus
	fi
	if recorded "package incus" && [ "$PURGE_INCUS" = 1 ]; then
		log "purging Incus"
		run env DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq incus-base incus-client
	fi
fi
# The pools on the owner's btrfs filesystem (POOL_PATH): only Incus' pool went above; the filesystem stays.
sed -n 's/^pool-path //p' "$MANIFEST" | while IFS=' ' read -r pool path; do
	log "left as the owner made it: the btrfs filesystem at $path (pool $pool), its mount and fstab line"
done

# Every certificate directory a setup made (CERT_DIR may have changed between runs).
sed -n 's/^cert //p' "$MANIFEST" | while IFS= read -r dir; do
	if [ -d "$dir" ]; then
		run rm -f "$dir/client.crt" "$dir/client.key" "$dir/server.crt"
		run rmdir --ignore-fail-on-non-empty "$dir"
	fi
done

[ "$DRY_RUN" = 1 ] && {
	log "dry run: nothing changed"
	exit 0
}

wait_ban_first || true
user_first || fail "FORWARD jumps to DOCKER-FORWARD before DOCKER-USER now (a firewalld reload with a Docker network removed does this): run 'systemctl restart docker' to restore Docker's order"
snapshot "$RUN_DIR/after"
compare "$RUN_DIR/before" "$RUN_DIR/after" teardown
residue "$RUN_DIR/after" "$BASELINE"
if [ -f "$BASELINE/rules" ]; then
	log "for information: $(LC_ALL=C comm -3 "$BASELINE/rules" "$RUN_DIR/after/rules" | wc -l | tr -d ' ') lines differ from the setup's baseline (changes made on the host since)"
fi
if [ "$FAILED" = 1 ]; then
	log "teardown incomplete; manifest kept, snapshots in $RUN_DIR; run it again after fixing the above"
	exit 1
fi
# A later host-setup records a new baseline.
mv "$MANIFEST" "$RUN_DIR/manifest"
if [ -d "$BASELINE" ]; then
	mv "$BASELINE" "$RUN_DIR/baseline"
fi
log "done: Flow's host changes are gone; manifest and baseline moved to $RUN_DIR"
