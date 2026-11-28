# Deploying Flow

Flow runs as one container (`compose.yaml`, uid 65532, distroless) on a host prepared by `deploy/host-setup.sh`,
behind the host's existing Traefik, signing the owner in through an existing OIDC provider. Everything below runs
as root on the host, from a checkout of this repository.

## Prerequisites

- Debian 13 (systemd, cgroup v2), Docker with Compose 2.24+, firewalld; the rest `deploy/host-setup.sh` checks in
  its preflight (header of that file).
- Traefik with the Docker provider, an entrypoint `websecure`, a certificate for Flow's host name (whichever
  Traefik serves for it: its own wildcard, a file provider's; Flow's router names no certresolver), and an
  external Docker network Traefik reaches containers on (`PROXY_NETWORK`, default `traefik`; it may have IPv6).
- An OIDC provider reachable from inside Flow's container at its issuer URL.
- A DNS name for Flow (`BACKEND_PUBLIC_FQDN`, e.g. `flow.example.com`) pointing at Traefik.

## Sizing a dedicated host

For a host that runs Flow and nothing else, e.g. 4 cores / 8 threads, 32 GB RAM, two NVMe drives of about 450 GB.
The host scripts never format, wipe or mount a disk: the layout is the installer's and the owner's.

**One root filesystem (the usual layout).** Installer partitioning: `/` ext4 on a software RAID 1 (md) across both
drives, about 418 GB, nothing else to spare. The sandboxes' storage pool is then Incus' default, a btrfs pool in a
loop file under `/var/lib/incus/disks/` on `/`, sized with `POOL_SIZE`:

```sh
POOL_SIZE=300GiB sh deploy/host-setup.sh --dry-run
POOL_SIZE=300GiB sh deploy/host-setup.sh
```

300 GiB (about 322 GB) on a 418 GB root leaves room for the system, Docker's images and Flow's data. The file is
sparse: it takes space only as sandboxes write, and the md RAID below mirrors it like everything else on `/`. The
setup refuses, also in the dry run, a `POOL_SIZE` larger than the free space where the file goes (a pool allowed
to outgrow its filesystem would fill `/`). The size is fixed at creation; a later run with another `POOL_SIZE`
changes nothing.

**A partition of its own (the alternative).** On a host with spare partitions the pool can live on a btrfs
filesystem the owner makes and mounts himself, named with `POOL_PATH` (its mount point, or an empty directory on
it). For a mirror across two partitions, e.g.:

```sh
mkfs.btrfs -L flow-pool -d raid1 -m raid1 /dev/nvme0n1p3 /dev/nvme1n1p3
mkdir -p /srv/flow-pool
# /etc/fstab, by the filesystem's UUID (blkid -s UUID -o value /dev/nvme0n1p3):
UUID=<uuid>  /srv/flow-pool  btrfs  user_subvol_rm_allowed,nofail  0  0
```

`user_subvol_rm_allowed` is what Incus mounts its own btrfs pools with (`btrfs.mount_options`, default
`user_subvol_rm_allowed`: [Incus btrfs driver](https://linuxcontainers.org/incus/docs/main/reference/storage_btrfs/)):
it lets unprivileged containers delete the subvolumes they create. For a filesystem given by path Incus mounts
nothing, so the fstab line has to carry it. Then `mount /srv/flow-pool` and
`POOL_PATH=/srv/flow-pool sh deploy/host-setup.sh`, which runs `incus storage create <pool> btrfs
source=/srv/flow-pool` (the existing-filesystem example of Incus' storage pool how-to). It refuses, before any
change, a path that does not exist, is no directory, is not on btrfs, lies on a root filesystem that is not btrfs,
or is not empty; a re-run with the same path changes nothing, another path is refused. The teardown deletes only
the pool (refused while sandboxes use it); the filesystem, its mount and the fstab line stay.

A drive missing at boot: btrfs RAID 1 does not mount degraded unless told, so the mount fails; `nofail` lets the boot
go on, and the pool is unavailable with its sandboxes stopped. Recovery, by hand (`btrfs filesystem show` lists the
missing devid):

```sh
mount -o degraded,user_subvol_rm_allowed /dev/nvme0n1p3 /srv/flow-pool
btrfs replace start <missing devid> /dev/nvme1n1p3 /srv/flow-pool
btrfs balance start -dconvert=raid1,soft -mconvert=raid1,soft /srv/flow-pool
```

The balance converts what was written as single copies while degraded. Nothing mounts degraded automatically: a
silently degraded pool would lose data with the next drive. Not measured: how Incus behaves while the path is an
empty directory on `/` (the mount failed); the setup refuses to run then (not btrfs).

Settings in `.env` for this host:

| Setting | Value |
|---|---|
| `BACKEND_SANDBOX_MEMORY_TOTAL_MIB` | `26624` (26 GiB; 6 GiB stay for the host, Docker and Flow) |
| `BACKEND_SANDBOX_MEMORY_MIB` | `6144` |
| `BACKEND_SANDBOX_MEMORY_HEADROOM_MIB` | default (one sandbox's limit) |
| `BACKEND_SANDBOX_CPUS` | `4` |

Measured: an idle sandbox with one session uses about 0.25 GiB and a full project check peaks near 4 GiB, so the
memory budget holds many idle sandboxes and several builds; the four cores, not memory, limit parallel builds to
two or three.

## 1. Host setup

```sh
sh deploy/host-setup.sh --dry-run
sh deploy/host-setup.sh
```

Creates the sandbox network (`flow` on `flowbr0`), the firewall policies, the Incus project `flow`, Incus' API on
the sandbox network's gateway (`172.30.0.1:8443`, `INCUS_LISTEN`) and Flow's restricted client certificate in
`/etc/flow-host/incus` (`CERT_DIR`), owned by 65532 so the container can read it. Only Flow's container, at its
static address on the sandbox network (`CONTAINER_ADDRESS`, default `172.30.1.2`, set in `compose.yaml`), reaches
the API; every other container is refused (firewalld policy `flow-sb-incus`). Sandboxes have no IPv6: the unit
`flow-sandbox-ipv6.service` keeps the nftables table `bridge flow_sandbox` (drops every IPv6 frame on `flowbr0`)
and `disable_ipv6` on the bridge; other Docker networks, an IPv6 one for Traefik included, are not touched, and
the preflight lists those with IPv6. Settings changed here
(`BACKEND_SANDBOX_*`, `BACKEND_INCUS_POOL`, `BACKEND_INCUS_PROJECT`, `INCUS_LISTEN`, `CONTAINER_ADDRESS`,
`CERT_DIR`, `DOCKER_NETWORK`) must reach `.env` too (step 3). The pool's size and place: `POOL_SIZE`, `POOL_PATH`
("Sizing a dedicated host"). A host set up before the listen address moved to the gateway: run it again, then `.env`'s `BACKEND_INCUS_URL` is `https://<INCUS_LISTEN>`, and `docker compose up -d`
makes a running Flow container again with its static address (until then the firewall refuses its Incus calls).

## 2. Sandbox base image

```sh
sh deploy/sandbox-image.sh
```

Builds the base image and publishes it as `flow-base` (`BACKEND_SANDBOX_IMAGE`). Takes a while; repeat it to
update the tools in new sandboxes.

## 3. Settings

```sh
install -m 0600 deploy/flow.env.example .env
install -d -o 65532 -g 65532 -m 0700 data
```

Fill in `.env` (every setting is described there): `BACKEND_PUBLIC_FQDN`, the OIDC client (step 4), the owner's
subject or group, `BACKEND_SESSION_SECRET` (`openssl rand -hex 32`), `BACKEND_SEAL_KEY` (`openssl rand -base64 32`,
made once; a new key makes stored credentials unreadable), `PROXY_NETWORK` if Traefik's network is not `traefik`.
`./data` holds the database; it must belong to 65532 before the first start, or Docker makes it as root and Flow
cannot open its database.

## 4. OIDC client

Register Flow at the provider (details and claims: `docs/auth.md`):

| Field | Value |
|---|---|
| Client type | confidential (has a secret); authorization code flow with PKCE S256 |
| Redirect URI | `https://<BACKEND_PUBLIC_FQDN>/api/auth/callback`, exactly |
| Scopes | `openid profile email`, plus `groups` when `BACKEND_OIDC_ALLOWED_GROUP` is set |
| Token endpoint auth | `client_secret_basic` (tried first; `client_secret_post` also works) |
| Response type | `code` |

Client ID and secret go into `.env` (`BACKEND_OIDC_CLIENT_ID`, `BACKEND_OIDC_CLIENT_SECRET`), the issuer URL into
`BACKEND_OIDC_ISSUER`.

## 5. Start

```sh
docker compose up -d
docker compose ps
```

`docker compose up -d --build` builds the image from this checkout instead of pulling it. The container is
healthy within seconds; `docker compose logs flow` shows `oidc sign-in` and `listening`. Flow fails to start, and
says why, when a setting is missing or wrong, or when it cannot reach the provider's discovery document.

## 6. First sign-in

Open `https://<BACKEND_PUBLIC_FQDN>/` and sign in. Without a known subject: set only
`BACKEND_OIDC_ALLOWED_GROUP`, or a placeholder subject, sign in once, read `sub` from the log line
`sign-in refused: not the configured owner` (`docker compose logs flow`), put it into
`BACKEND_OIDC_ALLOWED_SUBJECT` and `docker compose up -d` again.

## GitHub App

Private repositories, pushes and `gh` go through a GitHub App the owner registers once; agents then act as the
owner (commits and pull requests are the owner's). Without it only public repositories work. Register at
github.com → Settings → Developer settings → GitHub Apps → New GitHub App:

| Field | Value |
|---|---|
| GitHub App name | anything free, e.g. `flow-<your login>` |
| Homepage URL | `https://<BACKEND_PUBLIC_FQDN>` |
| Callback URL | empty; "Request user authorization (OAuth) during installation" off |
| Expire user authorization tokens | on (8-hour tokens, Flow renews them) |
| Enable Device Flow | on |
| Webhook | "Active" off, no URL |
| Repository permissions | Contents: read and write (clone, push branches); Pull requests: read and write (open and update pull requests); Workflows: read and write (edit `.github/workflows/`); Actions: read-only (read runs and logs); Metadata: read-only (implied). Nothing else, no Administration |
| Organization and account permissions | none |
| Where can this GitHub App be installed? | Only on this account |

Then on the App's page: Install App → your account → "Only select repositories", pick the repositories Flow may
reach (more can be added later; listGitHubRepos and new projects see them at once). Generate neither a client
secret nor a private key: Flow needs neither. Put the App's Client ID (`Iv23…`, not the numeric App ID) into
`.env` as `BACKEND_GITHUB_CLIENT_ID` and `docker compose up -d`; then Settings → GitHub → Connect in Flow, enter
the code on github.com/login/device. The authorization lasts six months from the last renewal; Flow renews it
every 8 hours while it runs. Disconnecting in Flow forgets the tokens; to revoke the authorization on GitHub too:
Settings → Applications → Authorized GitHub Apps.

## Update

```sh
git pull --ff-only
docker compose pull && docker compose up -d
```

(`docker compose up -d --build` when building from the checkout.) Sandboxes keep running across Flow's restart;
Flow pushes its own flowd into each sandbox when the versions differ. After a change to `deploy/`, run
`deploy/host-setup.sh` again (it completes what is missing) and `deploy/sandbox-image.sh` for new base images.

## Tear down

```sh
docker compose down
sh deploy/host-teardown.sh --dry-run
sh deploy/host-teardown.sh --delete-sandboxes
```

The teardown refuses while a container is attached to the sandbox network (`docker compose down` first) and while
sandboxes exist unless `--delete-sandboxes` is given. It removes only what host-setup created; `./data`, `.env` and
the image `flow-base` stay (`incus image delete flow-base`).
