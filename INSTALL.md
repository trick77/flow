# Installing Flow

The short path from a prepared Linux host to the first session. Every step runs as root on the host, in the
directory Flow is installed in, unless it says otherwise. The reasons, the alternatives and every setting are in
[`docs/deploy.md`](docs/deploy.md); sign-in is in [`docs/auth.md`](docs/auth.md).

## Before you start

| Needed | Notes |
|---|---|
| Debian 13, Docker with Compose 2.24+, firewalld | The setup's preflight checks the rest and refuses before changing anything. |
| Traefik on that host | Docker provider, entrypoint `websecure`, a certificate that covers Flow's host name, an external Docker network (default name `traefik`; it may have IPv6). |
| An OIDC provider | Reachable from inside Flow's container. |
| A DNS name for Flow | For example `flow.example.com`, pointing at Traefik. |
| A Claude credential | From `claude setup-token`; entered in Flow after the first sign-in. |

## 1. Get the files

Flow needs `compose.yaml`, `deploy/` and the documents, not the source. A sparse checkout keeps the rest away and
still updates with `git pull`. It works in a directory that already holds an `.env`:

```sh
git init -q
git remote add origin https://github.com/trick77/flow
git sparse-checkout set --no-cone '/compose.yaml' '/deploy/' '/docs/' '/INSTALL.md'
git fetch -q --filter=blob:none origin master
git checkout -q -b master origin/master
```

## 2. Storage for the sandboxes (optional)

Without this step the sandboxes live in a file on the root filesystem, sized with `POOL_SIZE` (step 3). To give
them partitions of their own, make and mount a btrfs filesystem yourself and name its mount point with `POOL_PATH`.
The setup never formats or mounts anything. A mirror across two partitions:

```sh
mkfs.btrfs -L flow-pool -d raid1 -m raid1 /dev/<part1> /dev/<part2>
mkdir -p /srv/flow-pool
echo 'LABEL=flow-pool /srv/flow-pool btrfs user_subvol_rm_allowed,nofail,x-systemd.device-timeout=30 0 0' >> /etc/fstab
systemctl daemon-reload && mount /srv/flow-pool
```

`mkfs.btrfs` erases what the partitions hold: check the device names with `lsblk` first. The mount option
`user_subvol_rm_allowed` is required; "Sizing a dedicated host" in `docs/deploy.md` says why and how to recover
from a missing drive.

## 3. Prepare the host

```sh
POOL_PATH=/srv/flow-pool sh deploy/host-setup.sh --dry-run
POOL_PATH=/srv/flow-pool sh deploy/host-setup.sh
```

Leave `POOL_PATH` out without step 2, and pass `POOL_SIZE=300GiB` (or what fits) instead. The dry run changes
nothing and prints every command it would run; read it before the real run. The real run installs Incus, makes
the sandbox network and the firewall rules, and ends with a list of what changed: `0 unexpected` is the line to
look for. Running it again changes nothing.

It refuses, before any change, when the firewall's `FORWARD` chain jumps to chains that are not Docker's, and asks
once whether one of them must stay first: answer with `BAN_CHAIN=<name>` or `BAN_CHAIN=` on that run.

To undo everything it made: `sh deploy/host-teardown.sh` (after `docker compose down`).

## 4. Build the sandbox image

```sh
sh deploy/sandbox-image.sh
```

A few minutes. It ends with `done ... flow-base`. Run it again later to refresh the tools in new sandboxes.

## 5. Register Flow at the OIDC provider

| Field | Value |
|---|---|
| Client type | Confidential, authorization code flow with PKCE (S256) |
| Redirect URI | `https://<your host name>/api/auth/callback`, exactly |
| Scopes | `openid profile email`, plus `groups` if a group admits the owner |
| Token endpoint auth | `client_secret_basic` |

Groups must be in the ID token itself. Keep the client ID and the secret for the next step.

## 6. Settings

```sh
install -m 0600 deploy/flow.env.example .env
install -d -o 65532 -g 65532 -m 0700 data
```

Fill in `.env`. The lines a first installation needs:

```sh
BACKEND_PUBLIC_FQDN=flow.example.com
BACKEND_OIDC_ISSUER=https://auth.example.com
BACKEND_OIDC_CLIENT_ID=flow
BACKEND_OIDC_CLIENT_SECRET='...'
BACKEND_OIDC_ALLOWED_GROUP=Flow          # or BACKEND_OIDC_ALLOWED_SUBJECT=<sub>
BACKEND_SESSION_SECRET=...               # openssl rand -hex 32
BACKEND_SEAL_KEY='...'                   # openssl rand -base64 32
BACKEND_INCUS_URL=https://172.30.0.1:8443
```

- Put values that hold `$` or ` #` in single quotes, or Compose rewrites them. That means the client secret and the
  seal key.
- Make `BACKEND_SEAL_KEY` once and keep a copy. A new key makes every stored credential unreadable.
- `data` must belong to 65532 before the first start, or Docker makes it as root and Flow cannot open its database.
- Memory and CPU limits for the sandboxes have defaults; "Sizing a dedicated host" gives values for a larger host.

## 7. Start

```sh
docker compose pull
docker compose up -d
docker compose logs flow | tail -20
```

The log shows `oidc sign-in` and `listening`. Flow refuses to start, and says why, when a setting is missing or the
provider's discovery document cannot be read.

## 8. Sign in and run a session

1. Open `https://<your host name>/` and sign in.
2. Settings: store the Claude credential.
3. Create a project with a public repository and start a session. The first start builds the project's sandbox and
   takes longer than later ones.

Private repositories, pushes and `gh` need a GitHub App that you register once: "GitHub App" in `docs/deploy.md`.

## Updating

```sh
git pull --ff-only
docker compose pull && docker compose up -d
```

Sandboxes keep running across the restart. After a change under `deploy/`, run `deploy/host-setup.sh` again (it
completes what is missing) and `deploy/sandbox-image.sh` for new base images.

## When something does not work

| What you see | Cause and way out |
|---|---|
| `docker compose pull` answers `denied` | The image package is not public, or the host is not logged in to the registry. |
| Flow exits at start naming a setting | Add it to `.env`, then `docker compose up -d`. |
| Flow cannot open its database | `data` belongs to root: `chown 65532:65532 data`. |
| Sign-in at the provider works, Flow says it did not | `docker compose logs flow` has the line `sign-in refused` with the reason; the subject or group in `.env` does not match. |
| The setup stops with `refusing:` | Nothing was changed. The message names what it found and what to pass. |
| The setup ends with unexpected differences | Do not run it again. Keep the output; `sh deploy/host-teardown.sh --dry-run` shows what a reversal would remove. |
| The proxy answers 404 for Flow's host name | Flow is not running, or it is not on the proxy's network (`PROXY_NETWORK` in `.env`). |
