#!/bin/sh
# Runs Flow as a plain binary on a host prepared by host-setup.sh: the dev setup, before Flow runs in its
# container. Installs the binaries from a staging directory and a systemd unit flow.service in dev auth mode on
# loopback. Idempotent; a second run replaces the binaries and restarts Flow.
#
#   sh deploy/dev-flow.sh /tmp/flowup     (holds flow, flowd-linux-amd64 and/or flowd-linux-arm64)
set -eu

STAGE="${1:?usage: dev-flow.sh <dir with flow and flowd-linux-*>}"
DEST=/opt/flow-host
CERT_DIR="${CERT_DIR:-/etc/flow-host/incus}"
ENV_FILE=/etc/flow-host/flow.env

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

# The seal key is made once: flowd tokens sealed with it must stay readable across restarts.
if [ ! -f "$ENV_FILE" ]; then
	umask 077
	{
		echo "BACKEND_ADDR=127.0.0.1:8080"
		echo "BACKEND_AUTH_MODE=dev"
		echo "BACKEND_DB_PATH=/var/lib/flow-host/flow.db"
		echo "BACKEND_SEAL_KEY=$(openssl rand -base64 32)"
		echo "BACKEND_INCUS_URL=https://127.0.0.1:8443"
		echo "BACKEND_INCUS_PROJECT=flow"
		echo "BACKEND_INCUS_CERT=$CERT_DIR/client.crt"
		echo "BACKEND_INCUS_KEY=$CERT_DIR/client.key"
		echo "BACKEND_INCUS_SERVER_CERT=$CERT_DIR/server.crt"
		echo "BACKEND_FLOWD_DIR=$DEST/flowd"
		echo "BACKEND_LOG_LEVEL=info"
	} >"$ENV_FILE"
fi

cat >/etc/systemd/system/flow.service <<EOF
[Unit]
Description=Flow (dev, plain binary)
After=network-online.target incus.service

[Service]
EnvironmentFile=$ENV_FILE
ExecStart=$DEST/flow
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --quiet flow.service
systemctl restart flow.service
echo "dev-flow: $DEST/flow restarted on 127.0.0.1:8080"
