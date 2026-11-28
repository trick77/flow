#!/bin/sh
# Checks a sandbox made from the base image, as the agent user, the way Flow's units run: no login shell. Baked
# into the image at /usr/local/share/flow/check.sh; from the host:
#
#   incus exec --project flow <sandbox> -- systemd-run --quiet --wait --pipe --collect --uid=flow \
#     -p MemorySwapMax=0 sh /usr/local/share/flow/check.sh </dev/null
#
# Each tool must start and report its pinned version (versions.sh beside it); Docker must run a container that
# reaches the internet through the nested NAT; Chromium must launch headless with its own sandbox and take a
# screenshot, WebKit must launch; a Go program must build with the race detector; npm ci must install. Prints one
# line per check, exits 1 if any failed. Needs the internet (Docker Hub, the npm registry, example.com).
#
#   sh check.sh --versions    only the tools' versions against the pins, as any user, no network and no Docker
#                             daemon: provision.sh runs it in the builder and fails the build on a mismatch.
set -u

SHARE="$(dirname "$0")"
# shellcheck source=deploy/image/versions.sh
. "$SHARE/versions.sh"

FAILED=0
ok() { printf 'ok    %-16s %s\n' "$1" "$2"; }
bad() {
	printf 'FAIL  %-16s %s\n' "$1" "$2"
	FAILED=1
}
# want NAME PINNED COMMAND...: the command must exit 0 and its first output line must hold the pinned version as
# a whole word (a leading v stripped): 1.65.1 is not 1.65.10, go1.27.4 not go1.27.40.
want() {
	name="$1" pin="$2"
	shift 2
	all="$("$@" 2>&1)"
	status=$?
	if [ "$status" != 0 ]; then
		bad "$name" "exit status $status: $(printf '%s\n' "$all" | head -n 1)"
		return
	fi
	out="$(printf '%s\n' "$all" | head -n 1)"
	if printf '%s\n' "$out" | tr -s ' \t,()' '\n' | sed 's/^v\([0-9]\)/\1/' | grep -qxF "$pin"; then
		ok "$name" "$out"
	else
		bad "$name" "want $pin, got: ${out:-nothing}"
	fi
}
# versions checks every pinned tool; the Docker daemon's version only outside --versions.
versions() {
	want go "go$GO_VERSION" go version
	want node "$NODE_VERSION" node --version
	want node-usr-bin "$NODE_VERSION" /usr/bin/node --version
	want npm "$NPM_VERSION" npm --version
	want claude "$CLAUDE_VERSION" /usr/local/bin/claude --version
	want opencode "$OPENCODE_VERSION" opencode --version
	want gh "$GH_VERSION" gh --version
	want shellcheck "$SHELLCHECK_VERSION" sh -c 'shellcheck --version | grep "^version:"'
	want playwright "$PLAYWRIGHT_VERSION" playwright --version
	# docker --version: the client alone, it never dials the daemon (docker version would, and waits on the socket).
	want docker "$DOCKER_VERSION" docker --version
	want containerd "${CONTAINERD_APT_VERSION%%-*}" containerd --version
	want compose "${COMPOSE_APT_VERSION%%-*}" docker compose version
	want buildx "${BUILDX_APT_VERSION%%-*}" docker buildx version
	browsers="${PLAYWRIGHT_BROWSERS_PATH:-/opt/ms-playwright}"
	for b in chromium chromium_headless_shell webkit; do
		is "browser" "$b" "no $b build in $browsers" sh -c "ls -d '$browsers/$b'-* >/dev/null 2>&1"
	done
}
# is NAME GOOD BAD TEST...: ok with GOOD when the test holds, else a failure with BAD.
is() {
	name="$1" good="$2" fail="$3"
	shift 3
	if "$@"; then ok "$name" "$good"; else bad "$name" "$fail"; fi
}
writable_dir() { [ -d "$1" ] && [ -w "$1" ]; }
# runs NAME COMMAND...: the command must exit 0; its last line is shown.
runs() {
	name="$1"
	shift
	if out="$("$@" 2>&1)"; then
		ok "$name" "$(printf '%s\n' "$out" | grep . | tail -n 1)"
	else
		bad "$name" "$(printf '%s\n' "$out" | tail -n 3 | tr '\n' ' ')"
	fi
}

if [ "${1:-}" = --versions ]; then
	versions
	if [ "$FAILED" = 0 ]; then echo "versions match the pins"; else exit 1; fi
	exit 0
fi

WORK="$(mktemp -d /tmp/flow-image-check.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

# Who and where: the agent user, in the docker group, with the image's environment. The shared caches come from
# the agent's own go and npm configuration (not the environment), so they hold in any of its processes.
is user "$(id)" "not uid 65532: $(id)" [ "$(id -u)" = 65532 ]
groups="$(id -nG)"
is docker-group "member" "flow is not in docker: $groups" sh -c 'id -nG | tr " " "\n" | grep -qx docker'
for v in PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright DISABLE_AUTOUPDATER=1 OPENCODE_DISABLE_AUTOUPDATE=1; do
	have="$(printenv "${v%%=*}")"
	is env "$v" "${v%%=*}=${have:-unset}, want ${v#*=}" [ "$have" = "${v#*=}" ]
done
have="$(go env GOMODCACHE)"
is go-modcache "$have" "$have" [ "$have" = /srv/flow-home/cache/go-mod ]
have="$(npm config get cache)"
is npm-cache "$have" "$have" [ "$have" = /srv/flow-home/cache/npm ]
# Every home-volume directory layout names (CLAUDE_CONFIG_DIR, XDG_CONFIG_HOME, XDG_DATA_HOME, the config
# checkout, the caches), the browsers and the home.
for d in /srv/flow-home/claude /srv/flow-home/config /srv/flow-home/data /srv/flow-home/agent-config \
	/srv/flow-home/cache /srv/flow-home/cache/go-mod /srv/flow-home/cache/npm /opt/ms-playwright "$HOME"; do
	is writable "$d" "$d is missing or not writable for $(id -un)" writable_dir "$d"
done

versions
want docker-server "$DOCKER_VERSION" docker version --format '{{.Server.Version}}'
is snapshotter "containerd snapshotter, overlayfs" "$(docker info --format '{{index .DriverStatus 0 1}} {{.Driver}}' 2>&1)" \
	sh -c 'docker info --format "{{index .DriverStatus 0 1}} {{.Driver}}" | grep -qx "io.containerd.snapshotter.v1 overlayfs"'
runs git git --version
runs make make --version
runs gcc gcc --version
runs curl curl --version
runs jq jq --version
runs python3 python3 --version
runs ripgrep rg --version
runs unzip sh -c 'unzip -v | head -n 1'
runs ssh ssh -V
runs psql psql --version
runs sqlite3 sqlite3 --version
runs ctags sh -c 'ctags --version | grep "Universal Ctags"'
runs ffmpeg ffmpeg -version
runs imagemagick magick -version
runs rsvg-convert rsvg-convert --version

# Docker: pull, run, and the container reaches the internet (DNS and HTTPS) through the sandbox's NAT.
# An image the project already has stays; only one this check pulled is removed again.
had_alpine=0
docker image inspect alpine:3 >/dev/null 2>&1 && had_alpine=1
runs docker-run docker run --rm alpine:3 sh -c 'wget -q -O /dev/null https://example.com && echo "example.com reached from a container"'
if [ "$had_alpine" = 0 ]; then
	docker image rm alpine:3 >/dev/null 2>&1 || true
fi

# Go with the race detector (cgo: gcc and libc headers).
mkdir -p "$WORK/race"
cat >"$WORK/race/main.go" <<'EOF'
package main

import (
	"fmt"
	"sync"
)

func main() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	n := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); mu.Lock(); n++; mu.Unlock() }()
	}
	wg.Wait()
	fmt.Println("race build ran, n =", n)
}
EOF
printf 'module example.com/race\n\ngo 1.27\n' >"$WORK/race/go.mod"
runs go-race sh -c "cd '$WORK/race' && go build -race -o race . && ./race"

# npm ci against the registry and the shared cache.
mkdir -p "$WORK/npm"
printf '{"name":"flow-image-check","version":"1.0.0","private":true,"dependencies":{"ms":"2.1.3"}}\n' >"$WORK/npm/package.json"
runs npm-ci sh -c "cd '$WORK/npm' && npm install --package-lock-only --no-audit --no-fund --loglevel=error && npm ci --no-audit --no-fund --loglevel=error && node -e 'console.log(\"npm ci: ms\", require(\"ms/package.json\").version)'"

# Browsers through the global Playwright: Chromium headless with its sandbox on (refuses to start without a
# usable one) and a screenshot; WebKit launches and renders.
cat >"$WORK/browsers.cjs" <<'EOF'
const { chromium, webkit } = require("playwright");
const fs = require("fs");
(async () => {
  const shot = process.argv[2];
  const c = await chromium.launch({ headless: true, chromiumSandbox: true });
  const p = await c.newPage();
  await p.goto("https://example.com");
  await p.screenshot({ path: shot });
  console.log(`chromium ${c.version()} sandboxed, title "${await p.title()}", screenshot ${fs.statSync(shot).size} bytes`);
  await c.close();
  const w = await webkit.launch();
  const wp = await w.newPage();
  await wp.setContent("<h1>webkit</h1>");
  console.log(`webkit ${w.version()} rendered "${await wp.textContent("h1")}"`);
  await w.close();
})().catch((e) => { console.error(e.message || e); process.exit(1); });
EOF
if out="$(cd "$WORK" && NODE_PATH="$(npm root -g)" node browsers.cjs "$WORK/shot.png" 2>&1)"; then
	printf '%s\n' "$out" | while IFS= read -r l; do ok browsers "$l"; done
else
	bad browsers "$(printf '%s\n' "$out" | tail -n 5 | tr '\n' ' ')"
fi
# The renderer runs in Chromium's namespace sandbox, which needs unprivileged user namespaces.
runs userns unshare --user --map-root-user true

if [ "$FAILED" = 0 ]; then
	echo "image check passed"
else
	echo "image check FAILED"
	exit 1
fi
