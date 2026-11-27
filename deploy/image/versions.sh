# shellcheck shell=sh disable=SC2034 # read by sandbox-image.sh, provision.sh and check.sh
# Every pinned version of the sandbox base image, one place: a bump edits only this file. Per architecture
# (Debian's names: amd64, arm64) the SHA-256 of each download; the upstream publishes it unless noted.
# Sourced on the host, in the builder and in every sandbox (/usr/local/share/flow/versions.sh).

# Base system: Incus' Debian 13 image (systemd), refreshed upstream daily.
SOURCE_IMAGE=images:debian/13

# Go: go.dev/dl/?mode=json.
GO_VERSION=1.27.4
GO_SHA256_amd64=7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840
GO_SHA256_arm64=3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92

# Node with its npm: nodejs.org/dist/v<version>/SHASUMS256.txt (Node calls amd64 x64).
NODE_VERSION=26.13.1
NPM_VERSION=11.20.0
NODE_SHA256_amd64=5c063e46bab0fbc1c17cc445d7ff129cbdcd0471be6ed83b6ab864f8143bc864
NODE_SHA256_arm64=89e1853138db51e8b82d3e98ba9c0d3f2956a28e5e55ea1b7e12d25aae92d5dd

# Claude Code CLI, the native build: its release bucket's <version>/manifest.json. The version the sidecar's
# Claude Agent SDK names as claudeCodeVersion (sidecar/package-lock.json, the SDK's package.json): bump together.
CLAUDE_VERSION=2.1.332
CLAUDE_SHA256_amd64=a2df1f41bb00250992926420d0063f89b127cb81abdd284dbefb80c50ff1d026
CLAUDE_SHA256_arm64=50c305fcb8912851ce774429131d94ed6f84edbe3d8d0ed7a59dade5d893acc0

# OpenCode v2: GitHub releases of anomalyco/opencode, SHA256SUMS (v2 is not on npm, whose latest is 1.x).
OPENCODE_VERSION=2.4.2
OPENCODE_SHA256_amd64=452912288f934d1771bd5bd0d8611ba3a6adc3db3ac3333c37facf373770ba66
OPENCODE_SHA256_arm64=bf8d18dde87224629341ff398e76c7c17216b9b406affc728b6c1a5c136abf93

# GitHub CLI: gh_<version>_checksums.txt.
GH_VERSION=2.106.0
GH_SHA256_amd64=a476449fa9625413c54a3734eded62e9e480e49f421ff08e7f3b3879c059a033
GH_SHA256_arm64=5e8a0c51e1a8345f36bd0b8bb88604b6a5388df16f3ab3e3b9b6d794fa223a67

# ShellCheck: Debian 13 ships 0.10.0, which fails riptide's scripts (SC2317) where 0.11.0 passes (spike 10).
# GitHub's asset digests.
SHELLCHECK_VERSION=0.11.0
SHELLCHECK_SHA256_amd64=8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198
SHELLCHECK_SHA256_arm64=12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588

# Playwright, installed globally from npm (npm checks the registry's integrity), with its Chromium and WebKit
# builds and their system dependencies (playwright install --with-deps).
PLAYWRIGHT_VERSION=1.65.1

# Docker Engine from Docker's apt repository for trixie. The repository's signing key, checked by its SHA-256
# (fingerprint 9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88, checked once with gpg; Docker publishes the
# fingerprint, not a file hash); apt checks the packages against it.
DOCKER_KEY_SHA256=1500c1f56fa9e26b9b8f42452a553675796ade0807cdce11975eb98170b3a570
DOCKER_VERSION=29.10.1
DOCKER_APT_VERSION=5:29.10.1-1~debian.13~trixie
CONTAINERD_APT_VERSION=2.4.2-4~debian.13~trixie
BUILDX_APT_VERSION=0.39.1-1~debian.13~trixie
COMPOSE_APT_VERSION=5.8.0-1~debian.13~trixie

# From Debian 13 itself (signed by Debian, versions as the point release has them; listed in the inventory):
# git, make, gcc, libc6-dev, curl, jq, python3, ripgrep, unzip, zip, xz-utils, openssh-client, sqlite3,
# postgresql-client (17), universal-ctags, ffmpeg, imagemagick, librsvg2-bin.
DEBIAN_PACKAGES="ca-certificates curl git make gcc libc6-dev jq python3 ripgrep unzip zip xz-utils openssh-client
sqlite3 postgresql-client universal-ctags ffmpeg imagemagick librsvg2-bin procps less file iproute2 iputils-ping"
