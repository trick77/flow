# flow

Self-hosted: runs Claude Code and OpenCode sessions in sandboxes on a Linux server, operated from the browser.

## Components
- `backend/cmd/flow`: server. JSON API, later auth, projects, sessions; serves the embedded UI.
- `backend/cmd/flowd`: supervisor inside each sandbox's Linux system container, owns the agent processes. Same
  module as the server; the image carries it for linux/amd64 + arm64, the server pushes the matching one in.
- `ui/`: React SPA, built into `backend/web/dist`, embedded in the server binary.
- `sidecar/`: Node package, will host the Claude Agent SDK and translate its messages into Flow's event stream.
  Not in Dependabot: its deps are bumped by hand.

## Contracts
- `docs/architecture.md` (processes, failures, credentials), `docs/events.md` (event model, provider mappings),
  `docs/ownership.md` (who edits what, build order).
- `docs/schema.md` = `TestSchemaDoc` golden (in `backend/`: `SCHEMADOC_UPDATE=1 go test ./... -run TestSchemaDoc`);
  regen after a migration or model.go change. model.go holds only row types, each with its `Table:` line.
- Event model source `contract/events.schema.json`, API source `backend/api/openapi.json`; `*.gen.go`/`*.gen.ts`
  come from `internal/contractgen` (`CONTRACTGEN_UPDATE=1 go test ./internal/contractgen/`), never by hand.
  The generator reads a JSON Schema subset and refuses the rest on purpose.
- Every API route is registered in `internal/httpapi/routes.go`, checked against the spec; areas implement their
  interface in `areas.go` in their own package. Unwired areas and an unwired Guard answer `not_implemented`.

## Working conventions
- Commit subjects + PR titles: Go style `area: what` (`server: …`, `flowd: …`, `ui: …`, `sidecar: …`), gated by ship; release notes list them by area.
- Fresh clone: `ship hooks` before the first push (check, conform and init install it too); without the pre-push hook a push skips the gate.
- Env vars: `BACKEND_*`, never the project name (a rename then touches no config). Host-only names Flow never reads (`PROXY_NETWORK`, `DOCKER_NETWORK`, `INCUS_CERT_DIR`, `CONTAINER_ADDRESS`, host-setup's) stay unprefixed.
- UI strings only through i18next, one namespace per UI area (`ui/src/i18n/locales/en/<area>.ts`), English only.
  Locale `en-CH`: numbers, dates, times via `ui/src/i18n/format.ts` (1'234.5, 26.11.2026, 14:05).
- No README on purpose.
