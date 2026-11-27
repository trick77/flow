#!/bin/sh
# Runs Flow as a plain binary on a host prepared by host-setup.sh: the dev setup, before Flow runs in its
# container. Installs the binaries from a staging directory and a systemd unit flow.service in dev auth mode on
# loopback. Idempotent; a second run replaces the binaries and restarts Flow.
#
#   sh deploy/dev-flow.sh /tmp/flowup     (holds flow, flowd-linux-amd64 and/or flowd-linux-arm64, and sidecar/:
#                                          package.json, dist/ and the production node_modules)
#
# Sessions replay the recorded demo turn (BACKEND_SESSION_REPLAY=1) unless the running Flow has a Claude credential
# stored; set BACKEND_SESSION_REPLAY=0 or 1 when running this to decide yourself.
set -eu

STAGE="${1:?usage: dev-flow.sh <dir with flow and flowd-linux-*>}"
SCRIPT=dev-flow
# INCUS_LISTEN, PROJECT and CERT_DIR as host-setup.sh used them.
# shellcheck source=deploy/host-lib.sh
. "$(dirname "$0")/host-lib.sh"
DEST=/opt/flow-host
ENV_FILE="${ENV_FILE:-/etc/flow-host/flow.env}"
# Flow dials the listener; a wildcard listen address is reached on loopback.
case "$INCUS_LISTEN" in
0.0.0.0:* | "[::]:"* | :*) INCUS_URL="https://127.0.0.1:${INCUS_LISTEN##*:}" ;;
*) INCUS_URL="https://$INCUS_LISTEN" ;;
esac

if [ "$(id -u)" -ne 0 ]; then
	echo "dev-flow: run as root" >&2
	exit 1
fi

install -d -m 0755 "$DEST" "$DEST/flowd"
install -d -m 0700 /var/lib/flow-host
install -m 0755 "$STAGE/flow" "$DEST/flow"
for f in "$STAGE"/flowd-linux-*; do
	[ -e "$f" ] && install -m 0644 "$f" "$DEST/flowd/"
done
# The sidecar bundle Flow delivers beside flowd, replaced whole.
if [ -f "$STAGE/sidecar/package.json" ]; then
	rm -rf "$DEST/sidecar.new"
	cp -R "$STAGE/sidecar" "$DEST/sidecar.new"
	rm -rf "$DEST/sidecar"
	mv "$DEST/sidecar.new" "$DEST/sidecar"
fi

# Asked before the restart, from the Flow still running: is a Claude credential stored? A Flow that does not answer
# (stopped, crashed, not installed yet) tells nothing: the installed unit's setting is kept, replay on a first install.
if [ -z "${BACKEND_SESSION_REPLAY:-}" ]; then
	if command -v curl >/dev/null &&
		CONNECTIONS=$(curl -fsS --max-time 5 http://127.0.0.1:8080/api/connections 2>/dev/null); then
		BACKEND_SESSION_REPLAY=1
		if printf '%s' "$CONNECTIONS" | grep -Eq '"kind": *"(claude_token|anthropic)"'; then
			BACKEND_SESSION_REPLAY=0
		fi
	else
		BACKEND_SESSION_REPLAY=$(sed -n 's/^Environment=.*BACKEND_SESSION_REPLAY=\([01]\).*/\1/p' \
			/etc/systemd/system/flow.service 2>/dev/null || true)
		BACKEND_SESSION_REPLAY=${BACKEND_SESSION_REPLAY:-1}
		echo "dev-flow: running Flow did not answer; keeping BACKEND_SESSION_REPLAY=$BACKEND_SESSION_REPLAY" >&2
	fi
fi

# The seal key is made once: flowd tokens sealed with it must stay readable across restarts.
if [ ! -f "$ENV_FILE" ]; then
	umask 077
	{
		echo "BACKEND_ADDR=127.0.0.1:8080"
		echo "BACKEND_AUTH_MODE=dev"
		echo "BACKEND_DB_PATH=/var/lib/flow-host/flow.db"
		echo "BACKEND_SEAL_KEY=$(openssl rand -base64 32)"
		echo "BACKEND_INCUS_URL=$INCUS_URL"
		echo "BACKEND_INCUS_PROJECT=$PROJECT"
		echo "BACKEND_INCUS_POOL=$POOL"
		echo "BACKEND_INCUS_CERT=$CERT_DIR/client.crt"
		echo "BACKEND_INCUS_KEY=$CERT_DIR/client.key"
		echo "BACKEND_INCUS_SERVER_CERT=$CERT_DIR/server.crt"
		echo "BACKEND_FLOWD_DIR=$DEST/flowd"
		echo "BACKEND_LOG_LEVEL=info"
		# The sandbox network as host-setup.sh made it; Flow's defaults are the same, so only overrides.
		for v in BRIDGE SUBNET RANGE GATEWAY RESOLVERS MEMORY_TOTAL_MIB; do
			eval "val=\${BACKEND_SANDBOX_$v:-}"
			if [ -n "$val" ]; then
				echo "BACKEND_SANDBOX_$v=$val"
			fi
		done
	} >"$ENV_FILE"
fi

# Flow on the host reaches the sandboxes through the bridge's route; the host-input policy only drops what
# sandboxes open, not their answers.
cat >/etc/systemd/system/flow.service <<EOF
[Unit]
Description=Flow (dev, plain binary)
After=network-online.target docker.service incus.service

[Service]
# Set on every run; the env file wins where it names the same variable.
Environment=BACKEND_SIDECAR_DIR=$DEST/sidecar BACKEND_SESSION_REPLAY=$BACKEND_SESSION_REPLAY
EnvironmentFile=$ENV_FILE
ExecStart=$DEST/flow
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --quiet flow.service
systemctl restart flow.service
echo "dev-flow: $DEST/flow restarted on 127.0.0.1:8080 (BACKEND_SESSION_REPLAY=$BACKEND_SESSION_REPLAY)"
