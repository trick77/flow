# Architecture

For implementers. Binding product decisions are the owner's spec; this file settles how the parts fit. Each
decision: what, why, what was rejected. External facts carry their source; **provisional** marks what could not be
confirmed.

## Components

```
browser ──HTTPS──▶ Flow (container: API, UI, SQLite, preview proxy)
                     │  Incus REST (restricted cert, pinned key): create, start, push files, short exec
                     │  HTTP/1.1 + SSE + WebSocket over the Incus bridge, Flow always dials
                     ▼
sandbox (Incus system container, one per project)
  flowd.service (root) ─── /run/flow/flowd.sock ◀── git-credential-flow, make shim, flowd mcp
    │ systemd-run, systemctl, unix sockets
    ├─ flow-session-<id>.service  (user flow)  flowd host ─┬─ stdio ─▶ node sidecar ─▶ claude CLI
    │                                                      └─ HTTP ──▶ flow-opencode.service (opencode serve)
    └─ flow-term-<id>.service     (user flow)  flowd term ── PTY ─▶ bash
```

| Process | Owns | Survives |
|---|---|---|
| Flow | DB, credentials (sealed), gate queue, child-session rules, browser streams | nothing in sandboxes depends on it staying up |
| flowd | unit management, credential delivery, control requests, diffs, repo/worktree git | restart and update: no unit is its child |
| session host | the session's event log (single writer), command socket, message queue, status | flowd restarts and updates |
| sidecar | Claude Agent SDK translation only (no product logic) | — it is the host's child |
| opencode serve | all OpenCode sessions of the sandbox | flowd and host restarts |
| terminal | PTY, scrollback ring | flowd restarts |

## The six open points

### 1. Process ownership and commands

- **Decision.** Every long-lived process is a transient systemd unit started by flowd with `systemd-run`
  (`--unit=flow-session-<id>`, `--uid=flow`, `-p MemoryMax= -p MemorySwapMax=0 -p CPUQuota=`, `-p Restart=no`,
  `-p EnvironmentFile=`). One *session host* per session (`flowd host`, the release's flowd binary) is the only
  writer of the session's log and owns `layout.HostSocket`. For Claude it runs the Node sidecar as its child over
  stdio (stdin: `Command` lines, kinds marked sidecar; stdout: `EventDraft` lines); for OpenCode it drives the one
  `flow-opencode.service` per sandbox. Terminals: `flow-term-<id>` running `flowd term`.
  Why: units outlive any client (spike 11); a Go host keeps log, seq, queue and idempotency in one tested place
  while the sidecar stays translation-only as the spec demands. Rejected: sidecar writes the log itself (Node has
  no flock, product logic would creep into it); flowd translates OpenCode in-process (flowd restarts would drop
  events and need resync every time).
- **Reattach.** On start flowd lists `flow-session-*`/`flow-term-*` units (`systemctl list-units`), connects to
  each socket, sends `hello` (`hostproto`). A host started from an older release keeps running it (versioned
  release dirs, `layout.Release`); flowd speaks `hostproto` from `MinProtocol` and asks old hosts to
  `host.shutdown {whenIdle}` for an upgrade; the next message restarts the host on the new release with resume.
- **Commands** travel Flow → flowd (`POST /v1/sessions/{id}/commands`) → host socket → (sidecar stdin). Each carries
  an id chosen by the sender (browser `clientId` for messages, deterministic ids for Flow's own: `gate-<req>-q`,
  `child-<sid>`). The host records the id in the `cmd` field of the events it writes and rebuilds its seen-set from
  the log on start, so a retry after any reconnect is acknowledged as `duplicate`, never applied twice.
- **Claude SDK settings** (sources: [typescript](https://code.claude.com/docs/en/agent-sdk/typescript),
  [sessions](https://code.claude.com/docs/en/agent-sdk/sessions)): streaming input mode (an `AsyncIterable` of
  `SDKUserMessage`) so the process lives across turns and `interrupt()`, `setPermissionMode()`, `setModel()` work
  (documented as streaming-input only); `sessionId` = Flow's session UUID on first start, `resume` = the same id
  afterwards; `pathToClaudeCodeExecutable` = the image's CLI; `includePartialMessages: true`;
  `settingSources: ["user","project","local"]` plus `settings: /etc/flow/claude-settings.json` (the flag-settings
  layer, above user/project/local, below managed). Never `--bare`.
- **Mode mapping.** plan→`plan`, ask→`default`, accept_edits→`acceptEdits`, auto→`auto`,
  full_access→`bypassPermissions` ([permissions](https://code.claude.com/docs/en/agent-sdk/permissions)). OpenCode:
  plan→agent `plan`, the rest → agent `build` with a `Permission.Ruleset` per mode on session create/update
  (OpenCode's default policy allows everything, so Flow must send ask rules); auto has no OpenCode classifier and
  maps to accept_edits plus read-only shell allows (**provisional**).

### 2. Event log format

- **Decision.** JSON Lines, one `event.Event` per line, file `layout.EventLog` on the sandbox root filesystem.
  `seq` from 1, gapless, assigned by the host; positions everywhere (flowd, Flow DB `last_seq`/`seen_seq`, SSE `id`,
  browser) are seqs, never byte offsets. No rotation, no compaction: logs are complete and readable from any
  position. Why: line framing is crash-friendly and greppable; seqs survive any re-encoding. Rejected: length-prefixed
  binary (not inspectable), SQLite per session (heavier to tail live, no gain for an append-only stream).
- **Crash safety.** One write per line with `O_APPEND`; exclusive `flock` held by the one writer; on open the writer
  truncates a torn last line (no `\n`) and continues from the last complete seq. `fsync` after each turn's
  `turn.ended` and at most every second while a turn runs; a crash may lose that last second, never corrupt
  earlier lines. A line is at most `event.MaxLine` (1 MiB): tool output is cut to 16 KiB (head and tail).
- **Ephemeral events** (`assistant.text_delta`, `tool.progress`, `live.snapshot`) are never logged and carry no
  seq; they travel only on the host's live subscription, then Flow's SSE. The durable `assistant.text` with the same
  `blockId` replaces the deltas. A late subscriber first gets `live.snapshot` of the open blocks. The spec's "logs are
  complete" holds for content: every delta is contained in a durable event that follows it; only token-by-token
  timing is not kept.
- **Reading and following.** Readers seek with a sparse in-memory index (seq → offset every 256 lines, built on
  first open) and follow by inotify, polling every 250 ms where inotify is missing; the host's `appended` frames wake
  flowd early. Requests that died with a process are closed in the log: a host that starts and finds an open turn
  writes `permission.resolved`/`question.resolved`/`plan.resolved` with `expired`, then `turn.ended {lost}`.

### 3. Flow↔flowd transport

- **Decision.** HTTP/1.1 with JSON bodies, server-sent events for streams (`GET /v1/sessions/{id}/events`,
  `GET /v1/control`), WebSocket only for terminal bytes, a reverse proxy for preview ports. Flow always dials:
  the network ACL blocks sandbox → Flow. Wire names: `flowdproto`. Why: SSE gives ordered resumable streams with
  `Last-Event-ID` on the standard library; the same shape as the browser leg. Rejected: gRPC (dependency, codegen,
  no gain at this scale), one WebSocket for everything (reinvents resumption and backpressure).
- **flowd → Flow requests** (child-session tools, gate acquire) ride the control stream: flowd queues them with ids,
  re-sends every unanswered one to each new control stream, Flow answers with `POST /v1/control/{id}`, idempotent
  by id. A Flow restart therefore loses nothing: an MCP tool call just waits.
- **Authentication.** Flow → flowd: bearer token, random per sandbox start, pushed with the Incus file API to
  `/etc/flow/flowd.json` (root 0600) and kept sealed in `sandboxes.flowd_token`. flowd → Flow: there is no separate
  credential; Flow dials the bridge address Incus reports and the sandbox NIC has `security.mac_filtering` and
  `security.ipv4_filtering` ([devices_nic](https://linuxcontainers.org/incus/docs/main/reference/devices_nic/)),
  so another sandbox cannot answer on it. Because the agent user is effectively root in its sandbox (docker group,
  spike 10), it can read the token and other sessions' MCP tokens: Flow treats everything from a sandbox as
  untrusted and authorizes by *which sandbox connection* a request arrived on, never by ids inside it.
- **Versioning.** `Flow-Protocol` header both ways; `Hello` reports `[minProtocol, protocol]`; outside Flow's range
  → `ErrProtocol` → Flow redeploys flowd. Within a version: additive only.
- **Delivery and update of flowd** without a long exec: push `/opt/flow/releases/<version>/flowd` (and the sidecar
  bundle unpacked by flowd from a pushed tar) and `/etc/systemd/system/flowd.service` with the Incus file API
  (`POST /1.0/instances/{name}/files`, headers `X-Incus-uid/gid/mode/type/write`,
  [rest-api.yaml](https://github.com/lxc/incus/blob/main/doc/rest-api.yaml)), then one bounded exec (≤ 15 s):
  `ln -sfn` the release to `/opt/flow/bin`, `systemctl daemon-reload`, `systemctl restart flowd`. Releases are
  never overwritten: running hosts and terminals keep their binary. Skipped when `Hello.version` already matches.

### 4. Sandbox layout

`layout` is the code; the rules:

| Where | What | Why |
|---|---|---|
| root fs `/home/flow/work/<repo>` | main checkouts | per project; source of worktrees only |
| root fs `/home/flow/work/.sessions/<name>/<repo>` | worktrees, agent cwd = session dir | spec |
| root fs `/var/lib/flow/sessions/<id>/events.jsonl` | logs | outside work trees: no git noise, survives worktree removal |
| root fs `/opt/flow/releases/<v>/`, `/etc/flow/` | flowd, sidecar, config, Claude settings layer | versioned, root-owned |
| home volume `/srv/flow-home/` | `claude/` (`CLAUDE_CONFIG_DIR`), `config/` + `data/` (XDG for OpenCode), `agent-config/`, `cache/{go-mod,npm,ms-playwright}` | one volume, all sandboxes |
| tmpfs `/run/flow/` | sockets, `credentials/` (gh hosts.yml, provider env) | gone at reboot, never on disk |

- Agent user `flow`, uid/gid 65532 (not 1000: shifted volumes keep raw uids on the host, spike 6).
- Home `/home/flow` stays on the root filesystem; the shared volume is mounted beside it and reached through
  environment variables, so no project's tree or log lands on the shared volume.
- `CLAUDE_CONFIG_DIR` relocates "all settings, session history, and plugins"
  ([env-vars](https://code.claude.com/docs/en/env-vars)); the hosting guide implies `~/.claude.json` moves with it
  ([hosting](https://code.claude.com/docs/en/agent-sdk/hosting), **provisional** until checked in the image).
- **Spec tension, reported:** the shared home volume makes Claude transcripts and logins of every project readable
  from every sandbox. The spec wants one volume; projects are therefore not isolated from each other's agent
  history. Mitigation option for the owner: per-project subdirectory for `claude/projects` via a symlink.
- Flow settings layer `/etc/flow/claude-settings.json`: `{"sandbox": {"enabled": false}}` and nothing that
  changes per session (stable tool lists keep the prompt cache). Passed as the SDK's `settings` option, which
  outranks user and project files but not managed settings
  ([settings](https://code.claude.com/docs/en/settings)). The env adds `DISABLE_AUTOUPDATER=1` and
  `CLAUDE_AGENT_SDK_CLIENT_APP=flow/<version>`. `CLAUDE_CODE_ENTRYPOINT` is not documented
  ([env-vars](https://code.claude.com/docs/en/env-vars)); setting it awaits the owner's confirmation.

### 5. Credentials, one place in flowd

- **Decision.** Flow pushes the whole set with `PUT /v1/env` (`flowdproto.Env`, monotonic revision); flowd's
  credentials component is the only code that writes credentials anywhere in a sandbox. Nothing that rotates is
  ever a process environment variable:
  - git: `/etc/gitconfig` `credential.https://github.com.helper = /opt/flow/bin/git-credential-flow` (flowd
    multi-call), which asks flowd over `flowd.sock` on every operation;
  - gh: `GH_CONFIG_DIR=/run/flow/credentials/gh`; flowd rewrites `hosts.yml` atomically (temp + rename) on every
    renewal; every `gh` run is a new process that reads it (**provisional**: gh's docs were not fetched; check
    `GH_CONFIG_DIR` and the hosts.yml token key in the image). Not `GH_TOKEN`: frozen at process start, stale after
    8 hours. The spec still names `GH_TOKEN`; this follows the coordinator's later instruction.
  - git identity: `GIT_AUTHOR_*`/`GIT_COMMITTER_*` from the profile in each unit's environment (changes apply to new
    units; identity does not expire).
- **Claude, per session** (owner decision): `claudeAuth` on the session (DB, API, `SessionSpec`, `session.started`),
  default `subscription_token`. flowd sets exactly one: `CLAUDE_CODE_OAUTH_TOKEN` (the one-year token from
  `claude setup-token`) for `subscription_token`, nothing for `subscription_login` (CLI login on the home volume),
  `ANTHROPIC_API_KEY` for `api_key`. Documented precedence, highest first: cloud-provider variables,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, `apiKeyHelper`, `CLAUDE_CODE_OAUTH_TOKEN`, profile credentials,
  `/login` credentials ([authentication](https://code.claude.com/docs/en/authentication),
  [env-vars](https://code.claude.com/docs/en/env-vars)). So flowd also removes the cloud-provider variables and
  `ANTHROPIC_AUTH_TOKEN` from host environments, and an `apiKeyHelper` in the owner's synced config would win over
  a subscription token: the config sync warns about it. Neither secret rotates; a replaced one reaches a session at
  its next host start.
- OpenCode provider keys: `providers.env` for the `opencode serve` unit; a change restarts it when no OpenCode
  turn runs.
- **Flow unreachable for more than 8 hours:** the token in flowd expires; the git helper and gh fail fast with
  `flow: GitHub token expired (github_token_expired); Flow will renew it`, flowd adds a `notice
  {code: github_token_expired}` to sessions that ran git/gh, the turn sees a normal command failure instead of a
  hang. The next `PUT /v1/env` fixes it.
- **Refresh token invalid:** Flow marks the connection `disconnected` (API `GitHubConnection.state`), pushes
  `githubState: disconnected` with no token; helpers fail with `github_disconnected`; the owner reconnects with the
  device flow.
- Encryption at rest: `kit/secret` only compares secrets in constant time; it has no encryption. Flow seals with
  AES-256-GCM (standard library) behind `credentials.Sealer`, key from a `BACKEND_*` setting (**owner decision**:
  name and rotation).

### 6. Child-session tools

- **Decision.** An MCP stdio server, `flowd mcp`, configured identically for every session (stable tool list):
  Claude through the SDK's `mcpServers: {flow: {type: "stdio", command, args, env}}`
  ([typescript](https://code.claude.com/docs/en/agent-sdk/typescript)); OpenCode through
  `mcp.servers.flow = {type: "local", command}`. Tools: `create_session`, `send_message`, `get_status`,
  `list_sessions`, `wait_for_sessions` (≤ 600 s; `MCP_TOOL_TIMEOUT` defaults to about 28 hours, so the cap is
  Flow's). No tool answers permission requests or changes modes. Why stdio: survives flowd restarts (it reconnects
  to `flowd.sock` per call) and needs no port. Rejected: an HTTP MCP endpoint in flowd (a flowd restart breaks the
  MCP session).
- **Caller identity.** The host gives its agent an env `FLOW_MCP_TOKEN` (random per host start); `flowd mcp` sends
  it with each call; flowd maps it to the session and raises a control request. Spoofable inside the sandbox (see
  3), so it is a convenience, not a boundary.
- **Rules, enforced by Flow** on every control request: same project as the sandbox the request came from; the
  target was started by the caller (`sessions.parent_id`); the caller has no parent (depth 1); at most
  `BACKEND_MAX_CHILD_SESSIONS` non-archived children; the child's mode is `min(requested, caller's mode)` and stored
  as `mode_cap`; later mode changes above the cap are `mode_above_cap`.
- **Identical prefix.** A started session gets exactly what any session gets: the same system prompt preset, the
  same settings layer, the same MCP list and tools; everything specific to it (the parent's instructions) is its
  first user message. So its prompt prefix matches the parent's and every other session's and hits the cache.
- **Reports.** When a child's turn ends (done, failed, or waiting for the owner), Flow sends the parent
  `child.report`; the parent host queues it until the parent is between turns, then logs `child.reported` and
  delivers it as a message with origin `child_report`. The child logs `session.reported`. Never mid-turn.

## Lifecycles

**Sandbox.** none → `setup` (Incus create from the base image or clone, limits, home volume, NIC filtering, start,
push flowd, `Hello`, `PUT /v1/env`, clone repos, run each `.flow/setup.sh` as a flowd job (`PUT /v1/jobs/{id}`, its
own unit, log read by line for the setup log; config sync is a job too) → `warm` →
`stopping` → `stopped` (filesystem kept) → `setup` again on demand (no re-clone; setup scripts rerun only when their
hash changed). The RAM budget stops the least recently used warm sandbox whose sessions are all idle and whose last
turn ended more than the prompt-cache lifetime ago (5 minutes unless the 1-hour TTL is configured,
[env-vars](https://code.claude.com/docs/en/env-vars); Flow never overrides it).

**Session.** create (DB row, name, branch) → worktrees on fresh `origin/<default>` → `PUT /v1/sessions/{id}` starts the
host → `session.started` → turns → idle with the host alive (token economy) → archive stops the host and removes
worktrees; the log stays. A stopped host restarts on the next message with resume and logs `session.resumed`.

## Failures

| Failure | What happens | Recovery |
|---|---|---|
| Browser disconnect | nothing in Flow or the sandbox changes | EventSource reconnects with `Last-Event-ID`; durable gap filled from the log, then `live.snapshot` |
| Flow restart or update | hosts keep running turns; control requests wait | Flow reads `last_seq` per session, re-dials flowd, re-follows from there; re-reads pending control requests |
| flowd restart or update | no unit is its child; turns go on, logs keep growing | flowd reattaches by unit name and socket; Flow's streams reconnect |
| host crash | sidecar dies with it; turn lost | flowd restarts it on the next message: `turn.ended {lost}`, open requests `expired`, `session.resumed` |
| opencode serve restart | the host's event stream breaks | host reconnects, resyncs from the message list (OpenCode's `/api/event` has no replay, [openapi.json](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/openapi.json)) |
| sandbox stop | all units end | on start, hosts are not started until needed; logs show `lost` turns |
| host (machine) reboot | Incus starts nothing by default; Flow reconciles | sandboxes start on demand; `BootID` change tells Flow every unit is gone |
| Incus restart | units survive (spike 11) | Flow holds no exec, so Incus restarts in about 2 s |

## Where state lives

| State | Place |
|---|---|
| Conversation, tool calls, requests, todo, PR set history | sandbox event log |
| Sidebar fields, worktrees, PR numbers, modes, credentials (sealed), sandbox rows | Flow SQLite (`docs/schema.md`) |
| Message queue | host, logged as `message.queued`/`message.dequeued` (survives reloads and restarts) |
| Gate queue | Flow memory; rebuilt from pending control requests after a restart |
| Terminal scrollback | terminal unit memory (256 KiB ring) |
| Claude login, transcripts, OpenCode data, caches | home volume |

## Security rules that follow

- Flow's API refuses requests whose `Origin`/`Referer` is a preview host (`preview_origin`); preview hosts get
  their own host-only cookie through a one-use ticket; the proxy strips `Cookie` and `Authorization`.
- Everything a sandbox sends (events, control requests, diffs) is untrusted input bound to that sandbox's project.
- Exec through Incus only for bounded bootstrap commands; never a long-lived exec.

## Provisional and unverified

- Plan approval through `canUseTool` for `ExitPlanMode` is not documented in the SDK pages; mapping assumes allow =
  approve, deny with message = keep planning.
- The SDK's todo tools differ by model: `TodoWrite` on older models, `TaskCreate`/`TaskUpdate` on newer ones
  ([todo-tracking](https://code.claude.com/docs/en/agent-sdk/todo-tracking)); the sidecar keeps the list and emits
  `todo.updated` for both.
- OpenCode has two release lines (v1.19.6 marked latest, v2.4.2 newest, both 2026-11); Flow pins a v2.x for the
  V2 API (`/api/*`, HTTP Basic user `opencode`, `OPENCODE_PASSWORD`; questions arrive as forms; no todo events).
- Incus facts come from `rest-api.yaml` on `main`, newer than the spike's 6.0.4 LTS: fields added after 6.0 are
  provisional until checked against the deployed version.
