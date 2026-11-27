#!/bin/sh
# Runs inside the builder instance as root (sandbox-image.sh pushes it with versions.sh and check.sh into
# /usr/local/share/flow/ and runs it once): installs the toolchain, the agent user and the environment, then
# writes the inventory. Not for a running sandbox.
set -eu

SHARE=/usr/local/share/flow
# shellcheck source=deploy/image/versions.sh
. "$SHARE/versions.sh"

# Flow's paths (backend/internal/layout): agent user and uid, the home volume's caches.
AGENT=flow
AGENT_UID=65532
HOME_DIR=/home/flow
HOME_VOLUME=/srv/flow-home
CLAUDE_CONFIG=$HOME_VOLUME/claude
XDG_CONFIG=$HOME_VOLUME/config
XDG_DATA=$HOME_VOLUME/data
AGENT_CONFIG=$HOME_VOLUME/agent-config
GO_MOD_CACHE=$HOME_VOLUME/cache/go-mod
NPM_CACHE=$HOME_VOLUME/cache/npm
# layout.PlaywrightDir: browsers live in the image, the home volume is not there at build time.
BROWSERS=/opt/ms-playwright

ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
amd64) NODE_ARCH=x64 CLAUDE_ARCH=linux-x64 OPENCODE_ARCH=linux-x64 SC_ARCH=x86_64 ;;
arm64) NODE_ARCH=arm64 CLAUDE_ARCH=linux-arm64 OPENCODE_ARCH=linux-arm64 SC_ARCH=aarch64 ;;
*)
	echo "provision: unsupported architecture $ARCH" >&2
	exit 1
	;;
esac
pinned() { eval "printf '%s' \"\${$1_SHA256_$ARCH}\""; }

step() { printf '%s provision: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
DL=/var/tmp/flow-downloads
rm -rf "$DL"
mkdir -p "$DL"
# fetch URL SHA256 FILE: download into $DL and refuse a file whose hash differs.
fetch() {
	curl -fsSL --retry 3 --retry-delay 2 -o "$DL/$3" "$1"
	printf '%s  %s\n' "$2" "$DL/$3" | sha256sum -c --quiet - || {
		echo "provision: checksum mismatch for $1" >&2
		exit 1
	}
}

export DEBIAN_FRONTEND=noninteractive
# No service starts while packages install (Docker's would fail in the builder, and none should run there);
# installs still enable their units. Removed again at the end.
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod 0755 /usr/sbin/policy-rc.d
# Never install documentation (copyright files stay).
cat >/etc/dpkg/dpkg.cfg.d/01-flow-nodoc <<'EOF'
path-exclude=/usr/share/doc/*
path-include=/usr/share/doc/*/copyright
path-exclude=/usr/share/man/*
path-exclude=/usr/share/info/*
EOF
cat >/etc/apt/apt.conf.d/01-flow-norecommends <<'EOF'
APT::Install-Recommends "false";
APT::Install-Suggests "false";
EOF

step "Debian packages"
apt-get update -qq
# shellcheck disable=SC2086 # the package list is meant to split
apt-get install -y -qq $DEBIAN_PACKAGES >/dev/null

step "Docker Engine $DOCKER_VERSION from Docker's repository"
install -d -m 0755 /etc/apt/keyrings
curl -fsSL --retry 3 -o /etc/apt/keyrings/docker.asc https://download.docker.com/linux/debian/gpg
printf '%s  %s\n' "$DOCKER_KEY_SHA256" /etc/apt/keyrings/docker.asc | sha256sum -c --quiet -
chmod 0644 /etc/apt/keyrings/docker.asc
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable\n' \
	"$ARCH" >/etc/apt/sources.list.d/docker.list
apt-get update -qq
apt-get install -y -qq "docker-ce=$DOCKER_APT_VERSION" "docker-ce-cli=$DOCKER_APT_VERSION" \
	"containerd.io=$CONTAINERD_APT_VERSION" "docker-buildx-plugin=$BUILDX_APT_VERSION" \
	"docker-compose-plugin=$COMPOSE_APT_VERSION" >/dev/null
apt-mark hold docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
# Inside a sandbox (an unprivileged Incus container with security.nesting): containerd's snapshotter gives native
# overlayfs on the sandbox's root filesystem (spike 4: no fuse-overlayfs or vfs fallback, no warnings). No IPv6:
# sandboxes have none. The local log driver rotates, so a chatty test container cannot fill the disk.
install -d -m 0755 /etc/docker
cat >/etc/docker/daemon.json <<'EOF'
{
  "features": { "containerd-snapshotter": true },
  "ipv6": false,
  "ip6tables": false,
  "log-driver": "local"
}
EOF
systemctl enable --quiet docker.service containerd.service

step "agent user $AGENT ($AGENT_UID) and Flow's directories"
getent group "$AGENT" >/dev/null || groupadd --gid "$AGENT_UID" "$AGENT"
# UID_MAX is 60000 on Debian; 65532 is deliberate (layout.AgentUID): useradd's warning about it is dropped, any
# other output is shown.
if ! id "$AGENT" >/dev/null 2>&1; then
	out="$(useradd --uid "$AGENT_UID" --gid "$AGENT_UID" --create-home --home-dir "$HOME_DIR" --shell /bin/bash \
		"$AGENT" 2>&1)" || {
		printf 'provision: useradd %s failed: %s\n' "$AGENT" "$out" >&2
		exit 1
	}
	printf '%s\n' "$out" | grep -v -e '^$' -e 'outside of the UID_MIN' >&2 || true
fi
# The docker group is root in the sandbox (spike 10): accepted, the sandbox is the boundary.
usermod -aG docker "$AGENT"
install -d -m 0755 /opt/flow/releases /var/lib/flow
# /etc/flow holds flowd.json (root 0600, pushed by Flow) and the Claude settings layer, which the agent user's CLI
# reads: the directory itself must be searchable.
install -d -m 0755 /etc/flow
install -d -o "$AGENT" -g "$AGENT" -m 0755 "$HOME_DIR/work" "$HOME_DIR/work/.sessions"
# The home volume's mount point with every directory layout names on it (claude.Env points CLAUDE_CONFIG_DIR,
# XDG_CONFIG_HOME and XDG_DATA_HOME there in every session), agent-owned, so a sandbox works without the volume too.
# A volume mounted over it hides these: a new custom volume's root is root 0711 (measured), so whoever attaches it
# must create the same directories on it, owned 65532:65532.
install -d -m 0755 "$HOME_VOLUME"
install -d -o "$AGENT" -g "$AGENT" -m 0700 "$CLAUDE_CONFIG"
install -d -o "$AGENT" -g "$AGENT" -m 0755 "$XDG_CONFIG" "$XDG_DATA" "$AGENT_CONFIG" "$HOME_VOLUME/cache" \
	"$GO_MOD_CACHE" "$NPM_CACHE"

step "Go $GO_VERSION"
fetch "https://go.dev/dl/go$GO_VERSION.linux-$ARCH.tar.gz" "$(pinned GO)" go.tar.gz
rm -rf /usr/local/go
tar -C /usr/local -xzf "$DL/go.tar.gz"
# Go's own test suite; nothing builds against it.
rm -rf /usr/local/go/test
ln -sf /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/

step "Node $NODE_VERSION"
fetch "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-$NODE_ARCH.tar.xz" "$(pinned NODE)" node.tar.xz
# Into /usr/local itself: node, npm and npx on the default PATH, npm's global prefix /usr/local. Headers stay
# (node-gyp builds against them).
tar -C /usr/local --strip-components=1 --no-same-owner -xJf "$DL/node.tar.xz" \
	--exclude='*/share/doc' --exclude='*/share/man' --exclude='*/CHANGELOG.md' --exclude='*/README.md'
# Flow's units and the sidecar find Node at /usr/bin/node (layout).
ln -sf /usr/local/bin/node /usr/bin/node

step "Claude Code $CLAUDE_VERSION"
fetch "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/$CLAUDE_VERSION/$CLAUDE_ARCH/claude" \
	"$(pinned CLAUDE)" claude
install -m 0755 "$DL/claude" /usr/local/bin/claude

step "OpenCode $OPENCODE_VERSION"
fetch "https://github.com/anomalyco/opencode/releases/download/v$OPENCODE_VERSION/opencode-$OPENCODE_ARCH.tar.gz" \
	"$(pinned OPENCODE)" opencode.tar.gz
mkdir -p "$DL/opencode"
tar -C "$DL/opencode" -xzf "$DL/opencode.tar.gz"
install -m 0755 "$(find "$DL/opencode" -type f -name opencode | head -n 1)" /usr/local/bin/opencode

step "GitHub CLI $GH_VERSION"
fetch "https://github.com/cli/cli/releases/download/v$GH_VERSION/gh_${GH_VERSION}_linux_$ARCH.tar.gz" "$(pinned GH)" gh.tar.gz
tar -C "$DL" -xzf "$DL/gh.tar.gz"
install -m 0755 "$DL/gh_${GH_VERSION}_linux_$ARCH/bin/gh" /usr/local/bin/gh

step "shellcheck $SHELLCHECK_VERSION"
fetch "https://github.com/koalaman/shellcheck/releases/download/v$SHELLCHECK_VERSION/shellcheck-v$SHELLCHECK_VERSION.linux.$SC_ARCH.tar.xz" \
	"$(pinned SHELLCHECK)" shellcheck.tar.xz
tar -C "$DL" -xJf "$DL/shellcheck.tar.xz"
install -m 0755 "$DL/shellcheck-v$SHELLCHECK_VERSION/shellcheck" /usr/local/bin/shellcheck

step "Playwright $PLAYWRIGHT_VERSION with Chromium and WebKit"
npm_config_cache="$DL/npm" npm install -g --no-fund --no-audit --loglevel=error "playwright@$PLAYWRIGHT_VERSION"
install -d -m 0755 "$BROWSERS"
PLAYWRIGHT_BROWSERS_PATH="$BROWSERS" playwright install --with-deps chromium webkit >"$DL/playwright.log" 2>&1 || {
	tail -n 40 "$DL/playwright.log" >&2
	exit 1
}
# The agent may add browser builds of other Playwright versions here (a repository pinning another one).
chown -R "$AGENT:$AGENT" "$BROWSERS"

step "environment"
# For every user and every way a process starts: systemd units (flowd's run as the agent user without a shell)
# through DefaultEnvironment, PAM sessions (su, login) through /etc/environment, other shells through profile.d.
# Every tool sits in /usr/local/bin, on the default PATH of all three; PATH only adds go install's directory.
# PLAYWRIGHT_BROWSERS_PATH is per sandbox, not shared, so root may see it too.
ENVIRONMENT="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$HOME_DIR/go/bin
LANG=C.UTF-8
PLAYWRIGHT_BROWSERS_PATH=$BROWSERS
DISABLE_AUTOUPDATER=1
OPENCODE_DISABLE_AUTOUPDATE=1"
printf '%s\n' "$ENVIRONMENT" >/etc/environment
install -d -m 0755 /etc/systemd/system.conf.d
{
	printf '# Flow: the sandbox environment for every unit (same list as /etc/environment).\n[Manager]\nDefaultEnvironment='
	printf '%s\n' "$ENVIRONMENT" | sed 's/.*/"&"/' | tr '\n' ' ' | sed 's/ $//'
	printf '\n'
} >/etc/systemd/system.conf.d/flow-environment.conf
# The shared caches only for the agent user: root's go and npm keep their own under /root, so nothing root runs
# leaves root-owned files in the caches every sandbox shares. Set in the agent's own tool configuration, which go
# and npm read in any process of that user (units without these variables included), and for its shells.
install -d -o "$AGENT" -g "$AGENT" -m 0755 "$HOME_DIR/.config" "$HOME_DIR/.config/go"
printf 'GOMODCACHE=%s\n' "$GO_MOD_CACHE" >"$HOME_DIR/.config/go/env"
printf 'cache=%s\n' "$NPM_CACHE" >"$HOME_DIR/.npmrc"
chown "$AGENT:$AGENT" "$HOME_DIR/.config/go/env" "$HOME_DIR/.npmrc"
cat >/etc/profile.d/flow-environment.sh <<EOF
# Flow: the sandbox environment for shells that do not come through PAM (incus exec bash -l, terminals); the
# shared caches only for the agent user ($AGENT_UID).
set -a
. /etc/environment
set +a
if [ "\$(id -u)" = $AGENT_UID ]; then
	export GOMODCACHE=$GO_MOD_CACHE npm_config_cache=$NPM_CACHE
fi
EOF
install -d -m 0755 /etc/systemd/journald.conf.d
printf '[Journal]\nSystemMaxUse=64M\n' >/etc/systemd/journald.conf.d/flow.conf

step "inventory"
INVENTORY="$SHARE/inventory"
first() { env DISABLE_AUTOUPDATER=1 OPENCODE_DISABLE_AUTOUPDATE=1 "$@" 2>&1 | head -n 1; }
{
	printf 'tool\tpinned\treported\n'
	printf 'debian\t%s\t%s\n' "13" "$(cat /etc/debian_version)"
	printf 'go\t%s\t%s\n' "$GO_VERSION" "$(first go version)"
	printf 'node\t%s\t%s\n' "$NODE_VERSION" "$(first node --version)"
	printf 'npm\t%s\t%s\n' "$NPM_VERSION" "$(first npm --version)"
	printf 'claude\t%s\t%s\n' "$CLAUDE_VERSION" "$(HOME=/tmp first claude --version)"
	printf 'opencode\t%s\t%s\n' "$OPENCODE_VERSION" "$(HOME=/tmp first opencode --version)"
	printf 'gh\t%s\t%s\n' "$GH_VERSION" "$(first gh --version)"
	printf 'shellcheck\t%s\t%s\n' "$SHELLCHECK_VERSION" "$(shellcheck --version | awk '/^version:/ { print $2 }')"
	printf 'playwright\t%s\t%s\n' "$PLAYWRIGHT_VERSION" "$(first playwright --version)"
	for b in "$BROWSERS"/*/; do
		printf 'browser\t-\t%s\n' "$(basename "$b")"
	done
	printf 'docker\t%s\t%s\n' "$DOCKER_VERSION" "$(first docker --version)"
	printf 'containerd\t%s\t%s\n' "$CONTAINERD_APT_VERSION" "$(first containerd --version)"
	printf 'docker-compose\t%s\t%s\n' "$COMPOSE_APT_VERSION" "$(first docker compose version)"
	printf 'docker-buildx\t%s\t%s\n' "$BUILDX_APT_VERSION" "$(first docker buildx version)"
	# shellcheck disable=SC2086 # the package list is meant to split
	dpkg-query -W -f='${Package}\tdebian\t${Version}\n' $DEBIAN_PACKAGES
} >"$INVENTORY"
chmod 0644 "$INVENTORY"

# The inventory only records; this refuses: every tool must start and report exactly its pin (check.sh's own
# comparison), else the build fails here and nothing is published.
step "versions against the pins"
HOME=/tmp sh "$SHARE/check.sh" --versions || {
	echo "provision: a tool does not start or does not report its pinned version (above)" >&2
	exit 1
}

step "clean-up"
rm -f /usr/sbin/policy-rc.d
apt-get clean
rm -rf "$DL" /var/lib/apt/lists/* /var/cache/apt/*.bin /var/cache/debconf/*-old /var/lib/dpkg/*-old \
	/var/log/apt/* /root/.npm /root/.cache /root/.config /root/.local /root/.claude* /tmp/* /tmp/.[!.]* \
	/var/tmp/* /usr/local/share/doc /usr/local/share/man
find /var/log -type f -name '*.log' -exec truncate -s 0 {} +
cat "$INVENTORY"
