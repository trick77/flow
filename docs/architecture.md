# Architecture

For implementers. Binding product decisions are the owner's spec; this file settles how the parts fit. Each
decision: what, why, what was rejected. External facts carry their source; **provisional** marks what could not be
confirmed.

## Components

```
browser ──HTTPS──▶ Flow (container: API, UI, SQLite, preview proxy)
                     │  Incus REST (restricted cert, pinned server cert): create, start, push files, short exec
                     │  HTTP/1.1 + SSE + WebSocket over Docker's bridge flowbr0, Flow always dials
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
  (`--unit=flow-session-<id>`, `--uid=flow`, `-p MemoryMax= -p MemorySwapMax=0 -p CPUQuota= -p TasksMax=`,
  `-p OOMPolicy=continue`, `-p Restart=no`, `-p EnvironmentFile=`). `OOMPolicy=continue` because systemd's default
  for services (`stop`) ends the whole unit when the kernel kills one of its processes at `MemoryMax` (measured,
  systemd 257): an agent's oversized build would take its session host with it; with `continue` only that process
  dies (measured in a session unit: the build gets 137, host, other units and sandbox run on). One *session host* per session (`flowd host`, the release's flowd binary) is the only
  writer of the session's log and owns `layout.HostSocket`. For Claude it runs the Node sidecar as its child over
  stdio (stdin: `Command` lines, kinds marked sidecar; stdout: `EventDraft` lines); for OpenCode it drives the one
  `flow-opencode.service` per sandbox. Terminals: `flow-term-<id>` running `flowd term`.
  Why: units outlive any client (spike 11); a Go host keeps log, seq, queue and idempotency in one tested place
  while the sidecar stays translation-only as the spec demands. Rejected: sidecar writes the log itself (Node has
  no flock, product logic would creep into it); flowd translates OpenCode in-process (flowd restarts would drop
  events and need resync every time).
- **Adapter boundary** (`host.Adapter`): sidecar-marked `Command`s in, `EventDraft`s out, `Send` never blocks, one
  `turn.ended` per `message.send`. The host writes `session.started` (from its spec, on the first start) and
  `worktree.added` itself; an adapter's own `session.started` draft is dropped. Ids an adapter puts into drafts
  (`toolId`, `requestId`, `blockId`) must be unique across the whole log, also across host restarts: Flow derives
  reply command ids from request ids, so a reused one turns a new reply into a duplicate. The host holds one
  request slot and resolves requests itself on the owner's reply; an interrupt expires the open request at once
  (the adapter answers it as the interrupt, so a later reply is `not_pending`, never logged as accepted). The only
  resolution it takes from an adapter is
  `expired` for the open request (the provider withdrew it, e.g. the SDK aborted `canUseTool`), which frees the
  slot and sets the session working again. The sidecar shows no next request while a withdrawn one is still open on
  the host. The spec picks the adapter: `SessionSpec.replay` plays `contract/fixtures/demo.jsonl` (tests, demo
  mode), else provider `claude` runs the sidecar (`internal/host/claude`) and `opencode` is refused with
  `not_implemented` (flowd answers it before creating anything). The host forwards `host.shutdown` to the adapter in
  its own mode. `BACKEND_SESSION_REPLAY` (default off) has Flow send `replay` in every spec, for demos and
  machines without a Claude credential; read at each host start, so a session that ran replayed turns runs its
  agent once the setting is off. `deploy/dev-flow.sh` turns it on unless the running Flow has a Claude credential.
- **Claude sidecar** (`internal/host/claude`). Started on the first message as `node <release>/sidecar/dist/main.js
  [--record <log dir>/sdk.jsonl]` (`layout.NodeBin`, the bundle beside the host's resolved flowd binary, recording
  when `SessionSpec.record`), cwd = the agent's working directory (below), own process group. First stdin line `sidecar.start`
  (`StartSpec`): `sessionId`, `resume`, `cwd`, current `model`/`effort`/`mode`/`modeCap`, `claudeAuth`, `claudeExecutable` =
  `layout.ClaudeCLI`, `settingsPath` = `layout.Settings`, `mcpServers.flow` = `{stdio, <flowd> mcp,
  env BACKEND_MCP_TOKEN}` once `flowd mcp` exists (none until then), `version` (the sidecar sets
  `CLAUDE_CODE_ENTRYPOINT=flow` and `CLAUDE_AGENT_SDK_CLIENT_APP=flow/<version>` from it), `priorCostUsd` on a
  resume. All of it is read from the log at each sidecar start, never from the sidecar. `resume`: the first
  `session.started` draft (the SDK's `system/init`) inside a turn is logged once as `notice {provider_session}`, the
  only evidence the provider has the session (the host's own `session.started` proves nothing; the replay, also in
  logs from before the marker, never reports an init). `priorCostUsd` = sum of the reported `turn.ended.costUsd` of
  turns ended after the marker (turns without one add nothing, so their spend lands in the next reported turn, as
  inside one SDK process). Model, effort and modes from `session.settings` and approved plans, the mode held to the cap (a
  lower cap pulls the mode in force down; the host logs and sends that mode). **Provisional:** what Claude Code does
  with `sessionId` for a session it has (an init with no transcript written yet) and with `resume` for one it lacks
  is not verified.
- **Agent working directory** (owner decision, deviates from the spec's "cwd = session directory"). A session made
  with one repository works in that worktree, one made with several in the session directory: in the session
  directory a single-repo agent wrote files beside its repository and `git status` found no repository (first real
  run). flowd chooses it once, before the first start, and keeps it as `host.Spec.cwd`: Claude Code stores its
  transcript by working directory, so a resume must find the same one. A spec without the field whose log exists
  (a session from before, or a spec lost or rewritten by an older flowd) keeps the directory the log's first
  `session.started` names, else the session directory. `session.started.cwd`, the sidecar's `cwd` (tool and request
  `path` relative to it) and a session terminal's start directory (flowd moves it there) all follow it. When the
  agent's directory is not the session's one worktree, `sidecar.start` carries `appendSystemPrompt`, a note listing
  each worktree (path, repo, branch, base ref) appended to Claude Code's preset prompt, and `additionalDirectories`
  = the worktrees outside the working directory (a repository added to a single-repo session). Both come from the
  log's `worktree.added` at each sidecar start, so a repository added while the sidecar runs reaches the agent at
  its next start, not mid-process (the SDK's runtime `register_repo_root` takes only subdirectories of known roots). The
  append goes with `snapshot: false`: by default Claude Code records the first request's prompt and ignores a
  different append on a later start (sdk.d.ts, `systemPrompt`). **Provisional:** that a resumed session already
  recorded honours the new append is not verified. Single-repo sessions now read project settings and `.mcp.json`
  from the worktree.
- **Sidecar environment.** Built from an allow-list of the host unit's own environment (PATH, HOME, user, shell,
  locale, TZ, TMPDIR, TERM, git identity, and the image's unit defaults GOMODCACHE, GOPATH, GOCACHE,
  npm_config_cache, PLAYWRIGHT_BROWSERS_PATH, DISABLE_AUTOUPDATER, OPENCODE_DISABLE_AUTOUPDATE; layout values fill in
  what the unit lacks), Flow's own locations set always (`CLAUDE_CONFIG_DIR`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`,
  `GH_CONFIG_DIR`), and the one credential variable of the session's mode. Nothing else passes: no other credential,
  no cloud-provider switch, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `GH_TOKEN` or MCP token. The sidecar spreads
  this environment into the SDK's `env`, which replaces Claude Code's, so it is also what every agent command sees.
- **Sidecar supervision.** stdin and stdout each have their own goroutine and an unbounded queue: `Send` never blocks,
  a sidecar flooding stdout while not reading stdin stalls nothing. Draft lines that are not JSON, name no contract
  kind, carry no object `data`, do not decode into their kind's payload, or exceed `event.MaxLine` are dropped and
  journalled by reason and size, never by content. stderr goes to the unit's journal line by line, at most 1 MiB per
  sidecar process, drained beyond that. The sidecar exits or its pipe breaks during a turn: the rest of its output
  is taken (stderr to its end, bounded), then the adapter logs `notice {message_may_not_have_run, messageId}` and ends
  the turn `error` / `adapter_exited` (`interrupted` when an interrupt was pending); the host expires the open
  request, and the next message starts a new sidecar that resumes. The message is never sent again by itself (owner
  decision: it may have started work, and running it twice can repeat commands); the owner re-sends. Exit between
  turns: restarted on the next message. Start failure: the turn ends `error` / `adapter`. A sidecar refusing its start
  line is killed. Host stop: `host.shutdown` (the host's own, or an immediate one), stdin closed, 40 s (past the
  sidecar's 30 s interrupt grace, so Claude Code closes in order), SIGTERM, 5 s, SIGKILL to the process group: at
  most 45 s (`claude.StopLimit`), below systemd's 90 s stop timeout.
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
  afterwards, never both (the SDK refuses the pair without `forkSession`); `pathToClaudeCodeExecutable` = the
  image's CLI; `includePartialMessages: true`; `settingSources: ["user","project","local"]` plus
  `settings: /etc/flow/claude-settings.json` (the flag-settings layer, above user/project/local, below managed).
  `env` replaces the CLI's environment instead of adding to it, so the sidecar passes its own environment plus
  Flow's variables. Never `--bare`.
- **SDK reporting.** `result.total_cost_usd` and `modelUsage` are cumulative over the SDK process; the sidecar
  emits per-turn differences (after a resume from the session's earlier cost, which the host passes as
  `priorCostUsd`), so `turn.ended.costUsd` is one turn's (`docs/events.md`, "Model per turn"). Bash tool results carry no exit code.
- **Mode mapping.** plan→`plan`, ask→`default`, accept_edits→`acceptEdits`, auto→`auto`,
  full_access→`bypassPermissions` ([permissions](https://code.claude.com/docs/en/agent-sdk/permissions)); the SDK
  refuses `bypassPermissions`, also as a later `setPermissionMode`, unless the query started with
  `allowDangerouslySkipPermissions`, which the sidecar sets exactly when the session's mode cap allows full access.
  OpenCode:
  plan→agent `plan`, the rest → agent `build` with a `Permission.Ruleset` per mode on session create/update
  (OpenCode's default policy allows everything, so Flow must send ask rules); auto has no OpenCode classifier and
  maps to accept_edits plus read-only shell allows (**provisional**).

### 2. Event log format

- **Decision.** JSON Lines, one `event.Event` per line, file `layout.EventLog` on the sandbox root filesystem.
  `seq` from 1, gapless, assigned by the host; positions everywhere (flowd, Flow DB `last_seq`/`seen_seq`, SSE `id`,
  browser) are seqs, never byte offsets. No rotation, no compaction: logs are complete and readable from any
  position. Why: line framing is crash-friendly and greppable; seqs survive any re-encoding. Rejected: length-prefixed
  binary (not inspectable), SQLite per session (heavier to tail live, no gain for an append-only stream).
- **Crash safety.** One write per line at the end of the last complete line (no `O_APPEND`: a failed or short
  write is cut back to that offset, so no fragment glues the next line onto itself; if the cut fails too the writer
  closes); exclusive `flock` held by the one writer; on open the writer truncates a torn last line (no `\n`, or an
  unreadable last line) and continues from the last complete seq. An unreadable line with complete lines after it
  is refused (`eventlog.ErrCorrupt`, the host does not start) and nothing is cut: truncating would drop the valid
  events after it, skipping it would break the gapless seqs. `fsync` after each turn's
  `turn.ended` and at most every second while a turn runs; a crash may lose that last second, never corrupt
  earlier lines. A line is at most `event.MaxLine` (1 MiB): tool output is cut to 16 KiB (head and tail).
- **Ephemeral events** (`assistant.text_delta`, `tool.progress`, `live.snapshot`) are never logged and carry no
  seq; they travel only on the host's live subscription, then Flow's SSE. The durable `assistant.text` with the same
  `blockId` replaces the deltas. A late subscriber first gets `live.snapshot` of the open blocks. The spec's "logs are
  complete" holds for content: every delta is contained in a durable event that follows it; only token-by-token
  timing is not kept.
- **Reading and following.** Readers seek with a sparse in-memory index (seq → offset every 256 lines, built on
  first read) and follow by polling every 250 ms, woken early by the host's `appended` frames (so a live session's
  events arrive without the poll delay; the poll alone serves a log with no host running). inotify was left out: the
  wake-ups already cover the live case, and polling has no per-platform code. Requests that died with a process are closed in the log: a host that starts and finds an open turn
  writes `permission.resolved`/`question.resolved`/`plan.resolved` with `expired`, then `turn.ended {lost}`.

### 3. Flow↔flowd transport

- **Decision.** HTTP/1.1 with JSON bodies, server-sent events for streams (`GET /v1/sessions/{id}/events`,
  `GET /v1/control`), WebSocket only for terminal bytes, a reverse proxy for preview ports. Flow always dials and
  listens for nothing from sandboxes (see "Sandbox network": they can reach Flow's container). Wire names:
  `flowdproto`. Why: SSE gives ordered resumable streams with
  `Last-Event-ID` on the standard library; the same shape as the browser leg. Rejected: gRPC (dependency, codegen,
  no gain at this scale), one WebSocket for everything (reinvents resumption and backpressure).
- **SSE framing** is Flow's own (`internal/ssewire`, both legs): kit/sse frames only `event:` and `data:`, and every
  durable event needs `id: <seq>`. The stream position is the larger of `?after` and `Last-Event-ID`: an EventSource
  that reconnects by itself keeps the `?after` it opened with.
- **Terminal WebSocket** (`internal/terminals/wsterm`, the same on both legs): binary frames are terminal bytes,
  text frames JSON, `{"resize":{"cols","rows"}}` towards the terminal (bounded 500×200 on every leg) and
  `{"exit":{"code"}}` from it as its last frame. Close codes, because a browser cannot read a refused handshake:
  1000 after an exit frame (the shell ended; restart is a new create); 4404 the terminal is gone (closed, never
  made, archived session, sandbox restarted without an exit file); 4409 the sandbox is not running (an attach never
  starts one); anything else (1001, 1011, 1012, a dropped connection) means Flow or flowd went away or could not be
  reached while the terminal may run on: attach again, the ring is replayed. `flowd term` keeps no state in flowd:
  its socket answers `info`, and its exit status waits in `layout.TerminalExitFile` until restart or close. Output
  for a slow client is throttled (the PTY reader waits, up to 4 MiB queued per client); a client that takes nothing
  for 5 s is dropped and re-attaches.
- **Session stop:** `DELETE /v1/sessions/{id}?remove=true` also removes the worktrees and frees the session
  directory's claim (`layout.SessionClaim`): a directory belongs to one session id, and flowd never adopts another
  session's worktrees found there. It can be repeated after a partial run (a worktree already gone counts as
  removed), and undoes a creation that failed halfway: EnsureSession writes the spec before claiming and after every
  worktree. Branches: a session branch is deleted only when every commit on it is reachable from its base
  (`origin/<default>`); one with commits of its own stays, pushed or not, for the owner. A new session meeting a
  leftover branch of its name resets it to the fresh base when it has no commits of its own, and is refused when it
  has some. Session ids that are no `layout.ValidName` are refused before they become paths.
- **host.shutdown** without `whenIdle` during a turn: the host tells the agent to stop and writes the turn's end as
  `interrupted` itself (with the shutdown's command id), then stops once the command's reply is written. flowd's
  session stop (`DELETE /v1/sessions/{id}`, reason `stop`, or `archive` with remove) sends it before stopping the
  unit (bounded, 5 s), so a stop or archive mid-turn leaves no open turn in the log; the agent's own `turn.ended`
  arrives after that and is dropped, so the cut-off turn's cost stays unreported. The stop needs no spec (one
  missing or tampered with never keeps a host and its credential running); only the remove step reads it. Request
  drafts (permission, question, plan) wait for the owner only when logged inside a turn; otherwise the host answers
  the adapter with a deny at once, so no session waits on a request nobody can see.
- **Command results** say whether a `message.send` was queued (`CommandResult.queued`, from the host): the browser's
  `MessageAccepted.queued` is that, not the mirrored status.
- **flowd → Flow requests** (child-session tools, gate acquire) ride the control stream: flowd queues them with ids,
  re-sends every unanswered one to each new control stream, Flow answers with `POST /v1/control/{id}`, idempotent
  by id. A Flow restart therefore loses nothing: an MCP tool call just waits.
- **Authentication.** Flow → flowd: bearer token, random per sandbox (made on its first bring-up, not per start),
  pushed with the Incus file API to `/etc/flow/flowd.json` (root 0600) and kept sealed in `sandboxes.flowd_token`.
  Fixed rather than per start because the agent user can read it anyway (below) and a per-start token would need a
  push and a flowd restart on every boot for no gain in isolation. Rotation: clear `sandboxes.flowd_token`; the next
  bring-up makes a new one, flowd refuses the old one, and Flow re-delivers the config. flowd → Flow: there is no separate
  credential; Flow dials the address it assigned to the sandbox, and the NIC has `security.mac_filtering` and
  `security.ipv4_filtering` ([devices_nic](https://linuxcontainers.org/incus/docs/main/reference/devices_nic/)),
  so another sandbox cannot answer on it (measured: ARP and source spoofing blocked). Because the agent user is effectively root in its sandbox (docker group,
  spike 10), it can read the token and other sessions' MCP tokens: Flow treats everything from a sandbox as
  untrusted and authorizes by *which sandbox connection* a request arrived on, never by ids inside it.
- **Versioning.** `Flow-Protocol` header both ways; `Hello` reports `[minProtocol, protocol]`; outside Flow's range
  → `ErrProtocol` → Flow redeploys flowd. Within a version: additive only.
- **Delivery and update of flowd** without a long exec: push `/opt/flow/releases/<release>/flowd.new`, the sidecar
  bundle as one gzip'd tar `<release>/sidecar.tar.gz` (`layout.SidecarArchive`), `/etc/flow/flowd.json` (root 0600)
  and `/etc/systemd/system/flowd.service` with the Incus file API (`POST /1.0/instances/{name}/files`, headers
  `X-Incus-uid/gid/mode/type/write`, [rest-api.yaml](https://github.com/lxc/incus/blob/main/doc/rest-api.yaml)),
  then one bounded exec (≤ 15 s): `ln -sfn` the release to `/opt/flow/bin`, `systemctl daemon-reload`,
  `systemctl restart flowd`. flowd unpacks the tar at its start, before it answers, into a temporary directory
  renamed to `<release>/sidecar/` (relative symlinks inside the bundle only, nothing else but files and
  directories), and drops a tar pushed into a release that has its bundle. Why a tar: the bundle (`package.json`,
  `dist/`, production `node_modules`) is 66 MB in 8,771 files, one file API request each plus their directories;
  as one 8.8 MB request it is complete or absent. Measured in the lab: packing 0.25 s once per Flow process,
  unpacking 0.18 s, the whole delivery (push, exec, flowd restart, unpack, `Hello`) 0.36 to 0.46 s. Releases are
  never overwritten: running hosts and terminals keep their binary and bundle. The binary is moved into place only
  when the release does not exist (Incus refuses to write onto a running executable: measured 500 "text file
  busy"). The release name is the version plus a hash of the flowd binary and the bundle (`Hello.release`): a
  rebuild reporting the same version (an unstamped `dev-<commit>-dirty`) is another release and is delivered,
  the same build after a Flow restart is not (the tar is packed byte-identically). Skipped when `Hello.release`
  matches; after a sandbox start Flow first waits for an already delivered flowd to answer instead of delivering
  again. A sealed flowd token that no longer opens (a new `BACKEND_SEAL_KEY`) is made again and delivered.

### 4. Sandbox layout

`layout` is the code; the rules:

| Where | What | Why |
|---|---|---|
| root fs `/home/flow/work/<repo>` | main checkouts | per project; source of worktrees only |
| root fs `/home/flow/work/.sessions/<name>/<repo>` | worktrees; agent cwd = the one worktree, or the session dir with several | spec; "Agent working directory" |
| root fs `/var/lib/flow/sessions/<id>/events.jsonl` | logs | outside work trees: no git noise, survives worktree removal |
| root fs `/var/lib/flow/sessions/<id>/spec.json`, `/var/lib/flow/session-dirs/<name>` | host spec; which session id owns a session directory | the log is served with no host running; no session adopts another's worktrees |
| root fs `/var/lib/flow/jobs/<id>.{log,json}` (root 0700) | setup scripts' logs and results | the result is the marker that a script ran; root's, so no planted link is followed |
| root fs `/opt/flow/releases/<v>/`, `/etc/flow/` | flowd, sidecar, config, Claude settings layer | versioned, root-owned |
| home volume `/srv/flow-home/` | `claude/` (`CLAUDE_CONFIG_DIR`), `config/` + `data/` (XDG for OpenCode), `agent-config/`, `cache/{go-mod,npm}` | one volume, all sandboxes |
| image `/opt/ms-playwright` | Playwright's browsers | the volume is not mounted when the image is built |
| tmpfs `/run/flow/` | sockets, `credentials/` (gh hosts.yml, provider env), `session-env/<id>.env` (root 0700: each host unit's EnvironmentFile) | gone at reboot, never on disk |

- Agent user `flow`, uid/gid 65532 (not 1000, the host's first user).
- **Home volume** `BACKEND_HOME_VOLUME` (default `flow-home`, pool `BACKEND_INCUS_POOL`): made by Flow when missing,
  attached at every bring-up (an older sandbox gets it then, also while running). Not `security.shifted`: the
  lab's dir pool refuses the idmapped mount ("Required idmapping abilities not available", measured). Instead
  every sandbox of the project shares Incus' one idmap (never `security.idmap.isolated`), so Incus maps the
  volume (its `volatile.idmap.last`) the same for all of them: files carry host uid 1000000 + 65532 and are 65532
  in every sandbox (measured: a file written in one sandbox is the agent's in the other). An isolated idmap would
  break this. A new volume's root is root 0711; flowd makes the agent-owned tree (`layout.HomeDirs`) at its start
  and on every `PUT /v1/env`, never by running go or npm as root.
- Home `/home/flow` stays on the root filesystem; the shared volume is mounted beside it and reached through
  environment variables, so no project's tree or log lands on the shared volume.
- `CLAUDE_CONFIG_DIR` relocates "all settings, session history, and plugins"
  ([env-vars](https://code.claude.com/docs/en/env-vars)); `.claude.json` moves with it (measured: Claude Code
  2.1.332 started by the sidecar wrote `.claude.json`, `projects/` and `backups/` below `CLAUDE_CONFIG_DIR`).
- **Spec tension, reported:** the shared home volume makes Claude transcripts and logins of every project readable
  from every sandbox. The spec wants one volume; projects are therefore not isolated from each other's agent
  history. Mitigation option for the owner: per-project subdirectory for `claude/projects` via a symlink.
- Flow settings layer `/etc/flow/claude-settings.json`: `{"sandbox": {"enabled": false}}` and nothing that
  changes per session (stable tool lists keep the prompt cache). flowd writes it at its start, root 0644, and keeps
  `/etc/flow` 0755 (a minimal image ships it 0700), while it makes `flowd.json` root 0600 whatever it arrived with. Passed as the SDK's `settings` option, which
  outranks user and project files but not managed settings
  ([settings](https://code.claude.com/docs/en/settings)). The env adds `DISABLE_AUTOUPDATER=1` and
  `CLAUDE_AGENT_SDK_CLIENT_APP=flow/<version>` and `CLAUDE_CODE_ENTRYPOINT=flow` (owner's decision; the variable is
  not documented in [env-vars](https://code.claude.com/docs/en/env-vars), and `flow` is not one of the entrypoint
  names the CLI knows, so what the CLI does with it is **provisional**).

### 5. Credentials, one place in flowd

- **Decision.** Flow pushes the whole set with `PUT /v1/env` (`flowdproto.Env`, monotonic revision); flowd's
  credentials component is the only code that writes credentials anywhere in a sandbox. Nothing that rotates is
  ever a process environment variable:
  - git: flowd writes `/etc/gitconfig` at its start with `git config --file` (other entries stay):
    `credential.https://github.com.helper = /opt/flow/bin/flowd git-credential` (an absolute path: git runs it as
    written and appends `get`/`store`/`erase`), `push.default=current`, `push.autoSetupRemote=true`. System-wide,
    so session units, terminals, setup scripts (private submodules, Go modules fetched over git) and flowd's own
    clones all use it. The helper asks flowd over `flowd.sock` (0666 in root's 0711 `/run/flow`; one request
    kind, no part of the bearer API) on every `get` and stores nothing; username = the login, password = the
    token, `password_expiry_utc` set. A token with under a minute left is not handed out: the helper waits up to
    10 s for the push of the renewed one. Without a token it writes the reason to stderr and answers nothing, so
    git fails with its own error; public clones never ask. The URL form `x-access-token:<token>@` is never used (it
    would put the token into process arguments and `.git/config`). Go's private modules over https need only
    this, plus `GOPRIVATE`/`GONOSUMDB` naming them (environment, not git config: set per repository, e.g. in
    `.flow/setup.sh` or the repository's own config; Flow sets none).
  - push: session branches are made with `--no-track` (no upstream), so a bare `git push` publishes the branch
    under its own name and sets the upstream (`push.default=current` also keeps a branch that tracks the default
    branch from pushing to it).
  - gh: `GH_CONFIG_DIR=/run/flow/credentials/gh` (root 0755); flowd rewrites `hosts.yml` (root 0644; temp file +
    rename) on every newer push: `github.com: {users: {<login>: {oauth_token}}, oauth_token, user, git_protocol:
    https}`, the layout gh itself writes since multiple accounts, plus `config.yml` `version: "1"`. Removed when
    the set carries no token. gh reads the token from `hosts.<host>.oauth_token` when no `GH_TOKEN`/`GITHUB_TOKEN`
    is set ([go-gh auth](https://github.com/cli/go-gh/blob/trunk/pkg/auth/auth.go)), and every run is a new
    process that reads the file. Not `GH_TOKEN`: frozen at process start, stale after 8 hours. The spec still
    names `GH_TOKEN`; this follows the coordinator's later instruction.
  - git identity: `GIT_AUTHOR_*`/`GIT_COMMITTER_*` from the profile in each unit's environment (changes apply to new
    units; identity does not expire).
- **Claude, per session** (owner decision): `claudeAuth` on the session (DB, API, `SessionSpec`, `session.started`),
  default `subscription_token`. flowd sets exactly one: `CLAUDE_CODE_OAUTH_TOKEN` (the one-year token from
  `claude setup-token`) for `subscription_token`, nothing for `subscription_login` (CLI login on the home volume),
  `ANTHROPIC_API_KEY` for `api_key`. Delivery: flowd keeps the last `PUT /v1/env` in memory (a restart reports
  revision 0) and starts no host or terminal unit before the first push: it answers `busy`, Flow forgets its
  connection, and the bring-up that follows pushes the set (a unit can never start without its credential). At
  every host start flowd writes the unit's `EnvironmentFile`
  (`layout.SessionEnvFile`, tmpfs, root 0600; never `spec.json`, which is on disk) with that one variable, the git
  identity and a fresh `BACKEND_MCP_TOKEN`; a stop removes it. The host passes on only the variable of the session's
  mode ("Sidecar environment"). Documented precedence, highest first: cloud-provider variables,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, `apiKeyHelper`, `CLAUDE_CODE_OAUTH_TOKEN`, profile credentials,
  `/login` credentials ([authentication](https://code.claude.com/docs/en/authentication),
  [env-vars](https://code.claude.com/docs/en/env-vars)). So flowd also removes the cloud-provider variables and
  `ANTHROPIC_AUTH_TOKEN` from host environments, and an `apiKeyHelper` in the owner's synced config would win over
  a subscription token: the config sync warns about it. Neither secret rotates; a replaced one reaches a session at
  its next host start.
- OpenCode provider keys: `providers.env` for the `opencode serve` unit; a change restarts it when no OpenCode
  turn runs.
- **GitHub App, Flow side** (`internal/github`, sources:
  [user tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app),
  [refresh](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens)).
  `BACKEND_GITHUB_CLIENT_ID` only: the device flow and the refresh need no client secret ("required unless the
  user access token was generated using the device flow"); unset → `GitHubConnection.available=false`, GitHub
  routes `not_implemented`, anonymous default-branch lookups. Device flow: one code at a time (a start while one
  is out returns it), the device code stays in Flow, GitHub is polled at most at its interval (+5 s per
  `slow_down`), a browser poll in between is answered `pending` from memory; `access_denied` → `disconnected`,
  `expired_token` → `expired`, other OAuth errors end the code with `upstream`, network failures keep it. A
  confirmed code keeps its tokens with the flow until `GET /user` and the connect succeeded (a failed `/user`
  answers `pending`, the next poll reads it again; the code's expiry no longer drops them). A poll while Flow holds
  no code answers the connection as it stands (another tab's poll, a lost answer, a page shown again: never read
  as a denial); an error answer means the code is over, the UI stops polling. The start answers the seconds left
  as well: the UI counts its deadline from them with its own clock (clocks differ), the poll's `expired` decides.
  The connection is one `credentials` row (kind `github`): login, expiries, sealed `{accessToken, refreshToken,
  expiries, accountId}`. One mutex holds every load, renewal, connect and disconnect: a refresh rotates the
  refresh token and kills the old access token ("Once you use a refresh token, that refresh token and the old
  user access token will no longer work"), so two renewals must never run. Renewal 15 minutes before the 8-hour
  token ends (once per token's life; every renewal cuts off the old token until the push lands), from a one-minute
  loop and from any use; never at Flow's start while the stored token is valid; not under the caller's context
  (a cut request must not lose the only valid refresh token). The new pair is stored before the access token is
  handed out; a failed write keeps it in memory and is retried, GitHub is never asked again with the burned one.
  Each renewal pushes the set to warm sandboxes at once (`credentials.Service.Changed`): push-driven because flowd
  cannot dial Flow. Network errors, 5xx and 429 (whatever their body) and answers without an OAuth error are
  transient (the token is handed out while valid, then `expired`). Only a known refusal drops the tokens:
  `bad_refresh_token` (GitHub's answer for a used, expired or revoked refresh token; GitHub's docs enumerate no
  refresh error codes, it is observed, community discussion 24745) or RFC 6749's `invalid_grant`; or the refresh
  token past its six months, checked locally. That keeps the row's login: `GitHubConnection.state=expired`
  ("connect again"), the set carries `disconnected`, and it is never retried. `incorrect_client_credentials` is
  Flow's configuration and keeps the authorization; any other code is logged and kept, GitHub asked again after
  5 minutes, never a discard. A crash between GitHub's answer and the write leaves the burned token stored: the
  next renewal gets that refusal and reports it the same way. Disconnect deletes the stored row by kind (not by
  the id in memory, which a reconnect whose write failed has replaced) and pushes; no revoke call (GitHub's revoke endpoints need the client secret). `listGitHubRepos`:
  `GET /user/installations` and `/user/installations/{id}/repositories` with the user token (what the App is
  installed on and the owner can reach), deduplicated, sorted, at most 1000. Default branch: `GET /repos/{name}`
  with the token, anonymous on 404 (public repositories outside the installation), on 401/403 (authorization
  revoked on GitHub, an org's SSO) and while not connected; a repository neither finds is `github_not_connected`
  while not connected, `upstream` when the token was refused, else `invalid_request`. The token goes
  only into the `Authorization` header and form bodies, never a URL, log or API answer.
- **Flow unreachable for more than 8 hours:** the token in flowd expires; the git helper fails with
  `flow: GitHub token expired (github_token_expired); Flow will renew it` (gh, reading the file, gets GitHub's 401),
  flowd adds a `notice {code: github_token_expired}` to the session whose git asked (the helper's process is found
  in its session unit's cgroup by `SO_PEERCRED`; at most one per session and code in 10 minutes, counted only
  once the host logged it, forgotten when the session is removed; through `hostproto` `notice`; terminals and setup scripts get the stderr line only), the turn sees a normal command
  failure instead of a hang. The next `PUT /v1/env` fixes it.
- **Refresh token invalid, or not connected:** the set carries `githubState: disconnected` and no token; the helper
  fails at once with `github_disconnected` and the session gets `notice {github_disconnected}`; the owner
  (re)connects with the device flow.
- **Flow side** (`internal/credentials`): the API's `createApiKey` stores the subscription token (`claude_token`),
  the Anthropic key (`anthropic`) or an OpenCode provider key (`opencode_provider`, `<PROVIDER>_API_KEY`) sealed,
  one per kind and provider; a second replaces the first with a new id and `createdAt`, which is the status the
  API reports (`getConnections`: kind, label, variable, since when, never the value; no schema addition was
  needed). The subscription login stores nothing. The whole set is pushed at every bring-up (after a delivery's
  restart) and, after a change, to every warm sandbox without starting one (one still in setup gets it once its
  bring-up ends). `Env.revision` is the clock's
  microseconds, taken before the store is read: it grows across Flow restarts and a push from an older read never
  wins. A value with a control character or past 4 KiB is refused without echo; values reach no log, error, event
  log, spec file or command line (tests scan the logs of a push; measured in the lab: only the session's
  `EnvironmentFile` and its host's environment hold it).
- Encryption at rest: `kit/secret` only compares secrets in constant time; it has no encryption. Flow seals with
  AES-256-GCM (standard library) behind `credentials.Sealer`, key from `BACKEND_SEAL_KEY` (32 bytes, base64),
  required in oidc mode; dev mode without it makes a key per start and says so loudly (stored credentials are lost
  at the next restart). Key rotation is not built: a new key makes every sealed value unreadable; a stored
  credential is then skipped with a warning and left out of `getConnections` (store it again), a flowd token is
  made again (see 3).

### 6. Child-session tools

- **Decision.** An MCP stdio server, `flowd mcp`, configured identically for every session (stable tool list):
  Claude through the SDK's `mcpServers: {flow: {type: "stdio", command, args, env}}`
  ([typescript](https://code.claude.com/docs/en/agent-sdk/typescript)); OpenCode through
  `mcp.servers.flow = {type: "local", command}`. Tools: `create_session`, `send_message`, `get_status`,
  `list_sessions`, `wait_for_sessions` (≤ 600 s; `MCP_TOOL_TIMEOUT` defaults to about 28 hours, so the cap is
  Flow's). No tool answers permission requests or changes modes. Why stdio: survives flowd restarts (it reconnects
  to `flowd.sock` per call) and needs no port. Rejected: an HTTP MCP endpoint in flowd (a flowd restart breaks the
  MCP session).
- **Caller identity.** flowd writes `BACKEND_MCP_TOKEN` (random per host start) into the host unit's environment file;
  the host puts it only into the MCP server's `env`, not the agent's; `flowd mcp` sends
  it with each call; flowd maps it to the session and raises a control request. Spoofable inside the sandbox (see
  3), so it is a convenience, not a boundary.
- **Rules, enforced by Flow** on every control request: same project as the sandbox the request came from; the
  target was started by the caller (`sessions.parent_id`); the caller has no parent (depth 1); at most
  `BACKEND_MAX_CHILD_SESSIONS` non-archived children; the child's mode is `min(requested, caller's mode)` and stored
  as `mode_cap`; later mode changes above the cap are `mode_above_cap`. Because the agent user is effectively root
  inside its sandbox, the mode cap and the own-sessions-only rule guard against mistakes within a project, not
  against a deliberately hostile agent in that project; the hard boundary is the sandbox.
- **Identical prefix.** A started session gets exactly what any session gets: the same system prompt preset, the
  same settings layer, the same MCP list and tools; everything specific to it (the parent's instructions) is its
  first user message. So its prompt prefix matches the parent's and every other session's and hits the cache.
- **Reports.** When a child's turn ends (done, failed, or waiting for the owner), Flow sends the parent
  `child.report`; the parent host queues it until the parent is between turns, then logs `child.reported` and
  delivers it as a message with origin `child_report`. The child logs `session.reported`. Never mid-turn.

## Sandbox network

The host is a shared server running Docker containers behind a layered firewall that must stay intact: Docker on
iptables-nft and firewalld on nftables (both required), possibly a third party's ban chain that keeps its jump first
in FORWARD, tables of its own. Measured in the lab `flow-net` (report `flow-net-findings.md`, owner's plans), with
and without a ban chain, then end to end with Flow itself.

- **Decision.** One Docker bridge network dedicated to Flow, `flow` on bridge `flowbr0` (`172.30.0.0/16`; Docker
  allocates only from `172.30.0.0/24`, the rest is the sandboxes' range `172.30.128.0/17`). Docker provides
  forwarding, masquerading, the firewalld zone `docker` and the separation from other Docker networks; neither
  Incus nor a bridged NIC adds a firewall rule. Each sandbox has one `bridged` NIC on it with
  `security.port_isolation` (no L2 traffic between sandboxes), `security.mac_filtering` and
  `security.ipv4_filtering` with a static `ipv4.address` (no spoofing; Incus adds its `table bridge incus`
  chains, which do not depend on `br_netfilter`). No IPv6 in sandboxes. Settings: `BACKEND_SANDBOX_BRIDGE`,
  `_SUBNET`, `_RANGE`, `_GATEWAY`, `_RESOLVERS` (`config/network.go`); `deploy/host-setup.sh` reads the same names.
  Rejected: an Incus-managed bridge with an Incus ACL (a second firewall manager on a host whose firewall must stay
  as it is); `enable_icc=false` (loads `br_netfilter` host-wide, changing bridged traffic for every container); a
  firewalld zone of its own for `flowbr0` (conflicts with Docker's runtime zone membership).
- **Host additions**, by the owner's hand with `deploy/host-setup.sh` (preconditions in its header; baseline, dry
  run, verified diff, `OTHER_CHURN` names others' churn to show but not count; the ban chain, when the host has
  one, first before and after: `BAN_CHAIN` names it, a name set explicitly or recorded by the setup must exist,
  `BAN_REQUIRE=1` refuses a host without the default) and reversed by `deploy/host-teardown.sh`: firewalld policies `flow-sb-host` (range → host rejected, all
  IPv6 from zone `docker` rejected), `flow-sb-docker` (range → other Docker bridges, also the routed hairpin back
  to another sandbox) and `flow-sb-lan` (range → 10/8, 172.16/12, 192.168/16, 169.254/16 with cloud metadata,
  100.64/10 behind the host, which it forwards otherwise: measured; so resolvers must be public or on the sandbox
  subnet, the config refuses others); one `DOCKER-USER` rule against published ports of other containers (firewalld accepts
  DNAT'd traffic before any policy), kept by the unit `flow-sandbox-published.service` (inserts it after Docker,
  before Incus; no ExecStop, so a Docker stop leaves it: measured without a gap across restarts and a simulated
  boot), effective only while FORWARD jumps to `DOCKER-USER` before `DOCKER-FORWARD` (the scripts check; once seen
  reversed in the lab until Docker restarted); Incus ordered after Docker; Incus' API on the sandbox network's
  gateway (`INCUS_LISTEN`, `172.30.0.1:8443`): Flow's container cannot reach the host's loopback, sandboxes are
  refused there by `flow-sb-host`, and every other container (all Docker bridges are in zone `docker`) by
  `flow-sb-incus`, which lets only Flow's static address (`CONTAINER_ADDRESS`, `172.30.1.2`, outside Docker's and
  the sandboxes' ranges) through; matched by source address, so a container on `flow` with raw sockets could forge
  it or take the address over on the bridge (not measured); Incus' client certificate stays the other bar. `flow-incus-listen.service`
  sets the listener again once Docker made the bridge (Incus binds a missing address once more after 30 s, then
  never; measured);
  the restricted project, which needs `restricted.devices.nic=allow` (a restricted project refuses NICs on an
  unmanaged bridge; measured). Owner's risk, measured on Incus 6.0.4: `allow` also lets the certificate's holder
  attach a NIC to any host bridge (other Docker networks, filtering off) and use macvlan, ipvlan, routed, p2p and
  physical (a host interface moved in); host disks and privileged containers stay refused. Nothing narrower
  exists: the values are `block|managed|allow`, a restricted project must have its own profiles, and under
  `managed` even the admin cannot put the bridged NIC into its profile.
- **Guest configuration** from outside, no exec: before every start Flow writes eth0's systemd-networkd unit
  (`layout.NetworkConfig`: address, gateway, resolvers, `LinkLocalAddressing=no`) with the file API, which works on
  stopped containers. The base image has no DHCP unit and no address of its own.
- **Addresses.** Flow assigns each sandbox the lowest free address of the range (never `.0`/`.255`, the gateway, a
  resolver or the image builder's, `sandbox.Network.BuilderAddress`), held from the first bring-up until the
  sandbox is deleted, then reused. Persisted in `sandboxes.ipv4` under a unique index (`sandboxes.SQLAddresses`,
  migration 0003); on first use after a Flow start the rows adopt the addresses the instances' NICs carry, so the
  switch from the earlier in-memory assignment moves no sandbox. A clone loses its NIC and gets its own before it
  starts.
- **What sandboxes reach** (measured from Flow's own sandboxes): internet and public DNS; nothing on the host (any
  address, loopback-bound services, IPv6); no container of another Docker network, published ports included; no
  private, link-local or shared network behind the host; no other sandbox, also not through the gateway; with a
  foreign source address or MAC nothing at all. Flow (and a
  container on `flow`) reach flowd in every sandbox. In production sandboxes also reach every container on
  `flow` at L2: the host has no `br_netfilter`, so bridged frames meet no firewall (the lab blocks them only
  because its kernel has it). Hence: Flow's browser API is never served there
  (`sandboxes.OffSandboxNetwork`, and the config refuses a `BACKEND_ADDR` on the subnet), and everything listening
  on `flow` authenticates; flowd's own API takes only Flow's per-sandbox token. The guard judges the local address
  as well, so Flow's container must also be on the reverse proxy's network and the proxy must reach it there
  (Traefik: label `traefik.docker.network` naming the proxy's network, else Traefik may pick the `flow` address):
  a container whose only network is `flow` refuses every request but loopback ones, and logs that as an error at
  start.
- **Missing bridge.** `docker network rm` deletes `flowbr0` even with sandboxes attached; they run on, cut off,
  and Incus never re-attaches them. Flow checks the bridge's ports before every bring-up: bridge missing → the
  sandbox is `failed` (`sandbox_network_missing`), nothing is created, started or delivered and nothing retries by
  itself; bridge back but the sandbox cut off (or on another address than its assignment, which is
  `sandbox_unavailable`) → the next bring-up restarts it onto the bridge (`Warm` only reports it). A cached flowd
  connection is handed out only while the instance runs, carries its address and is a port of the bridge.
- **Limits.** Per sandbox `BACKEND_SANDBOX_MEMORY_MIB` 6144 (one project's full check peaks at about 4 GiB, page
  cache included; 4 GiB was the floor) and `BACKEND_SANDBOX_CPUS` 4 (half the target host's threads), set at create
  and again before every start, so a changed setting reaches a stopped sandbox (0 lifts the limit there). Per session unit (the host
  and everything its agent runs): `BACKEND_SESSION_MEMORY_MIB` (default the sandbox's less 1 GiB: a check plus
  Claude Code, with room for flowd, Docker's daemon and a terminal), `BACKEND_SESSION_CPU_PERCENT` (default all but
  one of the sandbox's CPUs × 100), `BACKEND_SESSION_TASKS_MAX` 4096, `MemorySwapMax=0`; never above the sandbox's.
- **Memory admission.** A limit is a cap, not a reservation (owner decision: no fixed allocation): idle sandboxes
  hold a fraction of theirs (measured: empty 96 MiB, one idle session 240 MiB, three 763 MiB). A sandbox is created
  and started only while the measured memory of all running sandboxes plus `BACKEND_SANDBOX_MEMORY_HEADROOM_MIB`
  (default `BACKEND_SANDBOX_MEMORY_MIB`: room for one full build to begin) fits under
  `BACKEND_SANDBOX_MEMORY_TOTAL_MIB` (default 14 GiB; unset is the default, 0 turns the check off), else
  `sandbox_memory_full` with a log line stating measured, headroom and total. Checked before the create (about 40 s;
  a refused sandbox leaves no instance) and again at the start. Each admitted create or start holds a headroom of
  its own until it finishes (a sandbox just started measures almost nothing): two at once need room for two. A
  running sandbox restarted onto its bridge or address is not checked again (refused, it would stay stopped with
  its turns gone). Measured = the working set
  from Incus' metrics (`/1.0/metrics`): `MemTotal − MemFree` (the cgroup's `memory.current`) less `Inactive_file`.
  The instance state's `memory.usage` is `memory.current` with the page cache in it (measured, Incus 6.0.4: an idle
  sandbox just booted 270 MiB, 210 MiB of it inactive file cache): reclaimable cache must not refuse a start, so
  it is not used. Active file cache is counted (hot, reclaimed only under pressure), as Kubernetes' working set
  does. An instance the metrics do not measure (refused, or missing) counts with its `memory.usage`, cache
  included: too much rather than nothing, never an open door, never a block for good; an instance list that fails
  refuses the start (`sandbox_unavailable`) until the next one. Incus' project `limits.memory` was rejected: it
  counts stopped instances too (measured) and would cap how many sandboxes exist; host-setup sets none.

## Lifecycles

**Sandbox.** none → `setup` (Incus create from the base image or clone, limits, home volume, NIC on `flowbr0` with
its assigned address, start,
push flowd, `Hello`, `PUT /v1/env`, clone repos, run each `.flow/setup.sh` as a flowd job, see "Setup scripts") →
`warm` → `stopping` → `stopped` (filesystem kept) → `setup` again on demand (no re-clone, setup scripts not run
again). The RAM budget stops the least recently used warm sandbox whose sessions are all idle and whose last
turn ended more than the prompt-cache lifetime ago (5 minutes unless the 1-hour TTL is configured,
[env-vars](https://code.claude.com/docs/en/env-vars); Flow never overrides it).

**Session.** create (DB row, name, branch) → worktrees on fresh `origin/<default>` → `PUT /v1/sessions/{id}` starts the
host → `session.started` → turns → idle with the host alive (token economy) → archive (`DELETE /api/sessions/{id}`)
closes the session's terminal (a shell would keep its credential), stops the host and removes worktrees, then
publishes `session.removed` and ends the session's follower; the log stays. A worktree with uncommitted or untracked
changes (`git status --porcelain` not empty) is not removed: it stays on disk with its branch, the session directory
and its claim, the archive still succeeds and flowd logs which worktree it kept (only the owner may drop work; a
status that cannot be read fails the removal). Archiving never starts a sandbox (owner
decision): in a stopped one nothing of the session runs, so the row is marked archived with `cleanup_pending`, and
the sandbox's next bring-up (start, or Flow's reconnect) removes host and worktrees (`sessions.Service.Sweep`). Only
"not warm" with the instance stopped or gone, or "no sandbox", counts as stopped; a sandbox not warm by the records
(`failed`, say) whose instance may run, and any other failure to reach the sandbox, fails the archive. A reconnect
to a running warm sandbox that fails (a fetch, flowd briefly away) leaves it warm for the next try, whether the call
may start the sandbox or not; `failed` is recorded only when it was not warm before, the call had to create, start
or move its instance, or it is cut off from its bridge or on another address. Archive and
whatever starts the session's host or terminal hold one per-session lock, so a send in flight cannot recreate them
after an archive. A stopped host restarts on the next message with resume and logs `session.resumed`.

## Owner operations on projects and the profile

- **Project delete** (`DELETE /api/projects/{id}`). Refused with `project_has_sessions` while a session of the project
  is not archived (the spec does not say; archiving first keeps uncommitted work on disk and says so, a delete would
  not). Then the sandbox instance is stopped and deleted (`Manager.Delete`), its address released, and in one
  transaction the project's sessions, then the project (its repositories and sandbox row by cascade). Sessions go
  first so a worktree's `ON DELETE RESTRICT` on its repository never depends on the cascade's order. The row is
  `deleting` from the instance's removal on: `ensure` refuses it, `Warm` reports it not warm and `Stop` leaves it, so
  a queued setup, an environment push or a session start cannot make the instance again before the rows are gone; a
  project never brought up gets such a row too. The rows go under the same hold of the project's lock as the
  instance, so no bring-up comes between the two; when they cannot (a session made after the count, a store error)
  the row turns `stopped` and the project stays usable without an instance (a start makes it anew). A failed removal
  leaves the row `failed` and the project in place, to be tried again. The home volume stays (every sandbox shares it); metering rows stay (no foreign keys).
- **Rebuild** (`POST .../sandbox/rebuild`) re-creates the instance from the base image: it is deleted, the row keeps
  its id, address and flowd token, and a background bring-up creates it, delivers flowd and clones every repository
  afresh, then runs every repository's `.flow/setup.sh` again (the new instance holds no job results). Same
  refusals as the delete. Archived sessions' logs and kept worktrees go with the old instance; their pending
  cleanups find nothing and are done (`sessions.Service.stop`).
- **Setup scripts** (`sandboxes/setup.go`, `supervisor/jobs.go`). A bring-up that finds the sandbox not warm (the
  first after a create or Rebuild, a start, a retry after a failure; never a reconnect to a warm one) runs, after the
  clones, the job of each repository whose main checkout holds `.flow/setup.sh` as a regular file (no link at it or
  at `.flow`; committed in HEAD as mode 100644/100755), in project order, one at a time: `PUT /v1/jobs/setup-<repo
  dir>`, then `GET /v1/jobs/{id}` every 2 s (no call waits for the script, so the 5 min call timeout never cuts it;
  flowd away for a moment is waited out, the set pushed again, the start asked again, which is idempotent). The
  sandbox stays `setup` meanwhile; the bring-up's limit grows by each repository's script limit plus 2 min. Each job
  is a root unit `flow-job-<id>` running `flowd job` (multi-call): it opens the log `layout.JobLog` with
  `O_EXCL|O_NOFOLLOW` in `layout.JobDir` (root 0700, refused when it is a link or not root's), runs `bash
  .flow/setup.sh` in the main checkout as uid/gid 65532 with the agent's supplementary groups (docker: a bare
  uid/gid would drop them), its own process group, stdin `/dev/null`, a session's limits (`BACKEND_SESSION_*`,
  `MemorySwapMax=0`, `OOMPolicy=continue`) and `BACKEND_SETUP_TIMEOUT` (default 20 min; SIGTERM, 10 s, SIGKILL to the
  group; `RuntimeMaxSec` behind it). Environment (the spec says nothing about setup credentials; decided): what a
  `subscription_login` terminal gets (`claude.Env`: PATH, the caches, Playwright, git identity, `GH_CONFIG_DIR`, so
  git and gh reach GitHub through the helper and hosts file), never a Claude credential or the MCP token. Bash, not
  the file's own shebang: no executable bit needed. The log keeps whole lines only, each cut at 4 KiB, and the first
  4 MiB (a closing line counts what was dropped), so line numbers never move. At the end the runner writes
  `layout.JobResult` (temp file + rename: repository, HEAD commit, script blob, exit status, timed out, lines). That
  result is the marker: flowd never starts a job that has one, so a stop and start, Flow's or flowd's restart, or a
  second bring-up find it (or the still running unit, which outlives both) instead of running the script twice. A
  job whose unit ended without a result (the sandbox stopped under it) counts as cut off: that bring-up fails, the
  next one runs it again from line 1. Exit status not 0 or time limit reached: the sandbox turns `failed` with
  `sandbox_setup_failed`; a later start reports the same result without running the script (decided: retry is a
  Rebuild). Main checkouts are only fetched, never moved, so a script changed on the default branch runs only after
  a Rebuild (decided; this document's earlier "rerun when the hash changed" is dropped), and a repository added to
  the project runs its script when its checkout is first cloned. A sandbox made before setup scripts existed runs
  them once, at its next start. `getSetupLog` reads the jobs in project order without the project's lock (the
  bring-up holds it): a header line per repository, numbered across all; 1000 lines per answer, from `after` or the
  last `tail` lines (the UI reads the end first: a failed script's error is there); for `setup`, `warm`
  and `failed` only (`sandbox_unavailable` otherwise). Config sync, the other job kind, answers `not_implemented`.
- Delete and rebuild never wait for a bring-up: a project lock still held after `LockWait` (2 s) answers
  `sandbox_busy` (`tryLock`). The short wait lets a `Warm` (session followers, an environment push) pass. A
  follower whose sandbox does not run asks again after 2 s, doubling to 30 s, and at once on the project's
  `sandbox.updated` (as the session stream does).
  `Warm` never waits behind a bring-up that may start the sandbox: it answers "not warm, may run" at once (the
  environment push alone waits, to follow the bring-up); every other wait for the lock ends when its caller leaves.
  Under the lock both count the open sessions again, and the delete's transaction removes only archived sessions
  and fails with `project_has_sessions` while another is left: a session made after the API's count is never lost
  (the sandbox row then turns `stopped`, see Project delete).
- `Manager.Setup` records `setup` before its bring-up starts (a new row, or a stopped or failed one; skipped while a
  bring-up holds the lock). A creation publishes `project.updated` before it starts the setup: the browser drops a
  `sandbox.updated` of a project it does not know, and a bring-up that failed at once was followed by the creation's
  stale `setup`. `sandbox.updated` carries the sandbox, so the Projects page follows setup → warm from the app stream
  alone; it never applies an answer's sandbox, which may be older than an event already shown. A sandbox row that
  cannot be read publishes no `sandbox.updated` (never a made-up `none`).
- At Flow's start, before any follower runs, a row still `setup`, `stopping` or `deleting` was left by an operation
  cut off (a restart mid bring-up, a store error after the state was written) and turns `failed`
  (`Manager.Reconcile`): start, rebuild and delete all work from there. Incus is not asked at that point.
- **Profile** (`PATCH /api/profile`) decodes into pointers (the generated type cannot tell absent from empty) and
  refuses control characters and oversize values: git name and email end up in a unit's `EnvironmentFile`. A changed
  identity pushes the credential set to warm sandboxes at once; session hosts started afterwards carry it (measured:
  `GIT_AUTHOR_*`/`GIT_COMMITTER_*` in the new host's environment file and process), running hosts keep theirs. Only
  both name and email reach sessions; the Settings page says so when one is empty.

## Failures

| Failure | What happens | Recovery |
|---|---|---|
| Browser disconnect | nothing in Flow or the sandbox changes | EventSource reconnects with `Last-Event-ID`; durable gap filled from the log, then `live.snapshot` |
| Flow restart or update | hosts keep running turns; control requests wait | Flow reads `last_seq` per session, re-dials flowd, re-follows from there; re-reads pending control requests |
| flowd restart or update | no unit is its child; turns go on, logs keep growing | flowd reattaches by unit name and socket; Flow's streams reconnect |
| host crash | sidecar dies with it; turn lost | flowd restarts it on the next message: `turn.ended {lost}`, open requests `expired`, `session.resumed` |
| sidecar exit or broken pipe | during a turn: `notice {message_may_not_have_run}`, `turn.ended {error, adapter_exited}`, open request `expired`; between turns: nothing | the host lives on; the owner re-sends if needed; the next message starts a sidecar with `resume` |
| opencode serve restart | the host's event stream breaks | host reconnects, resyncs from the message list (OpenCode's `/api/event` has no replay, [openapi.json](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/openapi.json)) |
| sandbox stop | all units end | Flow sets a cut-off turn's row `failed` and keeps it open (`turn_started_at`); on start, only the hosts of those sessions start at once (two at a time, after the sandbox is warm), log the turn `lost` and `session.resumed {sandbox_start}`, leave queued messages queued and are stopped again; every other host starts when needed, and a message's start drains the queue |
| host (machine) reboot | Incus starts nothing by default; Flow reconciles | sandboxes start on demand; `BootID` change tells Flow every unit is gone |
| Incus restart | units survive (spike 11) | Flow holds no exec, so Incus restarts in about 2 s |
| setup script fails or runs past its limit | the sandbox is `failed` (`sandbox_setup_failed`), its log kept | the owner reads `getSetupLog`, fixes the script, Rebuilds |
| Flow or flowd restart during a setup script | the job's unit runs on; Flow's row turns `failed` at its start (`Reconcile`) | the next start finds the job and waits for it, never runs it twice |
| host bridge `flowbr0` missing | the sandbox is `failed`, nothing created or started, no retry | host-setup.sh makes the network again; the next start brings the sandbox up |
| bridge made again under running sandboxes | they run on, cut off (Incus never re-attaches a NIC) | the next bring-up restarts them onto the bridge |
| host boot | Incus waits for Docker's bridge (drop-in, at most 120 s), else autostart fails and leaves sandboxes stopped; an API address still missing then is retried once after 30 s, then never | `flow-incus-listen.service` sets the listener again once Docker has made the bridge; sandboxes start on demand |

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

- Sign-in (`docs/auth.md`): OIDC through kit/auth, admitting only the configured subject or group; every API route
  but the auth endpoints needs the session cookie and answers `unauthenticated` without it; state changes must come
  from Flow's public origin.
- Flow's API refuses requests whose `Origin`/`Referer` is a preview host (`preview_origin`); preview hosts get
  their own host-only cookie through a one-use ticket; the proxy strips `Cookie` and `Authorization`.
- Everything a sandbox sends (events, control requests, diffs) is untrusted input bound to that sandbox's project.
- Nothing on the sandbox network answers without authentication: sandboxes reach Flow's container there at L2.
  Flow's browser API refuses every request whose local or remote address is on the sandbox subnet.
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
