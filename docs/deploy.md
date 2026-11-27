# Deploying Flow

Flow runs as one container (`compose.yaml`, uid 65532, distroless) on a host prepared by `deploy/host-setup.sh`,
behind the host's existing Traefik, signing the owner in through an existing OIDC provider. Everything below runs
as root on the host, from a checkout of this repository.

## Prerequisites

- Debian 13 (systemd, cgroup v2), Docker with Compose 2.24+, firewalld; the rest `deploy/host-setup.sh` checks in
  its preflight (header of that file).
- Traefik with the Docker provider, an entrypoint `websecure`, a certificate (file provider) that covers Flow's
  host name, and an external Docker network Traefik reaches containers on (`PROXY_NETWORK`, default `traefik`).
- An OIDC provider reachable from inside Flow's container at its issuer URL.
- A DNS name for Flow (`BACKEND_PUBLIC_FQDN`, e.g. `flow.example.com`) pointing at Traefik.

## 1. Host setup

```sh
sh deploy/host-setup.sh --dry-run
sh deploy/host-setup.sh
```

Creates the sandbox network (`flow` on `flowbr0`), the firewall policies, the Incus project `flow`, Incus' API on
the sandbox network's gateway (`172.30.0.1:8443`, `INCUS_LISTEN`) and Flow's restricted client certificate in
`/etc/flow-host/incus` (`CERT_DIR`), owned by 65532 so the container can read it. Only Flow's container, at its
static address on the sandbox network (`CONTAINER_ADDRESS`, default `172.30.1.2`, set in `compose.yaml`), reaches
the API; every other container is refused (firewalld policy `flow-sb-incus`). Settings changed here
(`BACKEND_SANDBOX_*`, `BACKEND_INCUS_POOL`, `BACKEND_INCUS_PROJECT`, `INCUS_LISTEN`, `CONTAINER_ADDRESS`,
`CERT_DIR`, `DOCKER_NETWORK`) must reach `.env` too (step 3). A host set up before the listen address moved to the
gateway: run it again, then `.env`'s `BACKEND_INCUS_URL` is `https://<INCUS_LISTEN>`, and `docker compose up -d`
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
