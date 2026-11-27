# Ownership for the parallel phase

Each area owns its directories outright: nobody else edits files there. Areas meet only through the contracts named
below. A change to a contract or a shared file goes through the coordinator.

## Shared files (coordinator only)

- `backend/go.mod`, `backend/go.sum`; `ui/package.json`, `ui/package-lock.json`; `sidecar/package.json`,
  `sidecar/package-lock.json`
- `backend/internal/store/migrations/` (next free number per change, never edit an applied file)
- `ship.yaml`, `Makefile`, `backend/Containerfile`, `compose.yaml`, `.github/`, `.spectral.yaml`
- `contract/` (event model and fixtures), `backend/api/openapi.json`, every `*.gen.go` / `*.gen.ts`
- `backend/internal/httpapi/routes.go`, `areas.go`, `httpapi.go`, `respond.go`; `backend/cmd/flow/main.go` (wiring)
- UI globals: `ui/src/i18n/resources.ts` (namespace index), `ui/src/i18n/{index,format}.ts`,
  `ui/src/i18n/locales/en/common.ts`, `ui/src/App.tsx`, `ui/src/main.tsx`, `ui/src/index.css`, `ui/index.html`,
  `ui/vite.config.ts`, `ui/tsconfig*.json`, `ui/src/test/setup.ts`, `ui/.prettierignore`
- sidecar config: `sidecar/tsconfig*.json`, `sidecar/eslint.config.js`, `sidecar/.prettierignore`
- `backend/cmd/flowd/main.go` (subcommands `host`, `term`, `mcp`, `git-credential-flow`, the make shim: each area
  hands the coordinator a `Run(ctx, args)` entry point in its own package)
- `backend/internal/layout`, `flowdproto`, `hostproto`, `event`, `apitypes`, `sandbox` (interfaces), `contractgen`,
  `config` (new `BACKEND_*` settings: `BACKEND_GATE_CONCURRENCY`, `BACKEND_MAX_CHILD_SESSIONS`, the sealing key, …)
- `docs/architecture.md`, `docs/events.md`, `docs/ownership.md`, `AGENTS.md`; `docs/schema.md` is generated

Dependencies the coordinator adds when an area starts (vetted: stars, last commit 2026-11): `github.com/trick77/ship/kit/auth`
(house), `github.com/trick77/toolkit` (house: llmwire, mcpserver), `github.com/coder/websocket` (5.6k stars, 2026-11-19;
terminal WebSocket on both legs), `@anthropic-ai/claude-agent-sdk` (sidecar). The gate runs `go mod tidy`, so none
is added before code imports it.

## Areas

| Area | Owns | Implements | Uses |
|---|---|---|---|
| auth | `internal/auth`, `docs/auth.md` | `httpapi.Auth`, `httpapi.Guard` | kit/auth |
| profile and settings (backend) | `internal/profile` | `httpapi.Profile`, `profile.Store` | flowdproto (config sync) |
| GitHub App and credentials | `internal/github`, `internal/credentials` | `httpapi.Connections`, `credentials.Store`, `Sealer`, `Source`, `GitHub` (github implements it) | profile |
| projects | `internal/projects` | `httpapi.Projects`, `projects.Store` | sandboxes |
| sandboxes and Incus | `internal/sandboxes`, `internal/sandbox/incus`, `deploy/` (host setup and teardown, base image, dev run), the sandbox network (`config/network.go`) | `httpapi.Sandboxes`, `sandboxes.Store`, `sandboxes.Addresses`, `sandbox.Runtime` | flowdproto, credentials.Source |
| sessions | `internal/sessions`, `internal/gate` | `httpapi.Sessions`, `sessions.Store`, control requests (child tools, gate) | flowdproto, projects, sandboxes, helper |
| timeline API | `internal/timeline` | `httpapi.Timeline` (SSE by seq, log pages, commands) | flowdproto, sessions.Store |
| review | `internal/review` | `httpapi.Review` | flowdproto, github |
| terminal backend | `internal/terminals` | `httpapi.Terminals` | flowdproto |
| preview | `internal/preview` | `httpapi.Previews`, `Options.Preview`, `Options.IsPreviewHost` | flowdproto |
| helper-model tasks | `internal/helper` | titles, branch names, summaries, PR text | toolkit/llmwire, metering |
| metering | `internal/metering` | the usage ledger: `sessions.Meter` (fed by the follower), `metering.Store`, the `Record*` API | imports no area (every area may call it); reads `session_worktrees` + `project_repos` for a turn's repos |
| flowd runtime | `cmd/flowd`, `internal/supervisor`, `internal/flowdclient`, `internal/eventlog`, `internal/mcp` | flowdproto server and client, units, credentials delivery, MCP, gate shim, terminals (`flowd term`) | layout, hostproto |
| flowd Claude adapter host | `internal/host`, `internal/host/claude` (the sidecar child: start line, environment, supervision) | `flowd host`: log writer, queue, status, sidecar child | eventlog, hostproto, event |
| flowd OpenCode adapter | `internal/opencode` | OpenCode bridge inside the host | event |
| sidecar | `sidecar/src` (not `contract/`) | SDK → `EventDraft` on stdout, `Command` from stdin | `sidecar/src/contract/events.gen.ts` |
| UI shell and sidebar | `ui/src/shell`, `ui/src/sidebar`, `locales/en/shell.ts`, `sidebar.ts` | layout, header, sidebar sections, rail | API client |
| UI timeline | `ui/src/timeline`, `locales/en/timeline.ts` | folds events into the timeline | `ui/src/contract` |
| UI composer | `ui/src/composer`, `locales/en/composer.ts` | input, pickers, queue | API client |
| UI right panel | `ui/src/panel`, `locales/en/panel.ts` | diff with repo picker, preview | API client |
| UI terminal | `ui/src/terminal`, `locales/en/terminal.ts` | drawer over the WebSocket | API client |
| UI pages | `ui/src/pages`, `locales/en/projects.ts`, `settings.ts` | Projects and Settings | API client |
| UI API client | `ui/src/api` (not `*.gen.ts`), `locales/en/errors.ts` | typed fetch, error codes, EventSource by seq | `ui/src/api/types.gen.ts` |

Metering call sites (spec "Metering from day one"). The follower already feeds turn starts and turns, status
transitions, owner waits, permission requests and decisions, file-change counts and the counters `passthrough`
(class = the provider's message name), `unknown_kind` (class = the kind), `turn_error` (every error turn, class =
`errorCode` or `unknown`), `provider_error` (the same minus the owner's limits `max_turns`, `budget`,
`max_budget_usd`, `max_structured_output_retries` and the host's `adapter`, `adapter_exited`) and `turn_lost`.
Everything else is recorded by the area that sees it, through the `*metering.Recorder` that `cmd/flow` wires into
the sessions service (one per server):

| Record | Owner | When |
|---|---|---|
| `RecordCheckRun`, `RecordWait` reason `check_queue` (position, holder session and repo) | sessions (`internal/gate`) | a gate run ends; a queued run gets its slot |
| `RecordWait` `sandbox_start` / `sandbox_resume` | sandboxes and Incus | a session waited for its sandbox to start or resume |
| `RecordWait` `rate_limit` / `provider_retry` | flowd Claude adapter host, OpenCode adapter (via an event Flow can follow, or `notice` params) | a rate limit or retry ends |
| `RecordWait` `started_sessions` | sessions | a parent waited for its started sessions |
| `RecordWait` `credential_renewal`, `RecordCounter` `credential_renewal_failure` | GitHub App and credentials | a renewal held a session up / failed |
| `RecordHelperCall` | helper-model tasks | every llmwire call |
| `RecordPullRequest` opened / reopened / merged / closed, `Seq` set when read from `pull_requests.updated` | sessions or github | every state change (one row each; a repeat of the same id, or of the same event and PR, is a no-op) |
| `RecordSandboxSample` (memory, swap, disk, CPU, PSI totals, OOM kills) | sandboxes and Incus, flowd runtime (per unit) | every sampling interval |
| `RecordCounter` `flowd_restart`, `flow_restart`, `reconnect` | sandboxes / `cmd/flow` / flowdclient | on each occurrence |

Rules: an area that needs a new endpoint, event kind, column or dependency asks the coordinator; it never edits the
shared file itself. An API area writes its handlers in its own package and is wired in `cmd/flow`. Tests live next to
the code they cover; fakes for other areas' interfaces: `flowdfake`, `sandboxfake`, or the area's own test doubles.

## Build order

**Walking skeleton (coordinator, sequential):**
1. `cmd/flow` wires `store.Open`, a dev-mode `Guard`, `flowdfake` with `Replay(contract/fixtures/demo.jsonl)` as
   the flowd of a fixed demo session.
2. `internal/timeline`: `streamSessionEvents` (SSE by seq, `Last-Event-ID`) and `sendMessage` over `flowdproto.API`.
3. `ui/src/timeline`: one view rendering a session's stream with the generated types.
4. `eventlog` + `flowd host` + the sidecar replaying a recorded SDK stream (no SDK call), then flowd for real over
   `flowdclient`, with `sandboxfake` or a real Incus behind the `incus` build tag.

**Parallel packages after the skeleton** (→ = needs):

| Package | Needs |
|---|---|
| auth | — |
| profile; GitHub App and credentials | auth |
| projects; sandboxes and Incus | — (fakes) |
| flowd runtime (eventlog, units, client, MCP, terminals) | skeleton step 4 |
| flowd Claude adapter host; sidecar | eventlog |
| flowd OpenCode adapter | host |
| sessions (+ gate, child rules) | projects, sandboxes interfaces |
| timeline API; review; terminal backend; preview | flowdproto (fake until flowd runtime lands) |
| helper-model tasks | — |
| UI shell/sidebar, timeline, composer, panel, terminal, pages, API client | API client first (types exist), rest in parallel against `flowdfake` demo |

Integration order: flowd runtime + host + sidecar → sessions → timeline on real flowd → OpenCode → previews.
