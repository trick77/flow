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
# neither reported nor undone. Then nothing of Flow's may be left, the ban chain must be first in FORWARD again and
# DOCKER-USER before DOCKER-FORWARD.
#
# Refuses while a Docker container is attached to the Docker network (Flow's own: `docker network disconnect`
# it first). Left in place on purpose: anything the manifest does not list (an Incus that was there before, its
# pool), firewalld's own *.xml.old backups other than those of Flow's policies.
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
preflight

has_incus=0
command -v incus >/dev/null 2>&1 && has_incus=1
instances=""
volumes=""
if [ "$has_incus" = 1 ] && incus project show "$PROJECT" </dev/null >/dev/null 2>&1; then
	instances="$(incus list --project "$PROJECT" --format csv -c n </dev/null | tr '\n' ' ')"
	volumes="$(incus storage volume list default --project "$PROJECT" --format csv -c tn </dev/null |
		awk -F, '$1 == "custom" { print $2 }' | tr '\n' ' ')"
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
	log "deleting volume $v"
	run incus storage volume delete default "custom/$v" --project "$PROJECT" </dev/null
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

reload=0
for p in "$POLICY_HOST" "$POLICY_DOCKER" "$POLICY_LAN"; do
	if recorded "policy $p" && firewall-cmd --permanent --get-policies | tr ' ' '\n' | grep -qx "$p"; then
		log "deleting firewalld policy $p"
		run firewall-cmd -q --permanent --delete-policy="$p"
		reload=1
	fi
done
if [ "$reload" = 1 ]; then
	run firewall-cmd -q --reload
fi
for p in "$POLICY_HOST" "$POLICY_DOCKER" "$POLICY_LAN"; do
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
	if recorded "project $PROJECT" && incus project show "$PROJECT" </dev/null >/dev/null 2>&1; then
		log "deleting project $PROJECT"
		run incus profile device remove default root --project "$PROJECT" </dev/null || true
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
	if recorded "pool default" && incus storage show default </dev/null >/dev/null 2>&1; then
		run incus storage delete default </dev/null
	fi
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
